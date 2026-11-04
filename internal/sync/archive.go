package sync

import (
	"sort"
	"time"

	"github.com/Clash-Projects/lastwave-jam/internal/protocol"
)

// Ended rooms are kept for 24 hours for the admin panel (room details, who was
// there, and the chat), then deleted. Memory only: a server restart clears it.

const (
	archiveRetention = 24 * time.Hour
	maxSeenNames     = 200
)

// ArchivedRoom is an ended Jam as shown in the admin panel.
type ArchivedRoom struct {
	ID        string   `json:"id"`
	Room      RoomView `json:"room"`
	EndedAt   int64    `json:"endedAt"`
	ChatCount int      `json:"chatCount"`
	chat      []protocol.ChatMessage
}

// noteSeenLocked remembers a listener's name for the room's history. r.mu held.
func (r *Room) noteSeenLocked(name string) {
	for _, n := range r.seen {
		if n == name {
			return
		}
	}
	if len(r.seen) < maxSeenNames {
		r.seen = append(r.seen, name)
	}
}

// ChatAll returns the room's chat from the last 24 hours (admin panel).
func (r *Room) ChatAll() []protocol.ChatMessage {
	return r.chat.All()
}

// archiveLocked saves a room that is about to be removed. h.mu held.
func (h *Hub) archiveLocked(room *Room) {
	view := room.View()
	chat := room.chat.All()
	h.archive = append(h.archive, &ArchivedRoom{
		ID:        GenerateID("arc_"),
		Room:      view,
		EndedAt:   time.Now().UnixMilli(),
		ChatCount: len(chat),
		chat:      chat,
	})
}

// pruneArchiveLocked drops ended rooms and messages older than 24 hours. h.mu held.
func (h *Hub) pruneArchiveLocked(now time.Time) {
	cutoff := now.Add(-archiveRetention).UnixMilli()
	kept := h.archive[:0]
	for _, a := range h.archive {
		if a.EndedAt < cutoff {
			continue
		}
		msgs := a.chat[:0]
		for _, m := range a.chat {
			if m.Timestamp >= cutoff {
				msgs = append(msgs, m)
			}
		}
		a.chat = msgs
		a.ChatCount = len(msgs)
		kept = append(kept, a)
	}
	for i := len(kept); i < len(h.archive); i++ {
		h.archive[i] = nil
	}
	h.archive = kept
}

// ArchivedRooms lists Jams that ended in the last 24 hours, newest first.
func (h *Hub) ArchivedRooms() []ArchivedRoom {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneArchiveLocked(time.Now())
	out := make([]ArchivedRoom, 0, len(h.archive))
	for _, a := range h.archive {
		out = append(out, ArchivedRoom{ID: a.ID, Room: a.Room, EndedAt: a.EndedAt, ChatCount: a.ChatCount})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EndedAt > out[j].EndedAt })
	return out
}

// ArchivedChat returns the chat of an ended Jam, if it is still kept.
func (h *Hub) ArchivedChat(id string) ([]protocol.ChatMessage, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneArchiveLocked(time.Now())
	for _, a := range h.archive {
		if a.ID == id {
			return append([]protocol.ChatMessage(nil), a.chat...), true
		}
	}
	return nil, false
}

// cleanupOnce removes empty, non-permanent rooms (archiving them first).
func (h *Hub) cleanupOnce() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for code, room := range h.rooms {
		if room.IsEmpty() && !room.Permanent {
			h.archiveLocked(room)
			delete(h.rooms, code)
		}
	}
	h.pruneArchiveLocked(time.Now())
}
