package sync

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Clash-Projects/lastwave-jam/internal/protocol"
)

func drain(c *Client) []protocol.Envelope {
	var out []protocol.Envelope
	for {
		select {
		case msg := <-c.send:
			var env protocol.Envelope
			_ = json.Unmarshal(msg, &env)
			out = append(out, env)
		default:
			return out
		}
	}
}

func lastType(envs []protocol.Envelope, t string) *protocol.Envelope {
	for i := len(envs) - 1; i >= 0; i-- {
		if envs[i].Type == t {
			return &envs[i]
		}
	}
	return nil
}

func hasType(envs []protocol.Envelope, t string) *protocol.Envelope {
	for i := range envs {
		if envs[i].Type == t {
			return &envs[i]
		}
	}
	return nil
}

func TestHub_PermanentRoomsExistAndSurviveCleanup(t *testing.T) {
	hub := NewHub()
	hub.EnsurePermanentRooms()
	hub.EnsurePermanentRooms() // idempotent
	if hub.RoomCount() != 2 {
		t.Fatalf("expected 2 rooms, got %d", hub.RoomCount())
	}
	for _, code := range []string{"INDIA", "WORLD", "india"} {
		if _, ok := hub.GetRoom(code); !ok {
			t.Fatalf("room %s missing", code)
		}
	}
	hub.RemoveRoom("INDIA")
	if hub.RoomCount() != 2 {
		t.Fatal("permanent room must not be removable")
	}
}

func TestPermanentRoom_FirstJoinerIsHostAndLastJoinerSucceeds(t *testing.T) {
	hub := NewHub()
	hub.EnsurePermanentRooms()
	room, _ := hub.GetRoom("INDIA")
	room.Radio = false // test the non-radio permanent-room behaviour
	room.Mode = protocol.RoomModeCollaborative

	a, b, c := mockClient("a", "A"), mockClient("b", "B"), mockClient("c", "C")
	_ = room.Join(a, "A")
	_ = room.Join(b, "B")
	_ = room.Join(c, "C")
	if room.HostID() != "a" {
		t.Fatalf("first joiner should be host, got %s", room.HostID())
	}
	room.Leave(a)
	if room.HostID() != "c" {
		t.Fatalf("most recent joiner should become host, got %s", room.HostID())
	}
	room.Leave(c)
	room.Leave(b)
	if room.HostID() != "" || !room.IsEmpty() {
		t.Fatal("room should be empty without host")
	}
	d := mockClient("d", "D")
	_ = room.Join(d, "D")
	if room.HostID() != "d" {
		t.Fatal("next first joiner should be host again")
	}
}

func TestPermanentRoom_Cooldown(t *testing.T) {
	hub := NewHub()
	hub.EnsurePermanentRooms()
	room, _ := hub.GetRoom("WORLD")
	room.Radio = false // test the non-radio permanent-room behaviour
	room.Mode = protocol.RoomModeCollaborative
	clock := time.Unix(1_700_000_000, 0)
	room.now = func() time.Time { return clock }

	a, b := mockClient("a", "A"), mockClient("b", "B")
	_ = room.Join(a, "A")
	_ = room.Join(b, "B")
	drain(a)
	drain(b)

	track := &protocol.TrackInfo{ID: "t1", Title: "Song", Artist: "X"}
	room.HandlePlayback(b, protocol.PlaybackActionPayload{Action: protocol.ActionChangeTrack, Track: track, IsPlaying: true})
	drain(b)

	// Second limited command within a minute is rejected with retry info.
	room.HandlePlayback(b, protocol.PlaybackActionPayload{Action: protocol.ActionPause})
	envs := drain(b)
	errEnv := hasType(envs, protocol.MsgTypeError)
	if errEnv == nil {
		t.Fatal("expected rate_limited error")
	}
	var ep protocol.ErrorPayload
	_ = json.Unmarshal(errEnv.Payload, &ep)
	if ep.Code != "rate_limited" || ep.RetryAfterMs <= 0 || ep.RetryAtMs <= 0 {
		t.Fatalf("bad error payload: %+v", ep)
	}
	if hasType(envs, protocol.MsgTypeSyncPlayback) == nil {
		t.Fatal("rejected sender should be resynced")
	}

	// Another user has their own allowance.
	room.HandlePlayback(a, protocol.PlaybackActionPayload{Action: protocol.ActionPause})
	if hasType(drain(a), protocol.MsgTypeError) != nil {
		t.Fatal("other user must not be limited")
	}

	// Play is never limited, even right after a limited command.
	room.HandlePlayback(a, protocol.PlaybackActionPayload{Action: protocol.ActionPlay})
	if hasType(drain(a), protocol.MsgTypeError) != nil {
		t.Fatal("play must not be rate limited")
	}

	// After the window, the command is allowed again.
	clock = clock.Add(CooldownWindow + time.Second)
	room.HandlePlayback(b, protocol.PlaybackActionPayload{Action: protocol.ActionPause})
	if hasType(drain(b), protocol.MsgTypeError) != nil {
		t.Fatal("cooldown should have expired")
	}
}

func TestNormalRoom_NoCooldown(t *testing.T) {
	host := mockClient("h", "H")
	room := NewRoom("ABC234", host, protocol.RoomModeHostOnly)
	drain(host)
	room.HandlePlayback(host, protocol.PlaybackActionPayload{Action: protocol.ActionPause})
	room.HandlePlayback(host, protocol.PlaybackActionPayload{Action: protocol.ActionPause})
	if hasType(drain(host), protocol.MsgTypeError) != nil {
		t.Fatal("normal rooms must not rate limit")
	}
}

func TestCoHost_GrantRevokeAndLimits(t *testing.T) {
	host, g1, g2 := mockClient("h", "H"), mockClient("g1", "G1"), mockClient("g2", "G2")
	room := NewRoom("ABC234", host, protocol.RoomModeHostOnly)
	_ = room.Join(g1, "G1")
	_ = room.Join(g2, "G2")
	drain(host)
	drain(g1)
	drain(g2)

	// Guests cannot grant.
	room.GrantCoHost(g1, "g2")
	if hasType(drain(g1), protocol.MsgTypeError) == nil {
		t.Fatal("guest must not grant host")
	}

	room.GrantCoHost(host, "g1")
	if room.CoHostID() != "g1" || !room.IsHost(g1) {
		t.Fatal("g1 should be co-host")
	}
	drain(g2)

	// At most two hosts.
	room.GrantCoHost(host, "g2")
	if hasType(drain(host), protocol.MsgTypeError) == nil {
		t.Fatal("third host must be rejected")
	}

	// Co-host controls playback in host-only mode but cannot revoke.
	room.HandlePlayback(g1, protocol.PlaybackActionPayload{Action: protocol.ActionPlay})
	if hasType(drain(g1), protocol.MsgTypeError) != nil {
		t.Fatal("co-host should control playback")
	}
	room.RevokeCoHost(g1, "g1")
	if hasType(drain(g1), protocol.MsgTypeError) == nil {
		t.Fatal("co-host must not strip hosts")
	}

	room.RevokeCoHost(host, "g1")
	if room.CoHostID() != "" || room.IsHost(g1) {
		t.Fatal("co-host should be stripped")
	}

	// Co-host takes over when the main host leaves.
	room.GrantCoHost(host, "g2")
	room.Leave(host)
	if room.HostID() != "g2" || room.CoHostID() != "" {
		t.Fatalf("co-host should be promoted, host=%s co=%s", room.HostID(), room.CoHostID())
	}
}

func TestChat_ReplySnapshot(t *testing.T) {
	host, g := mockClient("h", "H"), mockClient("g", "G")
	room := NewRoom("ABC234", host, protocol.RoomModeCollaborative)
	_ = room.Join(g, "G")
	drain(host)
	drain(g)

	room.HandleChat(host, "original message", "")
	first := hasType(drain(g), protocol.MsgTypeChatMessage)
	var orig protocol.ChatMessage
	_ = json.Unmarshal(first.Payload, &orig)

	room.HandleChat(g, "a reply", orig.ID)
	second := lastType(drain(host), protocol.MsgTypeChatMessage)
	var reply protocol.ChatMessage
	_ = json.Unmarshal(second.Payload, &reply)
	if reply.ReplyTo == nil || reply.ReplyTo.ID != orig.ID || reply.ReplyTo.Text != "original message" || reply.ReplyTo.UserName != "H" {
		t.Fatalf("bad reply snapshot: %+v", reply.ReplyTo)
	}

	room.HandleChat(g, "dangling", "msg_unknown")
	third := lastType(drain(host), protocol.MsgTypeChatMessage)
	var dangling protocol.ChatMessage
	_ = json.Unmarshal(third.Payload, &dangling)
	if dangling.ReplyTo != nil {
		t.Fatal("unknown reply target must be ignored")
	}
}

func TestPermanentRoom_RejectsDefaultName(t *testing.T) {
	hub := NewHub()
	hub.EnsurePermanentRooms()
	room, _ := hub.GetRoom("INDIA")
	c := mockClient("a", "")
	if err := room.Join(c, " Listener "); err == nil {
		t.Fatal("default name must be rejected in a public room")
	}
	if !room.IsEmpty() {
		t.Fatal("rejected user must not be in the room")
	}
	if hasType(drain(c), protocol.MsgTypeError) == nil {
		t.Fatal("expected error")
	}
	if err := room.Join(c, "Aarav"); err != nil {
		t.Fatalf("custom name should join: %v", err)
	}
	// Normal rooms still allow the default name.
	h := mockClient("h", "H")
	normal := NewRoom("ABC234", h, protocol.RoomModeCollaborative)
	if err := normal.Join(mockClient("g", ""), "Listener"); err != nil {
		t.Fatal("normal rooms must allow default name")
	}
}

func TestQueue_AdminSharesReadOnly(t *testing.T) {
	host, g := mockClient("h", "H"), mockClient("g", "G")
	room := NewRoom("ABC234", host, protocol.RoomModeCollaborative)
	_ = room.Join(g, "G")
	drain(host)
	drain(g)

	tr := []protocol.TrackInfo{{ID: "a", Title: "A", Artist: "x"}, {ID: "b", Title: "B", Artist: "x"}, {ID: "bad"}}
	room.HandleQueueUpdate(g, tr) // guest: ignored
	if hasType(drain(host), protocol.MsgTypeQueueUpdated) != nil {
		t.Fatal("guest must not publish the queue")
	}
	room.HandleQueueUpdate(host, tr)
	env := hasType(drain(g), protocol.MsgTypeQueueUpdated)
	if env == nil {
		t.Fatal("guest should receive queue")
	}
	var p protocol.QueueUpdatePayload
	_ = json.Unmarshal(env.Payload, &p)
	if len(p.Queue) != 2 {
		t.Fatalf("invalid tracks must be dropped, got %d", len(p.Queue))
	}
	// Joiners get it in sync_state.
	n := mockClient("n", "N")
	_ = room.Join(n, "N")
	var ss protocol.SyncStatePayload
	_ = json.Unmarshal(hasType(drain(n), protocol.MsgTypeSyncState).Payload, &ss)
	if len(ss.Playback.Queue) != 2 {
		t.Fatal("sync_state should include shared queue")
	}
	// Admin leaves: stale queue is cleared for the next admin.
	room.Leave(host)
	if len(room.state.Queue) != 0 {
		t.Fatal("queue should clear on admin change")
	}
}

func TestMsgLimiter_BurstThenThrottle(t *testing.T) {
	l := newMsgLimiter()
	now := time.Now()
	allowed := 0
	for i := 0; i < 100; i++ {
		if l.allow(now) {
			allowed++
		}
	}
	if allowed != int(msgBurst) {
		t.Fatalf("burst should allow %d, got %d", int(msgBurst), allowed)
	}
	if !l.allow(now.Add(time.Second)) {
		t.Fatal("tokens should refill over time")
	}
}

func TestHub_PublicRoomsCountsAndCooldownMessage(t *testing.T) {
	hub := NewHub()
	hub.EnsurePermanentRooms()
	room, _ := hub.GetRoom("INDIA")
	room.Radio = false // test the non-radio permanent-room behaviour
	room.Mode = protocol.RoomModeCollaborative
	a := mockClient("a", "A")
	_ = room.Join(a, "Aarav")
	infos := hub.PublicRooms()
	if len(infos) != 2 || infos[0].Code != "INDIA" || infos[0].Name != "India" || infos[0].Participants != 1 {
		t.Fatalf("bad public rooms: %+v", infos)
	}
	var ss protocol.SyncStatePayload
	_ = json.Unmarshal(hasType(drain(a), protocol.MsgTypeSyncState).Payload, &ss)
	if ss.RoomName != "India" {
		t.Fatalf("sync_state should carry room_name, got %q", ss.RoomName)
	}
	room.HandlePlayback(a, protocol.PlaybackActionPayload{Action: protocol.ActionPause})
	if hasType(drain(a), protocol.MsgTypeCooldown) == nil {
		t.Fatal("expected cooldown message after a limited command")
	}
}
