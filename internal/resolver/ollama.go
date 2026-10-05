package resolver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	conflict "github.com/voltagebots/conflict-lens"
)

const (
	defaultOllamaURL     = "http://127.0.0.1:11434"
	defaultOllamaModel   = "llama3.1:8b"
	defaultOllamaTimeout = 30 * time.Second
)

const ollamaSystemPrompt = `You decide how a NEW fact relates to an EXISTING fact in a personal memory store.
Facts are short statements, usually about one attribute of one person or thing.
Reply with ONLY compact JSON: {"action":"update|duplicate|add","reason":"<=8 words"}.

Check the subject FIRST. If NEW and EXISTING are about different people or things, the answer is "add", even when the attribute and the value are identical.

- "update": NEW is about the SAME subject and the SAME attribute as EXISTING, and gives a different value that replaces it (new job, moved city, changed favorite).
- "duplicate": NEW says the same thing as EXISTING in other words: same subject, same attribute, same value.
- "add": anything else. A DIFFERENT subject. A DIFFERENT attribute of the same subject. Information that can be true at the same time.

Examples:
EXISTING: Priya works at Orchard Works
NEW: Priya now works at Pine Studio
{"action":"update","reason":"same person, new employer"}

EXISTING: Priya works at Orchard Works
NEW: Dan works at Orchard Works
{"action":"add","reason":"different person"}

EXISTING: Priya works at Orchard Works
NEW: Priya lives in Oslo
{"action":"add","reason":"different attribute"}

EXISTING: Priya works at Orchard Works
NEW: Priya is employed by Orchard Works
{"action":"duplicate","reason":"same fact reworded"}`

// Ollama implements conflict.Resolver with a local model served by Ollama, so
// conflict resolution needs no paid API and no data leaves the machine.
type Ollama struct {
	BaseURL string
	Model   string
	HTTP    *http.Client
}

// NewOllama builds a resolver. Empty baseURL or model fall back to defaults.
func NewOllama(baseURL, model string) *Ollama {
	if baseURL == "" {
		baseURL = defaultOllamaURL
	}
	if model == "" {
		model = defaultOllamaModel
	}
	return &Ollama{BaseURL: strings.TrimRight(baseURL, "/"), Model: model, HTTP: &http.Client{Timeout: defaultOllamaTimeout}}
}

// Resolve asks the local model to classify the relationship. Errors are returned
// so the engine decides what to do. Implements conflict.Resolver.
func (o *Ollama) Resolve(newContent string, candidate conflict.Fact) (conflict.Action, string, error) {
	reqBody := ollamaChatRequest{
		Model:  o.Model,
		Stream: false,
		Format: "json",
		Messages: []message{
			{Role: "system", Content: ollamaSystemPrompt},
			{Role: "user", Content: fmt.Sprintf("EXISTING: %s\nNEW: %s",
				strings.TrimSpace(candidate.Content), strings.TrimSpace(newContent))},
		},
		Options: ollamaOptions{Temperature: 0, NumPredict: 60},
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return conflict.ActionAdd, "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultOllamaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/chat", bytes.NewReader(raw))
	if err != nil {
		return conflict.ActionAdd, "", err
	}
	req.Header.Set("content-type", "application/json")
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return conflict.ActionAdd, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return conflict.ActionAdd, "", fmt.Errorf("ollama: status %d", resp.StatusCode)
	}
	var out ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return conflict.ActionAdd, "", err
	}
	if strings.TrimSpace(out.Message.Content) == "" {
		return conflict.ActionAdd, "", fmt.Errorf("ollama: empty response")
	}
	return parseDecision(out.Message.Content)
}

type ollamaChatRequest struct {
	Model    string        `json:"model"`
	Stream   bool          `json:"stream"`
	Format   string        `json:"format"`
	Messages []message     `json:"messages"`
	Options  ollamaOptions `json:"options"`
}

type ollamaOptions struct {
	Temperature float64 `json:"temperature"`
	NumPredict  int     `json:"num_predict"`
}

type ollamaChatResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}
