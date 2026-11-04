package sync

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Clash-Projects/lastwave-jam/internal/protocol"
)

var (
	ErrRoomNotFound = errors.New("room not found")
)

// Clean charset for readable room codes (omits 0, O, 1, I to prevent confusion).
const roomCodeCharset = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// Hub manages active listening rooms and facilitates global lifecycle events.
type Hub struct {
	mu      sync.RWMutex
	rooms   map[string]*Room
	archive []*ArchivedRoom // ended rooms, kept 24 h for the admin panel
}

// NewHub constructs a new centralized room registry.
func NewHub() *Hub {
	return &Hub{
		rooms: make(map[string]*Room),
	}
}

// PermanentRooms are the always-active rooms every server provides.
var PermanentRooms = []struct{ Code, Name, Key string }{
	{"INDIA", "India", "india"},
	{"WORLD", "International", "international"},
}

// RoomInfo is the public summary of an always-active room.
type RoomInfo struct {
	Code         string `json:"code"`
	Name         string `json:"name"`
	Participants int    `json:"participants"`
}

// PublicRooms lists the always-active rooms with live listener counts.
func (h *Hub) PublicRooms() []RoomInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]RoomInfo, 0, len(PermanentRooms))
	for _, pr := range PermanentRooms {
		if room, ok := h.rooms[pr.Code]; ok {
			out = append(out, RoomInfo{Code: pr.Code, Name: pr.Name, Participants: room.ParticipantCount()})
		}
	}
	return out
}

// EnsurePermanentRooms provisions the always-active rooms (INDIA, WORLD) as
// 24/7 radio stations: no host, nobody can control playback (see radio.go).
func (h *Hub) EnsurePermanentRooms() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, pr := range PermanentRooms {
		if _, exists := h.rooms[pr.Code]; exists {
			continue
		}
		room := NewRoom(pr.Code, nil, protocol.RoomModeHostOnly)
		room.Permanent = true
		room.Radio = true
		room.RadioKey = pr.Key
		room.Name = pr.Name
		h.rooms[pr.Code] = room
	}
}

// GenerateRoomCode generates a random 6-character alphanumeric room code.
func (h *Hub) GenerateRoomCode() string {
	b := make([]byte, 6)
	charLen := len(roomCodeCharset)
	for {
		randBytes := make([]byte, 6)
		_, _ = rand.Read(randBytes)
		for i := 0; i < 6; i++ {
			b[i] = roomCodeCharset[int(randBytes[i])%charLen]
		}
		code := string(b)
		h.mu.RLock()
		_, exists := h.rooms[code]
		h.mu.RUnlock()
		if !exists {
			return code
		}
	}
}

// CreateRoom validates the host's display name, provisions a room, and confirms creation.
func (h *Hub) CreateRoom(c *Client, name string, mode string) (*Room, error) {
	validName, err := protocol.ValidateName(name)
	if err != nil {
		c.SendError("invalid_name", "Display name is required before entering a room (1-32 chars)")
		return nil, err
	}

	c.SetName(validName)
	code := h.GenerateRoomCode()
	room := NewRoom(code, c, mode)

	h.mu.Lock()
	h.rooms[code] = room
	h.mu.Unlock()

	slog.Info("room created", "room_code", code, "host_id", c.ID, "host_name", validName, "mode", room.Mode)

	// Send confirmation envelope to room host
	_ = c.Send(protocol.MsgTypeRoomCreated, map[string]any{
		"room_code": code,
		"user_id":   c.ID,
		"name":      validName,
		"is_host":   true,
		"room_mode": room.Mode,
	})

	return room, nil
}

// JoinRoom routes a client into an existing room identified by its room code.
func (h *Hub) JoinRoom(c *Client, rawCode string, rawName string) error {
	code := strings.ToUpper(strings.TrimSpace(rawCode))
	if code == "" {
		c.SendError("missing_room_code", "Room code is required")
		return errors.New("missing room code")
	}

	h.mu.RLock()
	room, exists := h.rooms[code]
	h.mu.RUnlock()

	if !exists {
		c.SendError("room_not_found", fmt.Sprintf("Room %s does not exist or has expired", code))
		return ErrRoomNotFound
	}

	return room.Join(c, rawName)
}

// GetRoom retrieves a room by its code if active.
func (h *Hub) GetRoom(code string) (*Room, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	room, exists := h.rooms[strings.ToUpper(code)]
	return room, exists
}

// RemoveRoom evicts a room from the registry.
func (h *Hub) RemoveRoom(code string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	code = strings.ToUpper(code)
	room, ok := h.rooms[code]
	if !ok || room.Permanent {
		return
	}
	h.archiveLocked(room)
	delete(h.rooms, code)
}

// RoomCount returns the current number of active rooms.
func (h *Hub) RoomCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.rooms)
}

// TotalParticipants returns the cumulative number of participants across all rooms.
func (h *Hub) TotalParticipants() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	total := 0
	for _, r := range h.rooms {
		total += r.ParticipantCount()
	}
	return total
}

// RunCleanupLoop periodically purges stale rooms that have remained empty.
func (h *Hub) RunCleanupLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.cleanupOnce()
		}
	}
}
