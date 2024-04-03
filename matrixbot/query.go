package matrixbot

import (
	"bytes"
	"encoding/json"
	"io"
)

type Option func(q AIQuery) AIQuery

type AIQuery struct {
	Model  string `json:"model"`
	System string `json:"system,omitempty"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

func (q *AIQuery) MakePost() io.Reader {
	jsonBody, _ := json.Marshal(q)
	requestBody := bytes.NewBuffer(jsonBody)

	return requestBody
}

func NewQuery(options ...Option) AIQuery {
	q := AIQuery{}
	return q
}

func (q *AIQuery) WithModel(model string) {
	q.Model = model

}

func (q *AIQuery) WithPrompt(prompt string) {
	q.Prompt = prompt

}

func (q *AIQuery) WithSystem(system string) {
	q.System = system
}

func (q *AIQuery) WithStream(stream bool) {
	q.Stream = stream
}
