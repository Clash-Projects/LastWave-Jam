package sync

import (
	"errors"
	"sort"
	"time"

	"github.com/Clash-Projects/lastwave-jam/internal/protocol"
)

// Room administration: used by the web admin panel (/admin) and by the in-app
// "Make admin" on live stations.
//
// On the 24/7 stations (INDIA, WORLD) an admin pauses the auto radio and their
// player drives the station; taking admin away (or "Make auto") returns the
// station to auto. There are no co-hosts on stations, and when the station
// admin leaves the station goes back to auto.

const (
	stationUserID   = "station"
	stationUserName = "Station"
)

var (
	ErrUserNotInRoom = errors.New("user not in room")
	ErrNotStation    = errors.New("not a live station")
)

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

// announceHost tells the room who the host/admin is now (admin == nil: nobody).
func (r *Room) announceHost(admin *Client, coHostID string) {
	hostID, name := "", ""
	if admin != nil {
		hostID, name = admin.ID, admin.Name()
	}
	r.Broadcast(protocol.MsgTypeHostChanged, map[string]any{
		"new_host_id":   hostID,
		"new_host_name": name,
		"user_id":       hostID,
		"name":          name,
		"is_host":       admin != nil,
		"host_id":       hostID,
		"cohost_id":     coHostID,
	})
}

func (r *Room) announceStationMode(admin *Client) {
	r.announceHost(admin, "")
	payload := protocol.StationModePayload{Auto: admin == nil}
	if admin != nil {
		payload.AdminID, payload.AdminName = admin.ID, admin.Name()
	}
	r.Broadcast(protocol.MsgTypeStationMode, payload)
}

// TransferStationAdmin lets the station admin make someone else admin (in-app).
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
	if r.host == nil || r.host.ID != c.ID {
		return false
	}
	r.host = nil
	r.coHost = nil
	r.radioManual = false
	return true
}

/* ------------------------------------------------------------------ */
/* Admin panel actions                                                 */
/* ------------------------------------------------------------------ */

// AdminGiveHost makes a participant the host (private rooms) or the station
// admin (live stations, which pauses the auto radio).
func (r *Room) AdminGiveHost(userID string) error {
	r.mu.Lock()
	target, ok := r.participants[userID]
	if !ok {
		r.mu.Unlock()
		return ErrUserNotInRoom
	}
	if r.Radio {
		r.host = target
		r.coHost = nil
		r.radioManual = true
		r.mu.Unlock()
		r.announceStationMode(target)
		r.postSystemChat(target.Name() + " is now the station admin.")
		r.wakeRadio()
		return nil
	}
	if r.coHost != nil && r.coHost.ID == userID {
		r.coHost = nil
	}
	r.host = target
	_, coHostID := r.hostIDsLocked()
	r.mu.Unlock()
	r.announceHost(target, coHostID)
	r.postSystemChat(target.Name() + " is now the host.")
	return nil
}

// AdminTakeHost removes the host/admin. Live stations go back to auto radio.
func (r *Room) AdminTakeHost() error {
	r.mu.Lock()
	if r.Radio {
		wasManual := r.radioManual
		r.host = nil
		r.coHost = nil
		r.radioManual = false
		r.mu.Unlock()
		r.announceStationMode(nil)
		if wasManual {
			r.postSystemChat("The station is back on auto.")
		}
		r.wakeRadio()
		return nil
	}
	if r.host == nil && r.coHost == nil {
		r.mu.Unlock()
		return nil
	}
	r.host = nil
	r.coHost = nil
	r.mu.Unlock()
	r.announceHost(nil, "")
	r.postSystemChat("The host was removed.")
	return nil
}

// AdminMakeAuto puts a live station back on auto radio.
func (r *Room) AdminMakeAuto() error {
	if !r.Radio {
		return ErrNotStation
	}
	return r.AdminTakeHost()
}

// ParticipantView is a listener as shown in the admin panel.
type ParticipantView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	IsHost   bool   `json:"isHost"`
	IsCoHost bool   `json:"isCoHost"`
}

// RoomView is a room as shown in the admin panel.
type RoomView struct {
	Code         string              `json:"code"`
	Name         string              `json:"name"`
	Station      bool                `json:"station"`
	StationAuto  bool                `json:"stationAuto"`
	Mode         string              `json:"mode"`
	CreatedAt    int64               `json:"createdAt"`
	HostID       string              `json:"hostId"`
	HostName     string              `json:"hostName"`
	CoHostID     string              `json:"coHostId"`
	Participants []ParticipantView   `json:"participants"`
	NowPlaying   *protocol.TrackInfo `json:"nowPlaying"`
	IsPlaying    bool                `json:"isPlaying"`
	PositionMs   int64               `json:"positionMs"`
	ChatCount    int                 `json:"chatCount"`
}

// View returns a consistent snapshot of the room for the admin panel.
func (r *Room) View() RoomView {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v := RoomView{
		Code:        r.Code,
		Name:        r.Name,
		Station:     r.Radio,
		StationAuto: r.Radio && !r.radioManual,
		Mode:        r.Mode,
		CreatedAt:   r.CreatedAt.UnixMilli(),
		IsPlaying:   r.state.IsPlaying,
		PositionMs:  CalculateCurrentPosition(&r.state, time.Now().UnixMilli()),
		ChatCount:   len(r.chat.History()),
	}
	if r.state.CurrentTrack != nil {
		t := *r.state.CurrentTrack
		v.NowPlaying = &t
	}
	if r.host != nil {
		v.HostID, v.HostName = r.host.ID, r.host.Name()
	}
	if r.coHost != nil {
		v.CoHostID = r.coHost.ID
	}
	for _, id := range r.joinedOrder {
		p, ok := r.participants[id]
		if !ok {
			continue
		}
		v.Participants = append(v.Participants, ParticipantView{
			ID:       p.ID,
			Name:     p.Name(),
			IsHost:   v.HostID == p.ID,
			IsCoHost: v.CoHostID == p.ID,
		})
	}
	if v.Participants == nil {
		v.Participants = []ParticipantView{}
	}
	return v
}

// RoomViews lists every active room: stations first, then busiest rooms.
func (h *Hub) RoomViews() []RoomView {
	h.mu.RLock()
	rooms := make([]*Room, 0, len(h.rooms))
	for _, r := range h.rooms {
		rooms = append(rooms, r)
	}
	h.mu.RUnlock()

	out := make([]RoomView, 0, len(rooms))
	for _, r := range rooms {
		out = append(out, r.View())
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Station != out[j].Station {
			return out[i].Station
		}
		if len(out[i].Participants) != len(out[j].Participants) {
			return len(out[i].Participants) > len(out[j].Participants)
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	return out
}
