package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"sort"
	"time"

	"github.com/Clash-Projects/lastwave-jam/internal/protocol"
)

// The always-on rooms (INDIA, WORLD) are 24/7 radio stations: the server plays
// the approved songs from the contribution site back to back, shuffled, and no
// listener can pause, skip or change anything.

// RadioTrack is one song from the contribution site's public station feed.
type RadioTrack struct {
	VideoID    string `json:"videoId"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Thumbnail  string `json:"thumbnail"`
	DurationMs int64  `json:"durationMs"`
}

const (
	radioRefresh   = 10 * time.Minute // pick up newly approved / removed songs
	radioIdleRetry = 30 * time.Second // when the feed is empty or unreachable
	radioGap       = 1500 * time.Millisecond
	radioMinMs     = 30 * 1000
	radioMaxMs     = 20 * 60 * 1000 // skip long mixes / compilations
	radioQueueLen  = 5
)

var radioHTTP = &http.Client{Timeout: 15 * time.Second}

// fetchRadioTracks reads <source>/api/stations/<key>/songs and keeps playable songs.
func fetchRadioTracks(ctx context.Context, source, key string) ([]RadioTrack, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source+"/api/stations/"+key+"/songs", nil)
	if err != nil {
		return nil, err
	}
	resp, err := radioHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("feed status %d", resp.StatusCode)
	}
	var all []RadioTrack
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&all); err != nil {
		return nil, err
	}
	out := make([]RadioTrack, 0, len(all))
	for _, t := range all {
		if len(t.VideoID) == 11 && t.Title != "" && t.DurationMs >= radioMinMs && t.DurationMs <= radioMaxMs {
			out = append(out, t)
		}
	}
	return out, nil
}

// radioPlayer shuffles the station and never repeats a song until every song
// has played once (a "cycle"). A refresh mid-cycle keeps what already played.
type radioPlayer struct {
	rng    *rand.Rand
	tracks map[string]RadioTrack
	played map[string]bool
	order  []string // still to play in this cycle
	last   string
}

func newRadioPlayer(seed int64) *radioPlayer {
	return &radioPlayer{rng: rand.New(rand.NewSource(seed)), tracks: map[string]RadioTrack{}, played: map[string]bool{}}
}

func (p *radioPlayer) shuffled(ids []string) []string {
	sort.Strings(ids)
	p.rng.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	return ids
}

func (p *radioPlayer) setTracks(list []RadioTrack) {
	p.tracks = make(map[string]RadioTrack, len(list))
	for _, t := range list {
		p.tracks[t.VideoID] = t
	}
	rest := make([]string, 0, len(p.tracks))
	for id := range p.tracks {
		if !p.played[id] {
			rest = append(rest, id)
		}
	}
	p.order = p.shuffled(rest)
}

func (p *radioPlayer) next() (RadioTrack, bool) {
	if len(p.tracks) == 0 {
		return RadioTrack{}, false
	}
	for {
		if len(p.order) == 0 {
			p.played = map[string]bool{}
			ids := make([]string, 0, len(p.tracks))
			for id := range p.tracks {
				ids = append(ids, id)
			}
			p.order = p.shuffled(ids)
			if len(p.order) > 1 && p.order[0] == p.last {
				p.order[0], p.order[len(p.order)-1] = p.order[len(p.order)-1], p.order[0]
			}
		}
		id := p.order[0]
		p.order = p.order[1:]
		t, ok := p.tracks[id]
		if !ok {
			continue // removed by a refresh
		}
		p.played[id] = true
		p.last = id
		return t, true
	}
}

func (p *radioPlayer) upcoming(n int) []RadioTrack {
	out := make([]RadioTrack, 0, n)
	for _, id := range p.order {
		if t, ok := p.tracks[id]; ok {
			out = append(out, t)
			if len(out) == n {
				break
			}
		}
	}
	return out
}

func radioTrackInfo(t RadioTrack) protocol.TrackInfo {
	return protocol.TrackInfo{
		ID:         t.VideoID,
		Title:      t.Title,
		Artist:     t.Artist,
		ArtworkURL: t.Thumbnail,
		DurationMs: t.DurationMs,
		Source:     "radio",
	}
}

// radioPlay makes t the room's current track from position 0 and tells everyone.
// Late joiners get the live position from the normal sync_state.
func (r *Room) radioPlay(t RadioTrack, upcoming []RadioTrack) {
	now := r.now().UnixMilli()
	cur := radioTrackInfo(t)
	queue := make([]protocol.TrackInfo, 0, len(upcoming))
	for _, u := range upcoming {
		queue = append(queue, radioTrackInfo(u))
	}

	r.mu.Lock()
	r.state.CurrentTrack = &cur
	r.state.PositionMs = 0
	r.state.ServerTimeMs = now
	r.state.IsPlaying = true
	r.state.IsAutoDJ = false
	r.state.Queue = queue
	r.state.Revision++
	rev := r.state.Revision
	r.mu.Unlock()

	r.Broadcast(protocol.MsgTypeSyncPlayback, protocol.PlaybackActionPayload{
		Action:       protocol.ActionChangeTrack,
		Track:        &cur,
		PositionMs:   0,
		ServerTimeMs: now,
		IsPlaying:    true,
		Queue:        queue,
		Revision:     rev,
	})
	r.Broadcast(protocol.MsgTypeQueueUpdated, map[string]any{"queue": queue, "host_id": "", "revision": rev})
}

// RunRadio plays the station until ctx is cancelled. It keeps going with no
// listeners, so anyone who joins lands in the middle of a song, like real radio.
func (r *Room) RunRadio(ctx context.Context, source string) {
	p := newRadioPlayer(time.Now().UnixNano())
	var lastFetch time.Time
	refresh := func() {
		lastFetch = time.Now()
		list, err := fetchRadioTracks(ctx, source, r.RadioKey)
		if err != nil {
			slog.Warn("radio feed unavailable", "room", r.Code, "err", err)
			return
		}
		if len(list) == 0 {
			return // keep playing the last good list
		}
		p.setTracks(list)
		slog.Info("radio tracks loaded", "room", r.Code, "tracks", len(list))
	}

	refresh()
	for {
		if time.Since(lastFetch) > radioRefresh {
			refresh()
		}
		t, ok := p.next()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-time.After(radioIdleRetry):
			}
			refresh()
			continue
		}
		r.radioPlay(t, p.upcoming(radioQueueLen))
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(t.DurationMs)*time.Millisecond + radioGap):
		}
	}
}

// StartRadio launches the radio loop for every radio room.
func (h *Hub) StartRadio(ctx context.Context, source string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, room := range h.rooms {
		if room.Radio {
			go room.RunRadio(ctx, source)
		}
	}
}

// resyncLocked sends the room's current playback to one client so its player
// snaps back after a refused command. Caller holds r.mu.
func (r *Room) resyncLocked(c *Client) {
	if r.state.CurrentTrack == nil {
		return
	}
	nowMs := r.now().UnixMilli()
	_ = c.Send(protocol.MsgTypeSyncPlayback, protocol.PlaybackActionPayload{
		Action:       protocol.ActionChangeTrack,
		Track:        r.state.CurrentTrack,
		PositionMs:   CalculateCurrentPosition(&r.state, nowMs),
		ServerTimeMs: nowMs,
		IsPlaying:    r.state.IsPlaying,
		Queue:        append([]protocol.TrackInfo(nil), r.state.Queue...),
		Revision:     r.state.Revision,
	})
}
