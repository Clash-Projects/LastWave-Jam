package sync

import (
	"crypto/sha256"
	"crypto/subtle"
	"strings"
	"time"

	"github.com/Clash-Projects/lastwave-jam/internal/protocol"
)

// Station admin for the 24/7 radio rooms.
//
//   /admin <code>  in chat: you become the station admin; the auto radio pauses
//                  and your player drives the station for everyone.
//   /auto <code>   in chat: the station goes back to auto radio.
//
// The command line is shown to the room with the code masked. The code lives
// only in the server's environment (RADIO_ADMIN_CODE), never in the apps.
// The admin can hand admin to someone else (transfer_host); there are no
// co-hosts on stations. When the admin leaves, the station returns to auto.

const (
	cmdAttemptLimit  = 5
	cmdAttemptWindow = 10 * time.Minute
	stationUserID    = "station"
	stationUserName  = "Station"
	maskedCode       = "••••••"
)

// SetRadioAdminCode sets the station-admin code on every radio room ("" disables commands).
func (h *Hub) SetRadioAdminCode(code string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, room := range h.rooms {
		if room.Radio {
			room.mu.Lock()
			room.adminCode = strings.TrimSpace(code)
			room.mu.Unlock()
		}
	}
}

func codeMatches(given, want string) bool {
	if want == "" {
		return false
	}
	a := sha256.Sum256([]byte(given))
	b := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// parseStationCommand returns ("admin"|"auto", code) for a station command, or "".
func parseStationCommand(text string) (string, string) {
	f := strings.Fields(text)
	if len(f) == 0 {
		return "", ""
	}
	switch strings.ToLower(f[0]) {
	case "/admin":
		return "admin", strings.Join(f[1:], " ")
	case "/auto":
		return "auto", strings.Join(f[1:], " ")
	}
	return "", ""
}

// wakeRadio nudges the radio loop after a mode change.
func (r *Room) wakeRadio() {
	select {
	case r.radioWake <- struct{}{}:
	default:
	}
}

// IsStationAuto reports whether a radio room is on auto (no admin).
func (r *Room) IsStationAuto() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return !r.radioManual
}

// postSystemChat adds a "Station" line to the chat.
func (r *Room) postSystemChat(text string) {
	msg := protocol.ChatMessage{
		ID:        GenerateID("msg_"),
		UserID:    stationUserID,
		UserName:  stationUserName,
		Text:      text,
		Timestamp: time.Now().UnixMilli(),
	}
	r.chat.Add(msg)
	r.Broadcast(protocol.MsgTypeChatMessage, msg)
}

func (r *Room) announceStationMode(admin *Client) {
	hostID, adminName := "", ""
	if admin != nil {
		hostID, adminName = admin.ID, admin.Name()
	}
	r.Broadcast(protocol.MsgTypeHostChanged, map[string]any{
		"new_host_id":   hostID,
		"new_host_name": adminName,
		"user_id":       hostID,
		"name":          adminName,
		"is_host":       admin != nil,
		"host_id":       hostID,
		"cohost_id":     "",
	})
	r.Broadcast(protocol.MsgTypeStationMode, protocol.StationModePayload{
		Auto:      admin == nil,
		AdminID:   hostID,
		AdminName: adminName,
	})
}

// handleStationCommand runs a /admin or /auto chat command. The masked command
// line has already been posted to the chat.
func (r *Room) handleStationCommand(c *Client, cmd, code string) {
	now := r.now()
	r.mu.Lock()
	if r.adminCode == "" {
		r.mu.Unlock()
		c.SendError("commands_disabled", "Station commands are not set up on this server.")
		return
	}
	recent := r.cmdAttempts[c.ID][:0]
	for _, t := range r.cmdAttempts[c.ID] {
		if now.Sub(t) < cmdAttemptWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) >= cmdAttemptLimit {
		r.cmdAttempts[c.ID] = recent
		r.mu.Unlock()
		c.SendError("too_many_attempts", "Too many wrong codes. Try again later.")
		return
	}
	if !codeMatches(code, r.adminCode) {
		r.cmdAttempts[c.ID] = append(recent, now)
		r.mu.Unlock()
		c.SendError("wrong_code", "Wrong code.")
		return
	}
	delete(r.cmdAttempts, c.ID)

	if cmd == "admin" {
		r.host = c
		r.coHost = nil
		r.radioManual = true
		r.mu.Unlock()
		r.announceStationMode(c)
		r.postSystemChat(c.Name() + " is now the station admin. Auto radio is paused.")
		r.wakeRadio()
		return
	}

	wasManual := r.radioManual
	r.host = nil
	r.coHost = nil
	r.radioManual = false
	r.mu.Unlock()
	r.announceStationMode(nil)
	if wasManual {
		r.postSystemChat(c.Name() + " switched the station back to auto.")
	}
	r.wakeRadio()
}

// TransferStationAdmin lets the station admin make someone else admin.
func (r *Room) TransferStationAdmin(c *Client, targetID string) {
	r.mu.Lock()
	if !r.Radio {
		r.mu.Unlock()
		c.SendError("not_available", "Admin transfer is only for live stations.")
		return
	}
	if r.host == nil || r.host.ID != c.ID || !r.radioManual {
		r.mu.Unlock()
		c.SendError("unauthorized", "Only the station admin can do that.")
		return
	}
	target, ok := r.participants[targetID]
	if !ok || targetID == c.ID {
		r.mu.Unlock()
		c.SendError("invalid_target", "Pick someone else in the room.")
		return
	}
	r.host = target
	r.mu.Unlock()
	r.announceStationMode(target)
	r.postSystemChat(c.Name() + " made " + target.Name() + " the station admin.")
}

// stationAdminLeftLocked is called from Leave with r.mu held; it reports whether
// the leaving client was the station admin (the station then returns to auto).
func (r *Room) stationAdminLeftLocked(c *Client) bool {
	delete(r.cmdAttempts, c.ID)
	if r.host == nil || r.host.ID != c.ID {
		return false
	}
	r.host = nil
	r.coHost = nil
	r.radioManual = false
	return true
}
