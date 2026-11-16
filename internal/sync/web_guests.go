package sync

import (
	"encoding/json"

	"github.com/Clash-Projects/lastwave-jam/internal/protocol"
)

// webGuestsLocked reports whether browser guests may join and receive audio.
func (r *Room) webGuestsLocked() bool {
	return r.webHostID != "" && r.host != nil && r.host.ID == r.webHostID
}

// nextAppHostLocked picks the next main host among app clients: browser
// guests cannot play music, so they are never promoted.
func (r *Room) nextAppHostLocked(newest bool) *Client {
	n := len(r.joinedOrder)
	for i := 0; i < n; i++ {
		idx := i
		if newest {
			idx = n - 1 - i
		}
		if p := r.participants[r.joinedOrder[idx]]; p != nil && !p.web {
			return p
		}
	}
	return nil
}

// SetWebGuests lets the main host allow or stop browser guests. Stopping
// does not kick guests already in the room; they keep chat, lose audio.
// The always-on rooms (INDIA, WORLD) never offer browser listening.
func (r *Room) SetWebGuests(c *Client, enabled bool) {
	r.mu.Lock()
	if r.Radio || r.Permanent {
		r.mu.Unlock()
		c.SendError("not_available", "Browser listening isn't available in always-on rooms")
		return
	}
	if r.host == nil || r.host.ID != c.ID {
		r.mu.Unlock()
		c.SendError("unauthorized", "Only the main host can change browser listening")
		return
	}
	if enabled {
		r.webHostID = c.ID
	} else {
		r.webHostID = ""
	}
	r.mu.Unlock()
	r.Broadcast(protocol.MsgTypeWebGuestsChanged, map[string]any{"enabled": enabled})
}

// RelayRTCSignal forwards opaque WebRTC signaling between the main host and
// a browser guest only. Anything else is dropped silently.
func (r *Room) RelayRTCSignal(c *Client, to string, data json.RawMessage) {
	r.mu.RLock()
	target := r.participants[to]
	ok := target != nil && target != c && r.webGuestsLocked() &&
		((r.host == c && target.web) || (c.web && r.host == target))
	r.mu.RUnlock()
	if !ok {
		return
	}
	_ = target.Send(protocol.MsgTypeRTCSignal, protocol.RTCSignalPayload{From: c.ID, Data: data})
}
