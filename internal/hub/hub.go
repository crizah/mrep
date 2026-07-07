// Package hub is the in-process broker described in HLD §4 and detailed in
// docs/LLD-websocket.md: it persists every message exactly once and fans the
// result out to every sink (connected phones + the TUI), giving the whole
// system a single, consistently-ordered write path.
package hub

import (
	"context"
	"log"

	"mrep/internal/store"
)

// tuiSubBuffer is generous on purpose: the TUI is a must-deliver sink (LLD §3.4).
const tuiSubBuffer = 256

// Event is what a TUI subscription channel carries.
type Event interface{ isEvent() }

// MessageEvent is a newly published message to render.
type MessageEvent struct{ Msg *store.Message }

// PresenceEvent reports the current count of connected phones.
type PresenceEvent struct{ Count int }

func (MessageEvent) isEvent()  {}
func (PresenceEvent) isEvent() {}

type publishReq struct {
	msg   *store.Message
	reply chan publishResult
}

type publishResult struct {
	msg *store.Message
	err error
}

type registerReq struct {
	client *Client
	since  int64
}

// Hub owns all shared state behind a single goroutine (Run); nothing here
// needs a mutex because only that goroutine ever touches clients/tuiSubs.
type Hub struct {
	store        *store.Store
	historyLimit int

	inbound     chan publishReq
	register    chan registerReq
	unregister  chan *Client
	subscribe   chan chan Event
	unsubscribe chan chan Event

	clients map[*Client]struct{}
	tuiSubs map[chan Event]struct{}
}

// New builds a Hub. Call Run in its own goroutine before anything else
// touches it (Publish, Subscribe, and client registration all send on
// unbuffered channels that only Run drains).
func New(st *store.Store, historyLimit int) *Hub {
	return &Hub{
		store:        st,
		historyLimit: historyLimit,
		inbound:      make(chan publishReq),
		register:     make(chan registerReq),
		unregister:   make(chan *Client),
		subscribe:    make(chan chan Event),
		unsubscribe:  make(chan chan Event),
		clients:      make(map[*Client]struct{}),
		tuiSubs:      make(map[chan Event]struct{}),
	}
}

// Run is the hub's single-writer event loop. It blocks until ctx is
// canceled, closing every connected client's send channel on the way out.
func (h *Hub) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			for c := range h.clients {
				delete(h.clients, c)
				close(c.send)
			}
			return

		case sub := <-h.subscribe:
			h.tuiSubs[sub] = struct{}{}

		case sub := <-h.unsubscribe:
			delete(h.tuiSubs, sub)

		case req := <-h.register:
			h.clients[req.client] = struct{}{}
			h.replayHistory(req.client, req.since)
			h.emitPresence()

		case c := <-h.unregister:
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				close(c.send)
				h.emitPresence()
			}

		case req := <-h.inbound:
			h.handlePublish(req)
		}
	}
}

func (h *Hub) handlePublish(req publishReq) {
	m := req.msg
	if err := h.store.Insert(m); err != nil {
		req.reply <- publishResult{err: err}
		return
	}

	frame, err := encodeMsgFrame(m)
	if err != nil {
		log.Printf("hub: encode message frame: %v", err)
	} else {
		h.fanoutWS(frame)
	}
	h.fanoutTUI(MessageEvent{Msg: m})

	req.reply <- publishResult{msg: m}
}

// replayHistory runs as part of the same run() iteration that registers the
// client, so it is atomic with respect to concurrent Publish calls: any
// message published after this point is necessarily fanned out in a later
// select iteration, strictly after this history frame is already queued in
// c.send. That ordering is what prevents the client from ever missing (or
// receiving out of order relative to) a message straddling its connect.
func (h *Hub) replayHistory(c *Client, since int64) {
	msgs, err := h.store.Since(since, h.historyLimit)
	if err != nil {
		log.Printf("hub: history query: %v", err)
		return
	}
	frame, err := encodeHistoryFrame(msgs)
	if err != nil {
		log.Printf("hub: encode history frame: %v", err)
		return
	}
	select {
	case c.send <- frame:
	default:
		// Buffer was already full before the client got its first frame:
		// reap it now rather than serve a half-initialized connection.
		delete(h.clients, c)
		close(c.send)
	}
}

// fanoutWS is best-effort: a phone that can't keep up (full send buffer) is
// dropped rather than allowed to stall delivery to everyone else. It
// reconnects and catches up via `since` replay (LLD-websocket §8).
func (h *Hub) fanoutWS(frame []byte) {
	for c := range h.clients {
		select {
		case c.send <- frame:
		default:
			delete(h.clients, c)
			close(c.send)
		}
	}
}

// fanoutTUI is must-deliver: the TUI is the source-of-truth display, so this
// blocks rather than drops. See docs/LLD-websocket.md §10 for why this can't
// deadlock against a concurrent Publish call.
func (h *Hub) fanoutTUI(ev Event) {
	for ch := range h.tuiSubs {
		ch <- ev
	}
}

func (h *Hub) emitPresence() {
	h.fanoutTUI(PresenceEvent{Count: len(h.clients)})
}

// Publish persists m (assigning ID + CreatedAt in place) and fans the
// resulting record out to every connected phone and TUI subscriber. Safe to
// call concurrently from any goroutine (TUI send command, HTTP handlers,
// client readPumps).
func (h *Hub) Publish(m *store.Message) (*store.Message, error) {
	reply := make(chan publishResult, 1)
	h.inbound <- publishReq{msg: m, reply: reply}
	r := <-reply
	return r.msg, r.err
}

// Subscribe registers a new must-deliver event channel, used by the TUI to
// receive live messages and presence updates (HLD §10.3). The returned
// cancel func must be called when done to release the channel's slot.
func (h *Hub) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, tuiSubBuffer)
	h.subscribe <- ch
	return ch, func() { h.unsubscribe <- ch }
}

// registerClient and unregisterClient are invoked by Client, which lives in
// this package specifically so it can reach these without exporting them.
func (h *Hub) registerClient(c *Client, since int64) {
	h.register <- registerReq{client: c, since: since}
}

func (h *Hub) unregisterClient(c *Client) {
	h.unregister <- c
}
