package tui

import (
	"fmt"
	"os"
	"strings"

	lipgloss "charm.land/lipgloss/v2"

	"mrep/internal/store"
)

const minWidth, minHeight = 60, 20

func (m *Model) render() string {
	width := max(m.width, minWidth)
	height := max(m.height, minHeight)

	header := m.renderHeader(width)
	footer := m.renderFooter(width)

	bodyHeight := max(height-lipgloss.Height(header)-lipgloss.Height(footer), 5)

	leftWidth := width * 3 / 5
	rightWidth := width - leftWidth

	chat := m.renderChatPane(leftWidth, bodyHeight)
	side := m.renderSidePane(rightWidth, bodyHeight)
	body := lipgloss.JoinHorizontal(lipgloss.Top, chat, side)

	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m *Model) renderHeader(width int) string {
	status := statusHoldStyle.Render("○ HOLD")
	if m.state == stateLive {
		dot := "◉"
		if m.tick%2 == 0 {
			dot = "○"
		}
		status = statusLiveStyle.Render(fmt.Sprintf("%s LIVE  ·  %d 📱", dot, m.phoneCount))
	}
	if m.startErr != "" {
		status = statusErrStyle.Render("✕ " + m.startErr)
	}

	logo := logoStyle.Render(strings.TrimPrefix(asciiLogo, "\n"))
	tag := taglineStyle.Render(tagline)
	left := lipgloss.JoinVertical(lipgloss.Left, logo, tag)

	right := lipgloss.NewStyle().Width(width - lipgloss.Width(left)).AlignHorizontal(lipgloss.Right).Render(status)

	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

func (m *Model) renderFooter(width int) string {
	text := "ctrl+s start  ·  ctrl+v paste  ·  enter send  ·  tab focus history  ·  w save  ·  o open  ·  ctrl+x clear  ·  ctrl+c quit"
	if m.status != "" {
		text = toastStyle.Render(m.status)
	}
	return helpStyle.Width(width).Render(text)
}

func (m *Model) renderChatPane(width, height int) string {
	innerHeight := max(height-4, 1) // border + title + composer

	title := paneTitleStyle.Render("CHAT")
	history := m.renderHistory(width-4, innerHeight)
	composer := m.renderComposer(width - 4)

	content := lipgloss.JoinVertical(lipgloss.Left, title, history, composer)
	style := chatBorderStyle
	if m.focus == focusHistory {
		style = style.BorderForeground(colorGreen)
	}
	return style.Width(width - 2).Height(height - 2).Render(content)
}

func (m *Model) renderHistory(width, height int) string {
	if len(m.messages) == 0 {
		return metaStyle.Render("no messages yet — type below and press enter")
	}

	var bubbles []string
	for i, msg := range m.messages {
		bubbles = append(bubbles, m.renderBubble(msg, i == m.selected && m.focus == focusHistory, width))
	}
	joined := lipgloss.JoinVertical(lipgloss.Left, bubbles...)

	lines := strings.Split(joined, "\n")
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderBubble(msg *store.Message, selected bool, maxWidth int) string {
	style := bubbleLaptopStyle
	align := lipgloss.Right
	if msg.Sender != m.cfg.DeviceName {
		style = bubblePhoneStyle
		align = lipgloss.Left
	}
	if selected {
		style = bubbleSelectedStyle.BorderForeground(style.GetBorderTopForeground())
	}

	bw := maxWidth * 2 / 3
	if bw < 20 {
		bw = maxWidth
	}
	style = style.Width(bw)

	lines := []string{fmt.Sprintf("%s  %s", senderIcon(msg), displayText(msg))}
	if msg.Type != "text" {
		lines = append(lines, metaStyle.Render(fmt.Sprintf("%s · %s", msg.MimeType, humanSize(msg.Size))))
		if msg.Type != "text" && msg.SourcePath == "" && msg.Sender != m.cfg.DeviceName {
			lines = append(lines, metaStyle.Render("↳ w: save to…  o: open"))
		}
	}

	bubble := style.Render(strings.Join(lines, "\n"))
	return lipgloss.PlaceHorizontal(maxWidth, align, bubble)
}

func senderIcon(msg *store.Message) string {
	switch msg.Type {
	case "image":
		return "🖼"
	case "file":
		return "📄"
	default:
		return "💬"
	}
}

// displayText resolves the human-meaningful label for a bubble: SourcePath
// for laptop-origin files, else the display filename, else the text body —
// the on-disk blob token is never shown (HLD §10.4).
func displayText(msg *store.Message) string {
	if msg.Type == "text" {
		return msg.Content
	}
	if msg.SourcePath != "" {
		return abbreviateHome(msg.SourcePath)
	}
	return msg.Filename
}

func abbreviateHome(path string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func (m *Model) renderComposer(width int) string {
	style := composerStyle
	if m.focus != focusComposer {
		style = composerBlurredStyle
	}

	if m.savePromptOpen {
		return promptStyle.Render("save to: ") + m.savePromptValue + "█"
	}
	if m.confirmClear {
		return statusErrStyle.Render("clear all history? (y/n)")
	}

	prompt := "› " + m.input
	if m.focus == focusComposer {
		prompt += "█"
	}
	if m.input == "" && m.focus != focusComposer {
		prompt = metaStyle.Render("› type text or a file path…")
	}
	return style.Width(width).Render(prompt)
}

func (m *Model) renderSidePane(width, height int) string {
	title := paneTitleStyle.Render("SCAN TO CONNECT")

	var body string
	switch {
	case m.state != stateLive && m.startErr == "":
		body = metaStyle.Render("press ctrl+s to start the server")
	case m.startErr != "":
		body = statusErrStyle.Render(m.startErr)
	default:
		lines := []string{
			m.qr,
			"",
			m.serverURL,
			"",
			statusLiveStyle.Render("◉ waiting for scan…"),
			"",
			metaStyle.Render("files ▸ " + m.cfg.DataDir),
		}
		body = strings.Join(lines, "\n")
	}

	content := lipgloss.JoinVertical(lipgloss.Center, title, body)
	return qrBorderStyle.Width(width - 2).Height(height - 2).Render(content)
}
