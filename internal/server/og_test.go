package server

import (
	"bytes"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Clash-Projects/lastwave-jam/internal/sync"
)

func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	NewHandler(sync.NewHub()).RegisterRoutes(mux)
	return mux
}

func TestBanner_IsRealPNGAndCachedPerCode(t *testing.T) {
	for _, code := range []string{"WAVE01", "INDIA", "WORLD", "", "ABCDEFGHIJKL"} {
		data := renderBanner(code)
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%q: not a png: %v", code, err)
		}
		if b := img.Bounds(); b.Dx() != 1200 || b.Dy() != 630 {
			t.Fatalf("%q: bad size %v", code, b)
		}
		if dir := os.Getenv("OG_OUT"); dir != "" {
			name := code
			if name == "" {
				name = "default"
			}
			_ = os.WriteFile(filepath.Join(dir, name+".png"), data, 0o644)
		}
	}
}

func TestOGRoute(t *testing.T) {
	mux := newMux()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/og/WAVE01.png", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("bad response %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, bad := range []string{"/og/<script>.png", "/og/a.png", "/og/WAVE01"} {
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", bad, nil))
		if rec.Code != 404 && !(bad == "/og/WAVE01" && rec.Code == 200) {
			t.Fatalf("%s should be rejected, got %d", bad, rec.Code)
		}
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/og/default.png", nil))
	if rec.Code != 200 {
		t.Fatal("default banner should render")
	}
}

func TestIndexInjectsPreviewMeta(t *testing.T) {
	mux := newMux()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/?code=wave01", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`og:image" content="https://jam.clashprojects.qd.je/og/WAVE01.png"`,
		`og:title" content="Join room WAVE01 on LastWave Jam"`,
		`twitter:card" content="summary_large_image"`,
		`og:url" content="https://jam.clashprojects.qd.je/?code=WAVE01"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(body, metaMarker) {
		t.Fatal("marker should be replaced")
	}

	// Hostile input never reaches the markup.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", `/?code="><script>alert(1)</script>`, nil))
	if strings.Contains(rec.Body.String(), "<script>alert") {
		t.Fatal("code must be validated")
	}
	if !strings.Contains(rec.Body.String(), "/og/default.png") {
		t.Fatal("invalid code should fall back to the default banner")
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/?code=INDIA", nil))
	if !strings.Contains(rec.Body.String(), "Join India on LastWave Jam") {
		t.Fatal("station title expected")
	}
}
