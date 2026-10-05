package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	conflict "github.com/voltagebots/conflict-lens"
	"github.com/voltagebots/memkit/internal/store"
)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, conflict.NewEngine(), map[string]string{"k": "acme"}).Handler()
}

func do(t *testing.T, h http.Handler, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Authorization", "Bearer k")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestRememberThenConflictResolution(t *testing.T) {
	h := newTestServer(t)

	// Week 1: works at Google.
	code, resp := do(t, h, "POST", "/v1/memories", map[string]any{
		"user_id": "u1", "content": "User works at Google as a backend engineer", "category": "work",
	})
	if code != http.StatusCreated || resp["action"] != "add" {
		t.Fatalf("first insert: code=%d action=%v", code, resp["action"])
	}

	// Week 2: works at OpenAI → conflict-lens should supersede.
	code, resp = do(t, h, "POST", "/v1/memories", map[string]any{
		"user_id": "u1", "content": "User works at OpenAI as a backend engineer", "category": "work",
	})
	if code != http.StatusCreated || resp["action"] != "update" {
		t.Fatalf("conflict insert: code=%d action=%v reason=%v", code, resp["action"], resp["reason"])
	}
	if resp["superseded_id"] == nil || resp["superseded_id"] == "" {
		t.Fatal("expected superseded_id to be set")
	}

	// Search must return ONLY the active fact (OpenAI), Google archived.
	code, resp = do(t, h, "GET", "/v1/memories/search?user_id=u1&q=where+does+user+work", nil)
	if code != http.StatusOK {
		t.Fatalf("search code=%d", code)
	}
	mems, _ := resp["memories"].([]any)
	if len(mems) != 1 {
		t.Fatalf("want 1 active memory, got %d", len(mems))
	}
	got := mems[0].(map[string]any)["content"].(string)
	if got != "User works at OpenAI as a backend engineer" {
		t.Fatalf("active fact wrong: %q", got)
	}
}

func TestDuplicateIsNotStoredTwice(t *testing.T) {
	h := newTestServer(t)
	body := map[string]any{"user_id": "u1", "content": "User prefers dark mode", "category": "prefs"}

	_, _ = do(t, h, "POST", "/v1/memories", body)
	code, resp := do(t, h, "POST", "/v1/memories", body)
	if code != http.StatusOK || resp["action"] != "duplicate" {
		t.Fatalf("dup: code=%d action=%v", code, resp["action"])
	}

	_, resp = do(t, h, "GET", "/v1/categories?user_id=u1", nil)
	cats, _ := resp["categories"].([]any)
	if len(cats) != 1 {
		t.Fatalf("want 1 category, got %d", len(cats))
	}
	if c := cats[0].(map[string]any); int(c["count"].(float64)) != 1 {
		t.Fatalf("want 1 memory in category, got %v", c["count"])
	}
}

func TestExplicitUpdateAndForget(t *testing.T) {
	h := newTestServer(t)
	_, resp := do(t, h, "POST", "/v1/memories", map[string]any{
		"user_id": "u1", "content": "Deploy target is staging", "category": "ops",
		"resolve_conflicts": false,
	})
	id := resp["id"].(string)

	code, resp := do(t, h, "PUT", "/v1/memories/"+id, map[string]any{"content": "Deploy target is production"})
	if code != http.StatusOK || resp["action"] != "update" {
		t.Fatalf("update: code=%d action=%v", code, resp["action"])
	}
	newID := resp["id"].(string)

	code, _ = do(t, h, "DELETE", "/v1/memories/"+newID, nil)
	if code != http.StatusOK {
		t.Fatalf("forget: code=%d", code)
	}
}

func TestGDPRPurge(t *testing.T) {
	h := newTestServer(t)
	for _, c := range []string{"a", "b", "c"} {
		_, _ = do(t, h, "POST", "/v1/memories", map[string]any{
			"user_id": "u1", "content": "fact " + c, "category": c, "resolve_conflicts": false,
		})
	}
	code, resp := do(t, h, "DELETE", "/v1/users/u1", nil)
	if code != http.StatusOK {
		t.Fatalf("purge code=%d", code)
	}
	if int(resp["removed"].(float64)) != 3 {
		t.Fatalf("want 3 removed, got %v", resp["removed"])
	}
}

func TestAuthRequired(t *testing.T) {
	h := newTestServer(t)
	req := httptest.NewRequest("GET", "/v1/categories?user_id=u1", nil) // no bearer
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestTenantIsolation(t *testing.T) {
	st, _ := store.OpenSQLite(":memory:")
	defer st.Close()
	h := New(st, conflict.NewEngine(), map[string]string{"ka": "acme", "kb": "other"}).Handler()

	// acme stores a fact.
	req := httptest.NewRequest("POST", "/v1/memories", bytes.NewBufferString(
		`{"user_id":"u1","content":"acme secret topology","category":"net"}`))
	req.Header.Set("Authorization", "Bearer ka")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// other must not see it.
	req = httptest.NewRequest("GET", "/v1/memories/search?user_id=u1&q=topology", nil)
	req.Header.Set("Authorization", "Bearer kb")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if n := int(out["count"].(float64)); n != 0 {
		t.Fatalf("tenant isolation breach: other tenant saw %d memories", n)
	}
}

func TestSearchEmptyResultIsAnArrayNotNull(t *testing.T) {
	h := newTestServer(t)
	code, out := do(t, h, "GET", "/v1/memories/search?user_id=nobody&q=anything", nil)
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	memories, ok := out["memories"].([]any)
	if !ok {
		t.Fatalf("memories must be a JSON array when empty, got %T (%v)", out["memories"], out["memories"])
	}
	if len(memories) != 0 || out["count"] != float64(0) {
		t.Fatalf("want empty array and count 0, got %v count=%v", memories, out["count"])
	}
}
