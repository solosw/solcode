// Package changegraph persists described file changes and their affected symbols.
package changegraph

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db      *sql.DB
	options Options
}

// Options bounds durable project-change retention. A zero value disables the
// corresponding limit.
type Options struct {
	RetentionDays int
	MaxEvents     int
	MaxDatabaseMB int
}

type Change struct {
	SessionID    string
	ToolName     string
	Path         string
	Language     string
	Description  string
	ChangedLines string
	OccurredAt   time.Time
	Symbols      []Symbol
}

type Symbol struct {
	Kind       string
	Name       string
	FullName   string
	StartLine  int
	EndLine    int
	ChangeKind SymbolChangeKind
}

// SymbolChangeKind describes how a symbol participated in a file mutation.
// It is stored on the event relationship, so historic events remain accurate
// even when a symbol is later reintroduced or removed again.
type SymbolChangeKind string

const (
	SymbolAdded    SymbolChangeKind = "added"
	SymbolModified SymbolChangeKind = "modified"
	SymbolDeleted  SymbolChangeKind = "deleted"
)

type Event struct {
	ID           int64
	SessionID    string
	ToolName     string
	Path         string
	Language     string
	Description  string
	ChangedLines string
	OccurredAt   time.Time
	Symbols      []Symbol
}

func Open(path string) (*Store, error) {
	return OpenWithOptions(path, Options{})
}

func OpenWithOptions(path string, options Options) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("knowledge graph database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create knowledge graph directory: %w", err)
	}
	// Connection pragmas must be part of the DSN so they apply to every
	// connection opened by database/sql, not only the migration connection.
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open knowledge graph database: %w", err)
	}
	s := &Store{db: db, options: options}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("knowledge graph store is not open")
	}
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000", "PRAGMA foreign_keys=ON"} {
		if _, err := s.db.ExecContext(ctx, pragma); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS graph_files (
 id INTEGER PRIMARY KEY, path TEXT NOT NULL UNIQUE, language TEXT, updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS graph_symbols (
 id INTEGER PRIMARY KEY, file_id INTEGER NOT NULL REFERENCES graph_files(id) ON DELETE CASCADE,
 symbol_key TEXT NOT NULL UNIQUE, kind TEXT NOT NULL, name TEXT NOT NULL, full_name TEXT,
 start_line INTEGER, end_line INTEGER, active INTEGER NOT NULL DEFAULT 1,
 first_seen_at INTEGER NOT NULL, last_seen_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS change_events (
 id INTEGER PRIMARY KEY, session_id TEXT, tool_name TEXT NOT NULL,
 file_id INTEGER NOT NULL REFERENCES graph_files(id) ON DELETE CASCADE,
 description TEXT NOT NULL, changed_lines TEXT, occurred_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS change_event_symbols (
 event_id INTEGER NOT NULL REFERENCES change_events(id) ON DELETE CASCADE,
 symbol_id INTEGER NOT NULL REFERENCES graph_symbols(id) ON DELETE CASCADE,
 change_kind TEXT NOT NULL DEFAULT 'modified',
 PRIMARY KEY(event_id, symbol_id)
);
CREATE INDEX IF NOT EXISTS change_events_session_time ON change_events(session_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS change_events_file_time ON change_events(file_id, occurred_at DESC);
`)
	if err != nil {
		return fmt.Errorf("migrate knowledge graph database: %w", err)
	}
	if err := ensureColumn(ctx, s.db, "change_event_symbols", "change_kind", "TEXT NOT NULL DEFAULT 'modified'"); err != nil {
		return fmt.Errorf("migrate knowledge graph symbol changes: %w", err)
	}
	return nil
}

func ensureColumn(ctx context.Context, db *sql.DB, table, column, definition string) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+definition)
	return err
}

func (s *Store) Record(ctx context.Context, change Change) error {
	if s == nil || s.db == nil {
		return nil
	}
	change.Path = filepath.ToSlash(strings.TrimSpace(change.Path))
	change.Description = strings.TrimSpace(change.Description)
	if change.Path == "" || change.Description == "" {
		return nil
	}
	if change.OccurredAt.IsZero() {
		change.OccurredAt = time.Now().UTC()
	}
	at := change.OccurredAt.UTC().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `INSERT INTO graph_files(path, language, updated_at) VALUES(?, ?, ?)
ON CONFLICT(path) DO UPDATE SET language=COALESCE(NULLIF(excluded.language, ''), graph_files.language), updated_at=excluded.updated_at`, change.Path, change.Language, at); err != nil {
		return err
	}
	var fileID int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM graph_files WHERE path=?`, change.Path).Scan(&fileID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO change_events(session_id, tool_name, file_id, description, changed_lines, occurred_at) VALUES(?, ?, ?, ?, ?, ?)`, change.SessionID, change.ToolName, fileID, change.Description, change.ChangedLines, at)
	if err != nil {
		return err
	}
	eventID, err := res.LastInsertId()
	if err != nil {
		return err
	}
	for _, symbol := range change.Symbols {
		if strings.TrimSpace(symbol.Name) == "" {
			continue
		}
		changeKind := normalizeSymbolChangeKind(symbol.ChangeKind)
		key := fmt.Sprintf("%s:%s:%s", change.Path, symbol.Kind, symbol.FullName)
		if symbol.FullName == "" {
			key = fmt.Sprintf("%s:%s:%s", change.Path, symbol.Kind, symbol.Name)
		}
		active := changeKind != SymbolDeleted
		if _, err = tx.ExecContext(ctx, `INSERT INTO graph_symbols(file_id, symbol_key, kind, name, full_name, start_line, end_line, active, first_seen_at, last_seen_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(symbol_key) DO UPDATE SET active=excluded.active, start_line=excluded.start_line, end_line=excluded.end_line, last_seen_at=excluded.last_seen_at`, fileID, key, symbol.Kind, symbol.Name, symbol.FullName, symbol.StartLine, symbol.EndLine, active, at, at); err != nil {
			return err
		}
		var symbolID int64
		if err = tx.QueryRowContext(ctx, `SELECT id FROM graph_symbols WHERE symbol_key=?`, key).Scan(&symbolID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO change_event_symbols(event_id, symbol_id, change_kind) VALUES(?, ?, ?)
ON CONFLICT(event_id, symbol_id) DO UPDATE SET change_kind=excluded.change_kind`, eventID, symbolID, changeKind); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.prune(ctx)
}

func (s *Store) prune(ctx context.Context) error {
	if s == nil || s.db == nil {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if s.options.RetentionDays > 0 {
		cutoff := time.Now().AddDate(0, 0, -s.options.RetentionDays).UTC().UnixMilli()
		if _, err := tx.ExecContext(ctx, `DELETE FROM change_events WHERE occurred_at < ?`, cutoff); err != nil {
			return err
		}
	}
	if s.options.MaxEvents > 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM change_events WHERE id IN (
			SELECT id FROM change_events ORDER BY occurred_at DESC, id DESC LIMIT -1 OFFSET ?
		)`, s.options.MaxEvents); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM graph_symbols WHERE id NOT IN (SELECT DISTINCT symbol_id FROM change_event_symbols)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM graph_files WHERE id NOT IN (SELECT DISTINCT file_id FROM change_events)`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.pruneDatabaseSize(ctx)
}

func (s *Store) pruneDatabaseSize(ctx context.Context) error {
	if s.options.MaxDatabaseMB <= 0 {
		return nil
	}
	var pageCount, pageSize int64
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return err
	}
	limitBytes := int64(s.options.MaxDatabaseMB) * 1024 * 1024
	if pageCount*pageSize <= limitBytes {
		return nil
	}
	// SQLite does not return pages to the OS until VACUUM. Delete a bounded
	// oldest batch, reclaim pages once, then leave any remaining pressure for a
	// future recording cycle rather than unexpectedly discarding all history.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM change_events WHERE id IN (
		SELECT id FROM change_events ORDER BY occurred_at ASC, id ASC LIMIT 100
	)`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM graph_symbols WHERE id NOT IN (SELECT DISTINCT symbol_id FROM change_event_symbols)`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM graph_files WHERE id NOT IN (SELECT DISTINCT file_id FROM change_events)`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `VACUUM`)
	return err
}

func normalizeSymbolChangeKind(kind SymbolChangeKind) SymbolChangeKind {
	switch kind {
	case SymbolAdded, SymbolModified, SymbolDeleted:
		return kind
	default:
		return SymbolModified
	}
}

func (s *Store) Recent(ctx context.Context, sessionID string, limit int) ([]Event, error) {
	if s == nil || s.db == nil || limit <= 0 {
		return nil, nil
	}
	query := `SELECT e.id, e.session_id, e.tool_name, f.path, f.language, e.description, e.changed_lines, e.occurred_at FROM change_events e JOIN graph_files f ON f.id=e.file_id`
	args := []any{}
	if strings.TrimSpace(sessionID) != "" {
		query += ` WHERE e.session_id=?`
		args = append(args, sessionID)
	}
	query += ` ORDER BY e.occurred_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var sessionID, language, changedLines sql.NullString
		var millis int64
		if err := rows.Scan(&e.ID, &sessionID, &e.ToolName, &e.Path, &language, &e.Description, &changedLines, &millis); err != nil {
			return nil, err
		}
		e.SessionID = sessionID.String
		e.Language = language.String
		e.ChangedLines = changedLines.String
		e.OccurredAt = time.UnixMilli(millis).UTC()
		symbolRows, err := s.db.QueryContext(ctx, `SELECT s.kind, s.name, s.full_name, s.start_line, s.end_line, es.change_kind FROM graph_symbols s JOIN change_event_symbols es ON es.symbol_id=s.id WHERE es.event_id=? ORDER BY s.start_line`, e.ID)
		if err != nil {
			return nil, err
		}
		for symbolRows.Next() {
			var symbol Symbol
			var fullName sql.NullString
			if err := symbolRows.Scan(&symbol.Kind, &symbol.Name, &fullName, &symbol.StartLine, &symbol.EndLine, &symbol.ChangeKind); err != nil {
				_ = symbolRows.Close()
				return nil, err
			}
			symbol.FullName = fullName.String
			e.Symbols = append(e.Symbols, symbol)
		}
		if err := symbolRows.Close(); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
