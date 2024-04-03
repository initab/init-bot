// main.go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"init-bot/matrixbot"
	"io/ioutil"
	"os"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// Add struct for specific bot rather than generic one
type InitBot struct {
	*matrixbot.MatrixBot
}

type Config struct {
	Homeserver string `json:"homeserver"`
	Botname    string `json:"botname"`
	Username   string `json:"bot-username"`
	Password   string `json:"bot-password"`
	DB         DB     `json:"db"`
}

type DB struct {
	Host     string      `json:"host"`
	Port     json.Number `json:"port"`
	User     string      `json:"user"`
	Password string      `json:"password"`
	DB_name  string      `json:"db_name"`
	TZ       string      `json:"tz"`
}

var config Config

func main() {

	log := zerolog.New(zerolog.NewConsoleWriter(func(w *zerolog.ConsoleWriter) {
		w.Out = os.Stdout
		w.TimeFormat = time.Stamp
	})).With().Timestamp().Logger()

	jsonFile, err := os.Open("config.json")

	if err != nil {
		fmt.Println(err)
	}

	defer jsonFile.Close()

	byteValue, err := ioutil.ReadAll(jsonFile)

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

	fmt.Printf("Got homeserver: %s\n", config.Homeserver)

	bot, err := matrixbot.NewMatrixBot(config.Username, config.Password, config.Homeserver, config.Botname)

	if err != nil {
		fmt.Println("Couldn't initiate a bot:", err)
		return
	}

	var syncStopWait sync.WaitGroup
	syncStopWait.Add(1)

	go func() {
		err = bot.Client.SyncWithContext(bot.Context)
		defer syncStopWait.Done()
		if err != nil && !errors.Is(err, context.Canceled) {
			panic(err)
		}
	}()

	//initBot := InitBot{bot}

	fmt.Println("About to register commands")

	// Register a command like this
	//bot.RegisterCommand("!ping", int(0), "Sends a Ping to server", initBot.handlePing)

	for {
		//Loop forever. If you don't have anything that keeps running, the bot will exit.
	}
	bot.CancelFunc()
	syncStopWait.Wait()
	err = bot.CryptoHelper.Close()
	if err != nil {
		log.Error().Err(err).Msg("Error closing database")
	}
}

// Handles functions
// func (bot *InitBot) handlePing(message, room, sender string) {
// 	bot.SendTextToRoom(room, "pong!")
// }
