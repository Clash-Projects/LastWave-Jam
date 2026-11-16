package sync

import (
	"encoding/json"
	"testing"

	"github.com/Clash-Projects/lastwave-jam/internal/protocol"
)

func lastOfType(t *testing.T, c *Client, typ string) *protocol.Envelope {
	t.Helper()
	for {
		env := readLastEnvelope(t, c)
		if env == nil || env.Type == typ {
			return env
		}
	}
}

func TestWebGuests_GateRelayAndMigration(t *testing.T) {
	host := mockClient("usr_host", "Host")
	room := NewRoom("WEB001", host, protocol.RoomModeHostOnly)
	app := mockClient("usr_app", "App")
	if err := room.Join(app, "App"); err != nil {
		t.Fatal(err)
	}

	web := mockClient("usr_web", "Web")
	web.web = true
	if err := room.Join(web, "Web"); err == nil {
		t.Fatal("browser guest joined before the host allowed it")
	}

	room.SetWebGuests(app, true) // not the main host
	if room.webGuestsLocked() {
		t.Fatal("non-host enabled web guests")
	}
	room.SetWebGuests(host, true)
	if err := room.Join(web, "Web"); err != nil {
		t.Fatalf("browser guest rejected after enable: %v", err)
	}
	drain(host)
	drain(app)
	drain(web)

	// web -> host relays; web -> app and app -> web do not.
	data := json.RawMessage(`{"kind":"hello"}`)
	room.RelayRTCSignal(web, host.ID, data)
	env := lastOfType(t, host, protocol.MsgTypeRTCSignal)
	if env == nil {
		t.Fatal("host did not get the browser's signal")
	}
	var p protocol.RTCSignalPayload
	_ = json.Unmarshal(env.Payload, &p)
	if p.From != web.ID || string(p.Data) != string(data) {
		t.Fatalf("bad relay payload: %+v", p)
	}
	room.RelayRTCSignal(web, app.ID, data)
	room.RelayRTCSignal(app, web.ID, data)
	if lastOfType(t, app, protocol.MsgTypeRTCSignal) != nil || lastOfType(t, web, protocol.MsgTypeRTCSignal) != nil {
		t.Fatal("signal relayed outside host<->browser")
	}

	// Host leaves: the app user (not the browser guest) is promoted and
	// browser listening switches off.
	room.Leave(host)
	if room.HostID() != app.ID {
		t.Fatalf("expected app user promoted, got %q", room.HostID())
	}
	if lastOfType(t, web, protocol.MsgTypeWebGuestsChanged) == nil || room.webGuestsLocked() {
		t.Fatal("web guests stayed on after the enabling host left")
	}

	// Always-on rooms never allow browser guests.
	perm := NewRoom("INDIA", host, protocol.RoomModeCollaborative)
	perm.Permanent = true
	perm.SetWebGuests(host, true)
	if perm.webGuestsLocked() {
		t.Fatal("always-on room enabled browser guests")
	}

	// Browser guests are never promoted, even when they are the only ones left.
	room.Leave(app)
	if room.HostID() != "" {
		t.Fatalf("browser guest promoted to host: %q", room.HostID())
	}
}
