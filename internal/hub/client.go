package hub

import (
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"mrep/internal/store"
)

// Constants per docs/LLD-websocket.md §13.
const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10 // 54s, must stay under pongWait
	maxMessageSize = 32 << 10            // WS carries text/control only; files use HTTP
	sendBuffer     = 32
	maxTextLen     = 16 << 10
)

// Client is the server-side handle for one connected phone.
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
}

// NewClient wraps an already-upgraded websocket connection.
func NewClient(h *Hub, conn *websocket.Conn) *Client {
	return &Client{
		hub:  h,
		conn: conn,
		send: make(chan []byte, sendBuffer),
	}
}

// Start registers the client with the hub — triggering history replay for
// `since` — and launches its read/write pumps. Returns immediately; the
// connection is torn down in the background once the socket dies.
func (c *Client) Start(since int64) {
	go c.writePump()
	c.hub.registerClient(c, since)
	go c.readPump()
}

// readPump is the only reader of the connection (gorilla/websocket
// requirement) and the only place inbound frames are handled.
func (c *Client) readPump() {
	defer func() {
		c.hub.unregisterClient(c)
		c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return // includes read-deadline expiry from a missed pong
		}
		c.handle(data)
	}
}

func (c *Client) handle(data []byte) {
	var in InFrame
	if err := json.Unmarshal(data, &in); err != nil {
		return // malformed frame: ignore, don't disconnect
	}

	switch in.Kind {
	case "send_text":
		text := strings.TrimSpace(in.Content)
		if text == "" || len(text) > maxTextLen {
			return
		}
		if _, err := c.hub.Publish(&store.Message{
			Type:    "text",
			Content: text,
			Sender:  "phone",
		}); err != nil {
			log.Printf("hub: publish from phone failed: %v", err)
		}
	default:
		// Unknown kind: ignore for forward compatibility.
	}
}

// writePump is the only writer of the connection and owns the heartbeat.
func (c *Client) writePump() {
	ping := time.NewTicker(pingPeriod)
	defer func() {
		ping.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case frame, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok { // hub closed send: this client was reaped or hub is shutting down
				c.conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, frame); err != nil {
				return
			}

		case <-ping.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
