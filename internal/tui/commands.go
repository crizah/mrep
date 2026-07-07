package tui

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"mrep/internal/clipboard"
	"mrep/internal/config"
	"mrep/internal/hub"
	"mrep/internal/ingest"
	"mrep/internal/netutil"
	"mrep/internal/qrcode"
	"mrep/internal/server"
	"mrep/internal/store"
)

// hubEventMsg wraps a hub.Event as a tea.Msg (HLD §10.3).
type hubEventMsg struct{ event hub.Event }

// tickMsg drives the pulsing connection-dot animation.
type tickMsg struct{}

type serverStartedMsg struct {
	srv *server.Server
	url string
	qr  string
	err error
}

type publishResultMsg struct{ err error }

type saveResultMsg struct {
	dest string
	err  error
}

type clearResultMsg struct{ err error }

type toastExpireMsg struct{ gen int }

// waitForHub blocks on the hub subscription channel and re-arms itself after
// each event — the standard Bubble Tea "external event source" pattern
// (HLD §10.3, LLD-websocket §10).
func waitForHub(sub <-chan hub.Event) tea.Cmd {
	return func() tea.Msg {
		return hubEventMsg{event: <-sub}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
		return tickMsg{}
	})
}

// startServerCmd runs retention pruning, discovers the LAN address, binds
// the port synchronously (so bind failures surface immediately rather than
// after switching to a "live" state), and hands the listener off to a
// background goroutine to serve for the rest of the process's life.
func startServerCmd(ctx context.Context, cfg config.Config, h *hub.Hub, st *store.Store, assets fs.FS) tea.Cmd {
	return func() tea.Msg {
		if cfg.RetentionDays > 0 {
			cutoff := time.Now().AddDate(0, 0, -cfg.RetentionDays)
			if tokens, err := st.Prune(cutoff); err == nil {
				removeBlobs(cfg, tokens)
			}
		}

		srv, err := server.New(cfg, h, st, assets)
		if err != nil {
			return serverStartedMsg{err: err}
		}

		ln, err := srv.Listen()
		if err != nil {
			return serverStartedMsg{err: fmt.Errorf("port %d: %w", cfg.Port, err)}
		}

		ip, err := netutil.LANIPv4()
		if err != nil {
			ip = "127.0.0.1"
		}
		url := srv.URL(ip)

		go srv.Serve(ctx, ln)

		return serverStartedMsg{srv: srv, url: url, qr: qrcode.Render(url)}
	}
}

func removeBlobs(cfg config.Config, tokens []string) {
	for _, token := range tokens {
		matches, _ := filepath.Glob(filepath.Join(cfg.FilesDir(), token+".*"))
		for _, m := range matches {
			os.Remove(m)
		}
	}
}

// submitCmd is the composer's Enter action: if the text is an existing file
// path it's ingested as a file/image (HLD §9.3), otherwise sent as plain text
// (HLD §9.2). Either way it's published through the Hub and rendered only
// when it comes back via the subscription (render-on-echo, HLD §18 #3).
func submitCmd(cfg config.Config, h *hub.Hub, text string) tea.Cmd {
	return func() tea.Msg {
		text = strings.TrimSpace(text)
		if text == "" {
			return publishResultMsg{}
		}

		if info, err := os.Stat(text); err == nil && !info.IsDir() {
			abs, err := filepath.Abs(text)
			if err != nil {
				abs = text
			}
			res, err := ingest.FromPath(cfg.FilesDir(), abs)
			if err != nil {
				return publishResultMsg{err: err}
			}
			msgType := "file"
			if strings.HasPrefix(res.MimeType, "image/") {
				msgType = "image"
			}
			_, err = h.Publish(&store.Message{
				Type:       msgType,
				Content:    fmt.Sprintf("/blob/%s/%s", res.BlobToken, res.Filename),
				Filename:   res.Filename,
				MimeType:   res.MimeType,
				Sender:     cfg.DeviceName,
				BlobToken:  res.BlobToken,
				SourcePath: abs,
				Size:       res.Size,
			})
			return publishResultMsg{err: err}
		}

		_, err := h.Publish(&store.Message{Type: "text", Content: text, Sender: cfg.DeviceName})
		return publishResultMsg{err: err}
	}
}

// pasteCmd implements the clipboard "smart paste" (HLD §12): image -> ingest
// as a transient blob (no SourcePath — there's no durable original); a real
// file path (typed text or a uri-list hit) -> ingest with SourcePath set;
// plain text -> sent as-is.
func pasteCmd(cfg config.Config, h *hub.Hub, clip clipboard.Clipboard) tea.Cmd {
	return func() tea.Msg {
		if clip == nil {
			return publishResultMsg{err: fmt.Errorf("xclip not found on PATH")}
		}
		item, err := clip.Read()
		if err != nil {
			return publishResultMsg{err: err}
		}

		switch item.Kind {
		case clipboard.KindText:
			_, err := h.Publish(&store.Message{Type: "text", Content: item.Text, Sender: cfg.DeviceName})
			return publishResultMsg{err: err}

		case clipboard.KindFilePath, clipboard.KindImage:
			res, err := ingest.FromPath(cfg.FilesDir(), item.Path)
			if err != nil {
				return publishResultMsg{err: err}
			}
			msgType := "file"
			if strings.HasPrefix(res.MimeType, "image/") {
				msgType = "image"
			}
			sourcePath := ""
			if item.Kind == clipboard.KindFilePath {
				sourcePath = item.Path
			} else {
				// Transient temp file grabbed from the clipboard: it's not a
				// durable original, so clean it up now that it's ingested.
				os.Remove(item.Path)
			}
			_, err = h.Publish(&store.Message{
				Type:       msgType,
				Content:    fmt.Sprintf("/blob/%s/%s", res.BlobToken, res.Filename),
				Filename:   res.Filename,
				MimeType:   res.MimeType,
				Sender:     cfg.DeviceName,
				BlobToken:  res.BlobToken,
				SourcePath: sourcePath,
				Size:       res.Size,
			})
			return publishResultMsg{err: err}
		}
		return publishResultMsg{}
	}
}

// saveCmd copies a phone-origin (or clipboard-origin) blob out to dest,
// suffixing " (n)" on collision rather than overwriting (HLD §10.4).
func saveCmd(cfg config.Config, target *store.Message, destDir string) tea.Cmd {
	return func() tea.Msg {
		ext := filepath.Ext(target.Filename)
		diskPath := filepath.Join(cfg.FilesDir(), target.BlobToken+ext)

		dest := uniqueDest(destDir, target.Filename)
		if err := copyFile(diskPath, dest); err != nil {
			return saveResultMsg{err: err}
		}
		return saveResultMsg{dest: dest}
	}
}

func uniqueDest(destDir, filename string) string {
	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filename, ext)
	dest := filepath.Join(destDir, filename)
	for i := 1; ; i++ {
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			return dest
		}
		dest = filepath.Join(destDir, fmt.Sprintf("%s (%d)%s", base, i, ext))
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

// openCmd runs `xdg-open` on a blob (HLD §10.4's optional "o" action).
func openCmd(cfg config.Config, target *store.Message) tea.Cmd {
	return func() tea.Msg {
		ext := filepath.Ext(target.Filename)
		diskPath := filepath.Join(cfg.FilesDir(), target.BlobToken+ext)
		err := exec.Command("xdg-open", diskPath).Start()
		return publishResultMsg{err: err}
	}
}

// clearCmd deletes all history and its blobs (HLD §7.3, the "X" action).
func clearCmd(cfg config.Config, st *store.Store) tea.Cmd {
	return func() tea.Msg {
		tokens, err := st.ClearAll()
		if err != nil {
			return clearResultMsg{err: err}
		}
		removeBlobs(cfg, tokens)
		return clearResultMsg{}
	}
}
