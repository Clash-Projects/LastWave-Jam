package sync

import (
	"sync"
	"time"

	"github.com/Clash-Projects/lastwave-jam/internal/protocol"
)

// DefaultChatHistoryLimit is how many recent messages a joining app receives.
const DefaultChatHistoryLimit = 50

const (
	// ChatRetention: messages older than this are deleted everywhere.
	ChatRetention = 24 * time.Hour
	// chatHardCap bounds memory per room even for very busy rooms.
	chatHardCap = 5000
)

// ChatBuffer keeps a room's chat for 24 hours (bounded). Apps get the latest
// messages on join; the admin panel can read the whole day.
type ChatBuffer struct {
	mu       sync.RWMutex
	capacity int // messages handed to a joining app
	messages []protocol.ChatMessage
	now      func() time.Time
}

// NewChatBuffer creates a buffer that hands out up to `capacity` recent messages.
func NewChatBuffer(capacity int) *ChatBuffer {
	if capacity <= 0 {
		capacity = DefaultChatHistoryLimit
	}
	return &ChatBuffer{capacity: capacity, messages: make([]protocol.ChatMessage, 0, capacity), now: time.Now}
}

// pruneLocked drops messages past the retention window or over the hard cap.
func (b *ChatBuffer) pruneLocked() {
	cutoff := b.now().Add(-ChatRetention).UnixMilli()
	drop := 0
	for drop < len(b.messages) && b.messages[drop].Timestamp < cutoff {
		drop++
	}
	if over := len(b.messages) - drop - chatHardCap; over > 0 {
		drop += over
	}
	if drop > 0 {
		b.messages = append([]protocol.ChatMessage(nil), b.messages[drop:]...)
	}
}

// Add appends a message.
func (b *ChatBuffer) Add(msg protocol.ChatMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.messages = append(b.messages, msg)
	b.pruneLocked()
}

// History returns the latest messages for a joining app, oldest first.
func (b *ChatBuffer) History() []protocol.ChatMessage {
	all := b.All()
	if len(all) > b.capacity {
		all = all[len(all)-b.capacity:]
	}
	return all
}

// All returns every message from the last 24 hours, oldest first.
func (b *ChatBuffer) All() []protocol.ChatMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked()
	out := make([]protocol.ChatMessage, len(b.messages))
	copy(out, b.messages)
	return out
}

// Get returns the stored message with the given ID, if it is still kept.
func (b *ChatBuffer) Get(id string) (protocol.ChatMessage, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for i := len(b.messages) - 1; i >= 0; i-- {
		if b.messages[i].ID == id {
			return b.messages[i], true
		}
	}
	return protocol.ChatMessage{}, false
}
