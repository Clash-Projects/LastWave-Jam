package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	gosync "sync"
	"time"

	"github.com/Clash-Projects/lastwave-jam/internal/sync"
)

// Admin panel for every active Jam (private rooms and the live stations):
// see listeners, now playing and chat; give / take admin; put a station on auto.
//
// Password: JAM_ADMIN_PASSWORD in the server's environment (never in the repo).
// Session: random token in an HttpOnly, SameSite=Strict cookie scoped to /admin, 8 h.
// Login: 5 attempts per 15 min per IP. State-changing calls need the
// X-Requested-With header and a same-origin Origin.

const (
	adminCookie      = "jam_admin"
	adminSessionTTL  = 8 * time.Hour
	adminLoginWindow = 15 * time.Minute
	adminLoginMax    = 5
)

const adminCSP = "default-src 'self'; img-src 'self' https://i.ytimg.com data:; style-src 'self'; script-src 'self'; " +
	"connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

type adminAuth struct {
	password string
	mu       gosync.Mutex
	sessions map[string]time.Time // sha256(token) -> expiry
	attempts map[string][]time.Time
}

func newAdminAuth() *adminAuth {
	return &adminAuth{
		password: os.Getenv("JAM_ADMIN_PASSWORD"),
		sessions: map[string]time.Time{},
		attempts: map[string][]time.Time{},
	}
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

func (a *adminAuth) newSession() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for k, exp := range a.sessions {
		if now.After(exp) {
			delete(a.sessions, k)
		}
	}
	a.sessions[hashToken(token)] = now.Add(adminSessionTTL)
	return token
}

func (a *adminAuth) valid(r *http.Request) bool {
	c, err := r.Cookie(adminCookie)
	if err != nil || len(c.Value) != 64 {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.sessions[hashToken(c.Value)]
	return ok && time.Now().Before(exp)
}

func (a *adminAuth) drop(r *http.Request) {
	if c, err := r.Cookie(adminCookie); err == nil {
		a.mu.Lock()
		delete(a.sessions, hashToken(c.Value))
		a.mu.Unlock()
	}
}

// allowAttempt records a login attempt for ip; false once the limit is reached.
func (a *adminAuth) allowAttempt(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	recent := a.attempts[ip][:0]
	for _, t := range a.attempts[ip] {
		if now.Sub(t) < adminLoginWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) >= adminLoginMax {
		a.attempts[ip] = recent
		return false
	}
	a.attempts[ip] = append(recent, now)
	return true
}

func (a *adminAuth) passwordOK(given string) bool {
	if a.password == "" {
		return false
	}
	x := sha256.Sum256([]byte(given))
	y := sha256.Sum256([]byte(a.password))
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}

// clientIP trusts X-Real-IP only from this machine (our nginx overwrites it).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" && net.ParseIP(real) != nil {
			return real
		}
	}
	return host
}

func sameOriginPost(r *http.Request) bool {
	if r.Header.Get("X-Requested-With") != "lwjam" {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Host != r.Host {
			return false
		}
	}
	return true
}

func adminHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", adminCSP)
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	w.Header().Set("Cache-Control", "no-store")
}

func adminJSON(w http.ResponseWriter, status int, v any) {
	adminHeaders(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func serveAdminFile(w http.ResponseWriter, name, contentType string) {
	data, err := embeddedWebFS.ReadFile("web/" + name)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	adminHeaders(w)
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(data)
}

func (h *Handler) registerAdminRoutes(mux *http.ServeMux) {
	a := h.admin
	need := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !a.valid(r) {
				if strings.HasPrefix(r.URL.Path, "/admin/api/") || r.Method != http.MethodGet {
					adminJSON(w, http.StatusUnauthorized, map[string]string{"error": "Not logged in"})
					return
				}
				http.Redirect(w, r, "/admin/login", http.StatusFound)
				return
			}
			if r.Method == http.MethodPost && !sameOriginPost(r) {
				adminJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
				return
			}
			next(w, r)
		}
	}
	room := func(w http.ResponseWriter, r *http.Request) (*sync.Room, bool) {
		rm, ok := h.hub.GetRoom(r.PathValue("code"))
		if !ok {
			adminJSON(w, http.StatusNotFound, map[string]string{"error": "That Jam has ended"})
		}
		return rm, ok
	}

	mux.HandleFunc("GET /admin/login", func(w http.ResponseWriter, r *http.Request) {
		if a.valid(r) {
			http.Redirect(w, r, "/admin", http.StatusFound)
			return
		}
		serveAdminFile(w, "admin-login.html", "text/html; charset=utf-8")
	})
	mux.HandleFunc("GET /admin/login.js", func(w http.ResponseWriter, r *http.Request) {
		serveAdminFile(w, "admin-login.js", "application/javascript; charset=utf-8")
	})
	mux.HandleFunc("GET /admin/admin.css", func(w http.ResponseWriter, r *http.Request) {
		serveAdminFile(w, "admin.css", "text/css; charset=utf-8")
	})

	mux.HandleFunc("POST /admin/login", func(w http.ResponseWriter, r *http.Request) {
		if !sameOriginPost(r) {
			adminJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
			return
		}
		if !a.allowAttempt(clientIP(r)) {
			adminJSON(w, http.StatusTooManyRequests, map[string]string{"error": "Too many attempts. Try again in 15 minutes."})
			return
		}
		if a.password == "" {
			adminJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Admin login is not set up on this server."})
			return
		}
		var body struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&body); err != nil || !a.passwordOK(body.Password) {
			adminJSON(w, http.StatusUnauthorized, map[string]string{"error": "Wrong password"})
			return
		}
		secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
		http.SetCookie(w, &http.Cookie{
			Name: adminCookie, Value: a.newSession(), Path: "/admin",
			MaxAge: int(adminSessionTTL.Seconds()), HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode,
		})
		adminJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	mux.HandleFunc("POST /admin/logout", need(func(w http.ResponseWriter, r *http.Request) {
		a.drop(r)
		http.SetCookie(w, &http.Cookie{Name: adminCookie, Value: "", Path: "/admin", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		adminJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	dashboard := need(func(w http.ResponseWriter, r *http.Request) {
		serveAdminFile(w, "admin.html", "text/html; charset=utf-8")
	})
	mux.HandleFunc("GET /admin", dashboard)
	mux.HandleFunc("GET /admin/{$}", dashboard)
	mux.HandleFunc("GET /admin/admin.js", need(func(w http.ResponseWriter, r *http.Request) {
		serveAdminFile(w, "admin.js", "application/javascript; charset=utf-8")
	}))

	mux.HandleFunc("GET /admin/api/rooms", need(func(w http.ResponseWriter, r *http.Request) {
		adminJSON(w, http.StatusOK, map[string]any{
			"rooms": h.hub.RoomViews(),
			"ended": h.hub.ArchivedRooms(),
			"now":   time.Now().UnixMilli(),
		})
	}))
	mux.HandleFunc("GET /admin/api/rooms/{code}/chat", need(func(w http.ResponseWriter, r *http.Request) {
		if rm, ok := room(w, r); ok {
			adminJSON(w, http.StatusOK, map[string]any{"messages": rm.ChatAll()})
		}
	}))
	mux.HandleFunc("GET /admin/api/ended/{id}/chat", need(func(w http.ResponseWriter, r *http.Request) {
		msgs, ok := h.hub.ArchivedChat(r.PathValue("id"))
		if !ok {
			adminJSON(w, http.StatusNotFound, map[string]string{"error": "That history has expired"})
			return
		}
		adminJSON(w, http.StatusOK, map[string]any{"messages": msgs})
	}))
	mux.HandleFunc("POST /admin/api/rooms/{code}/host", need(func(w http.ResponseWriter, r *http.Request) {
		rm, ok := room(w, r)
		if !ok {
			return
		}
		var body struct {
			UserID string `json:"userId"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body)
		if err := rm.AdminGiveHost(body.UserID); err != nil {
			adminJSON(w, http.StatusNotFound, map[string]string{"error": "That listener has left"})
			return
		}
		adminJSON(w, http.StatusOK, rm.View())
	}))
	mux.HandleFunc("POST /admin/api/rooms/{code}/unhost", need(func(w http.ResponseWriter, r *http.Request) {
		if rm, ok := room(w, r); ok {
			_ = rm.AdminTakeHost()
			adminJSON(w, http.StatusOK, rm.View())
		}
	}))
	mux.HandleFunc("POST /admin/api/rooms/{code}/auto", need(func(w http.ResponseWriter, r *http.Request) {
		rm, ok := room(w, r)
		if !ok {
			return
		}
		if err := rm.AdminMakeAuto(); errors.Is(err, sync.ErrNotStation) {
			adminJSON(w, http.StatusBadRequest, map[string]string{"error": "Only live stations have auto mode"})
			return
		}
		adminJSON(w, http.StatusOK, rm.View())
	}))
}
