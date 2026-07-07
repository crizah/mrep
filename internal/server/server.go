// Package server exposes the HTTP + WebSocket surface described in HLD §8:
// the web UI, the /ws upgrade, phone file uploads, and blob downloads.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"mrep/internal/config"
	"mrep/internal/hub"
	"mrep/internal/store"
)

// maxUploadBytes caps a single POST /upload body (HLD §14/§16: never buffer
// an unbounded body, stream to disk with a hard ceiling instead).
const maxUploadBytes = 200 << 20 // 200 MiB

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// LAN-only tool; the real gate is the session token, not WS origin
	// (HLD §14).
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Server wires config, the Hub, and the Store into an HTTP server.
type Server struct {
	cfg     config.Config
	hub     *hub.Hub
	store   *store.Store
	assets  fs.FS
	token   string
	httpSrv *http.Server
}

// New builds a Server with a fresh random session token (HLD §14). The token
// gates every route when cfg.RequireToken is set and is embedded in the QR
// URL by the caller. assets is the embedded web/ directory (index.html,
// app.js, style.css).
func New(cfg config.Config, h *hub.Hub, st *store.Store, assets fs.FS) (*Server, error) {
	token, err := randomToken()
	if err != nil {
		return nil, fmt.Errorf("generate session token: %w", err)
	}
	return &Server{cfg: cfg, hub: h, store: st, assets: assets, token: token}, nil
}

func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Token is the current session's auth token.
func (s *Server) Token() string { return s.token }

// URL builds the phone-facing URL for a given LAN host, embedding the token.
func (s *Server) URL(host string) string {
	scheme := "http"
	if s.cfg.TLS {
		scheme = "https"
	}
	u := fmt.Sprintf("%s://%s:%d/", scheme, host, s.cfg.Port)
	if s.cfg.RequireToken {
		u += "?t=" + s.token
	}
	return u
}

func (s *Server) router() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	auth := s.authMiddleware()
	r.GET("/", auth, s.handleIndex)
	r.GET("/app.js", auth, s.handleAppJS)
	r.GET("/style.css", auth, s.handleStyleCSS)
	r.GET("/ws", auth, s.handleWS)
	r.POST("/upload", auth, s.handleUpload)
	r.GET("/blob/:token/:filename", auth, s.handleBlob)

	return r
}

// authMiddleware enforces the ?t= session token (HLD §14). It applies to
// every route, including /blob and /upload, per the hardening note in
// docs/HLD.md §14 ("gate them with the session token too").
func (s *Server) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.cfg.RequireToken || c.Query("t") == s.token {
			c.Next()
			return
		}
		c.AbortWithStatus(http.StatusUnauthorized)
	}
}

func (s *Server) handleIndex(c *gin.Context) {
	http.ServeFileFS(c.Writer, c.Request, s.assets, "index.html")
}

func (s *Server) handleAppJS(c *gin.Context) {
	http.ServeFileFS(c.Writer, c.Request, s.assets, "app.js")
}

func (s *Server) handleStyleCSS(c *gin.Context) {
	http.ServeFileFS(c.Writer, c.Request, s.assets, "style.css")
}

func (s *Server) handleWS(c *gin.Context) {
	since, _ := strconv.ParseInt(c.Query("since"), 10, 64)

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}

	client := hub.NewClient(s.hub, conn)
	client.Start(since)
}

// Listen binds the configured port synchronously, so a caller (the TUI's
// start command) can report "port already in use" immediately instead of
// discovering it after already having switched to a "live" state (HLD §16).
func (s *Server) Listen() (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf(":%d", s.cfg.Port))
}

// Serve runs the HTTP server on an already-bound listener until ctx is
// canceled, then gracefully shuts it down (HLD §15). Intended to be run in
// its own goroutine after a successful Listen.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	s.httpSrv = &http.Server{Handler: s.router()}

	errCh := make(chan error, 1)
	go func() {
		if err := s.httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.httpSrv.Shutdown(shutdownCtx)
	}
}
