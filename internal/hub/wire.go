package hub

import (
	"encoding/json"
	"time"

	"mrep/internal/store"
)

// WireMessage is the JSON shape sent to phones (LLD-websocket §5.1). Notably
// it omits store.Message.SourcePath: that's a laptop filesystem path, useful
// only to the TUI (HLD §10.4), and never sent over the wire.
type WireMessage struct {
	ID        int64     `json:"id"`
	Type      string    `json:"type"`
	Content   string    `json:"content"`
	Filename  string    `json:"filename"`
	Mime      string    `json:"mime"`
	Size      int64     `json:"size"`
	Sender    string    `json:"sender"`
	CreatedAt time.Time `json:"created_at"`
}

// ToWire converts a store.Message to the JSON shape sent over the wire and
// returned by POST /upload, so both paths produce byte-identical `id`-keyed
// records for client-side dedup (LLD-websocket §9).
func ToWire(m *store.Message) WireMessage {
	return WireMessage{
		ID:        m.ID,
		Type:      m.Type,
		Content:   m.Content,
		Filename:  m.Filename,
		Mime:      m.MimeType,
		Size:      m.Size,
		Sender:    m.Sender,
		CreatedAt: m.CreatedAt,
	}
}

type msgFrame struct {
	Kind    string      `json:"kind"`
	Message WireMessage `json:"message"`
}

type historyFrame struct {
	Kind     string        `json:"kind"`
	Messages []WireMessage `json:"messages"`
}

func encodeMsgFrame(m *store.Message) ([]byte, error) {
	return json.Marshal(msgFrame{Kind: "msg", Message: ToWire(m)})
}

func encodeHistoryFrame(msgs []*store.Message) ([]byte, error) {
	wire := make([]WireMessage, len(msgs))
	for i, m := range msgs {
		wire[i] = ToWire(m)
	}
	return json.Marshal(historyFrame{Kind: "history", Messages: wire})
}

// InFrame is the shape of client → server frames (LLD-websocket §5.3). Only
// "send_text" is handled in v1; unknown kinds are ignored for forward-compat.
type InFrame struct {
	Kind    string `json:"kind"`
	Content string `json:"content"`
}
