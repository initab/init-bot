package types

import (
	"encoding/json"
	"maunium.net/go/mautrix/id"
)

type Config struct {
	Homeserver     string `json:"homeserver"`
	Botname        string `json:"botname"`
	Username       string `json:"bot-username"`
	Password       string `json:"bot-password"`
	LogLevel       string `json:"log-level"`
	ContextHandler string `json:"context-handler"`
	DB             DB     `json:"db"`
	AI             AI     `json:"ai"`
}

type DB struct {
	Host     string      `json:"host"`
	Port     json.Number `json:"port"`
	User     string      `json:"user"`
	Password string      `json:"password"`
	DBName   string      `json:"db_name"`
}

type AI struct {
	Host                    string              `json:"host"`
	Port                    json.Number         `json:"port"`
	ClassificationThreshold json.Number         `json:"classification_threshold"`
	Timeout                 int64               `json:"timeout"`
	Endpoints               map[string]Endpoint `json:"endpoints"`
}

type Endpoint struct {
	Url         string      `json:"url"`
	Model       string      `json:"model,omitempty"`
	Host        string      `json:"host,omitempty"`
	Port        json.Number `json:"port,omitempty"`
	ResponseKey string      `json:"response_key,omitempty"`
	NumResults  json.Number `json:"num_results,omitempty"`
	Threshold   float64     `json:"threshold,omitempty"`
	Use         bool        `json:"use,omitempty"`
	CharLimit   int         `json:"character_limit,omitempty"`
}

func (e Endpoint) GetModelRef() *string {
	return &e.Model
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

type Rank struct {
	CorpusId int64       `json:"corpus_id"`
	Score    json.Number `json:"score"`
}

type Content struct {
	Text     string `json:"text"`
	Image    []byte `json:"image,omitempty"`
	Document []byte `json:"document,omitempty"`
}

type Message struct {
	Role    string    `json:"role"`
	Content []Content `json:"content"`
}

type RoomContext struct {
	Room     id.RoomID `json:"room"`
	Tokens   []int     `json:"tokens"`
	Messages []Message `json:"message"`
}

type TitanImageRequest struct {
	TaskType              string                `json:"taskType"`
	TextToImageParams     TextToImageParams     `json:"textToImageParams"`
	ImageGenerationConfig ImageGenerationConfig `json:"imageGenerationConfig"`
}
type TextToImageParams struct {
	Text string `json:"text"`
}
type ImageGenerationConfig struct {
	NumberOfImages int     `json:"numberOfImages"`
	Quality        string  `json:"quality"`
	CfgScale       float64 `json:"cfgScale"`
	Height         int     `json:"height"`
	Width          int     `json:"width"`
	Seed           int64   `json:"seed"`
}

type TitanImageResponse struct {
	Images []string `json:"images"`
}
