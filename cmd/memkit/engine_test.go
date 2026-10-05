package main

import (
	"strings"
	"testing"
	"time"

	conflict "github.com/voltagebots/conflict-lens"
	"github.com/voltagebots/memkit/internal/resolver"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestBuildEngine_DefaultIsHeuristicOnly(t *testing.T) {
	e, desc, err := buildEngine(env(nil))
	if err != nil || e.Resolver != nil || e.ConflictThreshold != 0.45 || e.MaxCandidates != 0 {
		t.Fatalf("default must be heuristic only, got resolver=%v thr=%v max=%d err=%v", e.Resolver, e.ConflictThreshold, e.MaxCandidates, err)
	}
	if !strings.Contains(desc, "heuristic") {
		t.Fatalf("description %q", desc)
	}
}

func TestBuildEngine_ClaudeKeyKeepsExistingBehavior(t *testing.T) {
	e, _, err := buildEngine(env(map[string]string{"ANTHROPIC_API_KEY": "k"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Resolver.(*resolver.Claude); !ok || e.ConflictThreshold != 0.2 || e.MaxCandidates != 0 {
		t.Fatalf("want Claude resolver with threshold 0.2 and default candidates, got %T thr=%v max=%d", e.Resolver, e.ConflictThreshold, e.MaxCandidates)
	}
}

func TestBuildEngine_OllamaWidensBandAndCandidates(t *testing.T) {
	e, _, err := buildEngine(env(map[string]string{"MEMKIT_RESOLVER": "ollama", "MEMKIT_OLLAMA_MODEL": "m", "MEMKIT_OLLAMA_URL": "http://x:1"}))
	if err != nil {
		t.Fatal(err)
	}
	p, ok := e.Resolver.(resolver.PairOnly)
	o, isOllama := p.Judge.(*resolver.Ollama)
	if !ok || !isOllama || o.Model != "m" || o.BaseURL != "http://x:1" {
		t.Fatalf("want a configured Ollama pair resolver, got %T", e.Resolver)
	}
	if e.ConflictThreshold != 0.1 || e.MaxCandidates != 10 {
		t.Fatalf("want threshold 0.1 and 10 candidates, got %v %d", e.ConflictThreshold, e.MaxCandidates)
	}
}

func TestBuildEngine_OllamaDefaultsToPairMode(t *testing.T) {
	e, _, err := buildEngine(env(map[string]string{"MEMKIT_RESOLVER": "ollama"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, isMulti := e.Resolver.(conflict.MultiResolver); isMulti {
		t.Fatal("pair mode must not expose the batched interface")
	}
}

func TestBuildEngine_OllamaBatchModeExposesBatchedInterface(t *testing.T) {
	e, _, err := buildEngine(env(map[string]string{"MEMKIT_RESOLVER": "ollama", "MEMKIT_OLLAMA_MODE": "batch"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, isMulti := e.Resolver.(conflict.MultiResolver); !isMulti {
		t.Fatal("batch mode must expose the batched interface")
	}
}

func TestBuildEngine_OllamaWinsOverClaudeKey(t *testing.T) {
	e, _, err := buildEngine(env(map[string]string{"MEMKIT_RESOLVER": "ollama", "ANTHROPIC_API_KEY": "k"}))
	if _, ok := e.Resolver.(resolver.PairOnly); err != nil || !ok {
		t.Fatalf("explicit mode must win, got %T err=%v", e.Resolver, err)
	}
}

func TestBuildEngine_NoneDisablesResolverEvenWithKey(t *testing.T) {
	e, _, err := buildEngine(env(map[string]string{"MEMKIT_RESOLVER": "none", "ANTHROPIC_API_KEY": "k"}))
	if err != nil || e.Resolver != nil {
		t.Fatalf("none must disable the resolver, got %v err=%v", e.Resolver, err)
	}
}

func TestBuildEngine_MaxCandidatesOverride(t *testing.T) {
	e, _, err := buildEngine(env(map[string]string{"MEMKIT_RESOLVER": "ollama", "MEMKIT_RESOLVER_MAX_CANDIDATES": "5"}))
	if err != nil || e.MaxCandidates != 5 {
		t.Fatalf("want 5, got %d err=%v", e.MaxCandidates, err)
	}
}

func TestBuildEngine_ThresholdOverride(t *testing.T) {
	e, _, err := buildEngine(env(map[string]string{"MEMKIT_RESOLVER": "ollama", "MEMKIT_RESOLVER_THRESHOLD": "0.15"}))
	if err != nil || e.ConflictThreshold != 0.15 {
		t.Fatalf("want 0.15, got %v err=%v", e.ConflictThreshold, err)
	}
}

func TestBuildEngine_InvalidConfigFailsFast(t *testing.T) {
	cases := map[string]map[string]string{
		"unknown mode":            {"MEMKIT_RESOLVER": "gpt"},
		"unknown ollama mode":     {"MEMKIT_RESOLVER": "ollama", "MEMKIT_OLLAMA_MODE": "fast"},
		"claude without key":      {"MEMKIT_RESOLVER": "claude"},
		"max candidates not int":  {"MEMKIT_RESOLVER": "ollama", "MEMKIT_RESOLVER_MAX_CANDIDATES": "many"},
		"max candidates zero":     {"MEMKIT_RESOLVER": "ollama", "MEMKIT_RESOLVER_MAX_CANDIDATES": "0"},
		"threshold not number":    {"MEMKIT_RESOLVER": "ollama", "MEMKIT_RESOLVER_THRESHOLD": "low"},
		"ollama single candidate": {"MEMKIT_RESOLVER": "ollama", "MEMKIT_RESOLVER_MAX_CANDIDATES": "1"},
		"threshold zero":          {"MEMKIT_RESOLVER": "ollama", "MEMKIT_RESOLVER_THRESHOLD": "0"},
		"threshold above dup":     {"MEMKIT_RESOLVER": "ollama", "MEMKIT_RESOLVER_THRESHOLD": "0.9"},
	}
	for name, c := range cases {
		if _, _, err := buildEngine(env(c)); err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
}

func TestResolveBudget(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want time.Duration
		fail bool
	}{
		{"unset", nil, 0, false},
		{"ollama default", map[string]string{"MEMKIT_RESOLVER": "ollama"}, 30 * time.Second, false},
		{"explicit", map[string]string{"MEMKIT_RESOLVER_BUDGET": "5s"}, 5 * time.Second, false},
		{"off", map[string]string{"MEMKIT_RESOLVER": "ollama", "MEMKIT_RESOLVER_BUDGET": "0s"}, 0, false},
		{"not a duration", map[string]string{"MEMKIT_RESOLVER_BUDGET": "soon"}, 0, true},
		{"negative", map[string]string{"MEMKIT_RESOLVER_BUDGET": "-1s"}, 0, true},
	}
	for _, c := range cases {
		got, err := resolveBudget(env(c.env))
		if (err != nil) != c.fail || got != c.want {
			t.Fatalf("%s: want %v fail=%v, got %v err=%v", c.name, c.want, c.fail, got, err)
		}
	}
}
