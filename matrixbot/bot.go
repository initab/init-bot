package matrixbot

import (
	"context"
	"encoding/json"
	"github.com/rs/zerolog"
	"go.mau.fi/util/exzerolog"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

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

// sendMessage is the internal method to call when the bot is ready to send a message to a room in Matrix
// Needs a Context, the RoomID to send the message to, and a String representation of the message to send
func (bot *MatrixBot) sendMessage(ctx context.Context, room id.RoomID, message string) {
	content := event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    message,
	}
	_, err := bot.Client.SendMessageEvent(ctx, room, event.EventMessage, content)
	if err != nil {
		bot.Client.Log.Error().Err(err).Msg("Error sending Message to Matrix!")
	}
}

// RegisterCommand allows to register a command to a handling function
func (bot *MatrixBot) RegisterCommand(pattern string, minpower int, help string, handler func(ctx context.Context, message string, room id.RoomID, sender id.UserID)) {
	mbch := CommandHandler{
		Pattern:  pattern,
		MinPower: minpower,
		Handler:  handler,
		Help:     help,
	}
	bot.Client.Log.Debug().
		Msgf("Registered command: %s [%v]", mbch.Pattern, mbch.MinPower)
	bot.Handlers = append(bot.Handlers, mbch)
}

func (bot *MatrixBot) handleCommands(ctx context.Context, message string, room id.RoomID, sender id.UserID) {

	//Don't do anything if the sender is the bot itself
	//TODO edge-case: bot has the same name as a user but on a different server
	if strings.Contains(sender.String(), bot.matrixUser) {
		bot.Client.Log.Debug().Msg("Bots own message, ignore")
		return
	}

	handled := false
	for _, v := range bot.Handlers {
		r, _ := regexp.Compile(v.Pattern)
		if r.MatchString(message) {
			v.Handler(ctx, message, room, sender)
			handled = true
		}
		if !handled {
			bot.Client.Log.Info().Msgf("'%s' is not a recognized command", v.Pattern)
		}
	}
}

// Sync syncs the matrix events
func (bot *MatrixBot) Sync() {
	if err := bot.Client.Sync(); err != nil {
		log := zerolog.New(zerolog.NewConsoleWriter(func(w *zerolog.ConsoleWriter) {
			w.Out = os.Stdout
			w.TimeFormat = time.Stamp
		})).With().Timestamp().Logger()
		log.Warn().
			Msgf("Sync() returned %s", err)
	}
}

// NewMatrixBot creates a new bot form user credentials
func NewMatrixBot(user string, pass string, host string, name string) (*MatrixBot, error) {

	cli, err := mautrix.NewClient(host, "", "")
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
		matrixPass: pass,
		matrixUser: user,
		Client:     cli,
		Name:       name,
	}

	syncCtx, cancelSync := context.WithCancel(context.Background())
	bot.Context = syncCtx
	bot.CancelFunc = cancelSync

	syncer := cli.Syncer.(*mautrix.DefaultSyncer)

	//Handle messages send to the channel
	syncer.OnEventType(event.EventMessage, func(ctx context.Context, ev *event.Event) {
		messageEvent := ev.Content.AsMessage()

		if messageEvent.MsgType == event.MsgNotice {
			log.Debug().Msg("Notice, do nothing")
			return
		}

		botUser := bot.Client.UserID

		if messageEvent.Mentions.UserIDs != nil && slices.Contains(messageEvent.Mentions.UserIDs, botUser) {
			log.Debug().
				Msgf("%s said \"%s\" in room %s", ev.Sender, messageEvent.Body, ev.RoomID)
			if messageEvent.Body == bot.Name+" help" {
				bot.handleCommands(bot.Context, messageEvent.Body, ev.RoomID, ev.Sender)
			} else {
				bot.handleCommands(bot.Context, "query "+messageEvent.Body, ev.RoomID, ev.Sender)
			}

		} else {
			log.Debug().Msg("Message not for bot")
		}

	})

	//Handle member events (kick, invite)
	syncer.OnEventType(event.StateMember, func(ctx context.Context, ev *event.Event) {
		eventMember := ev.Content.AsMember()
		log.Debug().
			Msgf("%s changed bot membership status in %s", ev.Sender, ev.RoomID)
		if eventMember.Membership.IsInviteOrJoin() {
			log.Debug().
				Msgf("Joining Room %s", ev.RoomID)
			if resp, err := cli.JoinRoom(ctx, ev.RoomID.String(), "", nil); err != nil {
				log.Fatal().
					Str("error", err.Error()).
					Msg("Problem joining room")
			} else {
				log.Debug().
					Msgf("Joined room %s", resp.RoomID)
			}
		} else if eventMember.Membership.IsLeaveOrBan() {
			log.Debug().
				Msgf("Kicked from room %S", ev.RoomID)
			log.Info().
				Msgf("Kicked from %s for reason: %s", ev.RoomID, eventMember.Reason)
		}
	})

	syncer.OnEventType(event.StatePowerLevels, func(ctx context.Context, ev *event.Event) {
		log.Debug().
			Msg("Got powerlevel event")
	})

	cryptoHelper, err := cryptohelper.NewCryptoHelper(cli, []byte("meow"), "./bot_crypto_db.sqllight")
	if err != nil {
		panic(err)
	}

	bot.CryptoHelper = cryptoHelper

	log.Debug().Msgf("User: %s", user)
	log.Debug().Msgf("Password: %s", pass)

	cryptoHelper.LoginAs = &mautrix.ReqLogin{
		Type:       mautrix.AuthTypePassword,
		Identifier: mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: user},
		Password:   pass,
	}

	err = cryptoHelper.Init(syncCtx)
	if err != nil {
		panic(err)
	}
	// Set the client crypto helper in order to automatically encrypt outgoing messages
	cli.Crypto = cryptoHelper

	// Log that we have started up and started listening
	log.Info().Msg("Now running")

	// Register the commands this bot should handle
	bot.RegisterCommand("help", 0, "Display this help", bot.handleCommandHelp)
	bot.RegisterCommand("^query", 0, "Query the AI", bot.QueryAI)

	return bot, nil
}

// Displays help text on how to handle the bot
func (bot *MatrixBot) handleCommandHelp(ctx context.Context, message string, room id.RoomID, sender id.UserID) {
	//TODO make this a markdown table?
	if len(message) > 0 {
		bot.Client.Log.Info().Msg(message)
	}
	helpMsg := `The following commands are avaitible for this bot:

Command			Power required		Explanation
----------------------------------------------------------------`

	for _, v := range bot.Handlers {
		helpMsg = helpMsg + "\n!" + v.Pattern + "\t\t\t[" + strconv.Itoa(v.MinPower) + "]\t\t\t\t\t" + v.Help
	}

	bot.sendMessage(ctx, room, helpMsg)
}

func (bot *MatrixBot) QueryAI(ctx context.Context, message string, room id.RoomID, sender id.UserID) {
	//TODO acrually query an AI somewhere
	//for now echo the message back

	//Prepare prompt, model and if exist system prompt
	promptText := strings.ReplaceAll(strings.ReplaceAll(strings.TrimPrefix(message, "query "), bot.Name, ""), "  ", " ")
	rawQuery := NewQuery()
	rawQuery.WithModel("llama2")
	rawQuery.WithPrompt(promptText)
	rawQuery.WithStream(false)

	// Convert AIQuery to a io.REader that http.Post can handle
	requestBody := rawQuery.MakePost()

	// Make the request
	resp, err := http.Post("http://localhost:11434/api/generate", "application/json", requestBody)
	if err != nil {
		bot.Client.Log.Error().Err(err).Msg("Error querying AI")
	}

	// We should now have the answer from the bot. Defer closing of the connection until we've read the data
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		bot.Client.Log.Error().Err(err).Msg("Error reading answer from AI")
	}

	var fullResponse map[string]interface{}
	err = json.Unmarshal(body, &fullResponse)
	if err != nil {
		bot.Client.Log.Error().Err(err).Msg("Can't unmarshal JSON response from AI")
	}

	response := fullResponse["response"]

	// We have the data, formulate a reply
	bot.sendMessage(ctx, room, response.(string))
}
