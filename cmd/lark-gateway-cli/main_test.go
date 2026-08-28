package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nerdneilsfield/lark-cli-gateway/internal/protocol"
)

type requestRecorder struct {
	requests atomic.Int32
	method   string
	path     string
	ct       string
	msg      protocol.Message
}

func startRecorder(t *testing.T, status int, body string) (*httptest.Server, *requestRecorder) {
	t.Helper()
	r := &requestRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.requests.Add(1)
		r.method = req.Method
		r.path = req.URL.Path
		r.ct = req.Header.Get("Content-Type")
		if err := json.NewDecoder(req.Body).Decode(&r.msg); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, r
}

func serverHostPort(t *testing.T, srv *httptest.Server) (string, string) {
	t.Helper()
	host, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func runCLI(t *testing.T, env map[string]string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := run(args, func(key string) string { return env[key] }, &http.Client{Timeout: 5 * time.Second}, &out)
	return out.String(), err
}

func TestRunSendsExactMessage(t *testing.T) {
	srv, r := startRecorder(t, http.StatusOK, `{"ok":true}`)
	host, port := serverHostPort(t, srv)

	out, err := runCLI(t, nil,
		"--host", host, "--port", port,
		"send-msg", "--chat-id", "oc_test", "--as", "user", "--text", "hello",
	)
	if err != nil {
		t.Fatalf("run error = %v", err)
	}
	if out != `{"ok":true}` {
		t.Fatalf("stdout = %q, want %q", out, `{"ok":true}`)
	}
	if got := r.requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
	if r.method != http.MethodPost || r.path != "/send" || r.ct != "application/json" {
		t.Fatalf("request = %s %s ct=%q, want POST /send application/json", r.method, r.path, r.ct)
	}
	want := protocol.Message{ChatID: "oc_test", As: "user", Type: "text", Content: "hello"}
	if r.msg != want {
		t.Fatalf("message = %+v, want %+v", r.msg, want)
	}
}

func TestRunUsesEnvDefaults(t *testing.T) {
	srv, r := startRecorder(t, http.StatusOK, `{"ok":true}`)
	host, port := serverHostPort(t, srv)

	out, err := runCLI(t, map[string]string{
		"LARK_GATEWAY_HOST": host,
		"LARK_GATEWAY_PORT": port,
		"LARK_CHAT_ID":      "oc_env",
	}, "send-msg", "--markdown", "hello")
	if err != nil {
		t.Fatalf("run error = %v", err)
	}
	if out != `{"ok":true}` {
		t.Fatalf("stdout = %q, want %q", out, `{"ok":true}`)
	}
	want := protocol.Message{ChatID: "oc_env", As: "bot", Type: "markdown", Content: "hello"}
	if r.msg != want {
		t.Fatalf("message = %+v, want %+v", r.msg, want)
	}
}

func TestRunExplicitFlagsOverrideEnv(t *testing.T) {
	srv, r := startRecorder(t, http.StatusOK, `{"ok":true}`)
	host, port := serverHostPort(t, srv)

	out, err := runCLI(t, map[string]string{
		"LARK_GATEWAY_HOST": "no.such.host",
		"LARK_GATEWAY_PORT": "1",
		"LARK_CHAT_ID":      "oc_bad",
	}, "--host", host, "--port", port,
		"send-msg", "--chat-id", "oc_override", "--as", "user", "--text", "hello")
	if err != nil {
		t.Fatalf("run error = %v", err)
	}
	if out != `{"ok":true}` {
		t.Fatalf("stdout = %q, want %q", out, `{"ok":true}`)
	}
	want := protocol.Message{ChatID: "oc_override", As: "user", Type: "text", Content: "hello"}
	if r.msg != want {
		t.Fatalf("message = %+v, want %+v", r.msg, want)
	}
}

func TestRunRejectsInvalidInputBeforeRequest(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{
			"both text and markdown",
			nil,
			[]string{"send-msg", "--chat-id", "c", "--text", "a", "--markdown", "b"},
			"exactly one of --text or --markdown is required",
		},
		{
			"neither text nor markdown",
			nil,
			[]string{"send-msg", "--chat-id", "c"},
			"exactly one of --text or --markdown is required",
		},
		{
			"invalid as",
			nil,
			[]string{"send-msg", "--chat-id", "c", "--as", "admin", "--text", "a"},
			"as must be user or bot",
		},
		{
			"port out of range",
			nil,
			[]string{"--port", "70000", "send-msg", "--chat-id", "c", "--text", "a"},
			"port must be between 1 and 65535",
		},
		{
			"port not a number",
			nil,
			[]string{"--port", "abc", "send-msg", "--chat-id", "c", "--text", "a"},
			"port must be between 1 and 65535",
		},
		{
			"missing chat id",
			nil,
			[]string{"send-msg", "--text", "a"},
			"chat-id is required (use --chat-id or LARK_CHAT_ID)",
		},
		{
			"missing subcommand",
			nil,
			[]string{"--host", "127.0.0.1"},
			"expected subcommand: send-msg",
		},
		{
			"unknown subcommand",
			nil,
			[]string{"foobar"},
			"unknown subcommand: foobar",
		},
		{
			"extra positional args",
			nil,
			[]string{"send-msg", "--chat-id", "c", "--text", "a", "extra"},
			"send-msg does not accept positional arguments",
		},
		{
			"host with scheme",
			nil,
			[]string{"--host", "http://example.com", "send-msg", "--chat-id", "c", "--text", "a"},
			"host must not include a scheme",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, r := startRecorder(t, http.StatusOK, `{"ok":true}`)
			_, err := runCLI(t, tc.env, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want contains %q", err, tc.want)
			}
			if got := r.requests.Load(); got != 0 {
				t.Fatalf("requests = %d, want 0", got)
			}
		})
	}
}

func TestRunGatewayErrorLeavesStdoutEmpty(t *testing.T) {
	srv, _ := startRecorder(t, http.StatusServiceUnavailable, `{"error":"queue full"}`)
	host, port := serverHostPort(t, srv)

	out, err := runCLI(t, nil, "--host", host, "--port", port,
		"send-msg", "--chat-id", "c", "--text", "a")
	if err == nil {
		t.Fatal("run error = nil, want gateway error")
	}
	want := `gateway returned 503 Service Unavailable: {"error":"queue full"}`
	if err.Error() != want {
		t.Fatalf("err = %q, want %q", err.Error(), want)
	}
	if out != "" {
		t.Fatalf("stdout = %q, want empty", out)
	}
}
