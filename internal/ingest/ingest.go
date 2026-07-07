// Package ingest turns an incoming file (a laptop path, a phone upload, or a
// clipboard grab) into a blob on disk plus the metadata a store.Message needs.
package ingest

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/gabriel-vasile/mimetype"
)

// Result is everything a caller needs to build a store.Message for an
// ingested file. Filename is authoritative: its extension always matches
// what's actually on disk (BlobToken+ext), even when the original name had
// no extension and one had to be synthesized from the sniffed MIME type.
type Result struct {
	BlobToken string
	Filename  string
	MimeType  string
	Size      int64
}

// FromPath ingests a file already on the laptop's filesystem (typed/pasted
// path or clipboard grab). The caller is expected to also record path as the
// message's SourcePath.
func FromPath(filesDir, path string) (Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return Result{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	return FromReader(filesDir, f, filepath.Base(path))
}

// FromReader ingests bytes from any source (an opened path, a multipart
// upload). suggestedName is the original/display filename if known; it may
// be empty.
func FromReader(filesDir string, r io.Reader, suggestedName string) (Result, error) {
	token, err := randomToken()
	if err != nil {
		return Result{}, fmt.Errorf("generate blob token: %w", err)
	}

	tmpPath := filepath.Join(filesDir, token+".tmp")
	tmp, err := os.Create(tmpPath)
	if err != nil {
		return Result{}, fmt.Errorf("create temp blob: %w", err)
	}

	size, copyErr := io.Copy(tmp, r)
	closeErr := tmp.Close()
	if copyErr != nil {
		os.Remove(tmpPath)
		return Result{}, fmt.Errorf("write blob: %w", copyErr)
	}
	if closeErr != nil {
		os.Remove(tmpPath)
		return Result{}, fmt.Errorf("close blob: %w", closeErr)
	}

	mtype, err := mimetype.DetectFile(tmpPath)
	if err != nil {
		os.Remove(tmpPath)
		return Result{}, fmt.Errorf("detect mime: %w", err)
	}

	filename, ext := resolveNameAndExt(suggestedName, mtype)

	finalPath := filepath.Join(filesDir, token+ext)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return Result{}, fmt.Errorf("finalize blob: %w", err)
	}

	return Result{
		BlobToken: token,
		Filename:  filename,
		MimeType:  mtype.String(),
		Size:      size,
	}, nil
}

// resolveNameAndExt picks the extension the blob will be stored under and a
// display filename that always carries that same extension, so a later
// GET /blob/:token/:filename handler can derive the on-disk path purely from
// filepath.Ext(message.Filename) without any risk of it disagreeing with what
// FromReader actually wrote.
func resolveNameAndExt(suggestedName string, mtype *mimetype.MIME) (filename, ext string) {
	if suggestedName != "" {
		if e := filepath.Ext(suggestedName); e != "" {
			return suggestedName, e
		}
	}

	ext = mtype.Extension()
	if ext == "" {
		ext = ".bin"
	}
	if suggestedName != "" {
		return suggestedName + ext, ext
	}
	return "file" + ext, ext
}

func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
