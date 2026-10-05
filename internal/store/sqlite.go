package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite" // pure-Go driver, no CGO → single static binary
)

// SQLite is the default Store backend: a single file, FTS5 full-text search,
// zero external services. Suitable for dev and small/medium self-hosted
// deployments. Swap for Postgres at scale via the same Store interface.
type SQLite struct {
	db *sql.DB
}

// OpenSQLite opens (and migrates) a SQLite-backed store at path. Use
// ":memory:" for ephemeral/test stores.
func OpenSQLite(path string) (*SQLite, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// modernc serializes writes; a single conn avoids "database is locked".
	db.SetMaxOpenConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			return nil, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}

	s := &SQLite{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *SQLite) migrate() error {
	const schema = `
	CREATE TABLE IF NOT EXISTS memories (
		id            TEXT PRIMARY KEY,
		tenant_id     TEXT NOT NULL,
		user_id       TEXT NOT NULL,
		content       TEXT NOT NULL,
		category      TEXT NOT NULL DEFAULT 'general',
		confidence    REAL NOT NULL DEFAULT 1.0,
		metadata      TEXT NOT NULL DEFAULT '{}',
		created_at    INTEGER NOT NULL,
		last_accessed INTEGER NOT NULL,
		access_count  INTEGER NOT NULL DEFAULT 0,
		superseded_by TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_mem_tenant_user ON memories(tenant_id, user_id);
	CREATE INDEX IF NOT EXISTS idx_mem_category    ON memories(tenant_id, user_id, category);

	CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
		id        UNINDEXED,
		tenant_id UNINDEXED,
		user_id   UNINDEXED,
		content,
		category,
		tokenize = 'porter ascii'
	);`
	_, err := s.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

func (s *SQLite) Insert(ctx context.Context, m Memory) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertMemory(ctx, tx, m); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) Replace(ctx context.Context, m Memory, oldID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertMemory(ctx, tx, m); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE memories SET superseded_by = ? WHERE tenant_id = ? AND id = ? AND superseded_by IS NULL`,
		m.ID, m.TenantID, oldID)
	if err != nil {
		return fmt.Errorf("supersede: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var existing string
		err := tx.QueryRowContext(ctx, `SELECT id FROM memories WHERE tenant_id = ? AND id = ?`, m.TenantID, oldID).Scan(&existing)
		if err == sql.ErrNoRows {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		return ErrNotActive
	}
	return tx.Commit()
}

func insertMemory(ctx context.Context, tx *sql.Tx, m Memory) error {
	meta, err := json.Marshal(orEmpty(m.Metadata))
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO memories
			(id, tenant_id, user_id, content, category, confidence, metadata, created_at, last_accessed, access_count, superseded_by)
		VALUES (?,?,?,?,?,?,?,?,?,?,NULL)`,
		m.ID, m.TenantID, m.UserID, m.Content, m.Category, m.Confidence, string(meta),
		m.CreatedAt.UnixMilli(), m.LastAccessed.UnixMilli(), m.AccessCount)
	if err != nil {
		return fmt.Errorf("insert memory: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO memories_fts (id, tenant_id, user_id, content, category)
		VALUES (?,?,?,?,?)`,
		m.ID, m.TenantID, m.UserID, m.Content, m.Category)
	if err != nil {
		return fmt.Errorf("insert fts: %w", err)
	}
	return nil
}

func (s *SQLite) Get(ctx context.Context, tenant, id string) (Memory, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, user_id, content, category, confidence, metadata,
		       created_at, last_accessed, access_count, COALESCE(superseded_by,'')
		FROM memories WHERE tenant_id = ? AND id = ?`, tenant, id)
	m, err := scanMemory(row)
	if err == sql.ErrNoRows {
		return Memory{}, ErrNotFound
	}
	return m, err
}

func (s *SQLite) Search(ctx context.Context, tenant, user, query string, opts SearchOpts) ([]Scored, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	fts := escapeFTS(query)

	// Capture BM25 rank in a subquery BEFORE joining — the FTS5 `rank` column
	// returns 0 when read through a JOIN alias. (Learned the hard way.)
	q := `
		SELECT m.id, m.tenant_id, m.user_id, m.content, m.category, m.confidence,
		       m.metadata, m.created_at, m.last_accessed, m.access_count,
		       COALESCE(m.superseded_by,''), s.rnk
		FROM memories m
		JOIN (
			SELECT id, rank AS rnk FROM memories_fts
			WHERE memories_fts MATCH ? AND tenant_id = ? AND user_id = ?
			ORDER BY rank LIMIT ?
		) s ON m.id = s.id
		WHERE m.superseded_by IS NULL`
	args := []any{fts, tenant, user, limit * 3}
	if opts.Category != "" {
		q += " AND m.category = ?"
		args = append(args, opts.Category)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	now := time.Now()
	var out []Scored
	for rows.Next() {
		var m Memory
		var meta string
		var created, accessed int64
		var ftsRank float64
		if err := rows.Scan(&m.ID, &m.TenantID, &m.UserID, &m.Content, &m.Category,
			&m.Confidence, &meta, &created, &accessed, &m.AccessCount, &m.SupersededBy, &ftsRank); err != nil {
			return nil, err
		}
		hydrate(&m, meta, created, accessed)
		out = append(out, Scored{Memory: m, Score: score(m, ftsRank, now)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Re-rank by composite score and trim to the requested limit.
	sortByScoreDesc(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *SQLite) Supersede(ctx context.Context, tenant, oldID, newID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE memories SET superseded_by = ? WHERE tenant_id = ? AND id = ? AND superseded_by IS NULL`,
		newID, tenant, oldID)
	if err != nil {
		return fmt.Errorf("supersede: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.Get(ctx, tenant, oldID); err != nil {
			return err
		}
		return ErrNotActive
	}
	return nil
}

func (s *SQLite) Touch(ctx context.Context, tenant, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE memories SET last_accessed = ?, access_count = access_count + 1
		 WHERE tenant_id = ? AND id = ?`, time.Now().UnixMilli(), tenant, id)
	return err
}

func (s *SQLite) Delete(ctx context.Context, tenant, id string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `DELETE FROM memories WHERE tenant_id = ? AND id = ?`, tenant, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memories_fts WHERE id = ?`, id); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *SQLite) PurgeUser(ctx context.Context, tenant, user string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `DELETE FROM memories WHERE tenant_id = ? AND user_id = ?`, tenant, user)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := tx.ExecContext(ctx, `DELETE FROM memories_fts WHERE tenant_id = ? AND user_id = ?`, tenant, user); err != nil {
		return 0, err
	}
	return int(n), tx.Commit()
}

func (s *SQLite) PruneSuperseded(ctx context.Context, olderThan time.Time) (int, error) {
	cutoff := olderThan.UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// Mirror the delete into the FTS table first (it has no FK cascade).
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM memories_fts WHERE id IN (
			SELECT id FROM memories WHERE superseded_by IS NOT NULL AND last_accessed < ?
		)`, cutoff); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM memories WHERE superseded_by IS NOT NULL AND last_accessed < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), tx.Commit()
}

func (s *SQLite) Categories(ctx context.Context, tenant, user string) ([]Category, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT category, COUNT(*), MAX(last_accessed)
		FROM memories
		WHERE tenant_id = ? AND user_id = ? AND superseded_by IS NULL
		GROUP BY category ORDER BY MAX(last_accessed) DESC`, tenant, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Category
	for rows.Next() {
		var c Category
		var last int64
		if err := rows.Scan(&c.Name, &c.Count, &last); err != nil {
			return nil, err
		}
		c.LastUpdated = time.UnixMilli(last)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *SQLite) ActiveByCategory(ctx context.Context, tenant, user, category string) ([]Memory, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, tenant_id, user_id, content, category, confidence, metadata,
		       created_at, last_accessed, access_count, COALESCE(superseded_by,'')
		FROM memories
		WHERE tenant_id = ? AND user_id = ? AND category = ? AND superseded_by IS NULL
		ORDER BY last_accessed DESC`, tenant, user, category)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *SQLite) Close() error { return s.db.Close() }

// ── helpers ────────────────────────────────────────────────────────────────

type scanner interface {
	Scan(dest ...any) error
}

func scanMemory(r scanner) (Memory, error) {
	var m Memory
	var meta string
	var created, accessed int64
	err := r.Scan(&m.ID, &m.TenantID, &m.UserID, &m.Content, &m.Category, &m.Confidence,
		&meta, &created, &accessed, &m.AccessCount, &m.SupersededBy)
	if err != nil {
		return Memory{}, err
	}
	hydrate(&m, meta, created, accessed)
	return m, nil
}

func hydrate(m *Memory, meta string, created, accessed int64) {
	m.CreatedAt = time.UnixMilli(created)
	m.LastAccessed = time.UnixMilli(accessed)
	if meta != "" && meta != "{}" {
		_ = json.Unmarshal([]byte(meta), &m.Metadata)
	}
}

// score combines BM25 relevance, recency decay, and an access boost.
// decay halves a memory's weight roughly every 30 days; frequently recalled
// facts get a mild lift. Mirrors the model proven in the memory-mcp prototype.
func score(m Memory, ftsRank float64, now time.Time) float64 {
	ageDays := now.Sub(m.LastAccessed).Hours() / 24.0
	decay := 1.0 / (1.0 + ageDays/30.0)
	accessBoost := math.Log1p(float64(m.AccessCount)) * 0.1
	relevance := -ftsRank // FTS5 rank is negative; more negative = better
	return m.Confidence * decay * (relevance + accessBoost)
}

func sortByScoreDesc(s []Scored) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].Score > s[j-1].Score; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// escapeFTS turns a free-text query into a safe FTS5 MATCH expression: an exact
// phrase OR'd with prefix tokens, so partial words still match.
//
// The index uses the `porter ascii` tokenizer, which treats ASCII letters and
// digits and every non-ASCII character as token characters and everything else as
// a separator. The query is split the same way, and only on the ASCII space the
// mapping produces (not on Unicode whitespace, which the tokenizer keeps inside a
// token), so a query token is exactly an indexed token. That removes every FTS5 operator character ('"', '*', '-', '^',
// '(', ')', ':', '+', ...) and every punctuation mark typed in a question. Each
// token is quoted so the bare words AND, OR, NOT and NEAR are searched as text,
// not parsed as operators.
func escapeFTS(q string) string {
	clean := strings.Map(func(r rune) rune {
		if r >= utf8.RuneSelf || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return ' '
	}, q)
	fields := strings.FieldsFunc(clean, func(r rune) bool { return r == ' ' })
	if len(fields) == 0 {
		return `""`
	}
	prefixed := make([]string, len(fields))
	for i, f := range fields {
		prefixed[i] = `"` + f + `"*`
	}
	return fmt.Sprintf(`"%s" OR %s`, strings.Join(fields, " "), strings.Join(prefixed, " OR "))
}

func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
