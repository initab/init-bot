package matrixbot

import (
	"bytes"
	"encoding/json"
	"io"
)

type Option func(q AIQuery) AIQuery

type AIQuery struct {
	Model   string                 `json:"model"`
	System  string                 `json:"system,omitempty"`
	Prompt  string                 `json:"prompt"`
	Stream  bool                   `json:"stream"`
	Context []int                  `json:"context"`
	Options map[string]interface{} `json:"options"`
}

func (q *AIQuery) ToIOReader() io.Reader {
	jsonBody, _ := json.Marshal(q)
	requestBody := bytes.NewBuffer(jsonBody)

	return requestBody
}

func NewQuery(options ...Option) *AIQuery {
	q := AIQuery{}
	return &q
}

func (q *AIQuery) WithModel(model string) *AIQuery {
	q.Model = model
	return q
}

func (q *AIQuery) WithPrompt(prompt string) *AIQuery {
	q.Prompt = prompt
	return q
}

func (q *AIQuery) WithSystem(system string) *AIQuery {
	q.System = system
	return q
}

func (q *AIQuery) WithStream(stream bool) *AIQuery {
	q.Stream = stream
	return q
}

func (q *AIQuery) WithContext(context []int) *AIQuery {
	q.Context = context
	return q
}

func (q *AIQuery) WithOptions(options map[string]interface{}) *AIQuery {
	q.Options = options
	return q
}
