package resolver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode"

	conflict "github.com/voltagebots/conflict-lens"
)

const (
	maxPromptRunes       = 300
	maxResponseBytes     = 64 << 10
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

const ollamaBatchSystemPrompt = `You maintain a personal memory store. A NEW fact arrives, with a numbered list of EXISTING facts that might relate to it.
Reply with ONLY compact JSON: {"action":"update|duplicate|add","target":<number or null>,"reason":"<=8 words"}.

Check the subject FIRST. Facts about different people or things never replace each other, even when the attribute and the value are identical.

- "update": NEW is about the SAME subject and the SAME attribute as one EXISTING fact, and gives a different value that replaces it. target is that fact's number.
- "duplicate": NEW says the same thing as one EXISTING fact: same subject, same attribute, same value, possibly reworded. target is that fact's number.
- "add": anything else. A DIFFERENT subject. A DIFFERENT attribute of the same subject. Information that can be true at the same time. target is null.

Example 1
NEW: Priya now works at Pine Studio
EXISTING:
1. Dan works at Orchard Works
2. Priya works at Orchard Works
3. Priya lives in Oslo
{"action":"update","target":2,"reason":"same person, new employer"}

Example 2
NEW: Dan works at Pine Studio
EXISTING:
1. Priya works at Pine Studio
2. Priya lives in Oslo
{"action":"add","target":null,"reason":"different person"}

Example 3
NEW: Priya is employed by Orchard Works
EXISTING:
1. Dan works at Orchard Works
2. Priya works at Orchard Works
{"action":"duplicate","target":2,"reason":"same fact reworded"}`

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
	user := fmt.Sprintf("EXISTING: %s\nNEW: %s", promptText(candidate.Content), promptText(newContent))
	text, err := o.chat(ollamaSystemPrompt, user)
	if err != nil {
		return conflict.ActionAdd, "", err
	}
	return parseDecision(text)
}

// ResolveAmong asks the local model which one of several candidates, if any, the
// new fact replaces or repeats. The returned index is into candidates. The batch
// call proposes; before anything is superseded or skipped, the single-pair judge
// must reach the same verdict on the proposed candidate. If it disagrees the fact
// is added. An invalid answer is an error, never a guess. Implements
// conflict.MultiResolver.
func (o *Ollama) ResolveAmong(newContent string, candidates []conflict.Fact) (conflict.Action, int, string, error) {
	if len(candidates) == 0 {
		return conflict.ActionAdd, -1, "", fmt.Errorf("ollama: no candidates")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "NEW: %s\nEXISTING:\n", promptText(newContent))
	for i, c := range candidates {
		fmt.Fprintf(&b, "%d. %s\n", i+1, promptText(c.Content))
	}
	text, err := o.chat(ollamaBatchSystemPrompt, b.String())
	if err != nil {
		return conflict.ActionAdd, -1, "", err
	}
	act, idx, reason, err := parseBatchDecision(text, len(candidates))
	if err != nil || act == conflict.ActionAdd {
		return act, idx, reason, err
	}
	verdict, _, err := o.Resolve(newContent, candidates[idx])
	if err != nil {
		return conflict.ActionAdd, -1, "", err
	}
	if verdict != act {
		return conflict.ActionAdd, -1, "verifier disagreed with the batch choice, keeping both facts", nil
	}
	return act, idx, reason, nil
}

// promptText prepares stored text for the judge prompt. Stored facts are
// untrusted, so every run of whitespace or control characters becomes one space,
// which keeps a fact on a single line and stops it from imitating the prompt's own
// structure (a fake NEW line, a fake numbered candidate), and the length is
// capped. Unicode format characters (bidirectional controls, zero-width
// characters) are removed. This reduces, and does not remove, the ability of a
// stored fact to steer the judge.
func promptText(s string) string {
	var b strings.Builder
	space := true
	runes := 0
	for _, r := range s {
		if unicode.Is(unicode.Cf, r) {
			continue
		}
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			if !space {
				b.WriteRune(' ')
				space = true
				runes++
			}
		} else {
			b.WriteRune(r)
			space = false
			runes++
		}
		if runes >= maxPromptRunes {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

func (o *Ollama) chat(system, user string) (string, error) {
	reqBody := ollamaChatRequest{
		Model:    o.Model,
		Stream:   false,
		Format:   "json",
		Messages: []message{{Role: "system", Content: system}, {Role: "user", Content: user}},
		Options:  ollamaOptions{Temperature: 0, NumPredict: 60},
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultOllamaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/chat", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxResponseBytes {
		return "", fmt.Errorf("ollama: response exceeds %d bytes", maxResponseBytes)
	}
	var out ollamaChatResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.Message.Content) == "" {
		return "", fmt.Errorf("ollama: empty response")
	}
	return out.Message.Content, nil
}

func parseBatchDecision(text string, candidates int) (conflict.Action, int, string, error) {
	start, end := strings.IndexByte(text, '{'), strings.LastIndexByte(text, '}')
	if start < 0 || end <= start {
		return conflict.ActionAdd, -1, "", fmt.Errorf("no JSON in response: %q", text)
	}
	var d struct {
		Action string          `json:"action"`
		Target json.RawMessage `json:"target"`
		Reason string          `json:"reason"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &d); err != nil {
		return conflict.ActionAdd, -1, "", err
	}
	reason := "llm: " + d.Reason
	switch strings.ToLower(strings.TrimSpace(d.Action)) {
	case "add":
		return conflict.ActionAdd, -1, reason, nil
	case "update", "duplicate":
		var n int
		if err := json.Unmarshal(d.Target, &n); err != nil || n < 1 || n > candidates {
			return conflict.ActionAdd, -1, "", fmt.Errorf("invalid target %s for %d candidates", string(d.Target), candidates)
		}
		act := conflict.ActionUpdate
		if strings.ToLower(strings.TrimSpace(d.Action)) == "duplicate" {
			act = conflict.ActionDuplicate
		}
		return act, n - 1, reason, nil
	default:
		return conflict.ActionAdd, -1, "", fmt.Errorf("unknown action %q", d.Action)
	}
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

// PairOnly exposes only the single-candidate judgment of a resolver, so the engine
// consults candidates one at a time, most similar first, instead of in one batch.
type PairOnly struct {
	Judge conflict.Resolver
}

// Resolve returns the judge's verdict. A failure is logged and returned, never
// turned into a verdict: with more than one candidate the engine then stops
// consulting candidates and adds the fact, so a model that is down, slow or
// answering invalidly can delay a write but can never cause an existing fact to
// be superseded.
func (p PairOnly) Resolve(newContent string, candidate conflict.Fact) (conflict.Action, string, error) {
	act, reason, err := p.Judge.Resolve(newContent, candidate)
	if err != nil {
		log.Printf("conflict resolver: judge failed, keeping both facts: %v", err)
	}
	return act, reason, err
}
