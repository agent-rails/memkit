package store

import (
	"context"
	"errors"
	"math/rand"
	"strings"
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

func TestSearchFindsFactsWithNonASCIITokenCharacters(t *testing.T) {
	s := mustOpen(t)
	ctx := context.Background()
	facts := map[string]string{
		"combining": "nickname is éabc",
		"joiner":    "code word is a‍bc",
		"emoji":     "favorite is \U0001F525abc",
		"circled":   "badge is ①abc",
		"accent":    "dog is named Zoë",
		"plain":     "city is Denver",
	}
	for id, content := range facts {
		insert(t, s, id, content, time.Now())
	}
	queries := map[string]string{
		"combining": "éabc",
		"joiner":    "a‍bc",
		"emoji":     "\U0001F525abc",
		"circled":   "①abc",
		"accent":    "Zoë",
	}
	for id, q := range queries {
		got, err := s.Search(ctx, "t", "u", q, SearchOpts{Limit: 3})
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if len(got) == 0 || got[0].ID != id {
			t.Fatalf("%s: query %q must find its own fact first, got %+v", id, q, got)
		}
	}
}

func TestSearchJoinerQueryDoesNotMatchSplitTokens(t *testing.T) {
	s := mustOpen(t)
	ctx := context.Background()
	insert(t, s, "joined", "code word is a‍bc", time.Now())
	insert(t, s, "unrelated", "a is for apple and b is for banana", time.Now())
	got, err := s.Search(ctx, "t", "u", "a‍bc", SearchOpts{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "joined" {
		t.Fatalf("a token containing a joiner must stay one token, got %+v", got)
	}
}

func TestEscapeFTSKeepsNonASCIIAndSplitsASCIIPunctuation(t *testing.T) {
	want := "\"café bar \U0001F525x\" OR \"café\"* OR \"bar\"* OR \"\U0001F525x\"*"
	if got := escapeFTS("café-bar? \U0001F525x"); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestSearchFuzzNeverErrors(t *testing.T) {
	s := mustOpen(t)
	ctx := context.Background()
	insert(t, s, "work", "Marisol Bellweather works at Harbor Partners", time.Now())
	alphabet := []rune("abcXYZ019 \t\n\"'*-^():+,;.?!/\\{}[]@#$%&=_~`|<>\u0000́‍①é日本\U0001F525‮")
	words := []string{"AND", "OR", "NOT", "NEAR", "near/3", "col:", "\"", "*", "-", "^"}
	r := rand.New(rand.NewSource(11))
	for i := 0; i < 20000; i++ {
		var b strings.Builder
		for n := r.Intn(24); n > 0; n-- {
			if r.Intn(6) == 0 {
				b.WriteString(words[r.Intn(len(words))])
				b.WriteRune(' ')
				continue
			}
			b.WriteRune(alphabet[r.Intn(len(alphabet))])
		}
		if _, err := s.Search(ctx, "t", "u", b.String(), SearchOpts{}); err != nil {
			t.Fatalf("query %q must not error: %v", b.String(), err)
		}
	}
}

func TestSearchFindsFactsContainingNonASCIIWhitespace(t *testing.T) {
	s := mustOpen(t)
	ctx := context.Background()
	facts := map[string]string{
		"emspace":   "note is  abc",
		"nbsp":      "tag is  abc",
		"ideograph": "label is 　abc",
		"plain":     "city is Denver",
	}
	for id, content := range facts {
		insert(t, s, id, content, time.Now())
	}
	for id, q := range map[string]string{"emspace": " abc", "nbsp": " abc", "ideograph": "　abc"} {
		got, err := s.Search(ctx, "t", "u", q, SearchOpts{Limit: 3})
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if len(got) == 0 || got[0].ID != id {
			t.Fatalf("%s: query %q must find its own fact first, got %+v", id, q, got)
		}
	}
}

func TestEscapeFTSSplitsOnASCIISpaceOnly(t *testing.T) {
	if got, want := escapeFTS("a b c"), "\"a b c\" OR \"a b\"* OR \"c\"*"; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestSupersedeIsConditionalOnTheOldFactBeingActive(t *testing.T) {
	s := mustOpen(t)
	ctx := context.Background()
	insert(t, s, "old", "city is Denver", time.Now())
	insert(t, s, "new1", "city is Boston", time.Now())
	insert(t, s, "new2", "city is Austin", time.Now())
	if err := s.Supersede(ctx, "t", "old", "new1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Supersede(ctx, "t", "old", "new2"); !errors.Is(err, ErrNotActive) {
		t.Fatalf("an already superseded fact must report ErrNotActive, got %v", err)
	}
	got, err := s.Get(ctx, "t", "old")
	if err != nil || got.SupersededBy != "new1" {
		t.Fatalf("the first supersession must be kept, got %+v %v", got, err)
	}
	if err := s.Supersede(ctx, "t", "missing", "new2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing fact must report ErrNotFound, got %v", err)
	}
}

func newMemory(id, content string) Memory {
	now := time.Now()
	return Memory{ID: id, TenantID: "t", UserID: "u", Content: content, Category: "c", Confidence: 1, CreatedAt: now, LastAccessed: now}
}

func TestReplaceInsertsAndSupersedesAtomically(t *testing.T) {
	s := mustOpen(t)
	ctx := context.Background()
	insert(t, s, "old", "city is Denver", time.Now())
	if err := s.Replace(ctx, newMemory("new", "city is Boston"), "old"); err != nil {
		t.Fatal(err)
	}
	old, _ := s.Get(ctx, "t", "old")
	fresh, err := s.Get(ctx, "t", "new")
	if err != nil || old.SupersededBy != "new" || fresh.SupersededBy != "" {
		t.Fatalf("want old superseded by new and new active, got old=%+v new=%+v err=%v", old, fresh, err)
	}
	got, _ := s.Search(ctx, "t", "u", "city", SearchOpts{})
	if len(got) != 1 || got[0].ID != "new" {
		t.Fatalf("only the new fact may be searchable, got %+v", got)
	}
}

func TestReplaceOfAnInactiveFactInsertsNothing(t *testing.T) {
	s := mustOpen(t)
	ctx := context.Background()
	insert(t, s, "old", "city is Denver", time.Now())
	insert(t, s, "mid", "city is Austin", time.Now())
	if err := s.Supersede(ctx, "t", "old", "mid"); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(ctx, newMemory("new", "city is Boston"), "old"); !errors.Is(err, ErrNotActive) {
		t.Fatalf("want ErrNotActive, got %v", err)
	}
	if _, err := s.Get(ctx, "t", "new"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a failed replace must not leave the new fact behind, got %v", err)
	}
	got, _ := s.Search(ctx, "t", "u", "Boston", SearchOpts{})
	if len(got) != 0 {
		t.Fatalf("a failed replace must not leave a searchable fact, got %+v", got)
	}
}

func TestReplaceOfAMissingFactInsertsNothing(t *testing.T) {
	s := mustOpen(t)
	ctx := context.Background()
	if err := s.Replace(ctx, newMemory("new", "city is Boston"), "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := s.Get(ctx, "t", "new"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a failed replace must not leave the new fact behind, got %v", err)
	}
}
