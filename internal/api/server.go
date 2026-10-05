// Package api exposes memkit's HTTP surface: a small REST API over the Store,
// with conflict-lens applied on write so contradictions are resolved rather
// than accumulated.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	conflict "github.com/voltagebots/conflict-lens"
	"github.com/voltagebots/memkit/internal/store"
)

// Server wires the store, the conflict engine, and tenant authentication.
type Server struct {
	store  store.Store
	engine *conflict.Engine
	// auth maps an API key to a tenant ID. A request with no matching key is
	// rejected. Keep small; swap for a DB-backed lookup at scale.
	auth map[string]string
}

// New builds a Server. auth maps api-key → tenant-id.
func New(s store.Store, e *conflict.Engine, auth map[string]string) *Server {
	return &Server{store: s, engine: e, auth: auth}
}

// Handler returns the root http.Handler with all routes mounted.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /v1/memories", s.auth0(s.remember))
	mux.HandleFunc("GET /v1/memories/search", s.auth0(s.search))
	mux.HandleFunc("PUT /v1/memories/{id}", s.auth0(s.update))
	mux.HandleFunc("DELETE /v1/memories/{id}", s.auth0(s.forget))
	mux.HandleFunc("GET /v1/categories", s.auth0(s.categories))
	mux.HandleFunc("DELETE /v1/users/{user_id}", s.auth0(s.purgeUser))
	return mux
}

// ── handlers ─────────────────────────────────────────────────────────────────

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type rememberReq struct {
	UserID           string            `json:"user_id"`
	Content          string            `json:"content"`
	Category         string            `json:"category"`
	Confidence       *float64          `json:"confidence"`
	Metadata         map[string]string `json:"metadata"`
	ResolveConflicts *bool             `json:"resolve_conflicts"` // default true
}

type rememberResp struct {
	ID           string  `json:"id"`
	Action       string  `json:"action"` // add | update | duplicate
	SupersededID string  `json:"superseded_id,omitempty"`
	Similarity   float64 `json:"similarity,omitempty"`
	Reason       string  `json:"reason"`
}

func (s *Server) remember(w http.ResponseWriter, r *http.Request, tenant string) {
	var req rememberReq
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		writeErr(w, http.StatusBadRequest, "content is required")
		return
	}
	user := orDefault(req.UserID, "default")
	category := orDefault(req.Category, "general")
	confidence := 1.0
	if req.Confidence != nil {
		confidence = *req.Confidence
	}
	resolve := req.ResolveConflicts == nil || *req.ResolveConflicts

	ctx := r.Context()

	// conflict-lens: compare against existing active facts in the same category.
	if resolve {
		existing, err := s.store.ActiveByCategory(ctx, tenant, user, category)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "lookup failed")
			return
		}
		decision := s.engine.Resolve(req.Content, toFacts(existing))

		switch decision.Action {
		case conflict.ActionDuplicate:
			_ = s.store.Touch(ctx, tenant, decision.TargetID)
			writeJSON(w, http.StatusOK, rememberResp{
				ID: decision.TargetID, Action: "duplicate",
				Similarity: decision.Similarity, Reason: decision.Reason,
			})
			return

		case conflict.ActionUpdate:
			id, err := s.insert(ctx, tenant, user, sanitize(req.Content), category, confidence, req.Metadata)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "store failed")
				return
			}
			if err := s.store.Supersede(ctx, tenant, decision.TargetID, id); err != nil {
				writeErr(w, http.StatusInternalServerError, "supersede failed")
				return
			}
			writeJSON(w, http.StatusCreated, rememberResp{
				ID: id, Action: "update", SupersededID: decision.TargetID,
				Similarity: decision.Similarity, Reason: decision.Reason,
			})
			return
		}
		// ActionAdd falls through to a plain insert below.
	}

	id, err := s.insert(ctx, tenant, user, sanitize(req.Content), category, confidence, req.Metadata)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store failed")
		return
	}
	writeJSON(w, http.StatusCreated, rememberResp{ID: id, Action: "add", Reason: "new information"})
}

func (s *Server) search(w http.ResponseWriter, r *http.Request, tenant string) {
	q := r.URL.Query()
	query := q.Get("q")
	if strings.TrimSpace(query) == "" {
		writeErr(w, http.StatusBadRequest, "q is required")
		return
	}
	user := orDefault(q.Get("user_id"), "default")
	opts := store.SearchOpts{Category: q.Get("category"), Limit: atoiDefault(q.Get("limit"), 10)}

	results, err := s.store.Search(r.Context(), tenant, user, query, opts)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "search failed")
		return
	}
	if results == nil {
		results = []store.Scored{}
	}
	for _, m := range results {
		_ = s.store.Touch(r.Context(), tenant, m.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(results), "memories": results})
}

type updateReq struct {
	Content    string   `json:"content"`
	Category   string   `json:"category"`
	Confidence *float64 `json:"confidence"`
}

// update explicitly supersedes the memory at {id} with a corrected fact.
func (s *Server) update(w http.ResponseWriter, r *http.Request, tenant string) {
	oldID := r.PathValue("id")
	var req updateReq
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		writeErr(w, http.StatusBadRequest, "content is required")
		return
	}
	old, err := s.store.Get(r.Context(), tenant, oldID)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "memory not found")
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	confidence := 1.0
	if req.Confidence != nil {
		confidence = *req.Confidence
	}
	newID, err := s.insert(r.Context(), tenant, old.UserID, sanitize(req.Content),
		orDefault(req.Category, old.Category), confidence, nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store failed")
		return
	}
	if err := s.store.Supersede(r.Context(), tenant, oldID, newID); err != nil {
		writeErr(w, http.StatusInternalServerError, "supersede failed")
		return
	}
	writeJSON(w, http.StatusOK, rememberResp{
		ID: newID, Action: "update", SupersededID: oldID, Reason: "explicit update",
	})
}

func (s *Server) forget(w http.ResponseWriter, r *http.Request, tenant string) {
	ok, err := s.store.Delete(r.Context(), tenant, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "delete failed")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "memory not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) categories(w http.ResponseWriter, r *http.Request, tenant string) {
	user := orDefault(r.URL.Query().Get("user_id"), "default")
	cats, err := s.store.Categories(r.Context(), tenant, user)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(cats), "categories": cats})
}

func (s *Server) purgeUser(w http.ResponseWriter, r *http.Request, tenant string) {
	n, err := s.store.PurgeUser(r.Context(), tenant, r.PathValue("user_id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "purge failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": n})
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (s *Server) insert(ctx context.Context, tenant, user, content, category string, confidence float64, meta map[string]string) (string, error) {
	now := time.Now()
	m := store.Memory{
		ID: newID(), TenantID: tenant, UserID: user, Content: content,
		Category: category, Confidence: confidence, Metadata: meta,
		CreatedAt: now, LastAccessed: now,
	}
	return m.ID, s.store.Insert(ctx, m)
}

// auth0 wraps a tenant-scoped handler with bearer-token authentication.
func (s *Server) auth0(h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := bearer(r)
		tenant, ok := s.auth[key]
		if key == "" || !ok {
			writeErr(w, http.StatusUnauthorized, "invalid or missing API key")
			return
		}
		h(w, r, tenant)
	}
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(after)
	}
	return ""
}

func toFacts(ms []store.Memory) []conflict.Fact {
	out := make([]conflict.Fact, len(ms))
	for i, m := range ms {
		out[i] = conflict.Fact{ID: m.ID, Content: m.Content}
	}
	return out
}

// sanitize strips the Unicode tag block (U+E0000–U+E007F): an invisible
// prompt-injection vector when stored memories later enter an LLM prompt.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0xE0000 && r <= 0xE007F {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
