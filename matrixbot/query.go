package matrixbot

import (
	"bytes"
	"encoding/json"
	"init-bot/types"
	"io"
)

type Query interface {
	WithModel(model string)
	WithPrompt(prompt string)
	WithStream(stream bool)
	WithSystem(system string)
	WithContext(context types.RoomContext)
	WithOptions(options map[string]interface{})
	ToIOReader() io.Reader
}

type AIQuery struct {
	Model   string                 `json:"model"`
	System  string                 `json:"system,omitempty"`
	Prompt  string                 `json:"prompt"`
	Stream  bool                   `json:"stream"`
	Context interface{}            `json:"context,omitempty"`
	Options map[string]interface{} `json:"options,omitempty"`
}

func (q AIQuery) ToIOReader() io.Reader {
	jsonBody, _ := json.Marshal(q)
	requestBody := bytes.NewBuffer(jsonBody)

	return requestBody
}

func NewQuery() AIQuery {
	return AIQuery{}
}

func NewTokenQuery() TokenQuery {
	return TokenQuery{}
}

func NewMessageQuery() MessageQuery {
	return MessageQuery{}
}

func (q AIQuery) WithModel(model string) {
	q.Model = model
}

func (q AIQuery) WithPrompt(prompt string) {
	q.Prompt = prompt
}

func (q AIQuery) WithSystem(system string) {
	q.System = system
}

func (q AIQuery) WithStream(stream bool) {
	q.Stream = stream
}

func (q *AIQuery) WithContext(context types.RoomContext) {
	q.Context = context
}

func (q AIQuery) WithOptions(options map[string]interface{}) {
	q.Options = options
}

type TokenQuery struct {
	AIQuery
}

func (q *TokenQuery) WithContext(context types.RoomContext) {
	q.Context = context.Tokens
}

type MessageQuery struct {
	AIQuery
	Model    string                `json:"modelId"`
	Messages []MessageQueryMessage `json:"messages"`
}
type MessageQueryMessage struct {
	Role    string                   `json:"role"`
	Content []map[string]interface{} `json:"content"`
}

func (q *MessageQuery) WithContext(context types.RoomContext) {
	var convCtx []MessageQueryMessage
	message := q.Messages[0]
	for index, message := range context.Messages {
		if index > 3 {
			break
		}
		var convContent []map[string]interface{}
		for _, content := range message.Content {
			convContent = append(convContent, map[string]interface{}{"text": content.Text})
		}
		convCtx = append(convCtx, MessageQueryMessage{
			Role:    message.Role,
			Content: convContent,
		})
	}
	q.Messages = convCtx
	q.Messages = append(q.Messages, message)
}
