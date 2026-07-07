package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gorilla/websocket"

	"mrep/internal/config"
	"mrep/internal/hub"
	"mrep/internal/store"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server, *hub.Hub) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		DataDir:       dir,
		HistoryLimit:  50,
		RequireToken:  true,
		RetentionDays: 0,
	}
	if err := os.MkdirAll(cfg.FilesDir(), 0o755); err != nil {
		t.Fatalf("mkdir files dir: %v", err)
	}

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	h := hub.New(st, cfg.HistoryLimit)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go h.Run(ctx)

	assets := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html></html>")},
		"app.js":     &fstest.MapFile{Data: []byte("")},
		"style.css":  &fstest.MapFile{Data: []byte("")},
	}
	srv, err := New(cfg, h, st, assets)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ts := httptest.NewServer(srv.router())
	t.Cleanup(ts.Close)

	return srv, ts, h
}

func TestUnauthorizedWithoutToken(t *testing.T) {
	_, ts, _ := newTestServer(t)

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestUploadThenBlobDownload(t *testing.T) {
	srv, ts, _ := newTestServer(t)

	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	part, err := w.CreateFormFile("file", "hello.txt")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	want := "hello from the phone"
	if _, err := part.Write([]byte(want)); err != nil {
		t.Fatalf("write part: %v", err)
	}
	w.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/upload?t="+srv.Token(), body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, b)
	}

	var wire hub.WireMessage
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if wire.ID == 0 || wire.Type != "file" || wire.Sender != "phone" {
		t.Fatalf("unexpected wire message: %+v", wire)
	}
	if !strings.HasPrefix(wire.Content, "/blob/") {
		t.Fatalf("expected content to start with /blob/, got %q", wire.Content)
	}

	// Download inline.
	dlResp, err := http.Get(ts.URL + wire.Content + "?t=" + srv.Token())
	if err != nil {
		t.Fatalf("GET blob: %v", err)
	}
	defer dlResp.Body.Close()
	got, _ := io.ReadAll(dlResp.Body)
	if string(got) != want {
		t.Fatalf("expected blob body %q, got %q", want, got)
	}
	if disp := dlResp.Header.Get("Content-Disposition"); !strings.HasPrefix(disp, "inline") {
		t.Fatalf("expected inline disposition by default, got %q", disp)
	}

	// Download as attachment.
	dlResp2, err := http.Get(ts.URL + wire.Content + "?t=" + srv.Token() + "&download=1")
	if err != nil {
		t.Fatalf("GET blob download=1: %v", err)
	}
	defer dlResp2.Body.Close()
	if disp := dlResp2.Header.Get("Content-Disposition"); !strings.HasPrefix(disp, "attachment") {
		t.Fatalf("expected attachment disposition, got %q", disp)
	}
}

func TestWebSocketHistoryAndSendText(t *testing.T) {
	srv, ts, h := newTestServer(t)

	// Seed one message before any phone connects.
	if _, err := h.Publish(&store.Message{Type: "text", Content: "seed", Sender: "laptop"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws?t=" + srv.Token() + "&since=0"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read history frame: %v", err)
	}
	var hist struct {
		Kind     string            `json:"kind"`
		Messages []hub.WireMessage `json:"messages"`
	}
	if err := json.Unmarshal(data, &hist); err != nil {
		t.Fatalf("unmarshal history: %v", err)
	}
	if hist.Kind != "history" || len(hist.Messages) != 1 || hist.Messages[0].Content != "seed" {
		t.Fatalf("unexpected history frame: %+v", hist)
	}

	// Phone sends text; expect it to echo back as a msg frame.
	out, _ := json.Marshal(map[string]string{"kind": "send_text", "content": "hi from phone"})
	if err := conn.WriteMessage(websocket.TextMessage, out); err != nil {
		t.Fatalf("write: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data2, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read msg frame: %v", err)
	}
	var frame struct {
		Kind    string          `json:"kind"`
		Message hub.WireMessage `json:"message"`
	}
	if err := json.Unmarshal(data2, &frame); err != nil {
		t.Fatalf("unmarshal msg frame: %v", err)
	}
	if frame.Kind != "msg" || frame.Message.Content != "hi from phone" || frame.Message.Sender != "phone" {
		t.Fatalf("unexpected msg frame: %+v", frame)
	}
}
