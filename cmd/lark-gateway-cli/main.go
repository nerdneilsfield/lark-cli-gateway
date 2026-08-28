// Command lark-gateway-cli sends messages to a lark-gateway-server over
// POST /send using the shared protocol.Message wire contract.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nerdneilsfield/lark-cli-gateway/internal/protocol"
)

// run parses argv, resolves config (explicit flag > environment > fallback),
// and posts the message to the gateway. Everything except the gateway URL
// and the HTTP client is injectable for tests.
func run(args []string, getenv func(string) string, client *http.Client, stdout io.Writer) error {
	fs := flag.NewFlagSet("lark-gateway-cli", flag.ContinueOnError)
	hostFlag := fs.String("host", "", "gateway host (default $LARK_GATEWAY_HOST or 127.0.0.1)")
	portFlag := fs.String("port", "", "gateway port (default $LARK_GATEWAY_PORT or 19090)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() == 0 {
		return errors.New("expected subcommand: send-msg")
	}
	sub := fs.Arg(0)
	if sub != "send-msg" {
		return fmt.Errorf("unknown subcommand: %s", sub)
	}

	sf := flag.NewFlagSet("send-msg", flag.ContinueOnError)
	chatIDFlag := sf.String("chat-id", "", "chat id (default $LARK_CHAT_ID)")
	asFlag := sf.String("as", "bot", "send as user or bot")
	textFlag := sf.String("text", "", "plain text content")
	markdownFlag := sf.String("markdown", "", "markdown content")
	if err := sf.Parse(fs.Args()[1:]); err != nil {
		return err
	}
	if sf.NArg() > 0 {
		return errors.New("send-msg does not accept positional arguments")
	}

	chatID := *chatIDFlag
	if chatID == "" {
		chatID = getenv("LARK_CHAT_ID")
	}
	if chatID == "" {
		return errors.New("chat-id is required (use --chat-id or LARK_CHAT_ID)")
	}

	host := *hostFlag
	if host == "" {
		host = getenv("LARK_GATEWAY_HOST")
	}
	if host == "" {
		host = "127.0.0.1"
	}
	if strings.Contains(host, "://") {
		return errors.New("host must not include a scheme")
	}

	portStr := *portFlag
	if portStr == "" {
		portStr = getenv("LARK_GATEWAY_PORT")
	}
	if portStr == "" {
		portStr = "19090"
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}

	if *asFlag != "user" && *asFlag != "bot" {
		return errors.New("as must be user or bot")
	}

	if (*textFlag == "") == (*markdownFlag == "") {
		return errors.New("exactly one of --text or --markdown is required")
	}

	msg := protocol.Message{ChatID: chatID, As: *asFlag}
	if *textFlag != "" {
		msg.Type = "text"
		msg.Content = *textFlag
	} else {
		msg.Type = "markdown"
		msg.Content = *markdownFlag
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("encode message: %w", err)
	}

	endpoint := url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		Path:   "/send",
	}
	resp, err := client.Post(endpoint.String(), "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("post to gateway: %w", err)
	}
	respBody, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil {
		return fmt.Errorf("read response body: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close response body: %w", closeErr)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	if _, err := stdout.Write(respBody); err != nil {
		return fmt.Errorf("write stdout: %w", err)
	}
	return nil
}

func main() {
	err := run(os.Args[1:], os.Getenv, &http.Client{Timeout: 5 * time.Second}, os.Stdout)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
