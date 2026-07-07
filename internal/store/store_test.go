package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "mrep.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestInsertAssignsIDAndCreatedAt(t *testing.T) {
	s := openTest(t)

	m := &Message{Type: "text", Content: "hello", Sender: "laptop"}
	if err := s.Insert(m); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if m.ID == 0 {
		t.Errorf("expected non-zero ID")
	}
	if m.CreatedAt.IsZero() {
		t.Errorf("expected CreatedAt to be set")
	}
}

func TestSinceFreshLoadReturnsNewestWithinLimit(t *testing.T) {
	s := openTest(t)

	for range 5 {
		if err := s.Insert(&Message{Type: "text", Content: "msg", Sender: "laptop"}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	// since=0, limit=2 should return the newest 2 (ids 4,5), ascending.
	got, err := s.Since(0, 2)
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	if got[0].ID != 4 || got[1].ID != 5 {
		t.Errorf("expected ids [4,5] ascending, got [%d,%d]", got[0].ID, got[1].ID)
	}
}

func TestSinceGapSmallerThanLimitReturnsAllNew(t *testing.T) {
	s := openTest(t)

	var ids []int64
	for range 3 {
		m := &Message{Type: "text", Content: "msg", Sender: "laptop"}
		if err := s.Insert(m); err != nil {
			t.Fatalf("Insert: %v", err)
		}
		ids = append(ids, m.ID)
	}

	got, err := s.Since(ids[0], 200)
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 messages after since=%d, got %d", ids[0], len(got))
	}
	if got[0].ID != ids[1] || got[1].ID != ids[2] {
		t.Errorf("expected ids [%d,%d], got [%d,%d]", ids[1], ids[2], got[0].ID, got[1].ID)
	}
}

func TestPruneDeletesOldRowsAndReturnsBlobTokens(t *testing.T) {
	s := openTest(t)

	old := &Message{Type: "image", Content: "/blob/aaa/cat.png", Filename: "cat.png", BlobToken: "aaa", Sender: "laptop"}
	if err := s.Insert(old); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// Backdate it directly so it falls before the cutoff.
	if _, err := s.db.Exec(`UPDATE messages SET created_at = ? WHERE id = ?`, time.Now().Add(-48*time.Hour), old.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	fresh := &Message{Type: "text", Content: "still here", Sender: "laptop"}
	if err := s.Insert(fresh); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	tokens, err := s.Prune(time.Now().Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(tokens) != 1 || tokens[0] != "aaa" {
		t.Fatalf("expected [\"aaa\"], got %v", tokens)
	}

	remaining, err := s.Since(0, 10)
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != fresh.ID {
		t.Fatalf("expected only the fresh message to remain, got %+v", remaining)
	}
}

func TestClearAllDeletesEverything(t *testing.T) {
	s := openTest(t)

	for range 3 {
		if err := s.Insert(&Message{Type: "text", Content: "x", Sender: "laptop"}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	if _, err := s.ClearAll(); err != nil {
		t.Fatalf("ClearAll: %v", err)
	}
	remaining, err := s.Since(0, 10)
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected 0 remaining, got %d", len(remaining))
	}
}
