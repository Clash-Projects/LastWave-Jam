package sync

import (
	"strings"
	"time"

	"github.com/Clash-Projects/lastwave-jam/internal/protocol"
)

// CooldownWindow is how long a user must wait between "disruptive" playback
// commands in an always-active room.
const CooldownWindow = time.Minute

// isCooldownAction reports whether the action counts against the cooldown.
// Play is deliberately exempt so a paused room can always be resumed.
func isCooldownAction(action string) bool {
	switch action {
	case protocol.ActionPause,
		protocol.ActionSeek,
		protocol.ActionChangeTrack,
		protocol.ActionSkipNext,
		protocol.ActionSkipPrev,
		protocol.ActionQueueClear:
		return true
	}
	return false
}

func (r *Room) cooldownRemainingLocked(userID string) time.Duration {
	last, ok := r.cooldowns[userID]
	if !ok {
		return 0
	}
	if remaining := CooldownWindow - r.now().Sub(last); remaining > 0 {
		return remaining
	}
	return 0
}

// rejectRateLimitedLocked tells the sender when they may retry and re-sends
// the authoritative playback state so their player snaps back in sync.
func (r *Room) rejectRateLimitedLocked(c *Client, wait time.Duration) {
	nowMs := r.now().UnixMilli()
	_ = c.Send(protocol.MsgTypeError, protocol.ErrorPayload{
		Code:         "rate_limited",
		Message:      "Only one pause or change per minute. Try again shortly.",
		RetryAfterMs: wait.Milliseconds(),
		RetryAtMs:    nowMs + wait.Milliseconds(),
	})
	if r.state.CurrentTrack == nil {
		return
	}
	_ = c.Send(protocol.MsgTypeSyncPlayback, protocol.PlaybackActionPayload{
		Action:       protocol.ActionChangeTrack,
		Track:        r.state.CurrentTrack,
		PositionMs:   CalculateCurrentPosition(&r.state, nowMs),
		ServerTimeMs: nowMs,
		IsPlaying:    r.state.IsPlaying,
		IsAutoDJ:     r.state.IsAutoDJ,
		Queue:        append([]protocol.TrackInfo(nil), r.state.Queue...),
		Revision:     r.state.Revision,
	})
}

// GrantCoHost lets the main host promote one other participant to co-host.
// A room never has more than two hosts.
func (r *Room) GrantCoHost(c *Client, targetID string) {
	r.mu.Lock()
	if r.host == nil || r.host.ID != c.ID {
		r.mu.Unlock()
		c.SendError("unauthorized", "Only the main host can give host")
		return
	}
	target, ok := r.participants[targetID]
	if !ok || targetID == c.ID {
		r.mu.Unlock()
		c.SendError("invalid_target", "Pick someone else in the room")
		return
	}
	if r.coHost != nil {
		r.mu.Unlock()
		c.SendError("cohost_exists", "A room can have at most two hosts. Remove the current co-host first")
		return
	}
	r.coHost = target
	hostID, coHostID := r.hostIDsLocked()
	r.mu.Unlock()

	r.Broadcast(protocol.MsgTypeCoHostChanged, map[string]any{
		"host_id":     hostID,
		"cohost_id":   coHostID,
		"cohost_name": target.Name(),
	})
}

// RevokeCoHost lets the main host strip the co-host role.
func (r *Room) RevokeCoHost(c *Client, targetID string) {
	r.mu.Lock()
	if r.host == nil || r.host.ID != c.ID {
		r.mu.Unlock()
		c.SendError("unauthorized", "Only the main host can remove a host")
		return
	}
	if r.coHost == nil || r.coHost.ID != targetID {
		r.mu.Unlock()
		c.SendError("invalid_target", "That person is not a co-host")
		return
	}
	r.coHost = nil
	hostID, coHostID := r.hostIDsLocked()
	r.mu.Unlock()

	r.Broadcast(protocol.MsgTypeCoHostChanged, map[string]any{
		"host_id":   hostID,
		"cohost_id": coHostID,
	})
}

// IsDefaultName reports whether a display name is one of the placeholder
// names the apps fall back to. Public rooms refuse these.
func IsDefaultName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "listener", "host", "guest", "user", "anonymous":
		return true
	}
	return false
}

// HandleQueueUpdate stores the main host's upcoming queue and shows it,
// read-only, to everyone else. Only the main host (admin) may publish it.
func (r *Room) HandleQueueUpdate(c *Client, tracks []protocol.TrackInfo) {
	r.mu.Lock()
	if r.host == nil || r.host.ID != c.ID {
		r.mu.Unlock()
		return // silently ignore: co-hosts and guests don't own the shared queue
	}
	queue := make([]protocol.TrackInfo, 0, len(tracks))
	for i := range tracks {
		if len(queue) >= protocol.MaxSharedQueue {
			break
		}
		t := tracks[i]
		if protocol.SanitizeTrack(&t) {
			queue = append(queue, t)
		}
	}
	r.state.Queue = queue
	r.state.Revision++
	revision := r.state.Revision
	hostID := r.host.ID
	r.mu.Unlock()

	r.Broadcast(protocol.MsgTypeQueueUpdated, map[string]any{
		"queue":    queue,
		"host_id":  hostID,
		"revision": revision,
	}, c.ID)
}
