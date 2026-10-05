package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

type countingResolver struct {
	running, peak int32
	delay         time.Duration
}

func (c *countingResolver) Resolve(string, conflict.Fact) (conflict.Action, string, error) {
	n := atomic.AddInt32(&c.running, 1)
	for {
		p := atomic.LoadInt32(&c.peak)
		if n <= p || atomic.CompareAndSwapInt32(&c.peak, p, n) {
			break
		}
	}
	time.Sleep(c.delay)
	atomic.AddInt32(&c.running, -1)
	return conflict.ActionAdd, "stub", nil
}

func serverWithResolver(t *testing.T, r conflict.Resolver) http.Handler {
	t.Helper()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := conflict.NewEngine()
	e.Resolver = r
	return New(st, e, map[string]string{"k": "acme"}).Handler()
}

func concurrentWrites(t *testing.T, h http.Handler, users []string) {
	t.Helper()
	for _, u := range users {
		do(t, h, "POST", "/v1/memories", map[string]any{"user_id": u, "content": "User works at Google as a backend engineer", "resolve_conflicts": false})
	}
	var wg sync.WaitGroup
	for _, u := range users {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			do(t, h, "POST", "/v1/memories", map[string]any{"user_id": u, "content": "User works at OpenAI as a backend engineer"})
		}(u)
	}
	wg.Wait()
}

func TestRememberSerializesResolutionPerUser(t *testing.T) {
	r := &countingResolver{delay: 30 * time.Millisecond}
	h := serverWithResolver(t, r)
	users := []string{"same", "same", "same"}
	for i := 0; i < 3; i++ {
		do(t, h, "POST", "/v1/memories", map[string]any{"user_id": "same", "content": "User works at Google as a backend engineer", "resolve_conflicts": false})
	}
	var wg sync.WaitGroup
	for range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			do(t, h, "POST", "/v1/memories", map[string]any{"user_id": "same", "content": "User works at OpenAI as a backend engineer"})
		}()
	}
	wg.Wait()
	if r.peak != 1 {
		t.Fatalf("resolution for one user must never overlap, peak=%d", r.peak)
	}
}

func TestRememberDoesNotBlockDifferentUsers(t *testing.T) {
	r := &countingResolver{delay: 150 * time.Millisecond}
	h := serverWithResolver(t, r)
	concurrentWrites(t, h, []string{"u1", "u2", "u3"})
	if r.peak < 2 {
		t.Fatalf("different users must be able to resolve in parallel, peak=%d", r.peak)
	}
}

type gatedResolver struct {
	entered chan struct{}
	release chan struct{}
}

func (g *gatedResolver) Resolve(string, conflict.Fact) (conflict.Action, string, error) {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	<-g.release
	return conflict.ActionAdd, "gated", nil
}

func TestMutationsWaitForAnInFlightResolutionOfTheSameUser(t *testing.T) {
	g := &gatedResolver{entered: make(chan struct{}, 1), release: make(chan struct{})}
	h := serverWithResolver(t, g)
	_, seed := do(t, h, "POST", "/v1/memories", map[string]any{"user_id": "u", "content": "User works at Google as a backend engineer", "resolve_conflicts": false})
	id, _ := seed["id"].(string)

	remembered := make(chan struct{})
	go func() {
		do(t, h, "POST", "/v1/memories", map[string]any{"user_id": "u", "content": "User works at OpenAI as a backend engineer"})
		close(remembered)
	}()
	<-g.entered

	type call struct {
		name string
		run  func()
	}
	calls := []call{
		{"update", func() {
			do(t, h, "PUT", "/v1/memories/"+id, map[string]any{"content": "User works at Meta as a backend engineer"})
		}},
		{"forget", func() { do(t, h, "DELETE", "/v1/memories/"+id, nil) }},
		{"purge", func() { do(t, h, "DELETE", "/v1/users/u", nil) }},
	}
	finished := make(chan string, len(calls))
	for _, c := range calls {
		go func(c call) {
			c.run()
			finished <- c.name
		}(c)
	}
	select {
	case name := <-finished:
		t.Fatalf("%s must wait for the in-flight resolution of the same user", name)
	case <-time.After(150 * time.Millisecond):
	}
	close(g.release)
	<-remembered
	for range calls {
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Fatal("mutations must proceed once the resolution finishes")
		}
	}
}

func TestExplicitUpdateOfAnAlreadySupersededFactIsAConflict(t *testing.T) {
	h := newTestServer(t)
	_, first := do(t, h, "POST", "/v1/memories", map[string]any{"user_id": "u", "content": "city is Denver"})
	oldID, _ := first["id"].(string)
	if code, _ := do(t, h, "PUT", "/v1/memories/"+oldID, map[string]any{"content": "city is Boston"}); code != http.StatusOK {
		t.Fatalf("first update must succeed, got %d", code)
	}
	if code, _ := do(t, h, "PUT", "/v1/memories/"+oldID, map[string]any{"content": "city is Austin"}); code != http.StatusConflict {
		t.Fatalf("updating a superseded fact must be a conflict, got %d", code)
	}
}

type slowResolver struct{ delay time.Duration }

func (s slowResolver) Resolve(string, conflict.Fact) (conflict.Action, string, error) {
	time.Sleep(s.delay)
	return conflict.ActionUpdate, "slow", nil
}

func TestRememberStopsWaitingForAModelBeyondTheBudget(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := conflict.NewEngine()
	e.Resolver = slowResolver{delay: 600 * time.Millisecond}
	srv := New(st, e, map[string]string{"k": "acme"})
	srv.SetResolveBudget(50 * time.Millisecond)
	h := srv.Handler()
	do(t, h, "POST", "/v1/memories", map[string]any{"user_id": "u", "content": "User works at Google as a backend engineer", "resolve_conflicts": false})
	start := time.Now()
	code, out := do(t, h, "POST", "/v1/memories", map[string]any{"user_id": "u", "content": "User works at OpenAI as a backend engineer"})
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Fatalf("the write must not wait for a model beyond the budget, took %s", elapsed)
	}
	if code != http.StatusCreated || out["action"] != "add" {
		t.Fatalf("a timed out resolution must add and supersede nothing, got %d %v", code, out)
	}
}

func TestPlainWriteAlsoWaitsForAnInFlightResolutionOfTheSameUser(t *testing.T) {
	g := &gatedResolver{entered: make(chan struct{}, 1), release: make(chan struct{})}
	h := serverWithResolver(t, g)
	do(t, h, "POST", "/v1/memories", map[string]any{"user_id": "u", "content": "User works at Google as a backend engineer", "resolve_conflicts": false})
	remembered := make(chan struct{})
	go func() {
		do(t, h, "POST", "/v1/memories", map[string]any{"user_id": "u", "content": "User works at OpenAI as a backend engineer"})
		close(remembered)
	}()
	<-g.entered
	plain := make(chan struct{})
	go func() {
		do(t, h, "POST", "/v1/memories", map[string]any{"user_id": "u", "content": "User likes tea", "resolve_conflicts": false})
		close(plain)
	}()
	select {
	case <-plain:
		t.Fatal("a write that skips conflict resolution must still wait for the user's in-flight resolution")
	case <-time.After(150 * time.Millisecond):
	}
	close(g.release)
	<-remembered
	select {
	case <-plain:
	case <-time.After(2 * time.Second):
		t.Fatal("the plain write must proceed once resolution finishes")
	}
}

type countingSlow struct {
	calls int32
	delay time.Duration
}

func (c *countingSlow) Resolve(string, conflict.Fact) (conflict.Action, string, error) {
	atomic.AddInt32(&c.calls, 1)
	time.Sleep(c.delay)
	return conflict.ActionUpdate, "slow", nil
}

func TestAbandonedResolutionsStillCountAgainstTheConcurrencyBound(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := &countingSlow{delay: 400 * time.Millisecond}
	e := conflict.NewEngine()
	e.Resolver = r
	srv := New(st, e, map[string]string{"k": "acme"})
	srv.SetResolveBudget(40 * time.Millisecond)
	if err := srv.SetMaxConcurrentResolutions(1); err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()
	for _, u := range []string{"a", "b"} {
		do(t, h, "POST", "/v1/memories", map[string]any{"user_id": u, "content": "User works at Google as a backend engineer", "resolve_conflicts": false})
	}
	start := time.Now()
	_, first := do(t, h, "POST", "/v1/memories", map[string]any{"user_id": "a", "content": "User works at OpenAI as a backend engineer"})
	_, second := do(t, h, "POST", "/v1/memories", map[string]any{"user_id": "b", "content": "User works at OpenAI as a backend engineer"})
	if time.Since(start) > 300*time.Millisecond {
		t.Fatalf("both writes must return within their budgets, took %s", time.Since(start))
	}
	if first["action"] != "add" || second["action"] != "add" {
		t.Fatalf("both timed out writes must add, got %v and %v", first["action"], second["action"])
	}
	time.Sleep(100 * time.Millisecond)
	if n := atomic.LoadInt32(&r.calls); n != 1 {
		t.Fatalf("the second write must not start a model call while the abandoned one still runs, got %d calls", n)
	}
}

type panickingResolver struct{}

func (panickingResolver) Resolve(string, conflict.Fact) (conflict.Action, string, error) {
	panic("model client exploded")
}

func TestAPanickingResolverCannotCrashTheProcess(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := conflict.NewEngine()
	e.Resolver = panickingResolver{}
	srv := New(st, e, map[string]string{"k": "acme"})
	srv.SetResolveBudget(time.Second)
	h := srv.Handler()
	do(t, h, "POST", "/v1/memories", map[string]any{"user_id": "u", "content": "User works at Google as a backend engineer", "resolve_conflicts": false})
	code, out := do(t, h, "POST", "/v1/memories", map[string]any{"user_id": "u", "content": "User works at OpenAI as a backend engineer"})
	if code != http.StatusCreated || out["action"] != "add" {
		t.Fatalf("a panicking resolver must resolve to add, got %d %v", code, out)
	}
	if len(srv.slots) != 0 {
		t.Fatalf("the slot must be released after a panic, %d still held", len(srv.slots))
	}
}

func TestSetMaxConcurrentResolutionsRejectsNonPositiveValues(t *testing.T) {
	srv := New(nil, conflict.NewEngine(), nil)
	for _, n := range []int{0, -1} {
		if err := srv.SetMaxConcurrentResolutions(n); err == nil {
			t.Fatalf("%d must be rejected", n)
		}
	}
	if err := srv.SetMaxConcurrentResolutions(3); err != nil || cap(srv.slots) != 3 {
		t.Fatalf("a positive value must be accepted, got %v cap=%d", err, cap(srv.slots))
	}
}
