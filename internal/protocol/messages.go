package protocol

import "encoding/json"

// WebSocket Message Types
const (
	// Client -> Server
	MsgTypeCreateRoom        = "create_room"
	MsgTypeJoinRoom          = "join_room"
	MsgTypeLeaveRoom         = "leave_room"
	MsgTypeSendChat          = "send_chat"
	MsgTypePlaybackAction    = "playback_action"
	MsgTypeSuggestTrack      = "suggest_track"
	MsgTypeApproveSuggestion = "approve_suggestion"
	MsgTypeRejectSuggestion  = "reject_suggestion"
	MsgTypeChangeRoomMode    = "change_room_mode"
	MsgTypeGrantHost         = "grant_host"
	MsgTypeQueueUpdate       = "queue_update"
	MsgTypeRevokeHost        = "revoke_host"
	MsgTypePing              = "ping"

	// Server -> Client
	MsgTypeRoomCreated        = "room_created"
	MsgTypeSyncState          = "sync_state"
	MsgTypeUserJoined         = "user_joined"
	MsgTypeUserLeft           = "user_left"
	MsgTypeChatMessage        = "chat_message"
	MsgTypeSyncPlayback       = "sync_playback"
	MsgTypeHostChanged        = "host_changed"
	MsgTypeCoHostChanged      = "cohost_changed"
	MsgTypeQueueUpdated       = "queue_updated"
	MsgTypeCooldown           = "cooldown"
	MsgTypeRoomModeChanged    = "room_mode_changed"
	MsgTypeSuggestionReceived = "suggestion_received"
	MsgTypePong               = "pong"
	MsgTypeError              = "error"
)

// Playback Actions
const (
	ActionPlay        = "play"
	ActionPause       = "pause"
	ActionSeek        = "seek"
	ActionSkipNext    = "skip_next"
	ActionSkipPrev    = "skip_prev"
	ActionChangeTrack = "change_track"
	ActionQueueAdd    = "queue_add"
	ActionQueueRemove = "queue_remove"
	ActionQueueClear  = "queue_clear"
)

// Room Modes
const (
	RoomModeHostOnly      = "host_only"
	RoomModeCollaborative = "collaborative"
)

// Envelope is the outer wrapper for every WebSocket frame.
type Envelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// CreateRoomPayload carries options when initiating a listening room.
type CreateRoomPayload struct {
	Name     string `json:"name"`
	RoomMode string `json:"room_mode,omitempty"`
}

// JoinRoomPayload requires room code and a display name.
type JoinRoomPayload struct {
	RoomCode string `json:"room_code"`
	Name     string `json:"name"`
}

// SendChatPayload is sent by a client to broadcast a chat message.
// ReplyToID optionally references an earlier message in the room's history.
type SendChatPayload struct {
	Text      string `json:"text"`
	ReplyToID string `json:"reply_to_id,omitempty"`
}

// GrantHostPayload targets a participant for a co-host grant or revoke.
type GrantHostPayload struct {
	UserID string `json:"user_id"`
}

// ChatReply is a server-built snapshot of the message being replied to.
type ChatReply struct {
	ID       string `json:"id"`
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
	Text     string `json:"text"`
}

// ChatMessage represents a single broadcast chat message.
type ChatMessage struct {
	ID        string     `json:"id"`
	UserID    string     `json:"user_id"`
	UserName  string     `json:"user_name"`
	IsHost    bool       `json:"is_host"`
	Text      string     `json:"text"`
	Timestamp int64      `json:"timestamp"`
	ReplyTo   *ChatReply `json:"reply_to,omitempty"`
}

// TrackInfo models a playable song or media item in LastWave.
type TrackInfo struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album,omitempty"`
	ArtworkURL  string `json:"artwork_url,omitempty"`
	DurationMs  int64  `json:"duration_ms,omitempty"`
	Source      string `json:"source,omitempty"`
	SuggestedBy string `json:"suggested_by,omitempty"`
	IsAutoDJ    bool   `json:"is_autodj,omitempty"`
}

// PlaybackActionPayload communicates playback commands and state broadcasts.
type PlaybackActionPayload struct {
	Action       string      `json:"action"`
	FromUserID   string      `json:"from_user_id,omitempty"`
	Track        *TrackInfo  `json:"track,omitempty"`
	PositionMs   int64       `json:"position_ms"`
	ClientTimeMs int64       `json:"client_time_ms,omitempty"`
	ServerTimeMs int64       `json:"server_time_ms"`
	IsPlaying    bool        `json:"is_playing"`
	IsAutoDJ     bool        `json:"is_autodj,omitempty"`
	Queue        []TrackInfo `json:"queue,omitempty"`
	Revision     int64       `json:"revision"`
}

// PlaybackState encapsulates the room's current synchronized playback timeline.
type PlaybackState struct {
	CurrentTrack *TrackInfo  `json:"current_track,omitempty"`
	PositionMs   int64       `json:"position_ms"`
	ServerTimeMs int64       `json:"server_time_ms"`
	IsPlaying    bool        `json:"is_playing"`
	IsAutoDJ     bool        `json:"is_autodj,omitempty"`
	Queue        []TrackInfo `json:"queue"`
	Revision     int64       `json:"revision"`
}

// Participant describes an active user inside a room.
type Participant struct {
	UserID   string `json:"user_id"`
	Name     string `json:"name"`
	IsHost   bool   `json:"is_host"`
	IsCoHost bool   `json:"is_cohost"`
	JoinedAt int64  `json:"joined_at"`
}

// SyncStatePayload provides a complete snapshot of the room upon entry.
type SyncStatePayload struct {
	RoomCode     string        `json:"room_code"`
	UserID       string        `json:"user_id"`
	Name         string        `json:"name"`
	IsHost       bool          `json:"is_host"`
	HostID       string        `json:"host_id"`
	CoHostID     string        `json:"cohost_id"`
	Permanent    bool          `json:"permanent"`
	RoomName     string        `json:"room_name,omitempty"`
	Radio        bool          `json:"radio,omitempty"`
	RoomMode     string        `json:"room_mode"`
	Playback     PlaybackState `json:"playback"`
	Participants []Participant `json:"participants"`
	ChatHistory  []ChatMessage `json:"chat_history"`
}

// ErrorPayload describes a protocol or validation error.
// RetryAfterMs / RetryAtMs are set on "rate_limited" errors so clients can
// tell the user exactly when they may try again.
type ErrorPayload struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	RetryAfterMs int64  `json:"retry_after_ms,omitempty"`
	RetryAtMs    int64  `json:"retry_at_ms,omitempty"`
}

// SuggestTrackPayload allows guests to propose a track to the host.
type SuggestTrackPayload struct {
	Track *TrackInfo `json:"track"`
}

// SuggestionDecisionPayload represents approving or declining a track suggestion.
type SuggestionDecisionPayload struct {
	SuggestionID string `json:"suggestion_id"`
}

// SuggestionReceivedPayload is sent to the host when a track is proposed.
type SuggestionReceivedPayload struct {
	SuggestionID string     `json:"suggestion_id"`
	FromUserID   string     `json:"from_user_id"`
	FromUserName string     `json:"from_user_name"`
	Track        *TrackInfo `json:"track"`
}

// MaxSharedQueue caps how many upcoming tracks the admin shares with the room.
const MaxSharedQueue = 5

// QueueUpdatePayload is sent by the main host to publish their upcoming queue.
type QueueUpdatePayload struct {
	Queue []TrackInfo `json:"queue"`
}
