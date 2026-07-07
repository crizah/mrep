# mrep — High-Level Design

> **mrep** is a LAN file/text sharing tool. On your laptop it's a neon-themed
> TUI; your phone joins by scanning a QR code and gets a chat-like web UI.
> Files and text flow both ways over your local WiFi, backed by SQLite so
> history survives restarts.

- **Status:** design (HLD)
- **Target platform:** Linux laptop (X11 clipboard), iPhone (Chrome/Safari-WebKit)
- **Language:** Go 1.25
- **Last updated:** 2026-07-07

---

## 1. Goals

1. `mrep` launches a **TUI** on the laptop. Pressing `s` starts the server,
   registers handlers, and shows a **QR code** to scan.
2. Scanning the QR opens a **chat-style web UI** on the phone.
3. **Both directions**: laptop → phone and phone → laptop.
4. Send **plain text** *and* **files/images**.
5. **Clipboard ingest** on the laptop (the `xclip` feature): paste a file path,
   or pull an image that was copied from the browser, or plain text.
6. **Stateful**: SQLite-backed history; configurable retention that prunes old
   rows (and their files) on startup.
7. Files stored under `~/.local/share/mrep`.
8. **iOS-friendly web UI**: images offer "Save to Photos"; other files offer
   "Save to Files". Works for *history* items too, not just new ones.
9. Realtime: phone→laptop writes trigger a **live TUI update**.
10. A distinctive, fun, neon (pink + green) TUI with an ASCII logo.

## 2. Non-goals (for v1)

- Internet / NAT traversal / relay servers. LAN only.
- More than one phone at a time (the design allows N clients, but UX targets 1).
- End-to-end encryption / accounts / multi-user auth (a URL token is the only gate).
- Android-specific save affordances (we degrade gracefully; standard download).
- Windows/macOS laptop clipboard (X11/`xclip` only in v1; abstracted for later).

## 3. Terminology

| Term | Meaning |
|------|---------|
| **Laptop / host** | The machine running the `mrep` binary + TUI + server. |
| **Phone / client** | The browser on the phone, loading the web UI. |
| **Hub** | In-process broker: persists a message, then fans it out to all sinks. |
| **Sink** | Anything that renders messages: the TUI, or a WebSocket client. |
| **Blob** | The raw bytes of a file, stored on disk and served over HTTP. |
| **Message** | One row in the DB: text or file metadata. |

---

## 4. Architecture overview

Single Go binary. The TUI owns the main goroutine (Bubble Tea's event loop);
the HTTP/WebSocket server runs in a background goroutine. They never touch each
other's state directly — they communicate **only through the Hub**.

```
                          mrep binary (one process)
┌───────────────────────────────────────────────────────────────────────────┐
│                                                                             │
│   ┌──────────────┐         ┌──────────────────┐        ┌────────────────┐   │
│   │   TUI         │  pub    │      HUB          │  SQL   │    Store        │  │
│   │ (Bubble Tea) │────────▶│  Publish(msg):    │───────▶│   (SQLite)      │  │
│   │              │◀────────│   1. persist      │◀───────│  messages tbl   │  │
│   │  sub chan    │  events │   2. fan-out       │        └────────────────┘  │
│   └──────┬───────┘         │      to all sinks  │        ┌────────────────┐   │
│          │                 │                    │───────▶│   Blob store    │  │
│          │ renders         └───────┬───────────┘  files  │ ~/.local/share  │  │
│          ▼                         │ ws broadcast         │   /mrep/files   │  │
│    terminal screen                 ▼                      └────────────────┘  │
│                            ┌──────────────────┐                               │
│                            │   HTTP Server     │  serves web UI, /blob, /upload│
│                            │  (Gin + Gorilla)  │  + WebSocket /ws              │
│                            └────────┬─────────┘                               │
└─────────────────────────────────────┼─────────────────────────────────────────┘
                                       │ LAN / WiFi
                                       ▼
                             ┌──────────────────┐
                             │   Phone browser   │
                             │  (web UI, WS +    │
                             │   HTTP blobs)     │
                             └──────────────────┘
```

**Key principle — one write path.** Whether a message originates from the TUI
input box or from a phone, it goes through `Hub.Publish()`, which (1) writes the
blob to disk if needed, (2) inserts the DB row (assigning `id` + `created_at`),
then (3) fans the finished record out to *every* sink. Neither the TUI nor the
phone renders optimistically; both render when the record comes back through the
Hub. This gives us a single source of truth and consistent ordering for free.

**Transport split — control vs. bytes.**
- **WebSocket** carries small control/text messages and new-message
  notifications (JSON envelopes). Low latency, bidirectional, drives live UI.
- **HTTP** carries the actual file **bytes**: `POST /upload` (phone → laptop)
  and `GET /blob/:token` (download / `<img src>`). We never shove big files
  through the WebSocket as base64.

---

## 5. Technology choices

| Concern | Choice | Rationale / notes |
|--------|--------|-------------------|
| TUI | `charm.land/bubbletea/v2` + `lipgloss` | Already vendored. **Remove** the v1 `charmbracelet/bubbletea` to avoid confusion. |
| HTTP + WS | `gin-gonic/gin` + `gorilla/websocket` | Already vendored and matches existing `server.go`. |
| DB | `modernc.org/sqlite` (pure-Go, `database/sql`) | No cgo → trivial cross-compile & single static binary. **Add** this; **remove** the unused `mongo-driver`. |
| QR (in terminal) | `github.com/mdp/qrterminal/v3` | Renders a scannable QR with half-block runes directly in the TUI. **Add.** |
| MIME sniffing | `github.com/gabriel-vasile/mimetype` | Already vendored (transitively). |
| Clipboard | shell out to `xclip` | Matches your intended UX; abstracted behind an interface for future backends. |
| Web assets | `//go:embed web/*` | Ship HTML/CSS/JS inside the binary; no external files at runtime. |
| Config | `pelletier/go-toml/v2` | Already vendored. TOML config file. |
| LAN IP | `net.Interfaces()` (stdlib) | Pick the non-loopback IPv4 to build the QR URL. |

**Dependency cleanup this design implies:**
- Add: `modernc.org/sqlite`, `github.com/mdp/qrterminal/v3`.
- Drop: `github.com/charmbracelet/bubbletea` (v1), `go.mongodb.org/mongo-driver/v2`.

---

## 6. Package layout

```
mrep/
├─ main.go                 # entrypoint: load config, build store/hub, run TUI
├─ internal/
│  ├─ config/             # TOML load/defaults, resolved paths
│  ├─ store/              # SQLite: schema, migrations, CRUD, retention prune
│  ├─ hub/               # Broker: Publish(), Subscribe(), client registry
│  ├─ server/            # Gin routes, WS handler, /upload, /blob, embedded web
│  ├─ ingest/            # copy bytes → blob store, mime detect, token gen
│  ├─ clipboard/         # xclip: smart paste (image / uri-list / text / path)
│  ├─ qrcode/            # URL → terminal QR string
│  ├─ netutil/           # LAN IPv4 discovery
│  └─ tui/               # Bubble Tea model, views, styles (neon theme)
├─ web/                   # embedded static web UI
│  ├─ index.html
│  ├─ app.js
│  └─ style.css
└─ docs/HLD.md
```

`cmd/send.go` (currently empty): reserved for an optional headless
`mrep send <path>` subcommand later (qrcp-style). Out of scope for v1.

---

## 7. Data model

### 7.1 SQLite schema (`messages`)

Your proposed schema, lightly extended (additions marked `+`):

```sql
CREATE TABLE IF NOT EXISTS messages (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    type        TEXT NOT NULL,                 -- 'text' | 'file' | 'image'
    content     TEXT NOT NULL,                 -- text body, OR '/blob/<token>/<filename>'
    filename    TEXT DEFAULT '',
    mime_type   TEXT DEFAULT '',
    sender      TEXT NOT NULL,                 -- 'laptop' | 'phone'
    blob_token  TEXT DEFAULT '',               -- + on-disk key: files/<blob_token>.<ext>
    source_path TEXT DEFAULT '',               -- + laptop-origin absolute path; '' for phone/clipboard
    size        INTEGER DEFAULT 0,             -- + bytes, for the UI
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_messages_created_at ON messages(created_at);
```

- **`type`**: `image` is just a `file` whose `mime_type` starts with `image/`;
  we store it explicitly so the web UI knows to render an `<img>` (and offer
  "Save to Photos") without re-sniffing.
- **`content`**: for `text` it's the message body; for files it's the download
  path `"/blob/<blob_token>/<filename>"` so both the phone and the TUI history
  render uniformly. The trailing `<filename>` gives the browser/iOS a real name
  + extension to infer from (§8.1).
- **`blob_token`**: random hex; the on-disk file is `files/<blob_token>.<ext>`
  (extension derived from `filename`/`mime_type` so the data dir is browsable).
  Generating the token up front lets us store the bytes *before* the row exists,
  avoiding an insert-then-rename dance.
- **`filename`**: the *original* basename (`cat.png`). This is the download name
  and the phone's display name.
- **`source_path`**: where a laptop-sent file came from (`~/Downloads/cat.png`).
  Empty for phone uploads and clipboard grabs. Drives the **TUI display name**
  (§10.4) — the uuid is never shown to a human.

### 7.2 Blob storage layout

```
~/.local/share/mrep/            # data_dir (configurable)
├─ mrep.db                      # SQLite database
└─ files/
   ├─ 9f2ac1.png                # raw bytes: <blob_token>.<ext>
   └─ 4b7ea0.pdf
```

The on-disk name is the opaque `<blob_token>.<ext>`; the original filename, MIME,
and (for laptop files) source path live in the DB. `GET /blob/:token/:filename`
streams the bytes with `Content-Type: <mime_type>`. By default the response is
**inline** (so `<img>` renders and long-press → Save to Photos works); with
`?download=1` it sets `Content-Disposition: attachment` with the filename
**RFC 5987-encoded** (`filename*=UTF-8''…`) to neutralize quotes/unicode/newlines
in the name. The `:filename` segment is used only for the download name and
`Content-Type` hinting — the blob is addressed solely by `:token`, so it is never
joined into a filesystem path.

### 7.3 Retention / cleanup

On startup (when `s` starts the server), if `retention_days > 0`:

```sql
-- collect victims first so we can delete their blobs
SELECT blob_token FROM messages
  WHERE created_at < datetime('now', printf('-%d days', ?));
DELETE FROM messages
  WHERE created_at < datetime('now', printf('-%d days', ?));
```

Then `os.Remove` each collected blob. `retention_days = 0` means keep forever.
A "clear history" TUI action (`X`) runs the same delete with a `0`-day cutoff
(everything) behind a confirm prompt.

---

## 8. Protocol

### 8.1 HTTP endpoints

| Method & path | Purpose |
|---------------|---------|
| `GET /` | Serve the web UI (`index.html`). Optional `?t=<token>` gate. |
| `GET /app.js`, `GET /style.css` | Embedded static assets. |
| `GET /ws` | Upgrade to WebSocket. |
| `POST /upload` | Phone → laptop file upload (`multipart/form-data`). Returns the created message JSON. |
| `GET /blob/:token/:filename` | Serve a file's bytes. Inline by default (`<img>` → Save to Photos); `?download=1` → `Content-Disposition: attachment` (→ Files). `:filename` is display-only. |

### 8.2 WebSocket envelope (JSON, both directions)

> **Finalized in [`LLD-websocket.md`](./LLD-websocket.md) §5**, which supersedes
> this sketch (single canonical `Message` object, `kind` discriminator, dedup by
> `id`, `since` replay). The version below is the original outline.

Server → client frames:

```jsonc
// On connect: replay recent history (post-retention)
{ "kind": "history", "messages": [ <Message>, … ] }

// On any new message (from either side)
{ "kind": "message", "message": <Message> }
```

`<Message>`:

```jsonc
{
  "id": 42,
  "type": "image",
  "content": "/blob/9f2a…c1",   // or the text body for type:"text"
  "filename": "cat.png",
  "mime_type": "image/png",
  "sender": "laptop",
  "size": 20481,
  "created_at": "2026-07-07T12:34:56Z"
}
```

Client → server frames (phone sending **text** only; files use `POST /upload`):

```jsonc
{ "kind": "send_text", "content": "hey from my phone" }
```

The server validates, calls `Hub.Publish`, and the resulting `message` frame is
broadcast back to everyone (including the sender) — that echo is what the phone
renders. Optional later optimization: client attaches a `nonce`, renders
optimistically, and reconciles on echo.

---

## 9. Core flows

### 9.1 Startup (`mrep`, then press `s`)

```
user runs `mrep`
  └─ load config, open SQLite (migrate), build Hub, start TUI (HOLD state)
user presses `s`
  ├─ run retention prune
  ├─ discover LAN IPv4  → build URL  http://192.168.x.y:PORT/?t=<token>
  ├─ start Gin server (goroutine): routes + /ws
  ├─ render QR of URL into the right pane
  └─ TUI state → LIVE; input box focused
user scans QR → phone loads web UI → opens WS → server sends `history`
```

### 9.2 Laptop → phone, text

```
TUI input "hello" + Enter
  └─ tea.Cmd: Hub.Publish(Message{type:text, content:"hello", sender:laptop})
       ├─ store.Insert → id, created_at
       ├─ fan-out → WS clients  (phone renders bubble)
       └─ fan-out → TUI sub chan (TUI appends to history, scrolls)
```

### 9.3 Laptop → phone, file (typed/pasted path)

```
TUI input "/home/me/cat.png" + Enter  (path exists on disk?)
  └─ ingest.FromPath: copy bytes → files/<token>.png, sniff mime, size, source_path=~/cat.png
  └─ Hub.Publish(Message{type:image, content:/blob/<token>/cat.png, filename, source_path, mime, size, sender:laptop})
       ├─ WS fan-out → phone shows <img src="/blob/<token>/cat.png"> (lazy GET fetches bytes)
       └─ TUI fan-out → history shows "🖼 cat.png (20 KB)"
```

### 9.4 Laptop clipboard "smart paste" (`c` / `v`) — the xclip feature

```
query:  xclip -selection clipboard -t TARGETS -o
  ├─ has image/*  → xclip -t image/png -o > $TMP/mrep-clip-<ts>.png → ingest.FromPath
  ├─ has text/uri-list → parse file:// URIs → ingest each existing path
  └─ else → clipboard text:
        ├─ if it's a path to an existing file → ingest as file
        └─ else → send as plain text
then → Hub.Publish(...) as in 9.2 / 9.3
```

This covers all three cases you described: "I copied an image in Chrome",
"I copied a file in the file manager", and "I just have a path/text".

### 9.5 Phone → laptop, text

```
web UI: type + send  →  WS {kind:send_text, content}
server: Hub.Publish(Message{type:text, sender:phone})
  ├─ store.Insert
  ├─ WS fan-out (phone renders its own bubble on echo)
  └─ TUI fan-out via sub chan → LIVE TUI UPDATE  ✅ (your requirement #9)
```

### 9.6 Phone → laptop, file

```
web UI: pick file → POST /upload (multipart)
server: ingest.FromMultipart → files/<token>.<ext>, mime, size (source_path='')
        Hub.Publish(Message{type:file|image, sender:phone})
  ├─ HTTP 200 returns the Message JSON (client can render immediately)
  ├─ WS fan-out to any other clients
  └─ TUI fan-out → live update; laptop can open the file from data_dir
```

### 9.7 History on connect (images keep their save options)

On WS connect the server sends `{kind:"history", messages:[…]}` — the latest N
(configurable) surviving rows. The web UI renders history items with the **same
component** as live items, so past images also get long-press "Save to Photos"
and the share button (requirement #8, second sentence).

---

## 10. TUI design

### 10.1 Layout

Full-width header (ASCII logo + status line), then a two-column body: chat on
the left, QR + connection info on the right. (Your note said "logo top right" and
"chat left / QR right" — a spanning header keeps the logo prominent while giving
the chat the whole left column. Easy to move to top-left; flagged as tweakable.)

```
┌──────────────────────────────────────────────────────────────────────────┐
│   ███╗   ███╗██████╗ ███████╗██████╗                       ◉ LIVE  ·  1 📱 │
│   ████╗ ████║██╔══██╗██╔════╝██╔══██╗       "beam it over"                 │
│   ██╔████╔██║██████╔╝█████╗  ██████╔╝                                      │
│   ██║╚██╔╝██║██╔══██╗██╔══╝  ██╔═══╝                                       │
│   ██║ ╚═╝ ██║██║  ██║███████╗██║                                           │
├───────────────────────────────────┬────────────────────────────────────────┤
│  CHAT                              │   SCAN TO CONNECT                       │
│                                    │                                        │
│  ┌ phone  12:31 ───────────────┐   │     █▀▀▀▀▀█ ▀▄█ █▀▀▀▀▀█                │
│  │ hey are you there            │   │     █ ███ █ ▀█▀ █ ███ █                │
│  └─────────────────────────────┘   │     █ ▀▀▀ █ █▄▀ █ ▀▀▀ █                │
│              ┌ laptop 12:31 ────┐   │     ▀▀▀▀▀▀▀ ▀▄▀ ▀▀▀▀▀▀▀                │
│              │ yep. sending pic │   │     █▀ ▄▀▀▀▄█ ▀▀ ▄▀ ...                │
│              └──────────────────┘   │                                        │
│              ┌ laptop 12:32 ────┐   │   http://192.168.1.24:8787            │
│              │ 🖼 cat.png  20 KB │   │                                        │
│              └──────────────────┘   │   ◉ waiting for scan…  ◜◝◞◟ (spinner) │
│                                    │                                        │
│                                    │   files ▸ ~/.local/share/mrep          │
│  ┌────────────────────────────────┐│                                        │
│  │ › type text or a file path…    ││                                        │
│  └────────────────────────────────┘│                                        │
├───────────────────────────────────┴────────────────────────────────────────┤
│  s start   c paste-clip   enter send   X clear   ? help   q quit            │
└──────────────────────────────────────────────────────────────────────────┘
```

### 10.2 Neon theme (lipgloss)

- Background: near-black `#0A0A0F`.
- **Neon pink** `#FF2EC4` — logo, borders, sender="laptop" bubbles, active input.
- **Neon green** `#39FF14` — status "LIVE", QR frame accent, sender="phone" bubbles.
- Dim gray `#5A5A6E` — timestamps, help bar, inactive borders.
- Fun visuals: pulsing `◉` connection dot (alternates dim/bright green on a
  `tea.Tick`), a braille spinner while awaiting the first scan, a subtle
  gradient rule under the logo, and a little "beam" animation on send.

### 10.3 Concurrency model (the tricky part)

Bubble Tea's `Update` loop is single-threaded; external events must enter as
`tea.Msg`. We use the **subscription-channel + self-re-issuing command** pattern:

```go
// Hub gives the TUI its own channel.
func (m Model) Init() tea.Cmd { return waitForHub(m.sub) }

func waitForHub(sub <-chan store.Message) tea.Cmd {
    return func() tea.Msg { return hubMsg(<-sub) } // blocks in a goroutine Bubble Tea manages
}

// In Update, on hubMsg: append to history, then wait for the next one.
case hubMsg:
    m.history = append(m.history, store.Message(msg))
    return m, waitForHub(m.sub) // re-arm
```

Sending from the TUI returns a `tea.Cmd` that calls `hub.Publish(...)` (returns
`nil` msg — the record comes back through the subscription, so we don't append
locally). This keeps ordering identical on both devices.

*(Alternative considered: hand the server a `*tea.Program` and call
`p.Send(...)`. Works, but couples the server to the TUI and needs late wiring.
The channel approach keeps the Hub as the only shared surface.)*

Long-running work (copying a large file during ingest, xclip calls) runs inside
`tea.Cmd` goroutines so the render loop never blocks.

### 10.4 What the chat shows, and saving files out

The on-disk name (`<uuid>.<ext>`) is storage plumbing and is **never shown to a
human**. Each file bubble resolves a **display name** by priority:

1. `source_path` (abbreviated with `~`) — for files the laptop sent from a real
   path, e.g. `~/Downloads/cat.png`. This is what the laptop user recognizes.
2. else `filename` — for phone uploads and clipboard grabs that have no laptop
   source, e.g. `receipt.pdf`, or a synthesized `clipboard-<ts>.png`.
3. never the `blob_token`.

Bubble anatomy (line 2 = `mime_type · human size`; line 3 = origin-specific):

```
┌ laptop 12:32 ───────────────┐      ┌ phone 12:33 ─────────────────┐
│ 🖼  ~/Downloads/cat.png      │      │ 📄  receipt.pdf              │
│     image/png · 240 KB       │      │     application/pdf · 88 KB  │
└──────────────────────────────┘      │     ↳ w: save to…           │
                                       └──────────────────────────────┘
```

**Saving a received file out of the store** (the laptop-side mirror of the
phone's Save-to-Photos/Files):

- **Tab** moves focus from the composer to the history pane; **↑/↓** (or `k`/`j`)
  moves a selection cursor over messages.
- **`w`** on a selected file opens a one-line path prompt **prefilled with
  `save_dir`** (config §13, default `~/Downloads/`). Enter copies the blob to
  `<dest>/<filename>` and the bubble shows `✓ saved → ~/Downloads/receipt.pdf`.
- **Copy, not move** — the canonical blob stays so history and phone re-download
  keep working.
- Name collisions get a ` (1)`, ` (2)`… suffix; never a silent overwrite.
- **`o`** (optional) runs `xdg-open` on the blob to open it with the default app.

> **`w` is gated on `source_path == ""`.** For a laptop-sent file the original
> still lives at `source_path`, so "save it out" is redundant — the TUI hides
> `w` there and offers only `o` (open). `w` is meaningful only for phone uploads
> and clipboard grabs, whose sole copy is the blob in the store.

This keeps storage exactly as desired — opaque `files/<uuid>.<ext>` — while the
chat reads meaningfully and any file can be placed wherever the user points.

---

## 11. Web UI design (iPhone-first)

Single embedded page: message list + composer (text input, file picker, send).
Connects via WebSocket; uploads via `POST /upload`; images fetched from `/blob`.

### 11.1 Rendering messages

- **Images**: real `<img src="/blob/<token>/<filename>">` in the DOM (inline, no
  attachment header). Deliberate — it enables iOS Safari/WebKit's native
  **long-press → "Save to Photos"** with zero JS, over plain `http`.
- **Every file — images included — also gets a download card**: filename, size,
  and a **"Save to Files"** link to `"/blob/<token>/<filename>?download=1"`,
  which sets `Content-Disposition: attachment` so iOS routes it to the Files
  app. Images thus get *both* paths: long-press → Photos, card → Files.
- The real filename rides in the URL's last path segment (so the browser/iOS
  download manager infers name + extension) **and** in `Content-Disposition`,
  **RFC 5987-encoded** (`filename*=UTF-8''…`). The on-disk blob stays the opaque
  `<token>.<ext>`.
- **Share** button on images is a progressive enhancement, enabled only when
  `window.isSecureContext && navigator.canShare` (see §11.2 caveat).
- **History uses the exact same components**, so old images keep long-press, the
  download card, and (when available) share.

### 11.2 The Web Share caveat (important, designed-around)

`navigator.share({ files: [...] })` invokes the iOS Share Sheet ("Save Image",
etc.) — **but sharing *files* requires a secure context (HTTPS)**. Over plain
`http://192.168.x.x:PORT` it will be unavailable. So:

- **Primary, always works over http**: long-press "Save to Photos" (images) and
  the "Save to Files" download link (other types). No secure context needed.
- **Progressive enhancement**: the Share button is only rendered/enabled when
  `window.isSecureContext && navigator.canShare?.({files:[testFile]})`. When it
  runs, it does `fetch('/blob/<token>/<name>') → blob → new File(...) → navigator.share`.
- **Opt-in TLS mode** (config `tls = true`): serve HTTPS with a self-signed cert
  so the Share button lights up. Trade-off: iOS shows a cert warning you must
  accept once. Documented as optional; the http path stays fully functional.

This is the one place where "it just works" isn't true on the open web, so the
design leans on the long-press method as the guaranteed path.

---

## 12. Clipboard / xclip ingest

Behind an interface so non-X11 backends can be added later:

```go
type Clipboard interface {
    // Returns either text, or a path to a temp file holding pasted bytes.
    Read() (ClipItem, error)
}
type ClipItem struct {
    Kind      Kind   // Text | FilePath | Image
    Text      string
    Path      string // for FilePath/Image
    MimeType  string
}
```

X11 implementation (shells out to `xclip`):

1. `xclip -selection clipboard -t TARGETS -o` → list of available MIME targets.
2. Priority: `image/*` > `text/uri-list` > `text/plain`.
   - image → `xclip -selection clipboard -t image/png -o` into
     `$TMPDIR/mrep-clip-<ts>.png`; return `Kind=Image`.
   - uri-list → parse `file://` lines into real paths; return `Kind=FilePath`.
   - text → if the string is a path to an existing file, `Kind=FilePath`, else
     `Kind=Text`.
3. `mrep` requires `xclip` on `PATH`; on startup we check and, if missing, the
   TUI shows a dim hint ("clipboard paste needs `xclip`") rather than crashing.

---

## 13. Configuration

`~/.config/mrep/config.toml` (created with defaults on first run):

```toml
port           = 8787
data_dir       = "~/.local/share/mrep"   # blobs + mrep.db
save_dir       = "~/Downloads"            # default target for the TUI "save file to…" (w)
retention_days = 7                        # 0 = keep forever
device_name    = "laptop"                 # the `sender` label for host messages
history_limit  = 200                      # rows replayed to phone on connect
tls            = false                     # true → self-signed HTTPS (share sheet)
require_token  = true                      # gate the URL with ?t=<random>
```

Flags override config (`--port`, `--data-dir`, etc.) for one-off runs.

---

## 14. Security considerations

- **LAN trust boundary**: anyone on the WiFi who knows the URL can connect.
  `require_token` adds a random `?t=` gate checked on `GET /` and `/ws`; the QR
  embeds it so scanning "just works" while typing the bare URL doesn't.
- **WebSocket origin**: the current `CheckOrigin: return true` is fine for LAN
  but combined with the token; tighten to same-host origins if desired.
- **Path traversal**: `/blob/:token` only accepts `[0-9a-f]+` tokens and never
  joins user-supplied filenames into a filesystem path — blobs are addressed by
  token, real filename lives only in headers.
- **Upload limits**: cap `POST /upload` body size (config-driven, e.g. 100 MB)
  and stream to disk; sniff MIME server-side rather than trusting the client.
- **TLS mode**: self-signed only; documented as convenience for the Share Sheet,
  not a real trust anchor.

## 15. Lifecycle & shutdown

- Goroutines: (1) Bubble Tea loop [main], (2) `http.Server`, (3) one reader +
  one writer goroutine per WS client, (4) transient `tea.Cmd` workers.
- `q` / `ctrl+c` → `tea.Quit` → cancel a root `context.Context` → `server.Shutdown(ctx)`
  → close WS clients → close DB. Blobs persist (that's the point).
- Server can be started once per session; restarting `s` is a no-op if already live.

## 16. Error handling & edge cases

| Case | Handling |
|------|----------|
| Port already in use | TUI shows red error in the right pane; suggest `--port`. |
| No non-loopback IPv4 | Fall back to `127.0.0.1` + warn (phone can't reach it). |
| `xclip` missing | Disable paste, dim hint; everything else works. |
| Pasted path doesn't exist | Treat as plain text (don't error). |
| Huge file | Enforce upload cap; stream, never buffer whole file in RAM. |
| Phone disconnects | WS reader errors → deregister client from Hub; TUI updates count. |
| Blob missing on disk but row exists | `/blob` returns 404; UI shows broken-file card. |
| Filename with quotes/unicode/newlines | RFC 5987-encode (`filename*=UTF-8''…`) in `Content-Disposition`; never build a filesystem path from it. |
| TUI save-as target already exists | Append ` (1)`, ` (2)`… suffix; never overwrite silently. |
| Concurrent inserts | SQLite single-writer; Hub serializes `Publish`. |

## 17. Implementation roadmap

Phased so each step is runnable:

1. **Foundations** — `config`, `store` (schema + migrate + CRUD + retention),
   `netutil`, blob dir bootstrap. Unit-test store.
2. **Hub** — `Publish` (persist + fan-out), `Subscribe`, client registry.
3. **Server core** — Gin, embedded web, `/ws` wired to Hub, `history` on connect,
   `send_text` in. Replace the current echo handler.
4. **Blobs** — `ingest`, `/upload`, `/blob/:token`.
5. **TUI** — model + neon styling + layout + subscription loop; input box sends
   text through the Hub; render history live.
6. **QR** — `qrcode` pane + status/spinner visuals.
7. **Clipboard** — `xclip` smart paste (`c`).
8. **Web UI polish** — image `<img>` render, Save-to-Files links, Share
   progressive enhancement, history parity, mobile CSS.
9. **Retention/clear** — startup prune + `X` clear-with-confirm.
10. **Hardening** — token gate, upload caps, graceful shutdown, `tls` mode.

## 18. Open decisions (your call)

1. **Header/logo placement**: full-width spanning header (mockup above) vs.
   strictly top-left of the chat column. Default: spanning.
2. **TLS mode in v1?** Ship the http-only path (long-press works) and add TLS
   later, or include self-signed TLS from the start for the Share button?
   Default: http-only in v1, TLS as a fast-follow.
3. **Optimistic rendering**: render-on-echo (simpler, tiny LAN latency) vs.
   optimistic + nonce reconcile. Default: render-on-echo.
4. **Multiple phones**: design supports N clients; do you want the TUI to show a
   per-client list, or just a count? Default: count.
```
