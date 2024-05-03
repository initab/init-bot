package types

import (
	"encoding/json"
)

type Config struct {
	Homeserver string `json:"homeserver"`
	Botname    string `json:"botname"`
	Username   string `json:"bot-username"`
	Password   string `json:"bot-password"`
	LogLevel   string `json:"log-level"`
	DB         DB     `json:"db"`
	AI         AI     `json:"ai"`
}

type DB struct {
	Host     string      `json:"host"`
	Port     json.Number `json:"port"`
	User     string      `json:"user"`
	Password string      `json:"password"`
	DBName   string      `json:"db_name"`
}

type AI struct {
	Host      string              `json:"host"`
	Port      json.Number         `json:"port"`
	Endpoints map[string]Endpoint `json:"endpoints"`
	PromptKey string              `json:"prompt-key"`
}

type Endpoint struct {
	Url   string `json:"url"`
	Model string `json:"model"`
}

type VectorResponse struct {
	IDs       []string                 `json:"ids"`
	Documents []string                 `json:"documents"`
	Distances []float64                `json:"distances"`
	Metadata  []map[string]interface{} `json:"metadata"`
}

type TopicClassifications struct {
	Sequence string    `json:"sequence"`
	Labels   []string  `json:"labels"`
	Scores   []float64 `json:"scores"`
}
