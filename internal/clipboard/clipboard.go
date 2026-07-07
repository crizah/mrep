// Package clipboard implements the laptop "smart paste" feature (HLD §12):
// pull whatever's on the X11 clipboard — an image copied from a browser, a
// file copied in a file manager, a plain path, or plain text — and figure out
// which of those it is.
package clipboard

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Kind identifies what Read found on the clipboard.
type Kind int

const (
	KindText Kind = iota
	KindFilePath
	KindImage
)

// Item is one clipboard read result.
type Item struct {
	Kind Kind
	Text string // set for KindText
	Path string // set for KindFilePath (a real, pre-existing path) and KindImage (a synthesized temp file)
}

// ErrEmpty is returned when the clipboard has nothing usable in it.
var ErrEmpty = errors.New("clipboard is empty")

// Clipboard abstracts the paste source so non-X11 backends can be added
// later without touching callers (HLD §12).
type Clipboard interface {
	Read() (Item, error)
}

// X11 shells out to `xclip`.
type X11 struct{}

// NewX11 returns the X11/xclip clipboard backend.
func NewX11() X11 { return X11{} }

// Available reports whether xclip is on PATH (HLD §16: "xclip missing -> dim
// hint" rather than crashing).
func Available() bool {
	_, err := exec.LookPath("xclip")
	return err == nil
}

func (X11) Read() (Item, error) {
	targets, err := xclipOutput("-t", "TARGETS")
	if err != nil {
		return Item{}, fmt.Errorf("read clipboard targets: %w", err)
	}
	targetList := strings.Split(strings.TrimSpace(string(targets)), "\n")

	if hasTarget(targetList, "image/png") {
		if path, err := grabImage(); err == nil {
			return Item{Kind: KindImage, Path: path}, nil
		}
		// fall through to text/uri-list below on failure
	}

	if hasTarget(targetList, "text/uri-list") {
		if path, ok := grabURIListPath(); ok {
			return Item{Kind: KindFilePath, Path: path}, nil
		}
	}

	text, err := xclipOutput()
	if err != nil {
		return Item{}, fmt.Errorf("read clipboard text: %w", err)
	}
	trimmed := strings.TrimSpace(string(text))
	if trimmed == "" {
		return Item{}, ErrEmpty
	}
	if info, err := os.Stat(trimmed); err == nil && !info.IsDir() {
		return Item{Kind: KindFilePath, Path: trimmed}, nil
	}
	return Item{Kind: KindText, Text: trimmed}, nil
}

func hasTarget(targets []string, want string) bool {
	for _, t := range targets {
		if strings.TrimSpace(t) == want {
			return true
		}
	}
	return false
}

func grabImage() (string, error) {
	data, err := xclipOutput("-t", "image/png")
	if err != nil || len(data) == 0 {
		return "", fmt.Errorf("no image on clipboard")
	}
	path := filepath.Join(os.TempDir(), fmt.Sprintf("mrep-clip-%d.png", time.Now().UnixNano()))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write temp image: %w", err)
	}
	return path, nil
}

func grabURIListPath() (string, bool) {
	data, err := xclipOutput("-t", "text/uri-list")
	if err != nil {
		return "", false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		u, err := url.Parse(line)
		if err != nil || u.Scheme != "file" {
			continue
		}
		if info, err := os.Stat(u.Path); err == nil && !info.IsDir() {
			return u.Path, true
		}
	}
	return "", false
}

func xclipOutput(extraArgs ...string) ([]byte, error) {
	args := append([]string{"-selection", "clipboard", "-o"}, extraArgs...)
	cmd := exec.Command("xclip", args...)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, stderr.String())
	}
	return out.Bytes(), nil
}
