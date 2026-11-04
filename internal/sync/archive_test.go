package sync

import (
	"fmt"
	"testing"
	"time"

	"github.com/Clash-Projects/lastwave-jam/internal/protocol"
)

func TestChatBuffer_KeepsADayButAppsGetTheLatest(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	buf := NewChatBuffer(50)
	buf.now = func() time.Time { return clock }
	old := clock.Add(-25 * time.Hour).UnixMilli()
	buf.Add(protocol.ChatMessage{ID: "old", Timestamp: old})
	for i := 0; i < 120; i++ {
		buf.Add(protocol.ChatMessage{ID: fmt.Sprintf("m%d", i), Timestamp: clock.Add(-time.Duration(120-i) * time.Minute).UnixMilli()})
	}
	all := buf.All()
	if len(all) != 120 || all[0].ID == "old" {
		t.Fatalf("admin view should hold the whole last day (120), got %d", len(all))
	}
	if h := buf.History(); len(h) != 50 || h[49].ID != "m119" {
		t.Fatalf("apps should get the latest 50, got %d", len(h))
	}
	// A day later everything is gone.
	clock = clock.Add(25 * time.Hour)
	if len(buf.All()) != 0 {
		t.Fatal("messages older than 24 h must be deleted")
	}
}

func TestArchive_EndedRoomsKeptForADay(t *testing.T) {
	hub := NewHub()
	hub.EnsurePermanentRooms()
	host := mockClient("h", "Host")
	room, _ := hub.CreateRoom(host, "Host", protocol.RoomModeCollaborative)
	guest := mockClient("g", "Guest")
	_ = room.Join(guest, "Guest")
	room.HandleChat(guest, "bye everyone", "")
	room.Leave(guest)
	room.Leave(host)

	hub.cleanupOnce()
	if _, live := hub.GetRoom(room.Code); live {
		t.Fatal("empty room should be removed")
	}
	if _, live := hub.GetRoom("INDIA"); !live {
		t.Fatal("stations are never removed")
	}
	ended := hub.ArchivedRooms()
	if len(ended) != 1 || ended[0].Room.Code != room.Code || ended[0].ChatCount != 1 {
		t.Fatalf("ended room should be in the history: %+v", ended)
	}
	if len(ended[0].Room.Seen) != 2 {
		t.Fatalf("history should remember who was there: %v", ended[0].Room.Seen)
	}
	msgs, ok := hub.ArchivedChat(ended[0].ID)
	if !ok || len(msgs) != 1 || msgs[0].Text != "bye everyone" {
		t.Fatal("ended room chat should be readable")
	}

	// After 24 hours the history is gone.
	hub.mu.Lock()
	hub.archive[0].EndedAt = time.Now().Add(-25 * time.Hour).UnixMilli()
	hub.mu.Unlock()
	if len(hub.ArchivedRooms()) != 0 {
		t.Fatal("history older than 24 h must be deleted")
	}
	if _, ok := hub.ArchivedChat(ended[0].ID); ok {
		t.Fatal("expired chat must not be readable")
	}
}
