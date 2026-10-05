package main

import (
	"fmt"
	"strconv"
	"time"

	conflict "github.com/voltagebots/conflict-lens"
	"github.com/voltagebots/memkit/internal/resolver"
)

const (
	resolverConflictThreshold = 0.2
	ollamaCandidateThreshold  = 0.1
	defaultOllamaCandidates   = 10
	defaultOllamaBudget       = 30 * time.Second
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
		switch ollamaMode := getenv("MEMKIT_OLLAMA_MODE"); ollamaMode {
		case "", "pair":
			engine.Resolver = resolver.PairOnly{Judge: o}
		case "batch":
			engine.Resolver = o
		default:
			return nil, "", fmt.Errorf("unknown MEMKIT_OLLAMA_MODE %q (want pair or batch)", ollamaMode)
		}
		engine.ConflictThreshold = ollamaCandidateThreshold
		engine.MaxCandidates = defaultOllamaCandidates
		desc = fmt.Sprintf("conflict resolver: Ollama %s at %s (local, %s mode, up to %d candidates)", o.Model, o.BaseURL, ollamaModeName(getenv("MEMKIT_OLLAMA_MODE")), engine.MaxCandidates)
	default:
		return nil, "", fmt.Errorf("unknown MEMKIT_RESOLVER %q (want none, claude or ollama)", mode)
	}

	if raw := getenv("MEMKIT_RESOLVER_MAX_CANDIDATES"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return nil, "", fmt.Errorf("MEMKIT_RESOLVER_MAX_CANDIDATES must be an integer >= 1, got %q", raw)
		}
		if mode == "ollama" && n < 2 {
			return nil, "", fmt.Errorf("MEMKIT_RESOLVER_MAX_CANDIDATES must be >= 2 with the ollama resolver: a single candidate falls back to a heuristic update when the model fails")
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

// resolveBudget is how long a write may wait for conflict resolution before the
// fact is added without superseding anything. It defaults to 30 seconds with the
// local model and to unbounded otherwise; "0s" turns it off.
func resolveBudget(getenv func(string) string) (time.Duration, error) {
	raw := getenv("MEMKIT_RESOLVER_BUDGET")
	if raw == "" {
		if getenv("MEMKIT_RESOLVER") == "ollama" {
			return defaultOllamaBudget, nil
		}
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("MEMKIT_RESOLVER_BUDGET must be a non-negative duration such as 30s, got %q", raw)
	}
	return d, nil
}

func firstOf(getenv func(string) string, keys ...string) string {
	for _, k := range keys {
		if v := getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func ollamaModeName(mode string) string {
	if mode == "" {
		return "pair"
	}
	return mode
}
