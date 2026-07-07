package hub

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mrep/internal/store"
)

func newTestHub(t *testing.T) *Hub {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "mrep.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	h := New(st, 200)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go h.Run(ctx)
	return h
}

func TestPublishAssignsIDAndFansOutToTUI(t *testing.T) {
	h := newTestHub(t)

	sub, cancel := h.Subscribe()
	defer cancel()

	m, err := h.Publish(&store.Message{Type: "text", Content: "hi", Sender: "laptop"})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if m.ID == 0 {
		t.Fatalf("expected non-zero ID")
	}

	select {
	case ev := <-sub:
		me, ok := ev.(MessageEvent)
		if !ok {
			t.Fatalf("expected MessageEvent, got %T", ev)
		}
		if me.Msg.ID != m.ID || me.Msg.Content != "hi" {
			t.Errorf("unexpected event payload: %+v", me.Msg)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for TUI event")
	}
}

func TestPublishWorksWithZeroSubscribers(t *testing.T) {
	h := newTestHub(t)

	if _, err := h.Publish(&store.Message{Type: "text", Content: "no one home", Sender: "laptop"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}

func TestOrderingAcrossManyPublishes(t *testing.T) {
	h := newTestHub(t)

	sub, cancel := h.Subscribe()
	defer cancel()

	const n = 50
	for range n {
		if _, err := h.Publish(&store.Message{Type: "text", Content: "x", Sender: "laptop"}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	var lastID int64
	for i := range n {
		select {
		case ev := <-sub:
			me := ev.(MessageEvent)
			if me.Msg.ID <= lastID {
				t.Fatalf("out-of-order delivery: got id %d after %d", me.Msg.ID, lastID)
			}
			lastID = me.Msg.ID
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for event %d", i)
		}
	}
}

func TestSlowClientIsReapedNotBlocking(t *testing.T) {
	h := newTestHub(t)

	// A client whose send buffer we fill manually and never drain, standing
	// in for a phone that's locked/backgrounded and not reading.
	slow := &Client{hub: h, send: make(chan []byte, sendBuffer)}
	h.registerClient(slow, 0)

	// Fill its buffer past capacity via real publishes so fanoutWS has to
	// drop it instead of blocking the whole hub.
	for range sendBuffer + 5 {
		if _, err := h.Publish(&store.Message{Type: "text", Content: "flood", Sender: "laptop"}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	// If fanoutWS blocked on the slow client, this Publish would hang and
	// the test would fail via timeout at the `go test` level. Reaching here
	// means the hub kept moving.
	if _, err := h.Publish(&store.Message{Type: "text", Content: "after flood", Sender: "laptop"}); err != nil {
		t.Fatalf("Publish after flood: %v", err)
	}
}

func TestHistoryReplayOnRegister(t *testing.T) {
	h := newTestHub(t)

	for range 3 {
		if _, err := h.Publish(&store.Message{Type: "text", Content: "seed", Sender: "laptop"}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	c := &Client{hub: h, send: make(chan []byte, sendBuffer)}
	h.registerClient(c, 0)

	select {
	case frame := <-c.send:
		if len(frame) == 0 {
			t.Fatal("expected non-empty history frame")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for history frame")
	}
}

func TestPresenceEventOnRegisterAndUnregister(t *testing.T) {
	h := newTestHub(t)

	sub, cancel := h.Subscribe()
	defer cancel()

	c := &Client{hub: h, send: make(chan []byte, sendBuffer)}
	h.registerClient(c, 0)

	// First event after registering should be the history replay's implicit
	// side effects aside — presence is emitted after replay, so drain until
	// we see a PresenceEvent with Count 1.
	if !waitForPresence(t, sub, 1) {
		t.Fatal("expected presence count 1 after register")
	}

	h.unregisterClient(c)
	if !waitForPresence(t, sub, 0) {
		t.Fatal("expected presence count 0 after unregister")
	}
}

func waitForPresence(t *testing.T, sub <-chan Event, want int) bool {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-sub:
			if pe, ok := ev.(PresenceEvent); ok && pe.Count == want {
				return true
			}
		case <-deadline:
			return false
		}
	}
}
