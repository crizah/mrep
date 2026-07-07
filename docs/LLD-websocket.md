# mrep — LLD: WebSocket + Hub layer

> Low-level design for the realtime layer: the Hub, the per-client goroutines,
> the wire protocol, heartbeat, reconnection, and the ordering/dedup guarantees.
> Refines and **supersedes HLD §8.2** (the envelope sketch). Read alongside
> HLD §4 (architecture) and §10.3 (TUI concurrency).

- **Status:** design (LLD)
- **Depends on:** `gorilla/websocket`, `gin`, `internal/store`, `internal/hub`

---

## 1. Scope

This document covers **control-plane** traffic only: the WebSocket carrying text
messages, new-message notifications, and history replay. **File bytes never
travel over the WebSocket** — they go over HTTP (`POST /upload`, `GET /blob`);
the WS only carries the resulting *metadata* frame. See HLD §8.1.

Goals for this layer:

- One consistent message order seen by every device (laptop TUI + all phones).
- Survive the phone locking / backgrounding / WiFi blips (heartbeat + reconnect).
- A slow or dead phone must never stall the laptop or other phones.
- The laptop works with **zero phones connected** (messages persist + replay).

---

## 2. Goroutine model

```
                    ┌──────────────────────────────────────────┐
   TUI send Cmd ───▶│                                          │
   /upload handler ─▶│   Hub.run()  (single goroutine)          │
   WS readPump ─────▶│   owns: clients map, tui subscribers      │
                    │   inbound: chan publishReq                │
                    │   register/unregister: chan *Client        │
                    └───┬───────────────────────┬──────────────┘
                        │ fan-out (per client)   │ fan-out (must-deliver)
              ┌─────────▼─────────┐     ┌────────▼─────────┐
              │ Client.send chan  │     │ TUI sub chan     │
              │ (buffered, 32)    │     │ (buffered, 256)  │
              └─────────┬─────────┘     └────────┬─────────┘
                writePump goroutine        waitForHub Cmd → Bubble Tea Update
                        │
                     WS conn ──▶ phone
              readPump goroutine ◀── phone
```

**Per process:** 1 `Hub.run()` goroutine. **Per connected phone:** 2 goroutines
(`readPump`, `writePump`). Everything that mutates shared hub state happens
**inside `run()`**, so there are no mutexes on the hub.

---

## 3. The Hub

### 3.1 Types

```go
package hub

type Hub struct {
    store   *store.Store          // SQLite

    inbound    chan publishReq    // publish requests (from TUI, /upload, readPump)
    register   chan *Client
    unregister chan *Client

    clients  map[*Client]struct{} // owned by run(); no lock
    tuiSubs  map[chan Event]struct{}
    historyLimit int
}

type publishReq struct {
    msg   *store.Message
    reply chan publishResult      // buffered(1); result of persist
}
type publishResult struct {
    msg *store.Message           // same pointer, now with ID + CreatedAt
    err error
}

// Event is what the TUI subscription channel carries.
type Event interface{ isEvent() }
type MessageEvent struct{ Msg *store.Message } // a new message to render
type PresenceEvent struct{ Count int }         // connected-phone count changed
func (MessageEvent) isEvent()  {}
func (PresenceEvent) isEvent() {}
```

### 3.2 The run loop (the only writer of hub state)

```go
func (h *Hub) run(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            for c := range h.clients { close(c.send) }
            return

        case c := <-h.register:
            h.clients[c] = struct{}{}
            h.emitPresence()               // tell TUI the new count

        case c := <-h.unregister:
            if _, ok := h.clients[c]; ok {
                delete(h.clients, c)
                close(c.send)              // stops its writePump
                h.emitPresence()
            }

        case req := <-h.inbound:
            m := req.msg
            if err := h.store.Insert(m); err != nil {   // assigns ID, CreatedAt
                req.reply <- publishResult{err: err}
                break
            }
            frame := encodeMsg(m)          // marshal once
            h.fanoutWS(frame)              // best-effort, per-client
            h.fanoutTUI(MessageEvent{m})   // must-deliver
            req.reply <- publishResult{msg: m}
        }
    }
}
```

Persist **and** fan-out happen in this one goroutine, so message `id` order ==
delivery order for everybody. `Insert` is metadata-only (bytes are already on
disk for files), so it's microseconds — blocking the loop on it is fine.

### 3.3 Publish (called from any goroutine)

```go
func (h *Hub) Publish(m *store.Message) (*store.Message, error) {
    reply := make(chan publishResult, 1)
    h.inbound <- publishReq{msg: m, reply: reply}
    r := <-reply
    return r.msg, r.err
}
```

- **TUI send** calls this in a `tea.Cmd`, ignores the returned msg (it renders
  the echo via its subscription — HLD §10.3).
- **`/upload`** calls this and returns the persisted `*Message` as JSON in the
  HTTP 200 (HLD §9.6).
- **WS `send_text`** (readPump) calls this, ignores the return.

Works with **zero clients connected**: the message is still persisted and shown
in the TUI, and replayed to a phone whenever it later connects.

### 3.4 Fan-out policy — asymmetric on purpose

```go
func (h *Hub) fanoutWS(frame []byte) {
    for c := range h.clients {
        select {
        case c.send <- frame:            // best-effort
        default:                          // buffer full → phone too slow/stuck
            delete(h.clients, c)
            close(c.send)                 // reap it; it can reconnect
        }
    }
}

func (h *Hub) fanoutTUI(ev Event) {
    for ch := range h.tuiSubs {
        ch <- ev                          // must-deliver (buffered 256, blocks if full)
    }
}
```

- **Phones are disposable** → non-blocking send; a phone whose 32-slot buffer is
  full is dropped and left to reconnect (its `since` replay heals the gap).
- **The TUI is the source-of-truth display** → blocking send into a generously
  buffered channel. If it ever fills, the whole app is wedged anyway. (See §10
  for why the drain goroutine keeps this from deadlocking against `Publish`.)

---

## 4. The Client (server-side representation of one phone)

```go
const (
    writeWait      = 10 * time.Second
    pongWait       = 60 * time.Second
    pingPeriod     = (pongWait * 9) / 10   // 54s, must be < pongWait
    maxMessageSize = 32 << 10              // 32 KiB; WS carries text/control only
    sendBuffer     = 32
)

type Client struct {
    hub  *Hub
    conn *websocket.Conn
    send chan []byte      // buffered(sendBuffer); fed by hub, drained by writePump
    id   string           // random; logging + future per-client presence
}
```

### 4.1 readPump — phone → server

```go
func (c *Client) readPump() {
    defer func() { c.hub.unregister <- c; c.conn.Close() }()

    c.conn.SetReadLimit(maxMessageSize)
    c.conn.SetReadDeadline(time.Now().Add(pongWait))
    c.conn.SetPongHandler(func(string) error {
        c.conn.SetReadDeadline(time.Now().Add(pongWait)); return nil
    })

    for {
        _, data, err := c.conn.ReadMessage()
        if err != nil { return }          // includes read-deadline expiry
        c.handle(data)
    }
}

func (c *Client) handle(data []byte) {
    var in InFrame
    if json.Unmarshal(data, &in) != nil { return }   // ignore malformed
    switch in.Kind {
    case "send_text":
        text := strings.TrimSpace(in.Content)
        if text == "" || len(text) > maxTextLen { return }
        _, _ = c.hub.Publish(&store.Message{
            Type: "text", Content: text, Sender: "phone",
        })
    default:
        // unknown kind → ignore (forward-compat)
    }
}
```

Files are **not** accepted here — a phone sending a file uses `POST /upload`.

### 4.2 writePump — server → phone (+ heartbeat)

```go
func (c *Client) writePump() {
    ping := time.NewTicker(pingPeriod)
    defer func() { ping.Stop(); c.conn.Close() }()

    for {
        select {
        case frame, ok := <-c.send:
            c.conn.SetWriteDeadline(time.Now().Add(writeWait))
            if !ok {                       // hub closed send → we were reaped
                c.conn.WriteMessage(websocket.CloseMessage, nil); return
            }
            if c.conn.WriteMessage(websocket.TextMessage, frame) != nil { return }

        case <-ping.C:
            c.conn.SetWriteDeadline(time.Now().Add(writeWait))
            if c.conn.WriteMessage(websocket.PingMessage, nil) != nil { return }
        }
    }
}
```

Browsers answer protocol PINGs with PONGs automatically — no client JS needed
for the server to detect a dead socket.

---

## 5. Wire protocol (finalized)

All frames are JSON text frames. A single canonical **Message** object is reused
everywhere (so history and live share one shape).

### 5.1 Message object

```jsonc
{
  "id": 42,
  "type": "text" | "file" | "image",
  "content": "hello",              // text body, OR "/blob/<token>/<filename>"
  "filename": "cat.png",           // "" for text
  "mime": "image/png",             // "" for text
  "size": 20481,                   // 0 for text
  "sender": "laptop" | "phone",
  "created_at": "2026-07-07T12:34:56Z"
}
```

Note: `source_path` (HLD §7) is **laptop-only** and is *not* sent to phones — it
leaks a local filesystem path and is irrelevant to the web UI. It stays in the DB
for the TUI.

### 5.2 Server → client frames

```jsonc
{ "kind": "msg",     "message": { <Message> } }              // one new message
{ "kind": "history", "messages": [ <Message>, … ] }          // replay batch
{ "kind": "error",   "code": "unauthorized", "message": "…" } // then close
```

### 5.3 Client → server frames

```jsonc
{ "kind": "send_text", "content": "hey from my phone" }
```

That's the only inbound kind in v1 (files use HTTP). Unknown kinds are ignored.

---

## 6. Connection lifecycle

```
GET /ws?t=<token>&since=<lastId>
  │
  1. AUTH (before upgrade): if require_token and t != session token → 401, stop.
  │
  2. UPGRADE: upgrader.Upgrade(); on failure log + stop.
  │
  3. REGISTER: build Client{ send: make(chan []byte, 32) };
  │             go c.writePump(); hub.register <- c; go c.readPump()
  │
  4. REPLAY: hub queues a `history` frame for this client (see §8):
  │             rows WHERE id > since ORDER BY id ASC LIMIT history_limit
  │             (since omitted / 0 → newest history_limit rows)
  │
  5. STEADY STATE: readPump handles send_text; writePump delivers msg frames + pings.
  │
  6. TEARDOWN: readPump error (deadline / close / network) → hub.unregister <- c
  │             → run() deletes from clients, close(c.send) → writePump exits
  │             → conn.Close(). Presence event fired to TUI.
```

Auth happens **before** the upgrade so we can return a clean HTTP 401 (you can't
send a friendly close frame on a not-yet-upgraded connection). The QR URL embeds
`t`, so scanning always authenticates; typing the bare URL does not.

---

## 7. Heartbeat (liveness)

| Side | Mechanism |
|------|-----------|
| Server → phone | `writePump` sends a PING every `pingPeriod` (54s). |
| Phone → server | Browser auto-replies PONG at the protocol level. |
| Detection | `readPump`'s read deadline is `pongWait` (60s), bumped on every PONG (and on any inbound frame). No PONG within 60s → `ReadMessage` errors → client reaped. |
| Phone-side detection | JS relies on `ws.onclose`/`onerror` to trigger reconnect (§8). |

`pingPeriod (54s) < pongWait (60s)` guarantees a healthy client is pinged before
its deadline lapses.

---

## 8. Reconnection & `since` replay

The phone client owns a `lastId` (highest message id it has rendered) and
reconnects with exponential backoff.

```js
let ws, lastId = 0, attempt = 0;
const seen = new Set();               // rendered message ids (dedup, §9)

function connect() {
  const url = `${wsProto}//${location.host}/ws?t=${TOKEN}&since=${lastId}`;
  ws = new WebSocket(url);
  ws.onopen    = () => { attempt = 0; setStatus("live"); };
  ws.onmessage = (e) => handleFrame(JSON.parse(e.data));
  ws.onclose   = () => { setStatus("reconnecting"); scheduleReconnect(); };
  ws.onerror   = () => ws.close();     // force onclose path
}
function scheduleReconnect() {
  const delay = Math.min(1000 * 2 ** attempt++, 15000) + Math.random() * 500;
  setTimeout(connect, delay);          // 1s,2s,4s,…capped 15s + jitter
}
```

Server-side replay query for a `(re)connect` with `since`:

```sql
SELECT * FROM messages
  WHERE id > :since AND created_at >= :retention_cutoff
  ORDER BY id ASC
  LIMIT :history_limit;
```

- Fresh load (`since=0`) → newest `history_limit` messages.
- Reconnect (`since=lastId`) → only the gap, so the phone catches up on anything
  sent while it was asleep.
- **Bound:** if the gap exceeds `history_limit`, only the most recent
  `history_limit` are replayed (rare on a LAN; acceptable for v1). Documented as
  a known limitation in §12.

---

## 9. Ordering & dedup guarantees

- **Ordering:** single `inbound` channel → single `run()` → `Insert` assigns a
  monotonic `id` → fan-out in that order. Every sink sees the same total order.
- **Dedup (client MUST):** the phone keeps a `Set` of rendered ids and ignores
  any `msg`/`history` entry whose `id` is already present. This single rule
  handles three overlaps at once:
  1. **Upload echo** — the uploader gets the `*Message` in the HTTP 200 *and* the
     WS `msg` echo (same id) → render once.
  2. **Reconnect overlap** — `since` is a lower bound; any re-sent row is deduped.
  3. **Future optimistic send** — an optimistic bubble keyed by a client `nonce`
     is reconciled/removed when its authoritative id arrives.
- **Render-on-echo (v1 default):** neither the TUI nor the phone renders its own
  outbound message locally; both render when it returns through the Hub. Tiny
  LAN latency, one code path, guaranteed order. (HLD §18 open decision #3.)

---

## 10. TUI integration & deadlock analysis

The TUI subscribes by handing the hub a `chan Event` (HLD §10.3). Its `Init`
returns a command that blocks on that channel and re-arms after each event:

```go
func waitForHub(sub <-chan hub.Event) tea.Cmd {
    return func() tea.Msg { return <-sub }   // Bubble Tea runs this in a goroutine
}
// Update:
case hub.MessageEvent: m.appendAndScroll(ev.Msg);      return m, waitForHub(m.sub)
case hub.PresenceEvent: m.phoneCount = ev.Count;        return m, waitForHub(m.sub)
```

**Why fan-out to the TUI can block-send without deadlocking `Publish`:** the TUI
send action runs `hub.Publish` in a *`tea.Cmd` goroutine* that blocks on its
`reply` channel — it is **not** the goroutine draining `sub`. The `sub` channel
is drained by the separate `waitForHub` goroutine. So when `run()` does
`ch <- MessageEvent{}` followed by `req.reply <- result`, both receivers are
distinct live goroutines; neither waits on the other. The 256-slot buffer only
has to absorb the microseconds between `waitForHub` consuming one event and
Bubble Tea re-arming the next command.

*(Alternative: give the hub the `*tea.Program` and call `p.Send(ev)`, which
enqueues without a hand-rolled channel. Kept as a fallback; the channel keeps the
hub decoupled + unit-testable — HLD §10.3.)*

---

## 11. Error handling

| Condition | Handling |
|-----------|----------|
| Bad/absent token on `/ws` | 401 before upgrade; no client created. |
| Upgrade failure | Log; connection closed; nothing registered. |
| Malformed inbound JSON | `handle` ignores the frame (no disconnect). |
| Unknown inbound `kind` | Ignored (forward-compatible). |
| `send_text` empty / > maxTextLen | Dropped silently. |
| `Insert` fails (disk/db) | `Publish` returns error → readPump logs; `/upload` → HTTP 500; TUI send Cmd surfaces an error toast. |
| Client `send` buffer full | Client reaped in `fanoutWS`; phone reconnects + `since`-replays. |
| No PONG within `pongWait` | `readPump` read deadline fires → reaped. |
| Write error / deadline | `writePump` returns → conn closed → readPump errors → unregister. |
| Context cancel (shutdown) | `run()` closes every `send`; writePumps send Close and exit. |

---

## 12. Known limitations (v1)

- **Gap > `history_limit`:** a phone offline long enough to miss more than
  `history_limit` messages won't get the oldest of them on reconnect.
- **DB wiped mid-session** (e.g. manual delete) could leave a phone's `lastId`
  ahead of the server's max id → replay returns nothing. Mitigation deferred; a
  `session_epoch` in the QR/`history` frame could force a client reset later.
- **Single `sender="phone"` label** — multiple phones are all "phone"; the TUI
  shows a count, not identities (HLD §18 open decision #4).

---

## 13. Constants & config surface

| Name | Value / source | Notes |
|------|----------------|-------|
| `writeWait` | 10s | Per-frame write deadline. |
| `pongWait` | 60s | Read deadline; reset on PONG/data. |
| `pingPeriod` | 54s | `< pongWait`. |
| `maxMessageSize` | 32 KiB | WS inbound cap (text/control only). |
| `maxTextLen` | 16 KiB | Rejected `send_text` above this. |
| `sendBuffer` | 32 | Per-client outbound buffer before reap. |
| TUI sub buffer | 256 | Must-deliver channel to the TUI. |
| `history_limit` | config (§13 HLD), default 200 | Replay cap. |
| reconnect backoff | 1s→15s + jitter | Client-side. |

---

## 14. Open questions

1. **Presence to phones?** v1 sends presence only to the TUI. Do phones need a
   "laptop online" indicator, or is the WS connection state enough? (Default:
   enough.)
2. **App-level heartbeat frame?** Protocol PING/PONG covers server-side liveness;
   add a JSON `ping` only if we later need client-measured latency. (Default: no.)
3. **Delivery receipts?** Should the TUI show "delivered to phone" once a `msg`
   frame is actually written to a client socket? (Default: no; the phone count is
   enough for v1.)
```
