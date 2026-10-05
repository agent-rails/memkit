package store

import (
	"context"
	"testing"
	"time"
)

func mustOpen(t *testing.T) *SQLite {
	t.Helper()
	s, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func insert(t *testing.T, s *SQLite, id, content string, lastAccessed time.Time) {
	t.Helper()
	err := s.Insert(context.Background(), Memory{
		ID: id, TenantID: "t", UserID: "u", Content: content, Category: "c",
		Confidence: 1, CreatedAt: lastAccessed, LastAccessed: lastAccessed,
	})
	if err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

func TestPruneSuperseded(t *testing.T) {
	s := mustOpen(t)
	ctx := context.Background()
	old := time.Now().Add(-100 * 24 * time.Hour)
	recent := time.Now()

	insert(t, s, "active", "still true", recent)
	insert(t, s, "old-archived", "outdated fact", old)
	insert(t, s, "new-archived", "recently archived", recent)
	// Archive the two that should be superseded.
	if err := s.Supersede(ctx, "t", "old-archived", "active"); err != nil {
		t.Fatal(err)
	}
	if err := s.Supersede(ctx, "t", "new-archived", "active"); err != nil {
		t.Fatal(err)
	}

	// Prune archived facts older than 30 days → only old-archived qualifies.
	n, err := s.PruneSuperseded(ctx, time.Now().Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 pruned, got %d", n)
	}

	// Active fact must survive; recently-archived must survive.
	if _, err := s.Get(ctx, "t", "active"); err != nil {
		t.Fatalf("active fact should survive: %v", err)
	}
	if _, err := s.Get(ctx, "t", "new-archived"); err != nil {
		t.Fatalf("recently-archived should survive: %v", err)
	}
	if _, err := s.Get(ctx, "t", "old-archived"); err != ErrNotFound {
		t.Fatalf("old archived fact should be gone, got %v", err)
	}
}

func TestSupersededExcludedFromSearch(t *testing.T) {
	s := mustOpen(t)
	ctx := context.Background()
	insert(t, s, "v1", "deploy target is staging", time.Now())
	insert(t, s, "v2", "deploy target is production", time.Now())
	if err := s.Supersede(ctx, "t", "v1", "v2"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Search(ctx, "t", "u", "deploy target", SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "v2" {
		t.Fatalf("want only active v2, got %+v", got)
	}
}

func TestSearchHandlesHyphenatedQuery(t *testing.T) {
	// Regression: a bareword FTS5 token containing '-' is a syntax error
	// unless quoted. escapeFTS handled '"', '*', '\'' but not '-', so any
	// query with a hyphenated word ("on-call", "PR-4821") returned a SQL
	// error instead of results -- live-reproduced against a running server
	// before this fix (2026-08-12).
	s := mustOpen(t)
	ctx := context.Background()
	insert(t, s, "oncall", "the on-call engineer is Priya", time.Now())

	got, err := s.Search(ctx, "t", "u", "who is on-call", SearchOpts{})
	if err != nil {
		t.Fatalf("search with hyphenated query must not error: %v", err)
	}
	if len(got) != 1 || got[0].ID != "oncall" {
		t.Fatalf("want the on-call fact to match, got %+v", got)
	}
}

func TestSearchNeverErrorsOnPunctuationOrFTSOperators(t *testing.T) {
	s := mustOpen(t)
	ctx := context.Background()
	insert(t, s, "work", "Marisol Bellweather works at Harbor Partners", time.Now())

	queries := []string{
		"Where does Marisol Bellweather work?",
		"What is Marisol's favorite drink?",
		"Marisol (Bellweather): works at Harbor; Partners, right?",
		"Marisol AND Bellweather", "Marisol OR Harbor", "NOT Marisol", "Marisol NEAR Harbor",
		"AND", "OR", "NOT", "NEAR", "^Marisol", "Marisol +Harbor", "Mari*sol", `"Marisol`, `\`, "/", "a/b",
		"!!!", "???", "...", "()", "{}", "[]", "@#$%", "émigré café", "日本語 works", "👍 Marisol",
		"Fly.io", "C++", "snake_case_word", "tab\there", "new\nline",
	}
	for _, q := range queries {
		if _, err := s.Search(ctx, "t", "u", q, SearchOpts{}); err != nil {
			t.Fatalf("query %q must not error: %v", q, err)
		}
	}
}

func TestSearchFindsFactForNaturalQuestion(t *testing.T) {
	s := mustOpen(t)
	ctx := context.Background()
	insert(t, s, "work", "Marisol Bellweather works at Harbor Partners", time.Now())
	insert(t, s, "other", "Nadia Okonkwo lives in Denver", time.Now())

	got, err := s.Search(ctx, "t", "u", "Where does Marisol Bellweather work?", SearchOpts{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].ID != "work" {
		t.Fatalf("want the Marisol fact first, got %+v", got)
	}
}

func TestEscapeFTSQuotesEveryTokenAndDropsPunctuation(t *testing.T) {
	got := escapeFTS(`Who's on-call? AND "x"`)
	want := `"Who s on call AND x" OR "Who"* OR "s"* OR "on"* OR "call"* OR "AND"* OR "x"*`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if escapeFTS("???") != `""` {
		t.Fatalf("punctuation-only query must become an empty phrase, got %s", escapeFTS("???"))
	}
}
