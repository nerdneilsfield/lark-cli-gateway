package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func uploadRequest(t *testing.T, name, content string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("chat_id", "oc_test"); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("as", "user"); err != nil {
		t.Fatal(err)
	}
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, content); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/send-file", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestFileUploadRetryAndCleanup(t *testing.T) {
	for _, success := range []bool{true, false} {
		t.Run(map[bool]string{true: "success", false: "exhausted"}[success], func(t *testing.T) {
			spool := t.TempDir()
			queue := make(chan job, 1)
			rec := httptest.NewRecorder()
			handleSendFile(queue, spool, 100)(rec, uploadRequest(t, "报告 1.pdf", "exact bytes\x00"))
			if rec.Code != 200 {
				t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
			}
			msg := <-queue
			if msg.As != "user" || msg.ChatID != "oc_test" || msg.filename != "报告 1.pdf" || len(msg.key) != 32 {
				t.Fatalf("job: %+v", msg)
			}
			calls := 0
			sendWithRetry(msg, 2, 0, func(got job) error {
				calls++
				if got != msg {
					t.Fatal("job changed across retries")
				}
				data, err := os.ReadFile(filepath.Join(got.dir, got.filename))
				if err != nil || string(data) != "exact bytes\x00" {
					t.Fatalf("file: %q %v", data, err)
				}
				if success && calls == 2 {
					return nil
				}
				return errors.New("retry")
			})
			wantCalls := 3
			if success {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("calls = %d", calls)
			}
			entries, err := os.ReadDir(spool)
			if err != nil || len(entries) != 0 {
				t.Fatalf("leaked files: %v %v", entries, err)
			}
		})
	}
}

func TestFileUploadRejectsAndCleans(t *testing.T) {
	for _, tc := range []struct {
		name, filename, content string
		full                    bool
		status                  int
	}{
		{"oversized", "a.txt", "12345", false, 413},
		{"empty", "a.txt", "", false, 400},
		{"traversal", "../a.txt", "abc", false, 400},
		{"windows traversal", `..\a.txt`, "abc", false, 400},
		{"full", "a.txt", "abc", true, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spool := t.TempDir()
			queue := make(chan job, 1)
			if tc.full {
				queue <- validMessage()
			}
			rec := httptest.NewRecorder()
			handleSendFile(queue, spool, 4)(rec, uploadRequest(t, tc.filename, tc.content))
			if rec.Code != tc.status {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			entries, err := os.ReadDir(spool)
			if err != nil || len(entries) != 0 {
				t.Fatalf("leaked files: %v %v", entries, err)
			}
		})
	}
}

// Re-execute the Go test binary as a CLI, without depending on a shell.
func init() {
	if os.Getenv("GATEWAY_TEST_BLOCK") == "1" {
		<-time.After(time.Minute)
		os.Exit(0)
	}
	if os.Getenv("GATEWAY_TEST_HELPER") != "1" {
		return
	}
	wd, _ := os.Getwd()
	data, err := os.ReadFile("file_report.txt")
	if err != nil || string(data) != "payload" {
		os.Exit(2)
	}
	want := []string{"im", "+messages-send", "--chat-id", "oc_test", "--as", "bot", "--file", "./file_report.txt", "--idempotency-key", "stable-key"}
	if !reflect.DeepEqual(os.Args[1:], want) || wd != os.Getenv("GATEWAY_TEST_DIR") {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestSendFileUsesJobDirectory(t *testing.T) {
	dir := t.TempDir()
	// Resolve symlinks so cwd comparisons also work with macOS /var aliases.
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file_report.txt"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	cli, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_TEST_HELPER", "1")
	t.Setenv("GATEWAY_TEST_DIR", dir)
	msg := validMessage()
	msg.Type, msg.dir, msg.filename, msg.key = "file", dir, "file_report.txt", "stable-key"
	msg.As = "bot"
	if err := send(cli, msg, sendTimeout); err != nil {
		t.Fatal(err)
	}
}

func TestFileUploadMalformedAndDuplicate(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		spool := t.TempDir()
		queue := make(chan job, 1)
		req := uploadRequest(t, "a.txt", "abc")
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		if duplicate {
			boundary := strings.TrimPrefix(req.Header.Get("Content-Type"), "multipart/form-data; boundary=")
			extra := "--" + boundary + "\r\nContent-Disposition: form-data; name=\"as\"\r\n\r\nbot\r\n"
			body = bytes.Replace(body, []byte("--"+boundary+"--"), []byte(extra+"--"+boundary+"--"), 1)
		} else {
			body = body[:len(body)-20]
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		rec := httptest.NewRecorder()
		handleSendFile(queue, spool, 100)(rec, req)
		if rec.Code != 400 || len(queue) != 0 {
			t.Fatalf("malformed upload: %d", rec.Code)
		}
		entries, _ := os.ReadDir(spool)
		if len(entries) != 0 {
			t.Fatal("leaked upload")
		}
	}
}

type blockedBody struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockedBody) Read([]byte) (int, error) { close(b.entered); <-b.release; return 0, io.EOF }
func (b *blockedBody) Close() error             { return nil }

func TestFileUploadConcurrencyLimit(t *testing.T) {
	spool := t.TempDir()
	handler := handleSendFile(make(chan job, 3), spool, 100)
	var wg sync.WaitGroup
	defer wg.Wait()
	for i := 0; i < 2; i++ {
		body := &blockedBody{make(chan struct{}), make(chan struct{})}
		defer close(body.release)
		req := httptest.NewRequest("POST", "/send-file", body)
		req.Header.Set("Content-Type", "multipart/form-data; boundary=test")
		wg.Add(1)
		go func() { defer wg.Done(); handler(httptest.NewRecorder(), req) }()
		<-body.entered
	}
	rec := httptest.NewRecorder()
	handler(rec, uploadRequest(t, "a.txt", "abc"))
	if rec.Code != 503 {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestSendDeadline(t *testing.T) {
	cli, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_TEST_BLOCK", "1")
	if err := send(cli, validMessage(), 50*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
}

func TestPrepareSpoolDiscardsStaleFiles(t *testing.T) {
	// A unique address isolates this test from any running gateway.
	address := t.TempDir()
	dir, err := prepareSpool(address)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.WriteFile(filepath.Join(dir, "stale"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	again, err := prepareSpool(address)
	if err != nil || again != dir {
		t.Fatalf("prepare: %s %v", again, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("stale uploads: %v %v", entries, err)
	}
}
