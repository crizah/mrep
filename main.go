package main

// connect x11 clipboard to terminal with xclip
// This command extracts the raw image/png data from the clipboard and saves it to a file
// xclip -selection clipboard -t image/png -o > /tmp/image.png

// then we can hopefully do smth like
// qrcp send <tmp-dir> to send the actual file

// all of this will be packaged into one command on the tui
// tui has 2 options, s and r
// on s -> if u type c -> the above 2 steps are carried out
// generates a qr code that opens a chat like window

// main functionalitites i want
// 1. paste option (got the rough idea on how to do that)
// 2. sending texts (not sure about this)
// 3. persistance of some kind, so maybe we can store to a non tmp dir, and make a sqlite db?
// -> have a clear history option?
//
import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
)

type State string

const (
	SEND    State = "send"
	RECEIVE State = "receive"
	HOLD    State = "hold"
)

type Message struct {
	Msg string
}

type App struct {
	State    State
	Messages []*Message
}

func NewApp() *App {
	return &App{
		Messages: make([]*Message, 0),
		State:    HOLD,
	}
}

func (a *App) Init() tea.Cmd {
	return nil
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	// Is it a key press?
	case tea.KeyPressMsg:

		// Cool, what was the actual key pressed?
		switch msg.String() {

		// These keys should exit the program.
		case "ctrl+c", "q":
			return a, tea.Quit

		// s means send
		case "s":
			a.State = SEND

		// r means receive
		case "r":
			a.State = RECEIVE
		}
	}

	// Return the updated model to the Bubble Tea runtime for processing.
	// Note that we're not returning a command.
	return a, nil
}

func (a *App) View() tea.View {
	s := "Lets find out "

	if a.State == SEND {
		s += "u clicked send "

	}

	if a.State == RECEIVE {
		s += "u clicked receive "

	}

	if a.State == HOLD {
		s += "womp womp"
	}

	return tea.NewView(s)
}

func main() {
	p := tea.NewProgram(NewApp())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}
}
