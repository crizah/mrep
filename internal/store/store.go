// Package store is the SQLite persistence layer (HLD §7). It owns the
// messages table, blob-token/on-disk bookkeeping, and retention pruning.
package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Message is one row of the messages table (HLD §7.1).
type Message struct {
	ID         int64
	Type       string // "text" | "file" | "image"
	Content    string // text body, or "/blob/<token>/<filename>"
	Filename   string
	MimeType   string
	Sender     string // "laptop" | "phone"
	BlobToken  string
	SourcePath string // laptop-origin absolute path; "" for phone/clipboard
	Size       int64
	CreatedAt  time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS messages (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    type        TEXT NOT NULL,
    content     TEXT NOT NULL,
    filename    TEXT DEFAULT '',
    mime_type   TEXT DEFAULT '',
    sender      TEXT NOT NULL,
    blob_token  TEXT DEFAULT '',
    source_path TEXT DEFAULT '',
    size        INTEGER DEFAULT 0,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_messages_created_at ON messages(created_at);
`

// Store wraps the SQLite connection. Safe for concurrent use.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and applies
// the schema. A single connection is used: this app has one writer goroutine
// (the Hub) and light read traffic, so there is no benefit to a pool and it
// sidesteps SQLite's well-known "database is locked" footguns entirely.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}

	return &Store{db: db}, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Insert persists m, assigning its ID and CreatedAt in place.
func (s *Store) Insert(m *Message) error {
	row := s.db.QueryRow(`
		INSERT INTO messages (type, content, filename, mime_type, sender, blob_token, source_path, size)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at
	`, m.Type, m.Content, m.Filename, m.MimeType, m.Sender, m.BlobToken, m.SourcePath, m.Size)

	return row.Scan(&m.ID, &m.CreatedAt)
}

// GetByToken looks up the message owning a given blob token, for the
// /blob/:token handler to resolve mime type and filename.
func (s *Store) GetByToken(token string) (*Message, error) {
	m := &Message{}
	err := s.db.QueryRow(`
		SELECT id, type, content, filename, mime_type, sender, blob_token, source_path, size, created_at
		FROM messages WHERE blob_token = ?
	`, token).Scan(&m.ID, &m.Type, &m.Content, &m.Filename, &m.MimeType, &m.Sender, &m.BlobToken, &m.SourcePath, &m.Size, &m.CreatedAt)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// Since returns up to limit messages with id > sinceID (LLD-websocket §8).
// Per the design, a phone should always catch up on its *most recent* gap:
// on a fresh connect (sinceID==0) that means the newest `limit` messages, and
// on a reconnect whose gap exceeds `limit` it means the newest `limit` of the
// qualifying rows, never the oldest. We select DESC-with-LIMIT first and then
// re-sort ascending for display order.
func (s *Store) Since(sinceID int64, limit int) ([]*Message, error) {
	rows, err := s.db.Query(`
		SELECT id, type, content, filename, mime_type, sender, blob_token, source_path, size, created_at
		FROM (
			SELECT id, type, content, filename, mime_type, sender, blob_token, source_path, size, created_at
			FROM messages
			WHERE id > ?
			ORDER BY id DESC
			LIMIT ?
		)
		ORDER BY id ASC
	`, sinceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Message
	for rows.Next() {
		m := &Message{}
		if err := rows.Scan(&m.ID, &m.Type, &m.Content, &m.Filename, &m.MimeType, &m.Sender, &m.BlobToken, &m.SourcePath, &m.Size, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Prune deletes messages older than cutoff and returns the blob tokens of any
// deleted file messages, so the caller can remove the corresponding on-disk
// blobs (HLD §7.3). A zero cutoff (time.Time{}) is never passed here; callers
// wanting "delete everything" should use ClearAll instead.
func (s *Store) Prune(cutoff time.Time) ([]string, error) {
	return s.deleteWhere(`created_at < ?`, cutoff)
}

// ClearAll deletes every message and returns all blob tokens to remove
// (HLD §7.3, the TUI "X" clear-history action).
func (s *Store) ClearAll() ([]string, error) {
	return s.deleteWhere(`1 = 1`)
}

func (s *Store) deleteWhere(cond string, args ...any) ([]string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT blob_token FROM messages WHERE `+cond+` AND blob_token != ''`, args...)
	if err != nil {
		return nil, err
	}
	var tokens []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return nil, err
		}
		tokens = append(tokens, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(`DELETE FROM messages WHERE `+cond, args...); err != nil {
		return nil, err
	}

	return tokens, tx.Commit()
}
