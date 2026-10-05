package resolver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	conflict "github.com/voltagebots/conflict-lens"
)

func mockOllama(t *testing.T, content string, status int, captured *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if captured != nil {
			_ = json.Unmarshal(body, captured)
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{"role": "assistant", "content": content},
		})
	}))
}

func newOllama(url string) *Ollama {
	o := NewOllama(url, "")
	return o
}

func TestOllama_ParsesUpdate(t *testing.T) {
	srv := mockOllama(t, `{"action":"update","reason":"same subject new employer"}`, http.StatusOK, nil)
	defer srv.Close()
	act, reason, err := newOllama(srv.URL).Resolve("Priya works at Pine Studio", conflict.Fact{ID: "1", Content: "Priya works at Orchard Works"})
	if err != nil || act != conflict.ActionUpdate || !strings.Contains(reason, "same subject") {
		t.Fatalf("want update, got %s %q %v", act, reason, err)
	}
}

func TestOllama_ParsesAddAndDuplicate(t *testing.T) {
	for want, body := range map[conflict.Action]string{
		conflict.ActionAdd:       `{"action":"add","reason":"different subject"}`,
		conflict.ActionDuplicate: `{"action":"duplicate","reason":"same"}`,
	} {
		srv := mockOllama(t, body, http.StatusOK, nil)
		act, _, err := newOllama(srv.URL).Resolve("n", conflict.Fact{ID: "1", Content: "e"})
		srv.Close()
		if err != nil || act != want {
			t.Fatalf("want %s, got %s %v", want, act, err)
		}
	}
}

func TestOllama_RequestIsDeterministicJSONChat(t *testing.T) {
	var got map[string]any
	srv := mockOllama(t, `{"action":"add","reason":"x"}`, http.StatusOK, &got)
	defer srv.Close()
	o := newOllama(srv.URL)
	o.Model = "test-model"
	if _, _, err := o.Resolve("NEW fact", conflict.Fact{ID: "1", Content: "EXISTING fact"}); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "test-model" || got["stream"] != false || got["format"] != "json" {
		t.Fatalf("model/stream/format wrong: %v", got)
	}
	opts, _ := got["options"].(map[string]any)
	if opts["temperature"] != float64(0) {
		t.Fatalf("temperature must be 0, got %v", opts["temperature"])
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("want system + user message, got %d", len(msgs))
	}
	user := msgs[1].(map[string]any)["content"].(string)
	if !strings.Contains(user, "EXISTING: EXISTING fact") || !strings.Contains(user, "NEW: NEW fact") {
		t.Fatalf("user message must carry both facts, got %q", user)
	}
	system := msgs[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, "DIFFERENT subject") {
		t.Fatalf("system prompt must instruct a subject check")
	}
}

func TestOllama_ErrorsAreReturnedNotSwallowed(t *testing.T) {
	cases := map[string]struct {
		content string
		status  int
	}{
		"http error":     {`{"action":"add"}`, http.StatusInternalServerError},
		"no json":        {"I think they conflict", http.StatusOK},
		"unknown action": {`{"action":"merge","reason":"x"}`, http.StatusOK},
		"empty":          {"", http.StatusOK},
	}
	for name, c := range cases {
		srv := mockOllama(t, c.content, c.status, nil)
		_, _, err := newOllama(srv.URL).Resolve("n", conflict.Fact{ID: "1", Content: "e"})
		srv.Close()
		if err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
}

func TestOllama_UnreachableServerReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if _, _, err := newOllama(url).Resolve("n", conflict.Fact{ID: "1", Content: "e"}); err == nil {
		t.Fatal("want an error for an unreachable server")
	}
}

func candidates() []conflict.Fact {
	return []conflict.Fact{
		{ID: "a", Content: "Nadia Bellweather works at Harbor Partners"},
		{ID: "b", Content: "Marisol Bellweather works at Cinder Labs"},
		{ID: "c", Content: "Idris Okonkwo lives in Denver"},
	}
}

func TestOllamaAmong_ParsesUpdateTarget(t *testing.T) {
	srv := mockOllama(t, `{"action":"update","target":2,"reason":"same person new employer"}`, http.StatusOK, nil)
	defer srv.Close()
	act, idx, reason, err := newOllama(srv.URL).ResolveAmong("Marisol Bellweather works at Harbor Partners", candidates())
	if err != nil || act != conflict.ActionUpdate || idx != 1 || !strings.Contains(reason, "same person") {
		t.Fatalf("want update of index 1, got %s %d %q %v", act, idx, reason, err)
	}
}

func TestOllamaAmong_AddNeedsNoTarget(t *testing.T) {
	srv := mockOllama(t, `{"action":"add","target":null,"reason":"different person"}`, http.StatusOK, nil)
	defer srv.Close()
	act, _, _, err := newOllama(srv.URL).ResolveAmong("n", candidates())
	if err != nil || act != conflict.ActionAdd {
		t.Fatalf("want add, got %s %v", act, err)
	}
}

func TestOllamaAmong_DuplicateTarget(t *testing.T) {
	srv := mockOllama(t, `{"action":"duplicate","target":1,"reason":"same"}`, http.StatusOK, nil)
	defer srv.Close()
	act, idx, _, err := newOllama(srv.URL).ResolveAmong("n", candidates())
	if err != nil || act != conflict.ActionDuplicate || idx != 0 {
		t.Fatalf("want duplicate of index 0, got %s %d %v", act, idx, err)
	}
}

func TestOllamaAmong_RequestNumbersCandidatesAndIsDeterministic(t *testing.T) {
	var got map[string]any
	srv := mockOllama(t, `{"action":"add","target":null,"reason":"x"}`, http.StatusOK, &got)
	defer srv.Close()
	if _, _, _, err := newOllama(srv.URL).ResolveAmong("NEW fact", candidates()); err != nil {
		t.Fatal(err)
	}
	if got["format"] != "json" || got["stream"] != false {
		t.Fatalf("format/stream wrong: %v", got)
	}
	opts, _ := got["options"].(map[string]any)
	if opts["temperature"] != float64(0) {
		t.Fatalf("temperature must be 0")
	}
	msgs, _ := got["messages"].([]any)
	user := msgs[1].(map[string]any)["content"].(string)
	for _, want := range []string{"NEW: NEW fact", "1. Nadia Bellweather works at Harbor Partners", "2. Marisol Bellweather works at Cinder Labs", "3. Idris Okonkwo lives in Denver"} {
		if !strings.Contains(user, want) {
			t.Fatalf("user message missing %q:\n%s", want, user)
		}
	}
}

func TestOllamaAmong_InvalidAnswersAreErrors(t *testing.T) {
	cases := map[string]string{
		"update without target":  `{"action":"update","target":null,"reason":"x"}`,
		"target out of range":    `{"action":"update","target":9,"reason":"x"}`,
		"target zero":            `{"action":"update","target":0,"reason":"x"}`,
		"target negative":        `{"action":"duplicate","target":-1,"reason":"x"}`,
		"unknown action":         `{"action":"merge","target":1,"reason":"x"}`,
		"not json":               "they conflict",
		"target is a string":     `{"action":"update","target":"two","reason":"x"}`,
		"update with fractional": `{"action":"update","target":1.5,"reason":"x"}`,
	}
	for name, body := range cases {
		srv := mockOllama(t, body, http.StatusOK, nil)
		_, _, _, err := newOllama(srv.URL).ResolveAmong("n", candidates())
		srv.Close()
		if err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
}

func TestOllamaAmong_EmptyCandidatesIsAnError(t *testing.T) {
	if _, _, _, err := newOllama("http://127.0.0.1:1").ResolveAmong("n", nil); err == nil {
		t.Fatal("want an error for no candidates")
	}
}

func TestOllamaAmong_ServerErrorIsReturned(t *testing.T) {
	srv := mockOllama(t, `{"action":"add"}`, http.StatusInternalServerError, nil)
	defer srv.Close()
	if _, _, _, err := newOllama(srv.URL).ResolveAmong("n", candidates()); err == nil {
		t.Fatal("want an error on HTTP 500")
	}
}

func TestOllama_ImplementsMultiResolver(t *testing.T) {
	var _ conflict.MultiResolver = (*Ollama)(nil)
}

func sequencedOllama(t *testing.T, batchBody, verifyBody string, calls *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		msgs := req["messages"].([]any)
		user := msgs[1].(map[string]any)["content"].(string)
		reply := verifyBody
		kind := "verify"
		if strings.Contains(user, "EXISTING:\n1.") {
			reply = batchBody
			kind = "batch"
		}
		*calls = append(*calls, kind)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"role": "assistant", "content": reply}})
	}))
}

func TestOllamaAmong_UpdateIsVerifiedByThePairJudge(t *testing.T) {
	var calls []string
	srv := sequencedOllama(t, `{"action":"update","target":2,"reason":"b"}`, `{"action":"update","reason":"v"}`, &calls)
	defer srv.Close()
	act, idx, _, err := newOllama(srv.URL).ResolveAmong("n", candidates())
	if err != nil || act != conflict.ActionUpdate || idx != 1 {
		t.Fatalf("want a verified update of index 1, got %s %d %v", act, idx, err)
	}
	if len(calls) != 2 || calls[0] != "batch" || calls[1] != "verify" {
		t.Fatalf("want batch then verify, got %v", calls)
	}
}

func TestOllamaAmong_VerifierDisagreementKeepsBothFacts(t *testing.T) {
	for _, verdict := range []string{`{"action":"add","reason":"different person"}`, `{"action":"duplicate","reason":"same"}`} {
		var calls []string
		srv := sequencedOllama(t, `{"action":"update","target":2,"reason":"b"}`, verdict, &calls)
		act, _, reason, err := newOllama(srv.URL).ResolveAmong("n", candidates())
		srv.Close()
		if err != nil || act != conflict.ActionAdd || !strings.Contains(reason, "verif") {
			t.Fatalf("disagreement must add, got %s %q %v", act, reason, err)
		}
	}
}

func TestOllamaAmong_VerifierFailureIsAnError(t *testing.T) {
	var calls []string
	srv := sequencedOllama(t, `{"action":"update","target":1,"reason":"b"}`, "not json", &calls)
	defer srv.Close()
	if _, _, _, err := newOllama(srv.URL).ResolveAmong("n", candidates()); err == nil {
		t.Fatal("a verifier that cannot answer must be an error so the engine adds")
	}
}

func TestOllamaAmong_AddIsNotVerified(t *testing.T) {
	var calls []string
	srv := sequencedOllama(t, `{"action":"add","target":null,"reason":"b"}`, `{"action":"update"}`, &calls)
	defer srv.Close()
	act, _, _, err := newOllama(srv.URL).ResolveAmong("n", candidates())
	if err != nil || act != conflict.ActionAdd || len(calls) != 1 {
		t.Fatalf("add needs no verification, got %s calls=%v err=%v", act, calls, err)
	}
}

func TestOllamaAmong_VerifiedDuplicate(t *testing.T) {
	var calls []string
	srv := sequencedOllama(t, `{"action":"duplicate","target":1,"reason":"b"}`, `{"action":"duplicate","reason":"v"}`, &calls)
	defer srv.Close()
	act, idx, _, err := newOllama(srv.URL).ResolveAmong("n", candidates())
	if err != nil || act != conflict.ActionDuplicate || idx != 0 {
		t.Fatalf("want a verified duplicate of index 0, got %s %d %v", act, idx, err)
	}
}
