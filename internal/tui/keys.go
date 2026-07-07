package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"mrep/internal/store"
)

// handleKey dispatches a keypress. Plain characters always go to the
// composer text buffer; every "action" keybind uses a Ctrl-combo (or, when
// the history pane is focused via Tab, a bare letter) specifically so typing
// an ordinary chat message never accidentally triggers a command (HLD §10.4).
func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if key == "ctrl+c" {
		m.rootCancel()
		return m, tea.Quit
	}

	if m.confirmClear {
		return m.handleConfirmClearKey(key)
	}
	if m.savePromptOpen {
		return m.handleSavePromptKey(msg, key)
	}

	switch key {
	case "ctrl+s":
		if m.state == stateHold {
			return m, startServerCmd(m.rootCtx, m.cfg, m.h, m.st, m.assets)
		}
		return m, nil

	case "ctrl+v":
		return m, pasteCmd(m.cfg, m.h, m.clip)

	case "ctrl+x":
		m.confirmClear = true
		return m, nil

	case "tab":
		if m.focus == focusComposer {
			m.focus = focusHistory
			if m.selected >= len(m.messages) {
				m.selected = len(m.messages) - 1
			}
		} else {
			m.focus = focusComposer
		}
		return m, nil

	case "esc":
		m.focus = focusComposer
		return m, nil
	}

	if m.focus == focusHistory {
		return m.handleHistoryKey(key)
	}
	return m.handleComposerKey(msg, key)
}

func (m *Model) handleComposerKey(msg tea.KeyPressMsg, key string) (tea.Model, tea.Cmd) {
	switch key {
	case "enter":
		if m.state != stateLive {
			return m, m.setStatus("press ctrl+s to start the server first")
		}
		text := m.input
		m.input = ""
		return m, submitCmd(m.cfg, m.h, text)

	case "backspace":
		if n := len(m.input); n > 0 {
			m.input = m.input[:n-1]
		}
		return m, nil

	case "space":
		m.input += " "
		return m, nil
	}

	if text := msg.Key().Text; text != "" {
		m.input += text
	}
	return m, nil
}

func (m *Model) handleHistoryKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.selected > 0 {
			m.selected--
		}
		return m, nil

	case "down", "j":
		if m.selected < len(m.messages)-1 {
			m.selected++
		}
		return m, nil

	case "w":
		target := m.selectedMessage()
		if target == nil || target.Type == "text" || target.SourcePath != "" {
			return m, nil // w is gated on SourcePath == "" (HLD §10.4)
		}
		m.savePromptOpen = true
		m.savePromptValue = m.cfg.SaveDir + "/"
		m.saveTarget = target
		return m, nil

	case "o":
		target := m.selectedMessage()
		if target == nil || target.Type == "text" {
			return m, nil
		}
		return m, openCmd(m.cfg, target)
	}
	return m, nil
}

func (m *Model) selectedMessage() *store.Message {
	if m.selected < 0 || m.selected >= len(m.messages) {
		return nil
	}
	return m.messages[m.selected]
}

func (m *Model) handleSavePromptKey(msg tea.KeyPressMsg, key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.savePromptOpen = false
		m.saveTarget = nil
		return m, nil

	case "enter":
		dest := strings.TrimSpace(m.savePromptValue)
		target := m.saveTarget
		m.savePromptOpen = false
		m.saveTarget = nil
		if dest == "" || target == nil {
			return m, nil
		}
		return m, saveCmd(m.cfg, target, dest)

	case "backspace":
		if n := len(m.savePromptValue); n > 0 {
			m.savePromptValue = m.savePromptValue[:n-1]
		}
		return m, nil

	case "space":
		m.savePromptValue += " "
		return m, nil
	}

	if text := msg.Key().Text; text != "" {
		m.savePromptValue += text
	}
	return m, nil
}

func (m *Model) handleConfirmClearKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y":
		m.confirmClear = false
		return m, clearCmd(m.cfg, m.st)
	default:
		m.confirmClear = false
		return m, nil
	}
}
