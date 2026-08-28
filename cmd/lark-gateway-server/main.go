// Command lark-gateway-server is a localhost HTTP gateway that relays
// notifications to the official lark-cli over a best-effort FIFO queue.
package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"os/exec"
	"time"

	"github.com/nerdneilsfield/lark-cli-gateway/internal/protocol"
)

// handleSend returns the POST /send handler. It decodes the request body,
// validates it, and enqueues the message without blocking; a full queue is
// rejected with 503.
func handleSend(queue chan<- protocol.Message) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dec := json.NewDecoder(r.Body)
		var msg protocol.Message
		if err := dec.Decode(&msg); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if err := dec.Decode(&struct{}{}); err != io.EOF {
			http.Error(w, "body must contain exactly one JSON value", http.StatusBadRequest)
			return
		}
		if msg.ChatID == "" || msg.As == "" || msg.Type == "" || msg.Content == "" {
			http.Error(w, "chat_id, as, type and content are required", http.StatusBadRequest)
			return
		}
		if msg.As != "user" && msg.As != "bot" {
			http.Error(w, "as must be user or bot", http.StatusBadRequest)
			return
		}
		if msg.Type != "text" && msg.Type != "markdown" {
			http.Error(w, "type must be text or markdown", http.StatusBadRequest)
			return
		}
		select {
		case queue <- msg:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if _, err := io.WriteString(w, `{"ok":true}`); err != nil {
				log.Printf("write response: %v", err)
			}
		default:
			http.Error(w, "queue full", http.StatusServiceUnavailable)
		}
	}
}

// send invokes lark-cli directly with the argv verified from
// `lark-cli im +messages-send --help`; it never goes through a shell and
// never rewrites the content.
func send(cli string, msg protocol.Message) error {
	args := []string{"im", "+messages-send", "--chat-id", msg.ChatID, "--as", msg.As}
	if msg.Type == "text" {
		args = append(args, "--text", msg.Content)
	} else {
		args = append(args, "--markdown", msg.Content)
	}
	return exec.Command(cli, args...).Run()
}

// sendWithRetry sends immediately, then retries synchronously at a fixed
// interval while budget remains. retries means extra attempts beyond the
// first (retries=2 => at most 3 calls). Final failures are logged and the
// message dropped.
func sendWithRetry(msg protocol.Message, retries int, retryInterval time.Duration, sendFn func(protocol.Message) error) {
	err := sendFn(msg)
	for attempts := 1; err != nil && attempts <= retries; attempts++ {
		time.Sleep(retryInterval)
		err = sendFn(msg)
	}
	if err != nil {
		log.Printf("send failed after %d retries: %v", retries, err)
	}
}

// worker consumes the queue one message at a time, in FIFO order, sleeping
// sendInterval after each message completes. Retries block the worker by
// design.
func worker(queue <-chan protocol.Message, sendInterval, retryInterval time.Duration, retries int, sendFn func(protocol.Message) error) {
	for msg := range queue {
		sendWithRetry(msg, retries, retryInterval, sendFn)
		time.Sleep(sendInterval)
	}
}

func main() {
	listen := flag.String("listen", "127.0.0.1:19090", "HTTP listen address")
	queueSize := flag.Int("queue-size", 100, "buffered queue capacity")
	interval := flag.Duration("interval", time.Second, "sleep between messages")
	retries := flag.Int("retries", 2, "extra retries per message")
	retryInterval := flag.Duration("retry-interval", 2*time.Second, "sleep between retries")
	larkCLI := flag.String("lark-cli", "lark-cli", "path to the lark-cli binary")
	flag.Parse()

	if *queueSize <= 0 {
		log.Fatal("queue-size must be greater than 0")
	}
	if *retries < 0 {
		log.Fatal("retries must not be negative")
	}
	if *interval < 0 {
		log.Fatal("interval must not be negative")
	}
	if *retryInterval < 0 {
		log.Fatal("retry-interval must not be negative")
	}

	queue := make(chan protocol.Message, *queueSize)
	go worker(queue, *interval, *retryInterval, *retries, func(m protocol.Message) error {
		return send(*larkCLI, m)
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /send", handleSend(queue))
	log.Fatal(http.ListenAndServe(*listen, mux))
}
