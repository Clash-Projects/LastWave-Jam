package sync

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Clash-Projects/lastwave-jam/internal/protocol"
)

func errCode(t *testing.T, envs []protocol.Envelope) string {
	t.Helper()
	e := hasType(envs, protocol.MsgTypeError)
	if e == nil {
		return ""
	}
	var ep protocol.ErrorPayload
	_ = json.Unmarshal(e.Payload, &ep)
	return ep.Code
}

func chatTexts(envs []protocol.Envelope) []string {
	var out []string
	for _, e := range envs {
		if e.Type == protocol.MsgTypeChatMessage {
			var m protocol.ChatMessage
			_ = json.Unmarshal(e.Payload, &m)
			out = append(out, m.UserName+": "+m.Text)
		}
	}
	return out
}

func stationRoom(t *testing.T, code string) (*Room, *Client, *Client, *Client) {
	t.Helper()
	hub := NewHub()
	hub.EnsurePermanentRooms()
	hub.SetRadioAdminCode(code)
	room, _ := hub.GetRoom("INDIA")
	a, b, c := mockClient("a", "A"), mockClient("b", "B"), mockClient("c", "C")
	_ = room.Join(a, "Aarav")
	_ = room.Join(b, "Bela")
	_ = room.Join(c, "Chirag")
	room.radioPlay(radioTrack("dQw4w9WgXcQ", "Live"), nil)
	drain(a)
	drain(b)
	drain(c)
	return room, a, b, c
}

func TestStation_CommandIsShownMaskedAndCodeNeverLeaks(t *testing.T) {
	room, a, b, _ := stationRoom(t, "secret-code-123")
	room.HandleChat(a, "/admin wrong-guess", "")
	ea, eb := drain(a), drain(b)
	if errCode(t, ea) != "wrong_code" {
		t.Fatal("wrong code should be refused")
	}
	lines := chatTexts(eb)
	if len(lines) != 1 || lines[0] != "Aarav: /admin ••••••" {
		t.Fatalf("others should see the masked command only, got %v", lines)
	}
	room.HandleChat(a, "/admin secret-code-123", "")
	for _, e := range drain(b) {
		if strings.Contains(string(e.Payload), "secret-code-123") {
			t.Fatal("the code must never be broadcast")
		}
	}
	for _, m := range room.ChatHistory() {
		if strings.Contains(m.Text, "secret") || strings.Contains(m.Text, "wrong-guess") {
			t.Fatal("the code must never be stored in chat history")
		}
	}
}

func TestStation_AdminTakesControlTransfersAndAutoReturns(t *testing.T) {
	room, a, b, c := stationRoom(t, "secret-code-123")

	room.HandleChat(a, "/admin secret-code-123", "")
	if room.HostID() != "a" || room.IsStationAuto() {
		t.Fatal("A should be the station admin with auto paused")
	}
	eb := drain(b)
	if hasType(eb, protocol.MsgTypeStationMode) == nil || hasType(eb, protocol.MsgTypeHostChanged) == nil {
		t.Fatal("everyone should be told the station is now run by an admin")
	}
	if got := chatTexts(eb); len(got) != 2 || !strings.HasPrefix(got[1], "Station: Aarav is now the station admin") {
		t.Fatalf("expected masked command + station announcement, got %v", got)
	}
	drain(a)

	// The auto radio cannot overwrite the admin's playback.
	if room.radioPlay(radioTrack("kJQP7kiw5Fk", "Next"), nil) {
		t.Fatal("radio must not play while an admin runs the station")
	}

	// Admin controls playback; listeners still cannot.
	room.HandlePlayback(a, protocol.PlaybackActionPayload{Action: protocol.ActionPause})
	if errCode(t, drain(a)) != "" {
		t.Fatal("the admin should control playback")
	}
	room.HandlePlayback(b, protocol.PlaybackActionPayload{Action: protocol.ActionSeek, PositionMs: 5000})
	if errCode(t, drain(b)) != "live_station" {
		t.Fatal("listeners must not seek")
	}

	// No co-hosts on stations; the admin hands admin over instead.
	room.GrantCoHost(a, "b")
	if errCode(t, drain(a)) != "not_available" || room.CoHostID() != "" {
		t.Fatal("co-hosts must be refused on stations")
	}
	room.TransferStationAdmin(b, "c")
	if errCode(t, drain(b)) != "unauthorized" {
		t.Fatal("only the admin can transfer")
	}
	room.TransferStationAdmin(a, "b")
	if room.HostID() != "b" || room.IsStationAuto() {
		t.Fatal("B should now be admin")
	}
	room.HandlePlayback(a, protocol.PlaybackActionPayload{Action: protocol.ActionPlay})
	if errCode(t, drain(a)) != "live_station" {
		t.Fatal("the previous admin loses control")
	}

	// Anyone with the code can put the station back on auto.
	drain(b)
	drain(c)
	room.HandleChat(c, "/auto secret-code-123", "")
	if room.HostID() != "" || !room.IsStationAuto() {
		t.Fatal("station should be back on auto with no admin")
	}
	if room.radioPlay(radioTrack("kJQP7kiw5Fk", "Next"), nil) == false {
		t.Fatal("auto radio should play again")
	}

	// Admin leaving also returns the station to auto.
	room.HandleChat(b, "/admin secret-code-123", "")
	drain(a)
	room.Leave(b)
	if room.HostID() != "" || !room.IsStationAuto() {
		t.Fatal("admin leaving must return the station to auto")
	}
	if hasType(drain(a), protocol.MsgTypeStationMode) == nil {
		t.Fatal("listeners should be told the station is back on auto")
	}
}

func TestStation_CommandsDisabledWithoutCodeAndBruteForceLimited(t *testing.T) {
	room, a, _, _ := stationRoom(t, "")
	room.HandleChat(a, "/admin anything", "")
	if errCode(t, drain(a)) != "commands_disabled" || room.HostID() != "" {
		t.Fatal("with no code configured nobody can become admin")
	}

	room2, x, _, _ := stationRoom(t, "secret-code-123")
	for i := 0; i < cmdAttemptLimit; i++ {
		room2.HandleChat(x, "/admin guess", "")
		drain(x)
	}
	room2.HandleChat(x, "/admin secret-code-123", "")
	if errCode(t, drain(x)) != "too_many_attempts" || room2.HostID() != "" {
		t.Fatal("after too many wrong codes even the right one is refused for a while")
	}
}

func TestStation_SyncStateReportsMode(t *testing.T) {
	room, a, _, _ := stationRoom(t, "secret-code-123")
	n := mockClient("n", "N")
	_ = room.Join(n, "Neha")
	var ss protocol.SyncStatePayload
	_ = json.Unmarshal(hasType(drain(n), protocol.MsgTypeSyncState).Payload, &ss)
	if !ss.Radio || !ss.StationAuto {
		t.Fatal("new joiner should see a radio on auto")
	}
	room.HandleChat(a, "/admin secret-code-123", "")
	m := mockClient("m", "M")
	_ = room.Join(m, "Meera")
	_ = json.Unmarshal(hasType(drain(m), protocol.MsgTypeSyncState).Payload, &ss)
	if ss.StationAuto || ss.HostID != "a" {
		t.Fatalf("joiner should see the admin running the station: %+v", ss)
	}
}
