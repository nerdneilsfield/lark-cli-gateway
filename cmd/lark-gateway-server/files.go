package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/nerdneilsfield/lark-cli-gateway/internal/protocol"
)

// job owns its private directory after enqueue. File paths never cross the wire.
type job struct {
	protocol.Message
	dir      string
	filename string
	key      string
}

func (j job) cleanup() {
	if j.dir != "" {
		if err := os.RemoveAll(j.dir); err != nil {
			log.Printf("remove upload: %v", err)
		}
	}
}

// The caller holds the listening socket before removing this endpoint's stale jobs.
func prepareSpool(address string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(address))
	dir := filepath.Join(cache, "lark-cli-gateway", hex.EncodeToString(sum[:16]))
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func safeFilename(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\:`) &&
		!strings.ContainsFunc(name, unicode.IsControl)
}

func handleSendFile(queue chan<- job, spool string, maxBytes int64) http.HandlerFunc {
	slots := make(chan struct{}, 2)
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "uploads busy", http.StatusServiceUnavailable)
			return
		}
		if len(queue) == cap(queue) {
			http.Error(w, "queue full", http.StatusServiceUnavailable)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes+(64<<10))
		reader, err := r.MultipartReader()
		if err != nil {
			http.Error(w, "expected multipart/form-data", http.StatusBadRequest)
			return
		}
		dir, err := os.MkdirTemp(spool, "job-")
		if err != nil {
			http.Error(w, "cannot create upload", http.StatusInternalServerError)
			return
		}
		msg := job{Message: protocol.Message{Type: "file"}, dir: dir}
		accepted := false
		defer func() {
			if !accepted {
				msg.cleanup()
			}
		}()
		seen := map[string]bool{}
		failRead := func(err error) {
			var sizeErr *http.MaxBytesError
			if errors.As(err, &sizeErr) {
				http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
			} else {
				http.Error(w, "invalid or incomplete upload", http.StatusBadRequest)
			}
		}
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				failRead(err)
				return
			}
			name := part.FormName()
			if seen[name] || (name != "chat_id" && name != "as" && name != "file") {
				http.Error(w, "unknown or duplicate multipart field", http.StatusBadRequest)
				return
			}
			seen[name] = true
			if name != "file" {
				if part.FileName() != "" {
					http.Error(w, "metadata must be a text field", http.StatusBadRequest)
					return
				}
				value, err := io.ReadAll(io.LimitReader(part, 4097))
				if err != nil {
					failRead(err)
					return
				}
				if len(value) > 4096 {
					http.Error(w, "metadata too large", http.StatusBadRequest)
					return
				}
				if name == "chat_id" {
					msg.ChatID = string(value)
				} else {
					msg.As = string(value)
				}
				continue
			}
			// Parse the original filename: Part.FileName strips traversal components.
			_, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
			if err != nil || !safeFilename(params["filename"]) {
				http.Error(w, "invalid filename", http.StatusBadRequest)
				return
			}
			msg.filename = params["filename"]
			file, err := os.OpenFile(filepath.Join(dir, msg.filename), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				http.Error(w, "cannot create file", http.StatusInternalServerError)
				return
			}
			n, copyErr := io.Copy(file, io.LimitReader(part, maxBytes+1))
			closeErr := file.Close()
			if n > maxBytes {
				http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
				return
			}
			if copyErr != nil {
				var pathErr *os.PathError
				if errors.As(copyErr, &pathErr) {
					http.Error(w, "cannot store file", http.StatusInternalServerError)
				} else {
					failRead(copyErr)
				}
				return
			}
			if closeErr != nil {
				http.Error(w, "cannot store file", http.StatusInternalServerError)
				return
			}
			if n == 0 {
				http.Error(w, "empty file", http.StatusBadRequest)
				return
			}
		}
		if msg.ChatID == "" || (msg.As != "bot" && msg.As != "user") || msg.filename == "" {
			http.Error(w, "chat_id, as (user or bot), and file are required", http.StatusBadRequest)
			return
		}
		var key [16]byte
		if _, err := rand.Read(key[:]); err != nil {
			http.Error(w, "cannot create job key", http.StatusInternalServerError)
			return
		}
		msg.key = hex.EncodeToString(key[:])
		select {
		case queue <- msg:
			accepted = true
			w.Header().Set("Content-Type", "application/json")
			if _, err := fmt.Fprint(w, `{"ok":true}`); err != nil {
				log.Printf("write response: %v", err)
			}
		default:
			http.Error(w, "queue full", http.StatusServiceUnavailable)
		}
	}
}
