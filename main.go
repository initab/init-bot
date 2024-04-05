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
	"os"
	"os/signal"
	"strconv"
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

func main() {
	log := zerolog.New(zerolog.NewConsoleWriter(func(w *zerolog.ConsoleWriter) {
		w.Out = os.Stdout
		w.TimeFormat = time.Stamp
	})).With().Timestamp().Logger()

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

	log.Info().Msgf("Got homeserver: %s", config.Homeserver)

	// Create the actual bot that will do the heavy lifting
	matrixBot, err := matrixbot.NewMatrixBot(config)
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
		err = bot.Client.SyncWithContext(bot.Context)
		defer bot.StopAndSyncGroup.Done()
		if err != nil && !errors.Is(err, context.Canceled) {
			panic(err)
		}
	}()

	// Main loop, keep this alive to keep bot alive
	for {
		time.Sleep(1 * time.Minute)
		bot.Log.Debug().Msg("Alive")
	}

}

func init() {
	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		// Run Cleanup
		bot.Log.Debug().Msg("Will close down Bot")
		bot.Context.Done()
		bot.StopAndSyncGroup.Done()
		bot.CancelFunc()
		bot.StopAndSyncGroup.Wait()
		err := bot.CryptoHelper.Close()
		if err != nil {
			bot.Log.Error().Err(err).Msg("Error closing database")
		}
		os.Exit(0)
	}()
}
