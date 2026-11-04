package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Clash-Projects/lastwave-jam/internal/protocol"
)

func radioTrack(id, title string) RadioTrack {
	return RadioTrack{VideoID: id, Title: title, Artist: "A", Thumbnail: "t", DurationMs: 200000}
}

func TestRadio_StationsHaveNoHostAndRefuseControl(t *testing.T) {
	hub := NewHub()
	hub.EnsurePermanentRooms()
	room, _ := hub.GetRoom("INDIA")
	if !room.Radio || room.RadioKey != "india" || room.Mode != protocol.RoomModeHostOnly {
		t.Fatalf("INDIA should be a host-only radio room: %+v", room)
	}
	a := mockClient("a", "A")
	_ = room.Join(a, "Aarav")
	if room.HostID() != "" {
		t.Fatal("radio rooms must never get a host")
	}
	var ss protocol.SyncStatePayload
	_ = json.Unmarshal(hasType(drain(a), protocol.MsgTypeSyncState).Payload, &ss)
	if !ss.Radio || ss.IsHost {
		t.Fatalf("sync_state should say radio and not host: %+v", ss)
	}

	room.radioPlay(radioTrack("dQw4w9WgXcQ", "Song"), nil)
	drain(a)
	for _, action := range []string{protocol.ActionPause, protocol.ActionPlay, protocol.ActionSkipNext, protocol.ActionChangeTrack, protocol.ActionSeek} {
		room.HandlePlayback(a, protocol.PlaybackActionPayload{Action: action})
		envs := drain(a)
		e := hasType(envs, protocol.MsgTypeError)
		if e == nil {
			t.Fatalf("%s must be refused", action)
		}
		var ep protocol.ErrorPayload
		_ = json.Unmarshal(e.Payload, &ep)
		if ep.Code != "live_station" {
			t.Fatalf("wrong error code %q", ep.Code)
		}
		if hasType(envs, protocol.MsgTypeSyncPlayback) == nil {
			t.Fatal("refused listener should be resynced to the live track")
		}
	}
	if room.state.CurrentTrack.ID != "dQw4w9WgXcQ" || !room.state.IsPlaying {
		t.Fatal("station state must be unchanged by listeners")
	}

	// Emptying the room does not stop or reset the station.
	room.Leave(a)
	if room.state.CurrentTrack == nil {
		t.Fatal("radio must keep playing with no listeners")
	}
}

func TestRadio_PlayBroadcastsAndLateJoinerLandsMidSong(t *testing.T) {
	hub := NewHub()
	hub.EnsurePermanentRooms()
	room, _ := hub.GetRoom("WORLD")
	a := mockClient("a", "A")
	_ = room.Join(a, "Aarav")
	drain(a)

	room.radioPlay(radioTrack("dQw4w9WgXcQ", "Now"), []RadioTrack{radioTrack("kJQP7kiw5Fk", "Next")})
	envs := drain(a)
	sp := hasType(envs, protocol.MsgTypeSyncPlayback)
	if sp == nil || hasType(envs, protocol.MsgTypeQueueUpdated) == nil {
		t.Fatal("listeners should get the new track and the up-next queue")
	}
	var p protocol.PlaybackActionPayload
	_ = json.Unmarshal(sp.Payload, &p)
	if p.Action != protocol.ActionChangeTrack || p.Track.ID != "dQw4w9WgXcQ" || !p.IsPlaying || p.FromUserID != "" {
		t.Fatalf("bad broadcast: %+v", p)
	}

	b := mockClient("b", "B")
	_ = room.Join(b, "Bela")
	var ss protocol.SyncStatePayload
	_ = json.Unmarshal(hasType(drain(b), protocol.MsgTypeSyncState).Payload, &ss)
	if ss.Playback.CurrentTrack == nil || ss.Playback.CurrentTrack.ID != "dQw4w9WgXcQ" || !ss.Playback.IsPlaying || len(ss.Playback.Queue) != 1 {
		t.Fatalf("late joiner should get the live track and queue: %+v", ss.Playback)
	}
}

func TestRadioPlayer_NoRepeatUntilEverySongPlayed(t *testing.T) {
	p := newRadioPlayer(42)
	p.setTracks([]RadioTrack{radioTrack("aaaaaaaaaaa", "1"), radioTrack("bbbbbbbbbbb", "2"), radioTrack("ccccccccccc", "3")})
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		tr, ok := p.next()
		if !ok || seen[tr.VideoID] {
			t.Fatalf("repeat or empty within a cycle at %d", i)
		}
		seen[tr.VideoID] = true
	}
	last := p.last
	if tr, _ := p.next(); tr.VideoID == last {
		t.Fatal("a new cycle must not start with the song that just played")
	}

	// A refresh that removes a song: it is never played again.
	p.setTracks([]RadioTrack{radioTrack("aaaaaaaaaaa", "1"), radioTrack("bbbbbbbbbbb", "2")})
	for i := 0; i < 6; i++ {
		if tr, _ := p.next(); tr.VideoID == "ccccccccccc" {
			t.Fatal("removed song played")
		}
	}

	empty := newRadioPlayer(1)
	if _, ok := empty.next(); ok {
		t.Fatal("empty station has nothing to play")
	}
}

func TestFetchRadioTracks_FiltersUnplayable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/stations/india/songs" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[
			{"videoId":"dQw4w9WgXcQ","title":"Good","artist":"A","thumbnail":"t","durationMs":213000},
			{"videoId":"kJQP7kiw5Fk","title":"No length","artist":"A","thumbnail":"t","durationMs":null},
			{"videoId":"9bZkp7q19f0","title":"Too long","artist":"A","thumbnail":"t","durationMs":7200000},
			{"videoId":"bad","title":"Bad id","artist":"A","thumbnail":"t","durationMs":200000}
		]`))
	}))
	defer srv.Close()
	list, err := fetchRadioTracks(context.Background(), srv.URL, "india")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].VideoID != "dQw4w9WgXcQ" {
		t.Fatalf("expected only the playable song, got %+v", list)
	}
	if _, err := fetchRadioTracks(context.Background(), srv.URL, "nope"); err == nil {
		t.Fatal("404 feed should be an error")
	}
}
