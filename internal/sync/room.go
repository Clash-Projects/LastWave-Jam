package sync

import (
	"errors"
	"sync"
	"time"

	"github.com/Clash-Projects/lastwave-jam/internal/protocol"
)

// Room manages the concurrent state of a live listening session.
type Room struct {
	Code      string
	Mode      string
	CreatedAt time.Time
	// Permanent rooms (INDIA, WORLD) are never removed from the hub and apply
	// the per-user command cooldown.
	Permanent bool
	// Name is the friendly title of a public room ("India"); empty otherwise.
	Name string
	// Radio rooms are 24/7 stations driven by the server: no host, no listener control.
	Radio bool
	// RadioKey is the contribution-site station feeding a radio room ("india").
	RadioKey string

	mu                 sync.RWMutex
	host               *Client
	coHost             *Client // at most one co-host, granted by the host
	cooldowns          map[string]time.Time
	radioManual        bool          // radio room run by a station admin (auto radio paused)
	radioWake          chan struct{} // nudges the radio loop on mode changes
	adminCode          string        // station-admin code (from the server's environment)
	cmdAttempts        map[string][]time.Time
	now                func() time.Time
	participants       map[string]*Client
	joinedOrder        []string
	state              protocol.PlaybackState
	chat               *ChatBuffer
	pendingSuggestions map[string]*protocol.SuggestionReceivedPayload
}

// NewRoom creates a new listening room with the specified host and mode.
func NewRoom(code string, host *Client, mode string) *Room {
	if mode != protocol.RoomModeCollaborative {
		mode = protocol.RoomModeHostOnly
	}

	r := &Room{
		Code:               code,
		Mode:               mode,
		CreatedAt:          time.Now(),
		host:               host,
		participants:       make(map[string]*Client),
		joinedOrder:        make([]string, 0),
		chat:               NewChatBuffer(DefaultChatHistoryLimit),
		pendingSuggestions: make(map[string]*protocol.SuggestionReceivedPayload),
		cooldowns:          make(map[string]time.Time),
		radioWake:          make(chan struct{}, 1),
		cmdAttempts:        make(map[string][]time.Time),
		now:                time.Now,
		state: protocol.PlaybackState{
			Queue:        make([]protocol.TrackInfo, 0),
			ServerTimeMs: time.Now().UnixMilli(),
		},
	}

	if host != nil {
		r.participants[host.ID] = host
		r.joinedOrder = append(r.joinedOrder, host.ID)
		host.SetRoom(r)
	}

	return r
}

// HostID returns the user ID of the current room host.
func (r *Room) HostID() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.host != nil {
		return r.host.ID
	}
	return ""
}

const (
	maxQueueLen           = protocol.MaxSharedQueue * 10 // collaborative queue_add bound
	maxPendingSuggestions = 20
	maxRoomParticipants   = 1000
)

// CoHostID returns the user ID of the co-host, or "" if there is none.
func (r *Room) CoHostID() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.coHost != nil {
		return r.coHost.ID
	}
	return ""
}

// IsHost reports whether the client is the host or the co-host.
func (r *Room) IsHost(c *Client) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.isHostLocked(c)
}

func (r *Room) isHostLocked(c *Client) bool {
	if c == nil {
		return false
	}
	return (r.host != nil && r.host.ID == c.ID) || (r.coHost != nil && r.coHost.ID == c.ID)
}

func (r *Room) hostIDsLocked() (string, string) {
	var h, co string
	if r.host != nil {
		h = r.host.ID
	}
	if r.coHost != nil {
		co = r.coHost.ID
	}
	return h, co
}

// IsEmpty returns true if there are no participants remaining.
func (r *Room) IsEmpty() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.participants) == 0
}

// ParticipantCount returns the number of currently connected users.
func (r *Room) ParticipantCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.participants)
}

// ChatHistory returns recent messages stored in the room's chat buffer.
func (r *Room) ChatHistory() []protocol.ChatMessage {
	return r.chat.History()
}

// Join adds a client to the room after validating their display name.
func (r *Room) Join(c *Client, rawName string) error {
	name, err := protocol.ValidateName(rawName)
	if err != nil {
		c.SendError("invalid_name", "Display name is required before entering a room (1-32 chars)")
		return err
	}

	if r.Permanent && IsDefaultName(name) {
		c.SendError("default_name", "Pick your own display name to join this public room")
		return errors.New("default display name not allowed in public room")
	}

	r.mu.RLock()
	full := len(r.participants) >= maxRoomParticipants
	r.mu.RUnlock()
	if full {
		c.SendError("room_full", "This room is full, try again in a bit")
		return errors.New("room full")
	}

	c.SetName(name)
	c.SetRoom(r)

	r.mu.Lock()
	r.participants[c.ID] = c
	r.joinedOrder = append(r.joinedOrder, c.ID)

	// Promote as host if room currently has no host (radio stations never have one)
	if r.host == nil && !r.Radio {
		r.host = c
	}

	isHost := r.isHostLocked(c)
	hostID, coHostID := r.hostIDsLocked()

	// Prepare participants snapshot
	participants := make([]protocol.Participant, 0, len(r.participants))
	for _, p := range r.participants {
		participants = append(participants, protocol.Participant{
			UserID:   p.ID,
			Name:     p.Name(),
			IsHost:   r.isHostLocked(p),
			IsCoHost: coHostID != "" && coHostID == p.ID,
			JoinedAt: time.Now().UnixMilli(),
		})
	}

	// Capture playback state snapshot
	playbackSnapshot := r.state
	playbackSnapshot.PositionMs = CalculateCurrentPosition(&r.state, time.Now().UnixMilli())
	playbackSnapshot.ServerTimeMs = time.Now().UnixMilli()
	chatHistory := r.chat.History()
	totalUsers := len(r.participants)
	r.mu.Unlock()

	// 1. Deliver complete sync_state to the newly joined client
	_ = c.Send(protocol.MsgTypeSyncState, protocol.SyncStatePayload{
		RoomCode:     r.Code,
		UserID:       c.ID,
		Name:         name,
		IsHost:       isHost,
		HostID:       hostID,
		CoHostID:     coHostID,
		Permanent:    r.Permanent,
		RoomName:     r.Name,
		Radio:        r.Radio,
		StationAuto:  r.Radio && !r.radioManual,
		RoomMode:     r.Mode,
		Playback:     playbackSnapshot,
		Participants: participants,
		ChatHistory:  chatHistory,
	})

	// 2. Broadcast user_joined to all other participants
	r.Broadcast(protocol.MsgTypeUserJoined, map[string]any{
		"user_id":     c.ID,
		"name":        name,
		"is_host":     isHost,
		"total_users": totalUsers,
	}, c.ID)

	return nil
}

// Leave handles graceful departure or disconnect of a participant.
func (r *Room) Leave(c *Client) {
	r.mu.Lock()
	if _, ok := r.participants[c.ID]; !ok {
		r.mu.Unlock()
		return
	}

	delete(r.participants, c.ID)
	c.SetRoom(nil)

	// Remove from joinedOrder
	for i, id := range r.joinedOrder {
		if id == c.ID {
			r.joinedOrder = append(r.joinedOrder[:i], r.joinedOrder[i+1:]...)
			break
		}
	}

	delete(r.cooldowns, c.ID)

	if r.Radio {
		adminLeft := r.stationAdminLeftLocked(c)
		totalUsers := len(r.participants)
		userName := c.Name()
		r.mu.Unlock()
		r.Broadcast(protocol.MsgTypeUserLeft, map[string]any{
			"user_id":     c.ID,
			"name":        userName,
			"is_host":     false,
			"total_users": totalUsers,
		})
		if adminLeft {
			r.announceStationMode(nil)
			r.postSystemChat(userName + " left. The station is back on auto.")
			r.wakeRadio()
		}
		return
	}

	var newHost *Client
	coHostCleared := false
	queueCleared := false
	if r.coHost != nil && r.coHost.ID == c.ID {
		r.coHost = nil
		coHostCleared = true
	}
	if r.host != nil && r.host.ID == c.ID {
		switch {
		case r.coHost != nil:
			// The co-host takes over as the main host.
			newHost = r.coHost
			r.coHost = nil
			coHostCleared = true
		case len(r.joinedOrder) > 0 && r.Permanent:
			// Always-active rooms: the most recent joiner becomes admin.
			newHost = r.participants[r.joinedOrder[len(r.joinedOrder)-1]]
		case len(r.joinedOrder) > 0:
			// Normal rooms: the longest-tenured member becomes host.
			newHost = r.participants[r.joinedOrder[0]]
		}
		r.host = newHost
		// The queue belonged to the previous admin; the new admin republishes.
		r.state.Queue = make([]protocol.TrackInfo, 0)
		r.state.Revision++
		queueCleared = true
	}

	if len(r.participants) == 0 && r.Permanent && !r.Radio {
		r.resetLocked()
	}

	totalUsers := len(r.participants)
	userName := c.Name()
	hostID, coHostID := r.hostIDsLocked()
	r.mu.Unlock()

	// Notify room that user left
	r.Broadcast(protocol.MsgTypeUserLeft, map[string]any{
		"user_id":     c.ID,
		"name":        userName,
		"is_host":     false,
		"total_users": totalUsers,
	})

	// Notify room of host change if promoted
	if newHost != nil {
		r.Broadcast(protocol.MsgTypeHostChanged, map[string]any{
			"new_host_id":   newHost.ID,
			"new_host_name": newHost.Name(),
			"user_id":       newHost.ID,
			"name":          newHost.Name(),
			"is_host":       true,
			"host_id":       hostID,
			"cohost_id":     coHostID,
		})
		if queueCleared {
			r.Broadcast(protocol.MsgTypeQueueUpdated, map[string]any{"queue": []protocol.TrackInfo{}, "host_id": hostID})
		}
	} else if coHostCleared {
		r.Broadcast(protocol.MsgTypeCoHostChanged, map[string]any{
			"host_id":   hostID,
			"cohost_id": coHostID,
		})
	}
}

// resetLocked clears playback, queue and suggestions. Used when an
// always-active room empties so the next visitor starts fresh.
func (r *Room) resetLocked() {
	r.state = protocol.PlaybackState{
		Queue:        make([]protocol.TrackInfo, 0),
		ServerTimeMs: r.now().UnixMilli(),
		Revision:     r.state.Revision + 1,
	}
	r.pendingSuggestions = make(map[string]*protocol.SuggestionReceivedPayload)
	r.cooldowns = make(map[string]time.Time)
}

// Broadcast sends a message to all connected participants in the room,
// optionally excluding specific client IDs.
func (r *Room) Broadcast(msgType string, payload any, excludeIDs ...string) {
	r.mu.RLock()
	clients := make([]*Client, 0, len(r.participants))
	for id, c := range r.participants {
		skip := false
		for _, ex := range excludeIDs {
			if id == ex {
				skip = true
				break
			}
		}
		if !skip && c != nil {
			clients = append(clients, c)
		}
	}
	r.mu.RUnlock()

	msg, err := encodeEnvelope(msgType, payload)
	if err != nil {
		return
	}
	for _, c := range clients {
		_ = c.sendRaw(msg)
	}
}

// HandleChat processes and broadcasts an incoming in-room chat message.
func (r *Room) HandleChat(c *Client, rawText string, replyToID string) {
	text, err := protocol.SanitizeChatText(rawText)
	if err != nil {
		c.SendError("invalid_chat", err.Error())
		return
	}

	r.mu.RLock()
	isHost := r.isHostLocked(c)
	r.mu.RUnlock()

	// Station commands: everyone sees that the command was typed, never the code.
	cmd, code := "", ""
	if r.Radio {
		cmd, code = parseStationCommand(text)
		if cmd != "" {
			text = "/" + cmd + " " + maskedCode
			replyToID = ""
		}
	}
	if cmd != "" {
		defer r.handleStationCommand(c, cmd, code)
	}

	msg := protocol.ChatMessage{
		ID:        GenerateID("msg_"),
		UserID:    c.ID,
		UserName:  c.Name(),
		IsHost:    isHost,
		Text:      text,
		Timestamp: time.Now().UnixMilli(),
	}

	if replyToID != "" {
		if orig, ok := r.chat.Get(replyToID); ok {
			quoted := orig.Text
			if runes := []rune(quoted); len(runes) > 120 {
				quoted = string(runes[:120]) + "…"
			}
			msg.ReplyTo = &protocol.ChatReply{
				ID:       orig.ID,
				UserID:   orig.UserID,
				UserName: orig.UserName,
				Text:     quoted,
			}
		}
	}

	r.chat.Add(msg)
	r.Broadcast(protocol.MsgTypeChatMessage, msg)
}

// HandlePlayback handles playback actions (play, pause, seek, queue, etc.).
func (r *Room) HandlePlayback(c *Client, p protocol.PlaybackActionPayload) {
	r.mu.Lock()
	defer r.mu.Unlock()

	isHost := r.isHostLocked(c)
	if r.Radio && !(r.radioManual && isHost) {
		msg := "This is a live 24/7 station. Playback can't be controlled."
		if r.radioManual {
			msg = "Only the station admin can control playback."
		}
		c.SendError("live_station", msg)
		r.resyncLocked(c)
		return
	}

	if r.Mode == protocol.RoomModeHostOnly && !isHost {
		c.SendError("unauthorized", "Only the host can control playback in host-only mode")
		return
	}

	if r.Permanent && !r.Radio && isCooldownAction(p.Action) {
		if wait := r.cooldownRemainingLocked(c.ID); wait > 0 {
			r.rejectRateLimitedLocked(c, wait)
			return
		}
		r.cooldowns[c.ID] = r.now()
		_ = c.Send(protocol.MsgTypeCooldown, map[string]any{
			"retry_at_ms": r.now().Add(CooldownWindow).UnixMilli(),
			"window_ms":   CooldownWindow.Milliseconds(),
		})
	}

	nowMs := time.Now().UnixMilli()

	switch p.Action {
	case protocol.ActionPlay:
		r.state.IsPlaying = true
		r.state.PositionMs = CompensateTransit(p.PositionMs, p.ClientTimeMs, nowMs)
		r.state.ServerTimeMs = nowMs

	case protocol.ActionPause:
		r.state.IsPlaying = false
		r.state.PositionMs = p.PositionMs
		r.state.ServerTimeMs = nowMs

	case protocol.ActionSeek:
		r.state.PositionMs = p.PositionMs
		r.state.ServerTimeMs = nowMs

	case protocol.ActionChangeTrack:
		if p.Track != nil && protocol.SanitizeTrack(p.Track) {
			r.state.CurrentTrack = p.Track
			if p.PositionMs > 0 {
				r.state.PositionMs = p.PositionMs
			} else {
				r.state.PositionMs = 0
			}
			r.state.ServerTimeMs = nowMs
			r.state.IsPlaying = p.IsPlaying
			r.state.IsAutoDJ = p.IsAutoDJ || p.Track.IsAutoDJ || p.Track.Source == "autodj"
		}

	case protocol.ActionQueueAdd:
		if p.Track != nil && protocol.SanitizeTrack(p.Track) && len(r.state.Queue) < maxQueueLen {
			r.state.Queue = append(r.state.Queue, *p.Track)
		}

	case protocol.ActionQueueRemove:
		if p.Track != nil && p.Track.ID != "" {
			newQueue := make([]protocol.TrackInfo, 0, len(r.state.Queue))
			for _, t := range r.state.Queue {
				if t.ID != p.Track.ID {
					newQueue = append(newQueue, t)
				}
			}
			r.state.Queue = newQueue
		}

	case protocol.ActionQueueClear:
		r.state.Queue = make([]protocol.TrackInfo, 0)

	case protocol.ActionSkipNext:
		if len(r.state.Queue) > 0 {
			nextTrack := r.state.Queue[0]
			r.state.CurrentTrack = &nextTrack
			r.state.Queue = r.state.Queue[1:]
			r.state.PositionMs = 0
			r.state.ServerTimeMs = nowMs
			r.state.IsPlaying = true
		}
	}

	r.state.Revision++

	var fromID string
	if c != nil {
		fromID = c.ID
	}
	broadcastPayload := protocol.PlaybackActionPayload{
		Action:       p.Action,
		FromUserID:   fromID,
		Track:        r.state.CurrentTrack,
		PositionMs:   r.state.PositionMs,
		ServerTimeMs: r.state.ServerTimeMs,
		IsPlaying:    r.state.IsPlaying,
		IsAutoDJ:     r.state.IsAutoDJ || p.IsAutoDJ || (p.Track != nil && (p.Track.IsAutoDJ || p.Track.Source == "autodj")),
		Queue:        append([]protocol.TrackInfo(nil), r.state.Queue...),
		Revision:     r.state.Revision,
	}

	// Broadcast synchronously while holding room lock or right after
	clients := make([]*Client, 0, len(r.participants))
	for _, participant := range r.participants {
		if participant != nil {
			clients = append(clients, participant)
		}
	}

	if msg, err := encodeEnvelope(protocol.MsgTypeSyncPlayback, broadcastPayload); err == nil {
		for _, client := range clients {
			_ = client.sendRaw(msg)
		}
	}
}

// HandleSuggestTrack allows guests to suggest songs to the room host.
func (r *Room) HandleSuggestTrack(c *Client, track *protocol.TrackInfo) {
	if track == nil || !protocol.SanitizeTrack(track) {
		c.SendError("invalid_track", "Suggested track must have valid ID and Title")
		return
	}

	r.mu.Lock()
	if r.host == nil || r.isHostLocked(c) {
		r.mu.Unlock()
		return
	}

	if len(r.pendingSuggestions) >= maxPendingSuggestions {
		r.mu.Unlock()
		c.SendError("too_many_suggestions", "The host has too many pending suggestions")
		return
	}

	sugID := GenerateID("sug_")
	track.SuggestedBy = c.Name()
	payload := &protocol.SuggestionReceivedPayload{
		SuggestionID: sugID,
		FromUserID:   c.ID,
		FromUserName: c.Name(),
		Track:        track,
	}
	r.pendingSuggestions[sugID] = payload
	host := r.host
	r.mu.Unlock()

	if host != nil {
		_ = host.Send(protocol.MsgTypeSuggestionReceived, payload)
	}
}

// HandleApproveSuggestion allows the host to approve a suggested track into the queue.
func (r *Room) HandleApproveSuggestion(c *Client, suggestionID string) {
	r.mu.Lock()
	if r.host == nil || r.isHostLocked(c) {
		sug, exists := r.pendingSuggestions[suggestionID]
		if !exists || sug == nil {
			r.mu.Unlock()
			c.SendError("not_found", "Suggestion not found")
			return
		}
		delete(r.pendingSuggestions, suggestionID)
		r.state.Queue = append(r.state.Queue, *sug.Track)
		r.state.Revision++
		nowMs := time.Now().UnixMilli()

		broadcastPayload := protocol.PlaybackActionPayload{
			Action:       protocol.ActionQueueAdd,
			Track:        sug.Track,
			PositionMs:   r.state.PositionMs,
			ServerTimeMs: nowMs,
			IsPlaying:    r.state.IsPlaying,
			Queue:        append([]protocol.TrackInfo(nil), r.state.Queue...),
			Revision:     r.state.Revision,
		}
		r.mu.Unlock()

		r.Broadcast(protocol.MsgTypeSyncPlayback, broadcastPayload)
		return
	}
	r.mu.Unlock()
	c.SendError("unauthorized", "Only the host can approve suggestions")
}

// HandleRejectSuggestion allows the host to reject a proposed track.
func (r *Room) HandleRejectSuggestion(c *Client, suggestionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.isHostLocked(c) {
		c.SendError("unauthorized", "Only the host can reject suggestions")
		return
	}
	delete(r.pendingSuggestions, suggestionID)
}

// SetMode updates the room mode (host_only or collaborative) and broadcasts the change.
func (r *Room) SetMode(mode string) {
	if mode != protocol.RoomModeCollaborative {
		mode = protocol.RoomModeHostOnly
	}
	r.mu.Lock()
	r.Mode = mode
	r.mu.Unlock()

	r.Broadcast(protocol.MsgTypeRoomModeChanged, map[string]any{
		"room_mode": mode,
	})
}
