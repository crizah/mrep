package tui

import "charm.land/lipgloss/v2"

// Neon palette (HLD §10.2).
var (
	colorBG    = lipgloss.Color("#0A0A0F")
	colorPink  = lipgloss.Color("#FF2EC4")
	colorGreen = lipgloss.Color("#39FF14")
	colorDim   = lipgloss.Color("#5A5A6E")
	colorRed   = lipgloss.Color("#FF4D4D")
	colorText  = lipgloss.Color("#E8E8F0")
)

var (
	logoStyle = lipgloss.NewStyle().Foreground(colorPink).Bold(true)

	taglineStyle = lipgloss.NewStyle().Foreground(colorDim).Italic(true)

	statusLiveStyle = lipgloss.NewStyle().Foreground(colorGreen).Bold(true)
	statusHoldStyle = lipgloss.NewStyle().Foreground(colorDim)
	statusErrStyle  = lipgloss.NewStyle().Foreground(colorRed).Bold(true)

	paneTitleStyle = lipgloss.NewStyle().Foreground(colorGreen).Bold(true)

	chatBorderStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(colorDim)

	qrBorderStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(colorGreen)

	bubbleLaptopStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(colorPink).
				Foreground(colorText).
				Padding(0, 1)

	bubblePhoneStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(colorGreen).
				Foreground(colorText).
				Padding(0, 1)

	bubbleSelectedStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.ThickBorder()).
				Foreground(lipgloss.Color("#FFFFFF")).
				Padding(0, 1)

	metaStyle = lipgloss.NewStyle().Foreground(colorDim)

	composerStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(colorPink).
			Padding(0, 1)

	composerBlurredStyle = composerStyle.BorderForeground(colorDim)

	helpStyle = lipgloss.NewStyle().Foreground(colorDim)

	toastStyle = lipgloss.NewStyle().Foreground(colorGreen).Bold(true)

	promptStyle = lipgloss.NewStyle().Foreground(colorPink).Bold(true)
)
