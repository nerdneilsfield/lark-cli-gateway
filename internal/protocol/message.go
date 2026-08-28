// Package protocol defines the wire contract shared by the lark gateway
// server and its command-line client.
package protocol

// Message is a notification payload accepted by POST /send.
type Message struct {
	ChatID  string `json:"chat_id"`
	As      string `json:"as"`
	Type    string `json:"type"`
	Content string `json:"content"`
}
