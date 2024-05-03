package matrixbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"init-bot/types"
	"io"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"net/http"
	"regexp"
	"strings"
	"syscall"
	"text/template"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/id"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/mattn/go-sqlite3"
)

// MatrixBot struct to hold the bot and it's methods
type MatrixBot struct {
	//Map a repository to matrix rooms
	Config       types.Config
	Client       *mautrix.Client
	matrixPass   string
	matrixUser   string
	Handlers     []CommandHandler
	Name         string
	Context      context.Context
	CancelFunc   context.CancelFunc
	CryptoHelper *cryptohelper.CryptoHelper
	Log          zerolog.Logger
	Database     *pgxpool.Pool
}

// CommandHandler struct to hold information about a command handler.
type CommandHandler struct {

	//Pattern to initiate the command
	Pattern string

	//The minimal power required to execute this command
	MinPower int

	//The function to handle this command
	Handler func(ctx context.Context, message *event.MessageEventContent, room id.RoomID, sender id.UserID)

	//Help to be displayed for this command
	Help string
}

var runningCmds = make(map[context.Context]*context.CancelFunc)

// Contexts is a map that stores the context values for each room in the bot.
var Contexts = make(map[id.RoomID][]int)

// cancelContext cancels the context and removes it from the runningCmds map.
// It retrieves the cancel function from the runningCmds map using the provided context.
// Then, it calls the cancel function to cancel the context.
// Finally, it deletes the context from the runningCmds map and returns true.
// If the provided context is not found in the runningCmds map, it returns false.
func cancelContext(ctx context.Context) bool {
	cancelFunc, ok := runningCmds[ctx]
	if !ok {
		return false
	}
	(*cancelFunc)()
	delete(runningCmds, ctx)
	return true
}

// NewMatrixBot creates a new MatrixBot instance with the provided configuration.
// It sets up logging, initializes a Mautrix client, and sets the necessary
// properties on the bot. It also sets up event handling for syncing and
// certain types of events. Additionally, it sets up the crypto helper with
// a PG backend for saving crypto keys. Finally, it logs in to the Matrix server
// and starts the sync, sets the client crypto helper, and initializes the database
// and contexts. It registers the commands the bot should handle and returns the
// initialized bot instance or an error.
// config: the configuration for the bot
// returns: the initialized MatrixBot instance or an error
func NewMatrixBot(config types.Config, log *zerolog.Logger) (*MatrixBot, error) {
	// Setup logging first of all, to be able to log as soon as possible

	// Initiate a Maytrix Client to work with
	cli, err := mautrix.NewClient(config.Homeserver, "", "")
	if err != nil {
		log.Panic().
			Err(err).
			Msg("Can't create a new Mautrix Client. Will quit")
	}
	cli.Log = *log
	// Initiate a Matrix bot from the Mautrix Client
	bot := &MatrixBot{
		matrixPass: config.Password,
		matrixUser: config.Username,
		Client:     cli,
		Name:       config.Botname,
		Log:        log.With().Str("component", config.Botname).Logger(),
	}
	// Set up the Context to use (this will be used for the entire bot)
	syncCtx, cancelSync := context.WithCancel(context.Background())
	bot.Context = syncCtx
	bot.CancelFunc = cancelSync

	// Set up event handling when bot syncs and gets certain types of events such as Room Invites and Messages
	_, err = SetupSyncer(bot)
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Problem setting up Syncer and Event handlers")
	}

	// Set up the cryptohelper with a PG backend so we can save crypto keys between restarts
	database, err := dbutil.NewWithDialect(fmt.Sprintf("postgres://%s:%s@%s:%s/%s", config.DB.User, config.DB.Password, config.DB.Host, config.DB.Port, config.DB.DBName), "pgx")
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Error connecting to Database")
		syscall.Exit(2)
	}
	cryptoHelper, err := cryptohelper.NewCryptoHelper(cli, []byte("voff"), database)
	if err != nil {
		panic(err)
	}
	bot.CryptoHelper = cryptoHelper

	// Now we are ready to try and login to the Matrix server we should be connected to
	log.Debug().Msgf("Logging in as user: %s", config.Username)

	cryptoHelper.LoginAs = &mautrix.ReqLogin{
		Type:       mautrix.AuthTypePassword,
		Identifier: mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: config.Username},
		Password:   config.Password,
	}

	err = cryptoHelper.Init(syncCtx)
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Error logging in and starting Sync")
		panic(err)
	}
	// Set the client crypto helper in order to automatically encrypt outgoing/incoming messages
	cli.Crypto = cryptoHelper

	// Log that we have started up and started listening
	log.Info().Msg("Now running")

	log.Info().Msg("Setting up DB and Contexts")
	bot.Database, err = pgxpool.New(syncCtx, fmt.Sprintf("postgres://%s:%s@%s:%s/%s", config.DB.User, config.DB.Password, config.DB.Host, config.DB.Port, config.DB.DBName))
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Error Creating DB Pool")
	}

	db, err := bot.Database.Acquire(syncCtx)
	if err != nil {
		bot.Log.Warn().
			Err(err).
			Msg("Error connecting to Database")
	}

	// Taken from StackOverflow as how to actually check for Real Tables that the user can access
	rows, _ := db.Query(syncCtx, "SELECT EXISTS (SELECT FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE  n.nspname = $1 AND c.relname = $2 AND c.relkind = 'r');", "public", "Bot-Context")
	defer rows.Close()
	bools, err := pgx.CollectRows(rows, pgx.RowTo[bool])
	rows.Close()

	// If the table doesn't exist in the database we create it. This is where the Bots "memory" will be stored in the form of LLM Tokens that makes up the LLM Context (Different from the Background Context used for all functions in this code)
	if !bools[0] {
		_, err := db.Exec(syncCtx, "CREATE TABLE \"Bot-Context\" (room text PRIMARY KEY, context integer ARRAY)")
		if err != nil {
			bot.Log.Error().
				Err(err).
				Msg("Error creating Bot-Context table")
		}
	}

	// Retrieve all rooms from the database
	rows, err = db.Query(syncCtx, "SELECT * FROM \"Bot-Context\"")
	if err != nil {
		bot.Log.Error().Err(err).Msg("Error retrieving rooms from the database")
	}
	defer rows.Close()
	// Iterate over each row and update the Contexts variable
	for rows.Next() {
		var roomID string
		var roomContext []int

		err = rows.Scan(&roomID, &roomContext)
		if err != nil {
			bot.Log.Error().
				Err(err).
				Msg("Error reading all room contexts from the database")
		}

		// Use the room column as key to Contexts variable and save the roomContext column
		Contexts[id.RoomID(roomID)] = roomContext
	}
	rows.Close()

	db.Release()

	// Set bots Config to use the config provided by the user
	bot.Config = config

	// Register the commands this bot should handle
	bot.RegisterCommand("help", 0, "Display this help", bot.handleCommandHelp)
	bot.RegisterCommand("search", 0, "Start message with 'search' to search SharePoint documents", bot.handleSearch)
	bot.RegisterCommand("avatar set", 0, "Set a new Avatar image for the bot. Image must be last part of an Matrix URI that starts with mxc:// and only the last par after the last / should be included. Also, the image referenced like that must be uploaded in an un-encrypted room!", bot.handleSetAvatar)
	bot.RegisterCommand("", 0, "Default action is to Query the AI", bot.handleQueryAI)

	return bot, nil
}

// RegisterCommand registers a new command handler with the provided pattern, minimum power level, help message, and handler function.
// The handler function should have the signature func(ctx context.Context, message string, room id.RoomID, sender id.UserID).
// It creates a new CommandHandler struct with the provided parameters and appends it to the bot's Handlers slice.
// It also logs a debug message indicating the registration of the command handler.
func (bot *MatrixBot) RegisterCommand(pattern string, minpower int, help string, handler func(ctx context.Context, message *event.MessageEventContent, room id.RoomID, sender id.UserID)) {
	mbch := CommandHandler{
		Pattern:  pattern,
		MinPower: minpower,
		Handler:  handler,
		Help:     help,
	}
	bot.Log.Debug().
		Msgf("Registered command: %s [%v]", mbch.Pattern, mbch.MinPower)
	bot.Handlers = append(bot.Handlers, mbch)
}

// handleCommands handles incoming messages by checking if the sender is the bot itself and ignores the message if true.
// Then it iterates over the registered command handlers and checks if the message matches the pattern for each handler.
// If a match is found, the corresponding handler function is called and the handled variable is set to true.
// If no match is found, it treats the message as an AIQuery and calls the handler function for AIQuery using the queryIndex.
func (bot *MatrixBot) handleCommands(ctx context.Context, message *event.MessageEventContent, room id.RoomID, sender id.UserID) {
	//Don't do anything if the sender is the bot itself
	if strings.Contains(sender.String(), bot.matrixUser) {
		bot.Log.Debug().Msg("Bots own message, ignore")
		return
	}

	bot.Log.Info().Msg("Handling input...")
	// Trim bot name, double spaces, and convert string to all lower case before trying to match with pattern of command
	matchMessage := message.Body
	bot.Log.Debug().
		Str("user", sender.String()).
		Msg("Message by user")
	bot.Log.Debug().
		Str("user_message", matchMessage).
		Msg("Message from the user")

	// Make the request to AI API
	client := http.Client{
		Timeout: 30 * time.Minute,
	}
	templateString := "{{$first := true}}{{range $key, $value := $}}{{if $first}}[{{$first = false}}{{else}}, {{end}}{{$value.Pattern}}{{end}}"
	templ := template.Must(template.New("example").Parse(templateString))
	builder := &strings.Builder{}
	err := templ.Execute(builder, templ)
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Error executing template")
		cancelContext(ctx)
		return
	}
	labelsStr := builder.String() + "]"
	data := "{\"text\": \"" + matchMessage + "\", \"labels\": " + labelsStr + "}"
	bot.Log.Debug().Any("request", data).Msg("Sending Body data")

	resp, err := client.Post("http://localhost:8081/api/classify", "application/json", bytes.NewBuffer([]byte(data)))
	if err != nil {
		bot.Log.Error().Err(err).Msg("Error making request to Topic Classification API")
		cancelContext(ctx)
		return
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			cancelContext(ctx)
			bot.Log.Error().Err(err).Msg("An error occurred closing Body")
		}
	}(resp.Body)

	// Check if the response status code is not 200
	if resp.StatusCode != http.StatusOK {
		bot.Log.Error().Msgf("Topic Classification API returned non-200 status code: %d", resp.StatusCode)
		cancelContext(ctx)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		bot.Log.Error().Err(err).Msg("Error reading answer from Topic Classification")
		cancelContext(ctx)
		return
	}

	bot.Log.Debug().Any("response", body).Msg("Response body")
	strBody := string(body)
	bot.Log.Debug().Any("strBody", strBody).Msg("Response body string")

	var fullResponse types.TopicClassifications
	err = json.Unmarshal([]byte(strBody), &fullResponse)
	if err != nil {
		bot.Log.Error().Err(err).Msg("Can't unmarshal JSON response from Topic Classification")
		cancelContext(ctx)
		return
	}
	handled := false
	var queryIndex int
	for k, v := range bot.Handlers {
		if v.Pattern == "" {
			queryIndex = k
			continue
		}
		r, _ := regexp.Compile(v.Pattern)
		if r.MatchString(matchMessage) {
			bot.Log.Info().Msgf("Handling message of type: %s", v.Pattern)
			handled = true
			cmdContext, cmdCancelFunc := context.WithTimeout(ctx, 30*time.Minute)
			runningCmds[cmdContext] = &cmdCancelFunc
			go v.Handler(cmdContext, message, room, sender)
		}

	}
	if !handled {
		bot.Log.Debug().Msg("Could not find a pattern to handle, treat this as AIQuery")
		cmdContext, cmdCancelFunc := context.WithTimeout(ctx, 30*time.Minute)
		runningCmds[cmdContext] = &cmdCancelFunc
		go bot.Handlers[queryIndex].Handler(cmdContext, message, room, sender)
	}
}

// handleCommandHelp displays the list of available commands for the bot
// in a formatted message. It iterates over the registered command handlers
// and appends their pattern and help message to a help message string.
//
// The help message is then sent as a notice to the specified room.
func (bot *MatrixBot) handleCommandHelp(ctx context.Context, message *event.MessageEventContent, room id.RoomID, sender id.UserID) {
	helpMsg := `# Init Bot Help
The bot will only react when you @ the bot. Commands do not take @Init-Bot into account when evaluating for commands. All commands are case insensitive

The following commands are avaitible for this bot:

|Command|Explanation|
|---|---|`

	for _, v := range bot.Handlers {
		helpMsg = helpMsg + "\n|@init-bot " + v.Pattern + "|" + v.Help + "|"
	}

	sendMsg := format.RenderMarkdown(helpMsg, true, true)
	_, err := bot.sendHTMLNotice(ctx, room, sendMsg.FormattedBody)
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Error sending help message")
	}
	cancelContext(ctx)
}

// handleQueryAI handles the processing of a query from a user and sends it to the AI for a response.
// It prepares the prompt, model, and system prompt (if available),
// makes the request to the AI API, reads and parses the response,
// updates the bot's context and saves it to permanent storage,
// and finally sends the response back to the user.
func (bot *MatrixBot) handleQueryAI(ctx context.Context, message *event.MessageEventContent, room id.RoomID, sender id.UserID) {
	response, err := bot.queryAI(ctx, message.Body, bot.Config.AI.Endpoints["chat"].Model, room)
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Couldn't query AI")
		cancelContext(ctx)
		return
	}

	bot.Log.Debug().Any("response", response).Msg("AI response")

	// We have the data, formulate a reply
	_, err = bot.sendHTMLNotice(ctx, room, response[bot.Config.AI.PromptKey].(string))
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Couldn't send response back to user")
		cancelContext(ctx)
	}
	bot.Log.Info().Msg("Sent response back to user")
	cancelContext(ctx)
}

// handleSearch performs a search using Ollama and ChromaDB
// It prepares the prompt text, Ollama and ChromaDB clients, and retrieves the collection from ChromaDB.
// Then it queries ChromaDB with the prompt text and sends the response to handleQueryAI function.
func (bot *MatrixBot) handleSearch(ctx context.Context, message *event.MessageEventContent, room id.RoomID, sender id.UserID) {
	bot.Log.Debug().Msg("Handling a Search query")
	//Prepare prompt, model and if exist system prompt
	promptText := bot.regexTrimLeft("search", message.Body)
	// Make the request to AI API
	client := http.Client{
		Timeout: 30 * time.Minute,
	}

	data := "{\"prompt\": \"" + promptText + "\", \"num_results\": 1}"
	bot.Log.Debug().Any("request", data).Msg("Sending Body data")

	resp, err := client.Post("http://localhost:8080/api/search", "application/json", bytes.NewBuffer([]byte(data)))
	if err != nil {
		bot.Log.Error().Err(err).Msg("Error making request to Vector Search API")
		cancelContext(ctx)
		return
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			cancelContext(ctx)
			bot.Log.Error().Err(err).Msg("An error occurred closing Body")
		}
	}(resp.Body)

	// Check if the response status code is not 200
	if resp.StatusCode != http.StatusOK {
		bot.Log.Error().Msgf("Vector Search API returned non-200 status code: %d", resp.StatusCode)
		cancelContext(ctx)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		bot.Log.Error().Err(err).Msg("Error reading answer from Vector search")
		cancelContext(ctx)
		return
	}

	bot.Log.Debug().Any("response", body).Msg("Response body")
	strBody := string(body)

	bot.Log.Debug().Any("strBody", strBody).Msg("Response body string")

	var fullResponse types.VectorResponse
	err = json.Unmarshal([]byte(strBody), &fullResponse)
	if err != nil {
		bot.Log.Error().Err(err).Msg("Can't unmarshal JSON response from Vector search")
		cancelContext(ctx)
		return
	}

	var documents string
	for _, doc := range fullResponse.Documents {
		documents = documents + "; " + doc
	}

	promptAI := fmt.Sprintf("Använd denna data:\n\n%s \n\n"+
		"För att besvara denna fråga noggrant och korrekt genom att bara använda datan du har tillgång till: \n%s",
		promptText, documents)

	response, err := bot.queryAI(ctx, promptAI, bot.Config.AI.Endpoints["chat"].Model, room)
	if err != nil {
		bot.Log.Error().Err(err).Msg("Error using Search response in AI query")
		cancelContext(ctx)
	}

	metadata := "\n\n_**Metadata:**_\n"
	for _, meta := range fullResponse.Metadata {
		for key, value := range meta {
			metadata += fmt.Sprintf("%s: %v\n", key, value)
		}
	}
	reply := response["response"].(string) + metadata
	// We have the data, formulate a reply
	replyMessage := format.RenderMarkdown(reply, true, true)
	_, err = bot.sendHTMLNotice(ctx, room, replyMessage.FormattedBody)
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Couldn't send response back to user")
	}
	bot.Log.Info().Msg("Sent response back to user")

	cancelContext(ctx)

}

// handleSetAvatar handles the "avatar set" command by extracting the avatar URL from the message body,
// retrieving the home server URL from the bot's configuration, and using the matrix client to set the avatar URL.
// The URL needs to be the last part of a Matrix URI Ex: mxc://matrix.init.se/SDfskjw29ewi then SDfskjw29ewi is the part that should be used by the avatar set command
// The URL also need to point to an image that is *NOT* encrypted, as Matrix doesn't handle encrypted avatars. At least I couldn't get it to work.
// It logs an error message if setting the avatar fails.
func (bot *MatrixBot) handleSetAvatar(ctx context.Context, message *event.MessageEventContent, room id.RoomID, sender id.UserID) {
	body := message.Body

	rtrimEvent := strings.Split(body, "avatar set ")[1]
	imgUrl := strings.Split(rtrimEvent, " ")[0]
	homeServer := strings.Split(bot.Config.Homeserver, "https://")[1]

	err := bot.Client.SetAvatarURL(ctx, id.ContentURI{Homeserver: homeServer, FileID: imgUrl})
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Couldn't set avatar")
	}
}

func (bot *MatrixBot) handleQueryTtI(ctx context.Context, message *event.MessageEventContent, room id.RoomID, sender id.UserID) {

}

// SaveContext saves the context for a given room in the "Bot-Context" table of the database.
// If the room already has a context, it updates the existing context.
// Returns an error if there was a problem acquiring the database connection or inserting the context.
// Acquires a database connection, inserts or updates the context in the database, and releases the connection.
func (bot *MatrixBot) SaveContext(ctx context.Context, room id.RoomID) error {
	db, err := bot.Database.Acquire(ctx)
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Error acquiring database connection")
		return err
	}

	_, err = db.Exec(ctx, "INSERT INTO \"Bot-Context\" VALUES ($1, $2) ON CONFLICT(room) DO UPDATE SET context = EXCLUDED.context", room.String(), Contexts[room])
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Error inserting context to database")
	}

	db.Release()
	return nil
}

// LoadContext loads the context for a given room from the "Bot-Context" table
// of the database. It acquires a database connection, queries the context from
// the table, and then stores the context in the Contexts map.
// Returns an error if there was a problem acquiring the database connection,
// querying the table, or scanning the row.
func (bot *MatrixBot) LoadContext(ctx context.Context, room id.RoomID) error {
	db, err := bot.Database.Acquire(ctx)
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Error acquiring database connection")
		return err
	}
	row, err := db.Query(ctx, "SELECT context FROM \"Bot-Context\" WHERE room = $1", room.String())
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Error querying Bot-Context table")
		return err
	}

	for row.Next() {
		var roomContext []int
		var roomID string
		err = row.Scan(&roomID, &roomContext)
		if err != nil {
			bot.Log.Error().
				Err(err).
				Msg("Error scanning row context")
			return err
		}
		Contexts[id.RoomID(roomID)] = roomContext
	}

	db.Release()
	return nil
}

// CancelRunningHandlers cancels all currently running command handlers. It iterates over
// the runningCmds map and calls the cancel function associated with each command context.
// After canceling the function, it removes the entry from the runningCmds map.
func (bot *MatrixBot) CancelRunningHandlers(ctx context.Context) {
	for cmdCtx, cancelFunc := range runningCmds {
		(*cancelFunc)()
		delete(runningCmds, cmdCtx)
	}
}

// sendHTMLNotice sends an HTML formatted notice message to the specified room.
// It converts the HTML message to plain text using the format.HTMLToText function.
// Then it calls the SendMessageEvent method of the bot's Client to send the message.
// The message event type is set to MsgNotice and the message body and formatted body
// are set to the plain text and original HTML message, respectively.
// The message format is set to FormatHTML.
// If an error occurs while sending the message, it returns nil and the error.
// Otherwise, it returns the response from SendMessageEvent.
func (bot *MatrixBot) sendHTMLNotice(ctx context.Context, room id.RoomID, message string) (*mautrix.RespSendEvent, error) {
	plainMsg := format.HTMLToText(message)
	resp, err := bot.Client.SendMessageEvent(ctx, room, event.EventMessage, &event.MessageEventContent{
		MsgType:       event.MsgNotice,
		Body:          plainMsg,
		FormattedBody: message,
		Format:        event.FormatHTML,
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// Simple function to toggle the "... is typing" state
func (bot *MatrixBot) toggleTyping(ctx context.Context, room id.RoomID, isTyping bool) {
	_, err := bot.Client.UserTyping(ctx, room, isTyping, 60*time.Second)
	if err != nil {
		bot.Log.Error().Err(err).Msg("Error setting typing status")
		cancelContext(ctx)
		return
	}
}

// queryAI sends a question to the AI API and returns the response as a map[string]interface{}.
// It prepares the prompt, model, and system prompt (if available) based on the provided message and room.
// Then, it makes a POST request to the AI API with the constructed query and waits for the response.
// The response is read, parsed, and returned as a map[string]interface{}.
// The method also updates the bot's context based on the response, if available, and saves it to permanent storage.
// Finally, it toggles the typing status and returns the full response or an error if any occurred.
func (bot *MatrixBot) queryAI(ctx context.Context, message string, model string, room id.RoomID) (map[string]interface{}, error) {
	//Prepare prompt, model and if exist system prompt
	promptText := strings.ReplaceAll(strings.ReplaceAll(strings.TrimPrefix(message, "query "), bot.Name, ""), "  ", " ")
	rawQuery := NewQuery().
		WithModel(model).
		WithPrompt(promptText).
		WithStream(false)

	queryContext, ok := Contexts[room]
	if ok {
		rawQuery.WithContext(queryContext)
	}

	// Convert AIQuery to an io.Reader that http.Post can handle
	requestBody := rawQuery.ToIOReader()

	// Toogle the bot to be typing for 30 seconds periods before sending typing again
	c := make(chan bool)
	go func(c chan bool, bot *MatrixBot) {
		for {
			select {
			case <-c:
				return
			default:
				bot.Log.Debug().Msg("Sending typing as at least one room has asked the bot something")
				bot.toggleTyping(ctx, room, true)
				time.Sleep(30 * time.Second)
			}
		}
	}(c, bot)

	// Make the request to AI API
	client := http.Client{
		Timeout: 30 * time.Minute,
	}
	bot.Log.Info().Msg("Sending question to AI, waiting...")
	resp, err := client.Post(fmt.Sprintf("%s:%s/%s", bot.Config.AI.Host, bot.Config.AI.Port, bot.Config.AI.Endpoints["chat"].Url), "application/json", requestBody)
	if err != nil {
		c <- true
		if len(runningCmds) <= 1 {
			bot.toggleTyping(ctx, room, false)
		}
		bot.Log.Error().
			Err(err).
			Msg("Error querying AI")
		return nil, err
	}

	// We should now have the answer from the bot. Defer closing of the connection until we've read the data
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			bot.Log.Error().
				Err(err).
				Msg("Problem closing connection to AI")
		}
	}(resp.Body)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c <- true
		bot.toggleTyping(ctx, room, false)
		bot.Log.Error().Err(err).Msg("Error reading answer from AI")
		return nil, err
	}

	var fullResponse map[string]interface{}
	err = json.Unmarshal(body, &fullResponse)
	if err != nil {
		c <- true
		if len(runningCmds) <= 1 {
			bot.toggleTyping(ctx, room, false)
		}
		bot.Log.Error().Err(err).Msg("Can't unmarshal JSON response from AI")
		return nil, err
	}

	bot.Log.Info().Msg("Updating Context")
	var botContext []int
	if rawContext, ok := fullResponse["context"]; ok {
		bot.Log.Debug().Msg("Retrieving Context from AI")
		for _, v := range rawContext.([]interface{}) {
			botContext = append(botContext, int(v.(float64)))
		}
	}
	bot.Log.Debug().Msg("Updating Init Bot Context and saving to permanent storage")
	Contexts[room] = botContext
	err = bot.SaveContext(ctx, room)
	if err != nil {
		bot.Log.Warn().Err(err).Msg("Context will not survive restart")
	}

	// Toggle typing to false if we are the last ones running and then send reply
	c <- true
	if len(runningCmds) <= 1 {
		bot.toggleTyping(ctx, room, false)
	}

	return fullResponse, nil
}

// regexTrimLeft trims the left side of a string haystack using a regular expression pattern
// It matches the pattern "^.*(<bot.Name>).*(<needle>) *" and removes the matched substring from the haystack
// The result is returned after trimming any leading or trailing whitespace
// Parameters:
// - needle: the substring to search for and trim from the left side of haystack
// - haystack: the string to trim from the left side
// Returns:
// - the trimmed string
func (bot *MatrixBot) regexTrimLeft(needle string, haystack string) string {
	matchRegex := regexp.MustCompile("^.*(" + bot.Name + ").*" + needle + " *")
	return strings.TrimSpace(string(matchRegex.ReplaceAll([]byte(haystack), []byte(""))))
}
