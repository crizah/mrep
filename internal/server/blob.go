package server

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"

	"github.com/gin-gonic/gin"
)

var tokenPattern = regexp.MustCompile(`^[0-9a-f]+$`)

// handleBlob serves a file's bytes (HLD §7.2/§8.1). Inline by default so
// `<img>` tags render (enabling iOS long-press → Save to Photos); with
// ?download=1 it sets Content-Disposition: attachment so iOS routes it to
// Files instead. The blob is addressed solely by :token — the DB lookup is
// the only thing that can produce a valid on-disk path, so an attacker can't
// use :filename (or an unrecognized :token) to reach an arbitrary file.
func (s *Server) handleBlob(c *gin.Context) {
	token := c.Param("token")
	if !tokenPattern.MatchString(token) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	m, err := s.store.GetByToken(token)
	if err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	diskPath := filepath.Join(s.cfg.FilesDir(), token+filepath.Ext(m.Filename))
	f, err := os.Open(diskPath)
	if err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	kind := "inline"
	if c.Query("download") == "1" {
		kind = "attachment"
	}

	c.Writer.Header().Set("Content-Type", m.MimeType)
	c.Writer.Header().Set("Content-Disposition", contentDisposition(kind, m.Filename))
	http.ServeContent(c.Writer, c.Request, m.Filename, info.ModTime(), f)
}

// contentDisposition builds a header carrying both an ASCII-safe fallback
// filename and an RFC 5987 UTF-8 filename*, so quotes/unicode/newlines in the
// original name can never corrupt the header (HLD §16).
func contentDisposition(kind, filename string) string {
	return fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`,
		kind, asciiFallback(filename), url.PathEscape(filename))
}

func asciiFallback(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r < 32 || r > 126 || r == '"' || r == '\\' {
			out = append(out, '_')
			continue
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return "file"
	}
	return string(out)
}
