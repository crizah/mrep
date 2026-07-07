// Package tui is mrep's neon-themed terminal UI (HLD §10): an ASCII logo, a
// chat pane fed live by the Hub, a composer that can send text or a file
// path, and a QR pane that appears once the server is started.
package tui

import (
	"context"
	"io/fs"
	"time"

	tea "charm.land/bubbletea/v2"

	"mrep/internal/clipboard"
	"mrep/internal/config"
	"mrep/internal/hub"
	"mrep/internal/server"
	"mrep/internal/store"
)

type appState int

const (
	stateHold appState = iota
	stateLive
)

type focusTarget int

const (
	focusComposer focusTarget = iota
	focusHistory
)

// Deps are the already-constructed collaborators the TUI is handed at
// startup (built once in main.go, per HLD §9.1).
type Deps struct {
	Cfg    config.Config
	Store  *store.Store
	Hub    *hub.Hub
	Clip   clipboard.Clipboard // nil if xclip isn't available (HLD §16)
	Assets fs.FS               // embedded web/ directory
}

// Model is the root Bubble Tea model.
type Model struct {
	cfg    config.Config
	st     *store.Store
	h      *hub.Hub
	clip   clipboard.Clipboard
	assets fs.FS

	rootCtx    context.Context
	rootCancel context.CancelFunc

	state appState
	focus focusTarget

	width, height int

	messages   []*store.Message
	maxSeenID  int64
	phoneCount int

	sub         <-chan hub.Event
	unsubscribe func()

	input string

	srv       *server.Server
	serverURL string
	qr        string
	startErr  string

	selected int

	savePromptOpen  bool
	savePromptValue string
	saveTarget      *store.Message

	confirmClear bool

	status    string
	statusGen int // invalidates stale toastExpireMsg from a prior status
	tick      int
}

// New builds the root model. Call it once at startup.
func New(deps Deps) *Model {
	ctx, cancel := context.WithCancel(context.Background())
	return &Model{
		cfg:        deps.Cfg,
		st:         deps.Store,
		h:          deps.Hub,
		clip:       deps.Clip,
		assets:     deps.Assets,
		rootCtx:    ctx,
		rootCancel: cancel,
		state:      stateHold,
		focus:      focusComposer,
	}
}

// Init subscribes to the hub, seeds the visible history from the store, and
// starts the pulse animation ticker.
func (m *Model) Init() tea.Cmd {
	sub, unsubscribe := m.h.Subscribe()
	m.sub = sub
	m.unsubscribe = unsubscribe

	if msgs, err := m.st.Since(0, m.cfg.HistoryLimit); err == nil {
		m.messages = msgs
		if n := len(msgs); n > 0 {
			m.maxSeenID = msgs[n-1].ID
		}
	}

	return tea.Batch(waitForHub(m.sub), tickCmd())
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case hubEventMsg:
		return m.handleHubEvent(msg)

	case tickMsg:
		m.tick++
		return m, tickCmd()

	case serverStartedMsg:
		return m.handleServerStarted(msg)

	case publishResultMsg:
		if msg.err != nil {
			return m, m.setStatus("send failed: " + msg.err.Error())
		}
		return m, nil

	case saveResultMsg:
		if msg.err != nil {
			return m, m.setStatus("save failed: " + msg.err.Error())
		}
		return m, m.setStatus("saved -> " + msg.dest)

	case clearResultMsg:
		if msg.err != nil {
			return m, m.setStatus("clear failed: " + msg.err.Error())
		}
		m.messages = nil
		m.maxSeenID = 0
		return m, m.setStatus("history cleared")

	case toastExpireMsg:
		if msg.gen == m.statusGen {
			m.status = ""
		}
		return m, nil
	}

	return m, nil
}

func (m *Model) handleHubEvent(msg hubEventMsg) (tea.Model, tea.Cmd) {
	switch ev := msg.event.(type) {
	case hub.MessageEvent:
		if ev.Msg.ID > m.maxSeenID {
			m.messages = append(m.messages, ev.Msg)
			m.maxSeenID = ev.Msg.ID
			if len(m.messages) > m.cfg.HistoryLimit {
				m.messages = m.messages[len(m.messages)-m.cfg.HistoryLimit:]
			}
		}
	case hub.PresenceEvent:
		m.phoneCount = ev.Count
	}
	return m, waitForHub(m.sub)
}

func (m *Model) handleServerStarted(msg serverStartedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.startErr = msg.err.Error()
		return m, m.setStatus(msg.err.Error())
	}
	m.srv = msg.srv
	m.serverURL = msg.url
	m.qr = msg.qr
	m.state = stateLive
	return m, m.setStatus("server live")
}

// setStatus shows a transient status/toast line that clears itself after a
// few seconds; statusGen guards against an older timer clearing a newer
// message.
func (m *Model) setStatus(s string) tea.Cmd {
	m.status = s
	m.statusGen++
	gen := m.statusGen
	return tea.Tick(4*time.Second, func(time.Time) tea.Msg {
		return toastExpireMsg{gen: gen}
	})
}

func (m *Model) View() tea.View {
	return tea.NewView(m.render())
}
