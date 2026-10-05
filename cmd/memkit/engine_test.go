package main

import (
	"strings"
	"testing"

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
	o, ok := e.Resolver.(*resolver.Ollama)
	if !ok || o.Model != "m" || o.BaseURL != "http://x:1" {
		t.Fatalf("want configured Ollama resolver, got %T %+v", e.Resolver, o)
	}
	if e.ConflictThreshold != 0.2 || e.MaxCandidates != 3 {
		t.Fatalf("want threshold 0.2 and 3 candidates, got %v %d", e.ConflictThreshold, e.MaxCandidates)
	}
}

func TestBuildEngine_OllamaWinsOverClaudeKey(t *testing.T) {
	e, _, err := buildEngine(env(map[string]string{"MEMKIT_RESOLVER": "ollama", "ANTHROPIC_API_KEY": "k"}))
	if _, ok := e.Resolver.(*resolver.Ollama); err != nil || !ok {
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

func TestBuildEngine_InvalidConfigFailsFast(t *testing.T) {
	cases := map[string]map[string]string{
		"unknown mode":           {"MEMKIT_RESOLVER": "gpt"},
		"claude without key":     {"MEMKIT_RESOLVER": "claude"},
		"max candidates not int": {"MEMKIT_RESOLVER": "ollama", "MEMKIT_RESOLVER_MAX_CANDIDATES": "many"},
		"max candidates zero":    {"MEMKIT_RESOLVER": "ollama", "MEMKIT_RESOLVER_MAX_CANDIDATES": "0"},
	}
	for name, c := range cases {
		if _, _, err := buildEngine(env(c)); err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
}
