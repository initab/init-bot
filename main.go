// main.go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"init-bot/matrixbot"
	"init-bot/types"
	"io"
	"maunium.net/go/mautrix/event"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog"
)

// InitBot Expand Generic MatrixBot Struct with specific Init Bot SyncGroup
type InitBot struct {
	*matrixbot.MatrixBot
	StopAndSyncGroup sync.WaitGroup
}

var bot InitBot
var config types.Config

// main is the entry point of the application.
//
// It initializes a logger, defines and parses command line flags, reads configuration
// from a file, overrides configuration values with command line flags if set,
// sets the log level, creates an instance of the MatrixBot, and starts the main loop
// to keep the bot alive.
//
// The main loop sleeps for 1 minute, logs a message, and repeats indefinitely.
// Any errors during the execution of the bot are logged and handled appropriately.
func main() {
	log := zerolog.New(zerolog.NewConsoleWriter(func(w *zerolog.ConsoleWriter) {
		w.Out = os.Stdout
		w.TimeFormat = time.Stamp
	})).With().Timestamp().Logger()

	cQuit := make(chan os.Signal)
	signal.Notify(cQuit, os.Interrupt, syscall.SIGTERM)

	// Define flags to use
	configFile := flag.String("config", "./config.json", "Specify path, inculding file, to configuration file. EX: ./config.json")
	homeServer := flag.String("server", "", "Ovverride Homeserver URL from config file")
	botName := flag.String("bot-name", "", "Override Bot human friendly name from config")
	userName := flag.String("username", "", "Override Username for the bot to login in with from config")
	password := flag.String("password", "", "Override Password for the bot to login in with from config")
	dbHost := flag.String("db-host", "", "Override Database Host from config")
	dbPort := flag.Int("db-port", -1, "Override Database Port from config")
	dbUsername := flag.String("db-username", "", "Override Database Username from config")
	dbPassword := flag.String("db-password", "", "Override Database Password from config")
	dbName := flag.String("db-name", "", "Override Database Name from config")
	logLevel := flag.String("log-level", "", "Override Log Level for bot")
	timeout := flag.Int64("timeout", -1, "Override Timeout for bot waiting on AI response")

	flag.Parse()

	// Read configuration from file
	jsonFile, err := os.Open(*configFile)
	if err != nil {
		log.Error().
			Err(err).
			Msg("Problem opening config JSON file")
		syscall.Exit(1)
	}

	defer func(jsonFile *os.File) {
		err := jsonFile.Close()
		if err != nil {
			log.Error().
				Err(err).
				Msg("Problem closing the config JSON file")
		}
	}(jsonFile)

	byteValue, err := io.ReadAll(jsonFile)
	if err != nil {
		fmt.Println("Coudln't read config.json file")
		return
	}

	err = json.Unmarshal(byteValue, &config)
	if err != nil {
		log.Error().
			Err(err).
			Msg("Couldn't read JSON in config file")
	}

	// If any flags are set, override values from config
	if *homeServer != "" {
		config.Homeserver = *homeServer
	}
	if *botName != "" {
		config.Botname = *botName
	}
	if *userName != "" {
		config.Username = *userName
	}
	if *password != "" {
		config.Password = *password
	}
	if *dbHost != "" {
		config.DB.Host = *dbHost
	}
	if *dbPort != -1 {
		config.DB.Port = json.Number(strconv.FormatInt(int64(*dbPort), 10))
	}
	if *dbUsername != "" {
		config.DB.User = *dbUsername
	}
	if *dbPassword != "" {
		config.DB.Password = *dbPassword
	}
	if *dbName != "" {
		config.DB.DBName = *dbName
	}

	if *logLevel != "" {
		config.LogLevel = *logLevel
	}

	if *timeout != -1 {
		config.AI.Timeout = *timeout
	}

	// This loop iterates over each of the AI endpoints specified in the config.
	// If the host, port, num_results or response key of an endpoint is not provided,
	// it assigns them default values from the main AI config or default values. This ensures
	// that all endpoints have these necessary properties set. Some endpoints won't use them.
	// but that is a different matter.
	for k, v := range config.AI.Endpoints {
		if v.Host == "" {
			v.Host = config.AI.Host
		}
		if v.Port == "" {
			v.Port = config.AI.Port
		}
		if v.ResponseKey == "" {
			v.ResponseKey = "response"
		}

		if v.NumResults == "" {
			if v.NumResults == "" {
				v.NumResults = "10"
			}
		}
		config.AI.Endpoints[k] = v
	}

	level, err := zerolog.ParseLevel(strings.ToLower(config.LogLevel))
	if err != nil {
		log.Error().Err(err).Msg("Couldn't parse log level")
	} else {
		zerolog.SetGlobalLevel(level)
	}

	log.Info().Msgf("Got homeserver: %s", config.Homeserver)
	log.Debug().Msg("Setting up Bot")
	// Create the actual bot that will do the heavy lifting
	matrixBot, err := matrixbot.NewMatrixBot(config, &log)
	if err != nil {
		log.Error().Err(err).
			Msg("Couldn't initiate a bot")
		return
	}

	bot = InitBot{
		matrixBot,
		sync.WaitGroup{},
	}
	bot.StopAndSyncGroup.Add(1)

	go func() {
		bot.Log.Info().Msg("Start Sync")
		err = bot.Client.SyncWithContext(bot.Context)
		defer bot.StopAndSyncGroup.Done()
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Error().
				Err(err).
				Msg("There was an error while running Sync")
		}
		if err != nil && errors.Is(err, context.Canceled) {
			log.Info().Err(err).Msg("Context was cancelled")
		}
	}()

	bot.Log.Info().Msgf("Setting %s online and sending hello", bot.Name)
	err = bot.Client.SetPresence(bot.Context, event.PresenceOnline)
	if err != nil {
		bot.Log.Warn().Msg("Couldn't set 'Online' presence")
	}
	respRooms, err := bot.Client.JoinedRooms(bot.Context)
	if err != nil {
		bot.Log.Info().Msg("Couldn't get joined rooms. Won't say hello/goodbye")
	} else {
		for _, v := range respRooms.JoinedRooms {
			_, innerErr := bot.Client.SendNotice(bot.Context, v, "I'm Online again. Hello!")
			if innerErr != nil {
				bot.Log.Info().Err(err).Any("room", v).Msg("Couldn't say hello")
			}
		}
	}

	// Wait for the signal to quit the bot
	<-cQuit
	stopBot()
}

// stopBot stops the bot and performs cleanup tasks before exiting the application.
func stopBot() {
	// Run Cleanup
	bot.Log.Info().Msg("Will close down Bot")
	respRooms, err := bot.Client.JoinedRooms(bot.Context)
	if err != nil {
		bot.Log.Info().Msg("Couldn't get joined rooms. Won't say goodbye")
	} else {
		for _, v := range respRooms.JoinedRooms {
			_, innerErr := bot.Client.SendNotice(bot.Context, v, "Going offline. Bye!")
			if innerErr != nil {
				bot.Log.Info().Err(err).Any("room", v).Msgf("Couldn't say goodbye in room %s", v)
			}
		}
	}
	err = bot.Client.SetPresence(bot.Context, event.PresenceOffline)
	if err != nil {
		bot.Log.Warn().Msg("Couldn't set 'Offline' presence")
	}
	bot.CancelRunningHandlers(bot.Context)
	bot.Log.Debug().Msg("Stopping sync")
	bot.Client.StopSync()
	bot.Log.Debug().Msg("Logging out")
	bot.Log.Debug().Msg("Waiting for sync group to finish")
	bot.StopAndSyncGroup.Wait()
	bot.Log.Debug().Msg("Will cancel Go Context")
	bot.CancelFunc()
	bot.Log.Debug().Msg("Will close down Crypto Helper DB")
	err = bot.CryptoHelper.Close()
	if err != nil {
		bot.Log.Error().Err(err).Msg("Error closing crypto db")
	}
	bot.Log.Debug().Msg("Will close PG DB connection")
	bot.Database.Close()
	bot.Log.Info().Msg("Bot closed")
	os.Exit(0)
}
