// Package store defines the persistence contract for memkit and its backends.
//
// The Store interface is intentionally small and backend-agnostic so that the
// SQLite backend (dev, single-binary self-host) and a future Postgres backend
// (multi-node prod) are drop-in swappable. All operations are scoped by tenant
// and user — there is no cross-tenant read path.
package store

import (
	"context"
	"time"
)

// Memory is a single atomic fact remembered for a (tenant, user).
type Memory struct {
	ID           string            `json:"id"`
	TenantID     string            `json:"tenant_id"`
	UserID       string            `json:"user_id"`
	Content      string            `json:"content"`
	Category     string            `json:"category"`
	Confidence   float64           `json:"confidence"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	LastAccessed time.Time         `json:"last_accessed"`
	AccessCount  int               `json:"access_count"`
	// SupersededBy is the ID of the memory that replaced this one, or "" if
	// this fact is still active. Conflict resolution archives rather than
	// deletes, preserving lineage. See github.com/voltagebots/conflict-lens.
	SupersededBy string `json:"superseded_by,omitempty"`
}

// Active reports whether the memory is the current truth (not superseded).
func (m Memory) Active() bool { return m.SupersededBy == "" }

// Scored is a Memory plus its computed retrieval score for a query.
type Scored struct {
	Memory
	Score float64 `json:"score"`
}

// Category is an aggregate view over a user's memories.
type Category struct {
	Name        string    `json:"name"`
	Count       int       `json:"count"`
	LastUpdated time.Time `json:"last_updated"`
}

// SearchOpts tunes a retrieval.
type SearchOpts struct {
	Category string // optional category filter
	Limit    int    // max results (0 → default)
}

// Store is the persistence contract. Implementations must be safe for
// concurrent use.
type Store interface {
	// Insert stores a new memory. The memory is active (SupersededBy == "").
	Insert(ctx context.Context, m Memory) error

	// Get returns a single memory by ID regardless of active state, or
	// (Memory{}, ErrNotFound).
	Get(ctx context.Context, tenant, id string) (Memory, error)

	// Search returns active memories for (tenant, user) ranked by relevance ×
	// time-decay. Superseded memories are never returned.
	Search(ctx context.Context, tenant, user, query string, opts SearchOpts) ([]Scored, error)

	// Replace inserts m and supersedes oldID with it in one transaction. If oldID
	// is missing it returns ErrNotFound, and if oldID is already superseded it
	// returns ErrNotActive; in both cases nothing is inserted.
	Replace(ctx context.Context, m Memory, oldID string) error

	// Supersede marks oldID as replaced by newID. Both must belong to tenant. It
	// succeeds only while oldID is active: a memory that is already superseded
	// returns ErrNotActive and is left unchanged.
	Supersede(ctx context.Context, tenant, oldID, newID string) error

	// Touch bumps LastAccessed and AccessCount for a recalled memory.
	Touch(ctx context.Context, tenant, id string) error

	// Delete permanently removes a memory (hard delete, for corrections).
	Delete(ctx context.Context, tenant, id string) (bool, error)

	// PurgeUser permanently removes ALL memories for a user (GDPR erasure).
	// Returns the number of rows removed.
	PurgeUser(ctx context.Context, tenant, user string) (int, error)

	// PruneSuperseded hard-deletes superseded (archived) memories last touched
	// before olderThan, across all tenants. Active facts are never removed.
	// Used by the maintenance loop to keep the store lean while retaining
	// recent history. Returns the number of rows removed.
	PruneSuperseded(ctx context.Context, olderThan time.Time) (int, error)

	// Categories lists active-memory categories for a user.
	Categories(ctx context.Context, tenant, user string) ([]Category, error)

	// ActiveByCategory returns all active memories in a category, newest first.
	// Used by conflict resolution to find candidates that a new fact may
	// contradict.
	ActiveByCategory(ctx context.Context, tenant, user, category string) ([]Memory, error)

	// Close releases backend resources.
	Close() error
}
