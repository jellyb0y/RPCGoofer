package ws

import (
	"github.com/gorilla/websocket"
)

// MessageType represents WebSocket message types
type MessageType int

const (
	TextMessage   MessageType = websocket.TextMessage
	BinaryMessage MessageType = websocket.BinaryMessage
	CloseMessage  MessageType = websocket.CloseMessage
	PingMessage   MessageType = websocket.PingMessage
	PongMessage   MessageType = websocket.PongMessage
)

// Message represents a WebSocket message
type Message struct {
	Type MessageType
	Data []byte
}

// NewTextMessage creates a new text message
func NewTextMessage(data []byte) *Message {
	return &Message{
		Type: TextMessage,
		Data: data,
	}
}
