// Package qrcode renders a URL as a scannable QR code directly in the
// terminal (HLD §5, §10), using half-block characters so it's compact enough
// to sit in the TUI's right-hand pane.
package qrcode

import (
	"strings"

	"github.com/mdp/qrterminal/v3"
)

// Render returns the QR code for url as a multi-line string of half-block
// characters, ready to drop into a lipgloss pane.
func Render(url string) string {
	var b strings.Builder
	qrterminal.GenerateHalfBlock(url, qrterminal.L, &b)
	return strings.TrimRight(b.String(), "\n")
}
