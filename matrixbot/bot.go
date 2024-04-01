package matrixbot

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/exzerolog"

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
	CryptoHelper cryptohelper.CryptoHelper
}

// CommandHandler struct to hold a pattern/command asocciated with the
// handling funciton and the needed minimum power of the user in the room
type CommandHandler struct {

	//The pattern or command to handle
	Pattern string

	//The minimal power requeired to execute this command
	MinPower int

	//The function to handle this command
	Handler func(message, room, sender string)

	//Help to be displayed for this command
	Help string
}

func (bot *MatrixBot) getUserPower(ctx context.Context, room id.RoomID, user string) int {

	powerLevels := struct {
		Users   map[string]int `json:"users"`
		Default int            `json:"users_default"`
	}{}

	if err := bot.Client.StateEvent(ctx, room, event.StatePowerLevels, "", &powerLevels); err != nil {
		bot.Client.Log.Fatal().
			Msg(err.Error())
	}

	//Return the users power or the default user power, if not found
	if power, ok := powerLevels.Users[user]; ok {
		bot.Client.Log.Debug().
			Msg(fmt.Sprintf("Found %s found in %v, his power is %v", user, powerLevels.Users, power))
		return power
	}
	bot.Client.Log.Debug().
		Msg(fmt.Sprintf("User %s not found in %v", user, powerLevels.Users))
	return powerLevels.Default
}

// RegisterCommand allows to register a command to a handling function
func (bot *MatrixBot) RegisterCommand(pattern string, minpower int, help string, handler func(message string, room string, sender string)) {
	mbch := CommandHandler{
		Pattern:  bot.Name + " " + pattern,
		MinPower: minpower,
		Handler:  handler,
		Help:     help,
	}
	bot.Client.Log.Debug().
		Msg(fmt.Sprintf("Registered command: %s [%v]", mbch.Pattern, mbch.MinPower))
	bot.Handlers = append(bot.Handlers, mbch)
}

func (bot *MatrixBot) handleCommands(ctx context.Context, message string, room id.RoomID, sender id.UserID) {

	//Don't do anything if the sender is the bot itself
	//TODO edge-case: bot has the same name as a user but on a different server
	if strings.Contains(sender.String(), bot.matrixUser) {
		return
	}

	//userPower := bot.getUserPower(ctx, room, sender)

	for _, v := range bot.Handlers {
		r, _ := regexp.Compile(v.Pattern)
		if r.MatchString(message) {
			if v.MinPower <= bot.getUserPower(ctx, room, sender.String()) {
				v.Handler(message, room.String(), sender.String())
			} else {
				//bot.SendTextToRoom(ctx, room, "You have not enough power to execute this command (!"+v.Pattern+").\nYour power: "+strconv.Itoa(userPower)+"\nRequired: "+strconv.Itoa(v.MinPower))
			}
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
			Msg(fmt.Sprintf("Sync() returned %s", err))
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
		log.Debug().
			Msg(fmt.Sprintf("%s said \"%s\" in room %s", ev.Sender, ev.Content.Raw["body"].(map[string]interface{}), ev.RoomID))
		bot.handleCommands(bot.Context, ev.Content.Raw["body"].(string), ev.RoomID, ev.Sender)

	})

	//Handle member events (kick, invite)
	syncer.OnEventType(event.StateMember, func(ctx context.Context, ev *event.Event) {
		log.Debug().
			Msg(fmt.Sprintf("%s invited bot to %s", ev.Sender, ev.RoomID))
		if ev.Content.Raw["membership"] == "invite" {
			log.Debug().
				Msg(fmt.Sprintf("Joining Room %s", ev.RoomID))
			if resp, err := cli.JoinRoom(ctx, ev.RoomID.String(), "", nil); err != nil {
				log.Fatal().
					Str("error", err.Error()).
					Msg("Problem joining room")
			} else {
				log.Debug().
					Msg(fmt.Sprintf("Joined room %s", resp.RoomID))
			}
		}
	})

	syncer.OnEventType(event.StatePowerLevels, func(ctx context.Context, ev *event.Event) {
		log.Debug().
			Msg("got powerlevel event")
		log.Debug().
			Msg(ev.Content.Raw["Body"].(string))

	})

	cryptoHelper, err := cryptohelper.NewCryptoHelper(cli, []byte("meow"), "./bot_crypto_db.sqllight")
	if err != nil {
		panic(err)
	}

	bot.CryptoHelper = *cryptoHelper

	log.Debug().Msgf("User: %s", user)
	log.Debug().Msgf("Password: %s", pass)

	cryptoHelper.LoginAs = &mautrix.ReqLogin{
		Type:       mautrix.AuthTypeToken,
		Identifier: mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: user},
		Token:      pass,
	}

	err = cryptoHelper.Init(context.TODO())
	if err != nil {
		panic(err)
	}
	// Set the client crypto helper in order to automatically encrypt outgoing messages
	cli.Crypto = cryptoHelper

	log.Info().Msg("Now running")

	//bot.RegisterCommand("help", 0, "Display this help", bot.handleCommandHelp)
	return bot, nil
}

func (bot *MatrixBot) handleCommandHelp(message, room, sender string) {
	//TODO make this a markdown table?

	helpMsg := `The following commands are avaitible for this bot:

Command			Power required		Explanation
----------------------------------------------------------------`

	for _, v := range bot.Handlers {
		helpMsg = helpMsg + "\n!" + v.Pattern + "\t\t\t[" + strconv.Itoa(v.MinPower) + "]\t\t\t\t\t" + v.Help
	}

	//bot.SendTextToRoom(room, helpMsg)
}
