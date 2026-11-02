package server

import (
	"bytes"
	_ "embed"
	"html"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"os"
	"regexp"
	"strings"
	gosync "sync"

	"github.com/Clash-Projects/lastwave-jam/internal/sync"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

//go:embed assets/logo.png
var logoPNG []byte

const (
	defaultBaseURL = "https://jam.clashprojects.qd.je"
	ogWidth        = 1200
	ogHeight       = 630
	maxOGCache     = 128
	metaMarker     = "<!--LW_META-->"
)

var (
	codeRe = regexp.MustCompile(`^[A-Z0-9]{3,12}$`)

	boldFont    *opentype.Font
	regularFont *opentype.Font
	logoImg     image.Image

	ogMu    gosync.Mutex
	ogCache = map[string][]byte{}
)

func init() {
	boldFont, _ = opentype.Parse(gobold.TTF)
	regularFont, _ = opentype.Parse(goregular.TTF)
	logoImg, _ = png.Decode(bytes.NewReader(logoPNG))
}

// publicBaseURL is the externally visible origin used in link previews.
// Set PUBLIC_BASE_URL when hosting somewhere other than the default.
func publicBaseURL() string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL")), "/"); v != "" {
		return v
	}
	return defaultBaseURL
}

// normalizeCode returns a safe upper-case room code, or "" when invalid.
func normalizeCode(raw string) string {
	c := strings.ToUpper(strings.TrimSpace(raw))
	if codeRe.MatchString(c) {
		return c
	}
	return ""
}

// stationName returns "India" / "International" for the always-on rooms.
func stationName(code string) string {
	for _, pr := range sync.PermanentRooms {
		if pr.Code == code {
			return pr.Name
		}
	}
	return ""
}

// renderMeta builds the <head> tags that make chat apps (Telegram, WhatsApp,
// Discord, X) show a rich preview. Crawlers do not run JavaScript, so this is
// rendered on the server.
func renderMeta(code string) string {
	base := publicBaseURL()
	title := "LastWave Jam"
	desc := "Listen together in real time with LastWave."
	pageURL := base + "/"
	image := base + "/og/default.png"
	if code != "" {
		title = "Join room " + code + " on LastWave Jam"
		desc = "Tap to join room " + code + " and listen together in real time."
		if name := stationName(code); name != "" {
			title = "Join " + name + " on LastWave Jam"
			desc = name + " is an always-on room. Tap to join and listen together."
		}
		pageURL = base + "/?code=" + code
		image = base + "/og/" + code + ".png"
	}
	e := html.EscapeString
	var b strings.Builder
	b.WriteString(`<meta name="description" content="` + e(desc) + `">` + "\n")
	b.WriteString(`<meta name="theme-color" content="#000000">` + "\n")
	b.WriteString(`<meta property="og:type" content="website">` + "\n")
	b.WriteString(`<meta property="og:site_name" content="LastWave Jam">` + "\n")
	b.WriteString(`<meta property="og:title" content="` + e(title) + `">` + "\n")
	b.WriteString(`<meta property="og:description" content="` + e(desc) + `">` + "\n")
	b.WriteString(`<meta property="og:url" content="` + e(pageURL) + `">` + "\n")
	b.WriteString(`<meta property="og:image" content="` + e(image) + `">` + "\n")
	b.WriteString(`<meta property="og:image:type" content="image/png">` + "\n")
	b.WriteString(`<meta property="og:image:width" content="1200">` + "\n")
	b.WriteString(`<meta property="og:image:height" content="630">` + "\n")
	b.WriteString(`<meta property="og:image:alt" content="` + e(title) + `">` + "\n")
	b.WriteString(`<meta name="twitter:card" content="summary_large_image">` + "\n")
	b.WriteString(`<meta name="twitter:title" content="` + e(title) + `">` + "\n")
	b.WriteString(`<meta name="twitter:description" content="` + e(desc) + `">` + "\n")
	b.WriteString(`<meta name="twitter:image" content="` + e(image) + `">` + "\n")
	b.WriteString(`<link rel="canonical" href="` + e(pageURL) + `">` + "\n")
	return b.String()
}

// handleOG serves the preview banner: /og/WAVE01.png (or /og/default.png).
func (h *Handler) handleOG(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSuffix(r.PathValue("file"), ".png")
	code := normalizeCode(name)
	if code == "" && name != "default" {
		http.NotFound(w, r)
		return
	}
	data, ok := cachedBanner(code)
	if !ok {
		// Too many banners rendering at once (e.g. random-code scraping).
		w.Header().Set("Retry-After", "5")
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// renderSlots bounds concurrent banner renders; cache hits never wait.
var renderSlots = make(chan struct{}, 2)

func cachedBanner(code string) ([]byte, bool) {
	ogMu.Lock()
	if b, ok := ogCache[code]; ok {
		ogMu.Unlock()
		return b, true
	}
	ogMu.Unlock()

	select {
	case renderSlots <- struct{}{}:
		defer func() { <-renderSlots }()
	default:
		return nil, false
	}
	b := renderBanner(code)

	ogMu.Lock()
	if len(ogCache) >= maxOGCache {
		ogCache = map[string][]byte{} // bounded: random codes can't grow memory
	}
	ogCache[code] = b
	ogMu.Unlock()
	return b, true
}

var (
	colBlack = color.RGBA{0, 0, 0, 255}
	colLime  = color.RGBA{0xC9, 0xFB, 0x00, 255}
	colWhite = color.RGBA{255, 255, 255, 255}
	colGray  = color.RGBA{0x8E, 0x8E, 0x9A, 255}
	colLine  = color.RGBA{0x24, 0x26, 0x2B, 255}
)

func newFace(f *opentype.Font, px float64) font.Face {
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil
	}
	return face
}

func textWidth(face font.Face, s string, spacing int) int {
	w := 0
	for _, r := range s {
		adv, _ := face.GlyphAdvance(r)
		w += adv.Round() + spacing
	}
	if w > 0 {
		w -= spacing
	}
	return w
}

func drawText(dst draw.Image, face font.Face, s string, x, baseline, spacing int, c color.Color) {
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: face}
	for _, r := range s {
		d.Dot = fixed.P(x, baseline)
		d.DrawString(string(r))
		adv, _ := face.GlyphAdvance(r)
		x += adv.Round() + spacing
	}
}

// renderBanner draws the 1200x630 preview: black background, LastWave
// signature, and the room code in large lime type.
func renderBanner(code string) []byte {
	img := image.NewRGBA(image.Rect(0, 0, ogWidth, ogHeight))
	draw.Draw(img, img.Bounds(), image.NewUniform(colBlack), image.Point{}, draw.Src)

	// Signature: logo + wordmark, top left.
	if logoImg != nil {
		const logoSize = 96
		dstRect := image.Rect(80, 64, 80+logoSize, 64+logoSize)
		xdraw.CatmullRom.Scale(img, dstRect, logoImg, logoImg.Bounds(), xdraw.Over, nil)
	}
	if f := newFace(boldFont, 52); f != nil {
		drawText(img, f, "LastWave", 196, 130, 0, colWhite)
		w := textWidth(f, "LastWave", 0)
		if f2 := newFace(regularFont, 52); f2 != nil {
			drawText(img, f2, "Jam", 196+w+16, 130, 0, colLime)
		}
	}

	label := "JOIN MY SESSION"
	sub := "Open LastWave and enter the code"
	big := code
	if name := stationName(code); name != "" {
		label = "ALWAYS-ON ROOM"
		sub = name + " room - open to everyone, join anytime"
	}
	if code == "" {
		label = "LISTEN TOGETHER"
		big = "JAM"
		sub = "Real-time synced listening with friends"
	}

	if f := newFace(boldFont, 30); f != nil {
		drawText(img, f, label, 80, 268, 6, colGray)
	}

	// Room code: shrink to fit the width.
	px := 190.0
	var face font.Face
	for ; px >= 90; px -= 10 {
		face = newFace(boldFont, px)
		if face != nil && textWidth(face, big, 10) <= ogWidth-160 {
			break
		}
	}
	if face != nil {
		drawText(img, face, big, 80, 452, 10, colLime)
	}

	if f := newFace(regularFont, 38); f != nil {
		drawText(img, f, sub, 80, 524, 0, colWhite)
	}

	// Footer.
	draw.Draw(img, image.Rect(80, 560, ogWidth-80, 562), image.NewUniform(colLine), image.Point{}, draw.Src)
	if f := newFace(regularFont, 26); f != nil {
		drawText(img, f, "Listen together in real time", 80, 604, 0, colGray)
		host := strings.TrimPrefix(strings.TrimPrefix(publicBaseURL(), "https://"), "http://")
		drawText(img, f, host, ogWidth-80-textWidth(f, host, 0), 604, 0, colGray)
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
