package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"

	tea "charm.land/bubbletea/v2"

	"mrep/internal/clipboard"
	"mrep/internal/config"
	"mrep/internal/hub"
	"mrep/internal/store"
	"mrep/internal/tui"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mrep: config: %v\n", err)
		os.Exit(1)
	}

	assets, err := fs.Sub(webFS, "web")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mrep: embedded assets: %v\n", err)
		os.Exit(1)
	}

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "mrep: store: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	h := hub.New(st, cfg.HistoryLimit)
	// The hub runs for the whole process lifetime; it needs no separate
	// cancellation since the process exits as soon as p.Run() returns below.
	go h.Run(context.Background())

	var clip clipboard.Clipboard
	if clipboard.Available() {
		clip = clipboard.NewX11()
	}

	m := tui.New(tui.Deps{Cfg: cfg, Store: st, Hub: h, Clip: clip, Assets: assets})

	p := tea.NewProgram(m)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "mrep: %v\n", err)
		os.Exit(1)
	}
}
