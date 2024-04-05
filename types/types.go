package types

import (
	"encoding/json"
)

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
	DBName   string      `json:"db_name"`
}
