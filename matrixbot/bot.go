package matrixbot

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"go.mau.fi/util/exzerolog"
	"init-bot/types"
	"io"
	"net/http"
	"os"
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
	Client       *mautrix.Client
	matrixPass   string
	matrixUser   string
	Handlers     []CommandHandler
	Name         string
	Context      context.Context
	CancelFunc   context.CancelFunc
	CryptoHelper *cryptohelper.CryptoHelper
	Log          zerolog.Logger
}

// CommandHandler struct to hold a pattern/command associated with the
// handling function and the needed minimum power of the user in the room
type CommandHandler struct {

	//Pattern to initiate the command
	Pattern string

	//The minimal power required to execute this command
	MinPower int

	//The function to handle this command
	Handler func(ctx context.Context, message string, room id.RoomID, sender id.UserID)

	//Help to be displayed for this command
	Help string
}

var Contexts map[id.RoomID]string

// NewMatrixBot creates a new bot form user credentials
func NewMatrixBot(config types.Config) (*MatrixBot, error) {

	cli, err := mautrix.NewClient(config.Homeserver, "", "")
	if err != nil {
		panic(err)
	}

	log := zerolog.New(zerolog.NewConsoleWriter(func(w *zerolog.ConsoleWriter) {
		w.Out = os.Stdout
		w.TimeFormat = time.Stamp
	})).With().Timestamp().Logger()

	exzerolog.SetupDefaults(&log)
	cli.Log = log

	bot := &MatrixBot{
		matrixPass: config.Password,
		matrixUser: config.Username,
		Client:     cli,
		Name:       config.Botname,
		Log:        log,
	}

	syncCtx, cancelSync := context.WithCancel(context.Background())
	bot.Context = syncCtx
	bot.CancelFunc = cancelSync

	// Setup event handling when bot syncs and gets certain types of events such as Room Invites and Messages
	_, err = SetupSyncer(bot)
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Problem setting up Syncer and Event handlers")
	}

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
	// Set the client crypto helper in order to automatically encrypt outgoing messages
	cli.Crypto = cryptoHelper

	// Log that we have started up and started listening
	log.Info().Msg("Now running")

	// Register the commands this bot should handle
	bot.RegisterCommand("help", 0, "Display this help", bot.handleCommandHelp)
	bot.RegisterCommand("", 0, "@init-bot with only the word 'help' to get help text. Type anything else to ask the AI", bot.handleQueryAI)

	return bot, nil
}

// RegisterCommand allows to register a command to a handling function
func (bot *MatrixBot) RegisterCommand(pattern string, minpower int, help string, handler func(ctx context.Context, message string, room id.RoomID, sender id.UserID)) {
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

// handleCommandHelp is the function that gets invoked when a message for the Bot is found. It then goes through the commands and tries to match the message with a command pattern
func (bot *MatrixBot) handleCommands(ctx context.Context, message string, room id.RoomID, sender id.UserID) {
	//Don't do anything if the sender is the bot itself
	if strings.Contains(sender.String(), bot.matrixUser) {
		bot.Log.Debug().Msg("Bots own message, ignore")
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
		if r.MatchString(message) {
			v.Handler(ctx, message, room, sender)
			handled = true
		}

	}
	if !handled {
		bot.Log.Debug().Msg("Could not find a pattern to handle, treat this as AIQuery")
		bot.Handlers[queryIndex].Handler(ctx, message, room, sender)
	}
}

// HandleCommandHelp Displays help text on how to handle the bot
func (bot *MatrixBot) handleCommandHelp(ctx context.Context, message string, room id.RoomID, sender id.UserID) {
	//TODO make this a markdown table?
	if len(message) > 0 {
		bot.Log.Info().Msg(message)
	}
	helpMsg := `The following commands are avaitible for this bot:

Command					Explanation
------------------------------------`

	for _, v := range bot.Handlers {
		helpMsg = helpMsg + "\n@init-bot " + v.Pattern + "\t\t\t\t\t" + v.Help
	}

	_, err := bot.Client.SendNotice(ctx, room, helpMsg)
	if err != nil {

	}
}

// handleQueryAI is the function to send queries to the associated AI and return the answer to the user
func (bot *MatrixBot) handleQueryAI(ctx context.Context, message string, room id.RoomID, sender id.UserID) {
	//Prepare prompt, model and if exist system prompt
	promptText := strings.ReplaceAll(strings.ReplaceAll(strings.TrimPrefix(message, "query "), bot.Name, ""), "  ", " ")
	rawQuery := NewQuery().
		WithModel("llama2").
		WithPrompt(promptText).
		WithStream(false)

	// Convert AIQuery to an io.Reader that http.Post can handle
	requestBody := rawQuery.ToIOReader()

	// Toogle the bot to be typing
	c := make(chan bool)
	go func(c chan bool, bot *MatrixBot) {
		for {
			select {
			case <-c:
				return
			default:
				bot.Log.Debug().Msg("Sending typing again")
				bot.toggleTyping(ctx, room, true)
				time.Sleep(15 * time.Second)
			}
		}
	}(c, bot)

	// Make the request
	client := http.Client{
		Timeout: 3600 * time.Second,
	}
	bot.Log.Debug().Msg("Sending question to AI")
	resp, err := client.Post("http://localhost:11434/api/generate", "application/json", requestBody)
	if err != nil {
		c <- true
		bot.toggleTyping(ctx, room, false)
		bot.Log.Error().Err(err).Msg("Error querying AI")
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
	}

	var fullResponse map[string]interface{}
	err = json.Unmarshal(body, &fullResponse)
	if err != nil {
		c <- true
		bot.toggleTyping(ctx, room, false)
		bot.Log.Error().Err(err).Msg("Can't unmarshal JSON response from AI")
	}

	response := fullResponse["response"]
	Contexts[room] = fullResponse["context"].(string)

	// Toggle typing to false and then send reply
	c <- true
	bot.toggleTyping(ctx, room, false)
	bot.Log.Debug().Msg("About to send AI answer")
	// We have the data, formulate a reply
	_, err = bot.Client.SendNotice(ctx, room, response.(string))
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Couldn't send response back to user")
	}
}

// Simple function to toggle the "... is typing" state
func (bot *MatrixBot) toggleTyping(ctx context.Context, room id.RoomID, isTyping bool) {
	_, err := bot.Client.UserTyping(ctx, room, isTyping, 15*time.Second)
	if err != nil {
		bot.Log.Error().Err(err).Msg("Error setting typing status")
	}
}

// Sync syncs the matrix events
func (bot *MatrixBot) Sync() {
	err := bot.Client.Sync()
	if err != nil {
		bot.Log.Error().
			Err(err).
			Msg("Sync() returned an error!")
	}
}
