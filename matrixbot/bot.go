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
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"net/http"
	"regexp"
	"strings"
	"syscall"
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

// RoomsWithTyping is a map that stores the typing status for each room in the bot.
var RoomsWithTyping = make(map[id.RoomID]int)

// RoomChannel is a map that stores a boolean channel for each room in the system.
var RoomChannel = make(map[id.RoomID]chan bool)

// EndOfText represents the end marker in the prompt for the queryAI function.
const EndOfText = "<|eot_id|>"

// HeaderStart is a constant that represents the starting string for a header in an AI prompt.
const HeaderStart = "<|start_header_id|>"

// HeaderEnd is a constant that represents the ending string for a header in an AI prompt.
const HeaderEnd = "<|end_header_id|>"

// UserPromptHeader is a formatted string that represents the header for a user prompt in an AI conversation. It combines the HeaderStart and HeaderEnd constants.
var UserPromptHeader = fmt.Sprintf("%suser%s%", HeaderStart, HeaderEnd)

// AssistantPromptHeader is a string variable that represents the header for an assistant prompt.
var AssistantPromptHeader = fmt.Sprintf("%sassistant%s%", HeaderStart, HeaderEnd)

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

// HandleRoomTyping handles the typing status of a specific room.
// It takes the room ID, a counter value, and a MatrixBot pointer as parameters.
// It retrieves the current counter value for the specified room from the RoomsWithTyping map.
// It then adds the new counter value to the old counter value and checks if it was the first or last workload in that room.
// The new counter value is logged using the MatrixBot's Debug logger.
// If the new counter value is less than 0, it is set to 0.
// If the new counter value is 1, it means a typing activity is starting in the room.
// A new channel is created for the room and a goroutine is started to handle the typing activity.
// If the new counter value is 0, it means the typing activity has ended in the room.
// The value "true" is sent to the channel associated with the room and the channel is deleted.
// Finally, the new counter value is updated in the RoomsWithTyping map.
func HandleRoomTyping(room id.RoomID, counter int, bot *MatrixBot) {
	var roomCount = 0
	roomCount = RoomsWithTyping[room]

	// Add new counter (±1) to old counter and see if this was the first or last workload in that room
	newCount := roomCount + counter
	bot.Log.Debug().Msgf("Counter for room %v is %d", room, newCount)
	if newCount < 0 {
		newCount = 0
	}
	if newCount == 1 {
		bot.Log.Debug().Msg("Starting up typing")
		RoomChannel[room] = make(chan bool)
		go startTyping(context.Background(), room, RoomChannel[room], bot)
	} else if newCount == 0 {
		bot.Log.Debug().Msg("Sending true to channel!")
		RoomChannel[room] <- true
		delete(RoomChannel, room)
	}
	// Update counter
	RoomsWithTyping[room] = newCount
}

// startTyping toggles the typing state of the bot in a specific room.
// It continues to send typing notifications until a signal is received on the channel 'c'.
// If the signal is received, it stops sending typing notifications and returns.
// It sleeps for 30 seconds between sending each typing notification.
// It logs a message when it starts and stops sending typing notifications.
func startTyping(ctx context.Context, room id.RoomID, c chan bool, bot *MatrixBot) {
	// Toogle the bot to be typing for 30 seconds periods before sending typing again
	for {
		select {
		case <-c:
			bot.Log.Info().Msg("Done typing")
			bot.toggleTyping(ctx, room, false)
			return
		default:
			bot.Log.Info().Msg("Sending typing as at least one room has asked the bot something")
			bot.toggleTyping(ctx, room, true)
			time.Sleep(30 * time.Second)
		}
	}
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

	// Set up the cryptohelper with a PG backend, so we can save crypto keys between restarts
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
		RoomsWithTyping[id.RoomID(roomID)] = 0
	}
	rows.Close()

	db.Release()

	// Set bots Config to use the config provided by the user
	bot.Config = config

	// Register the commands this bot should handle
	if config.AI.Endpoints["search"].Use {
		bot.RegisterCommand("sökning", 0, "Start message with 'search' to search SharePoint documents", bot.handleSearch)
	}
	if config.AI.Endpoints["image"].Use {
		bot.RegisterCommand("bildgenerering", 0, "Generate an image via AI and send to the Matrix Chat Room", bot.handleGenerateImage)
	}
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
		if message.MsgType == event.MsgImage {
			bot.Log.Debug().Any("message", message).Msg("Image message")
		}
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

	bot.Log.Debug().
		Any("RunnningCMDs", runningCmds).
		Msg("Running CMDs")
	// Make the request to AI API
	client := http.Client{
		Timeout: time.Duration(bot.Config.AI.Timeout) * time.Minute,
	}
	var handlerPatterns []string
	for _, handler := range bot.Handlers {
		if handler.Pattern != "" {
			handlerPatterns = append(handlerPatterns, handler.Pattern)
		}
	}
	bot.Log.Debug().Msgf("Patterns: %v", handlerPatterns)
	labelsStr, err := json.Marshal(handlerPatterns)
	if err != nil {
		bot.Log.Error().Err(err).Msg("Error marshaling handler patterns")
		cancelContext(ctx)
		return
	}
	data := "{\"text\": \"" + matchMessage + "\", \"labels\": " + string(labelsStr) + "}"
	bot.Log.Debug().Any("request", data).Msg("Sending Body data")
	// Build the URL to Classify AI from config
	classifyURL := bot.Config.AI.Endpoints["classify"].Host + ":" + string(bot.Config.AI.Endpoints["classify"].Port) + "/" + bot.Config.AI.Endpoints["classify"].Url
	resp, err := client.Post(classifyURL, "application/json", bytes.NewBuffer([]byte(data)))
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
		threshold, err := bot.Config.AI.ClassificationThreshold.Float64()
		if err != nil {
			bot.Log.Warn().
				Err(err).
				Msg("Error converting Json Number for Classification Threshold to Float64. Setting it to 85%")
			threshold = 0.85
		}
		for i, j := range fullResponse.Labels {
			if r.MatchString(j) && fullResponse.Scores[i] >= threshold {
				bot.Log.Info().Msgf("Handling message of type: %s", v.Pattern)
				handled = true
				cmdContext, cmdCancelFunc := context.WithTimeout(ctx, time.Duration(bot.Config.AI.Timeout)*time.Minute)
				runningCmds[cmdContext] = &cmdCancelFunc
				go v.Handler(cmdContext, message, room, sender)
			}
		}

	}
	if !handled {
		bot.Log.Debug().Msg("Could not find a pattern to handle, treat this as general chatting")
		cmdContext, cmdCancelFunc := context.WithTimeout(ctx, time.Duration(bot.Config.AI.Timeout)*time.Minute)
		runningCmds[cmdContext] = &cmdCancelFunc
		go bot.Handlers[queryIndex].Handler(cmdContext, message, room, sender)
	}
}

// handleQueryAI handles the processing of a query from a user and sends it to the AI for a response.
// It prepares the prompt, model, and system prompt (if available),
// makes the request to the AI API, reads and parses the response,
// updates the bot's context and saves it to permanent storage,
// and finally sends the response back to the user.
func (bot *MatrixBot) handleQueryAI(ctx context.Context, message *event.MessageEventContent, room id.RoomID, sender id.UserID) {
	defer cancelContext(ctx)
	// Handle bot typing
	HandleRoomTyping(room, 1, bot)
	defer HandleRoomTyping(room, -1, bot)

	response, err := bot.queryAI(ctx, message.Body, "", bot.Config.AI.Endpoints["chat"].Model, room)
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Couldn't query AI")
		return
	}

	//bot.Log.Debug().Any("response", response).Msg("AI response")

	// We have the data, formulate a reply
	_, err = bot.sendHTMLNotice(ctx, room, response[bot.Config.AI.Endpoints["chat"].ResponseKey].(string), &sender)
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Couldn't send response back to user")
	}
	bot.Log.Info().Msg("Sent response back to user")
	bot.toggleTyping(ctx, room, false)
}

// handleSearch performs a search using Ollama and ChromaDB
// It prepares the prompt text, Ollama and ChromaDB clients, and retrieves the collection from ChromaDB.
// Then it queries ChromaDB with the prompt text and sends the response to handleQueryAI function.
func (bot *MatrixBot) handleSearch(ctx context.Context, message *event.MessageEventContent, room id.RoomID, sender id.UserID) {
	defer cancelContext(ctx)
	// Handle bot typing
	HandleRoomTyping(room, 1, bot)
	defer HandleRoomTyping(room, -1, bot)

	bot.Log.Debug().Msg("Handling a Search query")
	//Prepare prompt, model, and if it exists, system prompt
	promptText := bot.regexTrimLeft(message.Body)

	client := http.Client{
		Timeout: time.Duration(bot.Config.AI.Timeout) * time.Minute,
	}

	data := fmt.Sprintf("{\"prompt\": \"%s\", \"num_results\": %s}", promptText, bot.Config.AI.Endpoints["search"].NumResults)
	bot.Log.Debug().Any("request", data).Msg("Sending Body data")
	// Build searchURL string from config
	searchURL := bot.Config.AI.Endpoints["search"].Host + ":" + string(bot.Config.AI.Endpoints["search"].Port) + "/" + bot.Config.AI.Endpoints["search"].Url
	resp, err := client.Post(searchURL, "application/json", bytes.NewBuffer([]byte(data)))
	if err != nil {
		bot.Log.Error().Err(err).Msg("Error making request to Vector Search API")
		return
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			bot.Log.Error().Err(err).Msg("An error occurred closing Body")
		}
	}(resp.Body)

	// Check if the response status code is not 200
	if resp.StatusCode != http.StatusOK {
		bot.Log.Error().Msgf("Vector Search API returned non-200 status code: %d", resp.StatusCode)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		bot.Log.Error().Err(err).Msg("Error reading answer from Vector search")
		return
	}

	var fullResponse types.VectorResponse
	err = json.Unmarshal(body, &fullResponse)
	if err != nil {
		bot.Log.Error().Err(err).Msg("Can't unmarshal JSON response from Vector search")
		return
	}

	var qualifiedDocs []int
	if bot.Config.AI.Endpoints["rank"].Use {
		bot.Log.Debug().Msg("Qualifying docs")
		qualifiedDocs = bot.qualifySearch(fullResponse.Documents, promptText)
	} else {
		bot.Log.Debug().Msg("NOT Qualifying docs, all will be returned")
		qualifiedDocs = make([]int, 0, len(fullResponse.Documents))
		for i := range qualifiedDocs {
			qualifiedDocs[i] = i
		}
	}
	bot.Log.Debug().Msgf("Index of qualified docs: %v", qualifiedDocs)

	var documents string
	for _, docIndex := range qualifiedDocs {
		documents = documents + "\n\n" + fullResponse.Documents[docIndex]
		bot.Log.Debug().Msgf("Document index: %v, Titel: %s", docIndex, fullResponse.Metadata[docIndex]["title"])
	}

	promptAI := fmt.Sprintf("Använd denna data:\n%s\n För att svara på denna fråga: %s",
		documents, promptText)

	response, err := bot.queryAI(ctx, promptAI, "", bot.Config.AI.Endpoints["chat"].Model, room)
	if err != nil {
		bot.Log.Error().Err(err).Msg("Error using Search response in AI query")
	}

	var urls = make(map[string]bool)
	metadata := "\n\n_**Metadata:**_\n"
	for _, docIndex := range qualifiedDocs {
		meta := fullResponse.Metadata[docIndex]
		for key, value := range meta {
			if key == "url" {
				urls[value.(string)] = true
			}
		}
	}

	for url := range urls {
		metadata += fmt.Sprintf("url: %v\n", url)
	}

	reply := response[bot.Config.AI.Endpoints["chat"].ResponseKey].(string) + metadata
	// We have the data, formulate a reply
	replyMessage := format.RenderMarkdown(reply, true, true)
	_, err = bot.sendHTMLNotice(ctx, room, replyMessage.FormattedBody, &sender)
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Couldn't send response back to user")
	}
	bot.Log.Info().Msg("Sent response back to user")
	bot.toggleTyping(ctx, room, false)
	return
}

// handleGenerateImage handles a request to generate an image based on a prompt text.
// It sends a notice message to the user indicating that the image is being generated.
// Then, it makes a request to the AI API with the prompt text and receives the generated image.
// The image is encrypted and uploaded to the Matrix server using the client's media upload endpoint.
// Finally, it sends a message event containing the generated image to the specified room.
func (bot *MatrixBot) handleGenerateImage(ctx context.Context, message *event.MessageEventContent, room id.RoomID, sender id.UserID) {
	defer cancelContext(ctx)
	HandleRoomTyping(room, 1, bot)
	defer HandleRoomTyping(room, -1, bot)

	bot.Log.Debug().Msg("Handling a Generate Image query")
	//Prepare prompt, model and if exist system prompt
	promptText := bot.regexTrimLeft(message.Body)

	_, err := bot.sendHTMLNotice(ctx, room, "<i>Som jag har förstått det vill du få en bild genererad. "+
		"Jag håller på med det och återkommer med bilden strax...</i>", &sender)

	if err != nil {
		bot.Log.Warn().Msg("Couldn't inform user image is being generated")
	}

	// Make the request to AI API
	client := http.Client{
		Timeout: time.Duration(bot.Config.AI.Timeout) * time.Minute,
	}

	data := "{\"prompt\": \"" + promptText + "\", \"inference_steps\": 50, \"guidance_scale\": 10.5}"
	bot.Log.Debug().Any("request", data).Msg("Sending Body data ")
	// Build searchURL string from config
	searchURL := bot.Config.AI.Endpoints["image"].Host + ":" + string(bot.Config.AI.Endpoints["image"].Port) + "/" + bot.Config.AI.Endpoints["image"].Url
	resp, err := client.Post(searchURL, "application/json", bytes.NewBuffer([]byte(data)))
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Error making request to Image generation API")
		return
	}
	// defer closing of the body to close off HTTP session
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			bot.Log.Error().
				Err(err).
				Msg("An error occurred closing Body")
			return
		}
	}(resp.Body)

	// First we need a MXC URI from the server to upload to
	MXCResponse, err := bot.Client.CreateMXC(ctx)
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Error getting MXC URI response")
		return
	}

	// Create encrypted file object to hold the image
	encryptedFile := attachment.NewEncryptedFile()

	//Read image from imgData
	encCloser := encryptedFile.EncryptStream(resp.Body)

	// Save image into imgData variable
	imgEncData, err := io.ReadAll(encCloser)
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Error reading response from encryption stream")
		return
	}
	err = encCloser.Close()
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Error encrypting image")
		return
	}

	mediaUpload := mautrix.ReqUploadMedia{
		ContentBytes:      imgEncData,
		ContentLength:     int64(len(imgEncData)),
		ContentType:       "image/jpeg",
		MXC:               MXCResponse.ContentURI,
		UnstableUploadURL: MXCResponse.UnstableUploadURL,
	}

	_, err = bot.Client.UploadMedia(ctx, mediaUpload)
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Error uploading image to Matrix")
		return
	}

	encFileInfo := event.EncryptedFileInfo{
		EncryptedFile: *encryptedFile,
		URL:           MXCResponse.ContentURI.CUString(),
	}

	content := event.MessageEventContent{
		MsgType:       event.MsgImage,
		Body:          "Image of: " + promptText,
		Format:        event.FormatHTML,
		FormattedBody: "Image of: " + promptText,
		File:          &encFileInfo,
		Info: &event.FileInfo{
			MimeType: "image/jpeg",
			Width:    1024,
			Height:   1024,
		},
		URL:      MXCResponse.ContentURI.CUString(),
		FileName: "generated.jpg",
		Mentions: &event.Mentions{
			UserIDs: []id.UserID{sender},
		},
	}

	bot.Log.Debug().Any("content", content).Msg("Image content")

	_, err = bot.Client.SendMessageEvent(ctx, room, event.EventMessage, content)
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Error sending image to Matrix")
	}
	bot.toggleTyping(ctx, room, false)
	return
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
func (bot *MatrixBot) CancelRunningHandlers() {
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
func (bot *MatrixBot) sendHTMLNotice(ctx context.Context, room id.RoomID, message string, at *id.UserID) (*mautrix.RespSendEvent, error) {
	plainMsg := format.HTMLToText(message)
	var mention event.Mentions
	if at != nil {
		mention = event.Mentions{
			UserIDs: []id.UserID{*at},
		}
	} else {
		mention = event.Mentions{}
	}
	resp, err := bot.Client.SendMessageEvent(ctx, room, event.EventMessage, &event.MessageEventContent{
		MsgType:       event.MsgNotice,
		Body:          plainMsg,
		FormattedBody: message,
		Format:        event.FormatHTML,
		Mentions:      &mention,
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
func (bot *MatrixBot) queryAI(ctx context.Context, message string, system string, model string, room id.RoomID) (map[string]interface{}, error) {
	//Prepare prompt, model and if exist system prompt
	promptText := strings.ReplaceAll(strings.ReplaceAll(strings.TrimPrefix(message, "query "), bot.Name, ""), "  ", " ")
	prompt := fmt.Sprintf("%s\n%s\n%s\n%s", UserPromptHeader, promptText, EndOfText, AssistantPromptHeader)
	rawQuery := NewQuery().
		WithModel(model).
		WithPrompt(prompt).
		WithStream(false).
		WithSystem(system)

	queryContext, ok := Contexts[room]
	if ok {
		rawQuery.WithContext(queryContext)
	}

	// Convert AIQuery to an io.Reader that http.Post can handle
	requestBody := rawQuery.ToIOReader()

	// Make the request to AI API
	client := http.Client{
		Timeout: time.Duration(bot.Config.AI.Timeout) * time.Minute,
	}
	bot.Log.Info().Msg("Sending question to AI, waiting...")
	//bot.Log.Debug().Msgf("Data: %s", requestBody)
	resp, err := client.Post(fmt.Sprintf("%s:%s/%s", bot.Config.AI.Endpoints["chat"].Host,
		bot.Config.AI.Endpoints["chat"].Port, bot.Config.AI.Endpoints["chat"].Url),
		"application/json", requestBody)
	if err != nil {
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
		bot.Log.Error().Err(err).Msg("Error reading answer from AI")
		return nil, err
	}

	var fullResponse map[string]interface{}
	err = json.Unmarshal(body, &fullResponse)
	if err != nil {
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
	go func() {
		err := bot.SaveContext(ctx, room)
		if err != nil {
			bot.Log.Warn().Err(err).Msg("Context will not survive restart")
		}
	}()

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
func (bot *MatrixBot) regexTrimLeft(haystack string) string {
	matchRegex := regexp.MustCompile("^.*" + bot.Name + "[,:.]* ")
	return strings.TrimSpace(string(matchRegex.ReplaceAll([]byte(haystack), []byte(""))))
}

// qualifySearch sends a POST request to the rankURL with a payload containing the list of documents and user prompt.
// It checks the response status code and reads the response body.
// If the response status code is not 200, it logs an error message and returns an empty approvedDocs slice.
// It then unmarshals the response body into a slice of types.Rank structs and closes the response body.
// For each rank in the respRank slice, it logs the document ID and score, and if the score is greater than 0.0,
// it appends the document ID to the approvedDocs slice. Finally, it returns the approvedDocs slice.
func (bot *MatrixBot) qualifySearch(documents []string, userPrompt string) []int {
	rankURL := bot.Config.AI.Endpoints["rank"].Host + ":" + string(bot.Config.AI.Endpoints["rank"].Port) + "/" + bot.Config.AI.Endpoints["rank"].Url
	var approvedDocs = make([]int, 0, len(documents))
	client := http.Client{
		Timeout: time.Duration(bot.Config.AI.Timeout) * time.Minute,
	}
	// Empty context
	_, err := client.Post(rankURL, "application/json", strings.NewReader(""))
	if err != nil {
		bot.Log.Error().Err(err).Msg("Error emptying context for qualify url")
		return approvedDocs
	}
	payload, err := json.Marshal(map[string]interface{}{
		"documents": documents,
		"question":  userPrompt,
	})
	if err != nil {
		bot.Log.Error().Err(err).Msg("Error marshaling qualify payload")
		return make([]int, 0)
	}
	resp, ierr := client.Post(rankURL, "application/json", bytes.NewBuffer(payload))
	if ierr != nil {
		bot.Log.Warn().Err(ierr).Msg("Couldn't get response from qualify")
		return make([]int, 0)
	}

	// Check if the response status code is not 200
	if resp.StatusCode != http.StatusOK {
		bot.Log.Error().Msgf("Qualify API returned non-200 status code: %d", resp.StatusCode)
		return make([]int, 0)
	}

	body, ierr := io.ReadAll(resp.Body)
	if ierr != nil {
		bot.Log.Error().Err(ierr).Msg("Couldn't read response from qualify")
		return make([]int, 0)
	}

	var respRank []types.Rank
	ierr = json.Unmarshal(body, &respRank)
	if ierr != nil {
		bot.Log.Error().Err(ierr).Msg("Couldn't unmarshal response from qualify")
	}

	err = resp.Body.Close()
	if err != nil {
		return make([]int, 0)
	}

	for _, rank := range respRank {
		score, _ := rank.Score.Float64()
		bot.Log.Debug().Msgf("Document ID: %d, Document score: %f", int(rank.CorpusId), score)
		if score > bot.Config.AI.Endpoints["rank"].Threshold {
			approvedDocs = append(approvedDocs, int(rank.CorpusId))
		}
	}
	return approvedDocs
}
