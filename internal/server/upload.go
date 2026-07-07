package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"mrep/internal/hub"
	"mrep/internal/ingest"
	"mrep/internal/store"
)

// handleUpload is the phone → laptop file path (HLD §9.6): the phone POSTs
// multipart form data; bytes are ingested to a blob, the resulting message is
// published through the Hub (so the TUI updates live), and the persisted
// message is returned as JSON so the uploader can render it immediately.
func (s *Server) handleUpload(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadBytes)

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	defer file.Close()

	res, err := ingest.FromReader(s.cfg.FilesDir(), file, header.Filename)
	if err != nil {
		if strings.Contains(err.Error(), "request body too large") {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	msgType := "file"
	if strings.HasPrefix(res.MimeType, "image/") {
		msgType = "image"
	}

	m := &store.Message{
		Type:      msgType,
		Content:   fmt.Sprintf("/blob/%s/%s", res.BlobToken, url.PathEscape(res.Filename)),
		Filename:  res.Filename,
		MimeType:  res.MimeType,
		Sender:    "phone",
		BlobToken: res.BlobToken,
		Size:      res.Size,
	}

	persisted, err := s.hub.Publish(m)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, hub.ToWire(persisted))
}
