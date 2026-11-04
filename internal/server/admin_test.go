package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Clash-Projects/lastwave-jam/internal/protocol"
	"github.com/Clash-Projects/lastwave-jam/internal/sync"
)

type adminEnv struct {
	t      *testing.T
	hub    *sync.Hub
	mux    *http.ServeMux
	cookie *http.Cookie
}

func newAdminEnv(t *testing.T) *adminEnv {
	t.Setenv("JAM_ADMIN_PASSWORD", "pw-123")
	hub := sync.NewHub()
	hub.EnsurePermanentRooms()
	mux := http.NewServeMux()
	NewHandler(hub).RegisterRoutes(mux)
	return &adminEnv{t: t, hub: hub, mux: mux}
}

func (e *adminEnv) do(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "jam.example"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if e.cookie != nil {
		req.AddCookie(e.cookie)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

var xhr = map[string]string{"X-Requested-With": "lwjam", "Content-Type": "application/json"}

func (e *adminEnv) login() {
	rec := e.do("POST", "/admin/login", `{"password":"pw-123"}`, xhr)
	if rec.Code != 200 {
		e.t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == adminCookie {
			e.cookie = c
		}
	}
	if e.cookie == nil {
		e.t.Fatal("no session cookie")
	}
}

func TestAdmin_LoginAndAccessControl(t *testing.T) {
	e := newAdminEnv(t)

	if rec := e.do("GET", "/admin", "", nil); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/admin/login" {
		t.Fatalf("dashboard without login should redirect, got %d", rec.Code)
	}
	if rec := e.do("GET", "/admin/api/rooms", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("api without login should be 401, got %d", rec.Code)
	}
	if rec := e.do("GET", "/admin/admin.js", "", nil); rec.Code == 200 {
		t.Fatal("dashboard script must need a login")
	}
	if rec := e.do("GET", "/admin/login", "", nil); rec.Code != 200 || !strings.Contains(rec.Header().Get("X-Robots-Tag"), "noindex") {
		t.Fatal("login page should load and be noindex")
	}
	if rec := e.do("POST", "/admin/login", `{"password":"pw-123"}`, map[string]string{"Content-Type": "application/json"}); rec.Code != http.StatusForbidden {
		t.Fatal("login without the X-Requested-With header must be refused")
	}
	if rec := e.do("POST", "/admin/login", `{"password":"nope"}`, xhr); rec.Code != http.StatusUnauthorized {
		t.Fatal("wrong password must be refused")
	}

	e.login()
	c := e.cookie
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/admin" {
		t.Fatalf("cookie flags wrong: %+v", c)
	}
	if rec := e.do("GET", "/admin", "", nil); rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatal("dashboard should load with a strict CSP")
	}

	// Cross-site requests are refused even with a valid session.
	if rec := e.do("POST", "/admin/api/rooms/INDIA/auto", "{}", map[string]string{"Content-Type": "application/json"}); rec.Code != http.StatusForbidden {
		t.Fatal("POST without X-Requested-With must be refused")
	}
	if rec := e.do("POST", "/admin/api/rooms/INDIA/auto", "{}", map[string]string{"X-Requested-With": "lwjam", "Origin": "https://evil.example"}); rec.Code != http.StatusForbidden {
		t.Fatal("POST from another origin must be refused")
	}

	// Logout ends the session.
	e.do("POST", "/admin/logout", "{}", xhr)
	if rec := e.do("GET", "/admin/api/rooms", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatal("session should end at logout")
	}
}

func TestAdmin_LoginIsRateLimited(t *testing.T) {
	e := newAdminEnv(t)
	for i := 0; i < adminLoginMax; i++ {
		e.do("POST", "/admin/login", `{"password":"nope"}`, xhr)
	}
	if rec := e.do("POST", "/admin/login", `{"password":"pw-123"}`, xhr); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt should be blocked, got %d", rec.Code)
	}
}

func TestAdmin_DisabledWithoutPassword(t *testing.T) {
	t.Setenv("JAM_ADMIN_PASSWORD", "")
	hub := sync.NewHub()
	mux := http.NewServeMux()
	NewHandler(hub).RegisterRoutes(mux)
	req := httptest.NewRequest("POST", "/admin/login", strings.NewReader(`{"password":""}`))
	req.Header.Set("X-Requested-With", "lwjam")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no password configured must disable login, got %d", rec.Code)
	}
}

func TestAdmin_ListGiveTakeAutoAndChat(t *testing.T) {
	e := newAdminEnv(t)
	e.login()

	india, _ := e.hub.GetRoom("INDIA")
	a, b := sync.NewClient(nil, e.hub), sync.NewClient(nil, e.hub)
	_ = india.Join(a, "Aarav")
	_ = india.Join(b, "Bela")
	india.HandleChat(a, "hello station", "")

	owner := sync.NewClient(nil, e.hub)
	guest := sync.NewClient(nil, e.hub)
	private, _ := e.hub.CreateRoom(owner, "Owner", protocol.RoomModeHostOnly)
	_ = private.Join(guest, "Guest Person")

	// Every active Jam is listed, stations first.
	rec := e.do("GET", "/admin/api/rooms", "", nil)
	var list struct {
		Rooms []sync.RoomView `json:"rooms"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Rooms) != 3 || !list.Rooms[0].Station || list.Rooms[2].Code != private.Code {
		t.Fatalf("unexpected room list: %+v", list.Rooms)
	}

	// Chat is visible.
	rec = e.do("GET", "/admin/api/rooms/INDIA/chat", "", nil)
	if !strings.Contains(rec.Body.String(), "hello station") {
		t.Fatal("admin should see the room chat")
	}

	// Give admin on a station: auto radio pauses.
	rec = e.do("POST", "/admin/api/rooms/INDIA/host", `{"userId":"`+b.ID+`"}`, xhr)
	var v sync.RoomView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if rec.Code != 200 || v.HostID != b.ID || v.StationAuto {
		t.Fatalf("give admin failed: %d %+v", rec.Code, v)
	}
	// Make auto: admin removed, radio back on.
	rec = e.do("POST", "/admin/api/rooms/INDIA/auto", "{}", xhr)
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.HostID != "" || !v.StationAuto {
		t.Fatalf("make auto failed: %+v", v)
	}

	// Private Jam: give host to the guest, then take it away.
	rec = e.do("POST", "/admin/api/rooms/"+private.Code+"/host", `{"userId":"`+guest.ID+`"}`, xhr)
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.HostID != guest.ID {
		t.Fatalf("give host in a private Jam failed: %+v", v)
	}
	rec = e.do("POST", "/admin/api/rooms/"+private.Code+"/unhost", "{}", xhr)
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.HostID != "" {
		t.Fatal("take host failed")
	}
	if rec := e.do("POST", "/admin/api/rooms/"+private.Code+"/auto", "{}", xhr); rec.Code != http.StatusBadRequest {
		t.Fatal("private Jams have no auto mode")
	}

	// Unknown listener / room.
	if rec := e.do("POST", "/admin/api/rooms/INDIA/host", `{"userId":"usr_nobody"}`, xhr); rec.Code != http.StatusNotFound {
		t.Fatal("unknown listener should be 404")
	}
	if rec := e.do("GET", "/admin/api/rooms/ZZZZZZ/chat", "", nil); rec.Code != http.StatusNotFound {
		t.Fatal("unknown room should be 404")
	}

	// The chat shows what happened ("Station" lines).
	rec = e.do("GET", "/admin/api/rooms/INDIA/chat", "", nil)
	if !strings.Contains(rec.Body.String(), "is now the station admin") || !strings.Contains(rec.Body.String(), "back on auto") {
		t.Fatal("station changes should be announced in chat")
	}
}

func TestAdmin_StationAdminLeavingReturnsToAuto(t *testing.T) {
	e := newAdminEnv(t)
	e.login()
	world, _ := e.hub.GetRoom("WORLD")
	a := sync.NewClient(nil, e.hub)
	_ = world.Join(a, "Aarav")
	e.do("POST", "/admin/api/rooms/WORLD/host", `{"userId":"`+a.ID+`"}`, xhr)
	if world.IsStationAuto() {
		t.Fatal("station should be under admin control")
	}
	world.Leave(a)
	if !world.IsStationAuto() || world.HostID() != "" {
		t.Fatal("admin leaving must return the station to auto")
	}
}
