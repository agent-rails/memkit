package main

import (
	"fmt"
	"strconv"

	conflict "github.com/voltagebots/conflict-lens"
	"github.com/voltagebots/memkit/internal/resolver"
)

const (
	resolverConflictThreshold = 0.2
	ollamaCandidateThreshold  = 0.1
	defaultOllamaCandidates   = 10
)

// buildEngine selects the conflict engine from the environment. MEMKIT_RESOLVER
// is one of "", "none", "claude", "ollama". Empty keeps the original behavior:
// the Claude resolver when an Anthropic key is present, otherwise heuristic only.
// An invalid setting is an error so a typo cannot silently disable resolution.
func buildEngine(getenv func(string) string) (*conflict.Engine, string, error) {
	engine := conflict.NewEngine()
	mode := getenv("MEMKIT_RESOLVER")
	key := firstOf(getenv, "MEMKIT_ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY")
	if mode == "" && key != "" {
		mode = "claude"
	}

	var desc string
	switch mode {
	case "", "none":
		desc = "conflict resolver: none (lexical heuristic only)"
	case "claude":
		if key == "" {
			return nil, "", fmt.Errorf("MEMKIT_RESOLVER=claude needs MEMKIT_ANTHROPIC_API_KEY or ANTHROPIC_API_KEY")
		}
		engine.Resolver = resolver.NewClaude(key, getenv("MEMKIT_RESOLVER_MODEL"))
		engine.ConflictThreshold = resolverConflictThreshold
		desc = "conflict resolver: Claude enabled (LLM judgment on ambiguous facts)"
	case "ollama":
		o := resolver.NewOllama(getenv("MEMKIT_OLLAMA_URL"), getenv("MEMKIT_OLLAMA_MODEL"))
		engine.Resolver = o
		engine.ConflictThreshold = ollamaCandidateThreshold
		engine.MaxCandidates = defaultOllamaCandidates
		desc = fmt.Sprintf("conflict resolver: Ollama %s at %s (local, one batched call over up to %d candidates)", o.Model, o.BaseURL, engine.MaxCandidates)
	default:
		return nil, "", fmt.Errorf("unknown MEMKIT_RESOLVER %q (want none, claude or ollama)", mode)
	}

	if raw := getenv("MEMKIT_RESOLVER_MAX_CANDIDATES"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return nil, "", fmt.Errorf("MEMKIT_RESOLVER_MAX_CANDIDATES must be an integer >= 1, got %q", raw)
		}
		engine.MaxCandidates = n
	}
	if raw := getenv("MEMKIT_RESOLVER_THRESHOLD"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || v <= 0 || v >= engine.DupThreshold {
			return nil, "", fmt.Errorf("MEMKIT_RESOLVER_THRESHOLD must be a number in (0, %.2f), got %q", engine.DupThreshold, raw)
		}
		engine.ConflictThreshold = v
	}
	return engine, desc, nil
}

func firstOf(getenv func(string) string, keys ...string) string {
	for _, k := range keys {
		if v := getenv(k); v != "" {
			return v
		}
	}
	return ""
}
