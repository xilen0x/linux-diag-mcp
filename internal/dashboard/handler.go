package dashboard

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
)

// MinTokenLength is the shortest access token NewHandler accepts.
const MinTokenLength = 32

// tokenBytes of crypto/rand output give a 43-character URL-safe token.
const tokenBytes = 32

//go:embed web/index.html web/app.js web/app.css
var web embed.FS

// contentSecurityPolicy allows only the page's own files and same-origin
// fetches: no inline script or style, no frames, no other hosts.
const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; " +
	"connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Snapshotter returns the current snapshot.
type Snapshotter interface {
	Snapshot(ctx context.Context) *Snapshot
}

// HandlerOptions configures NewHandler.
type HandlerOptions struct {
	// Token must be sent as "Authorization: Bearer <token>" to read data.
	Token string
	// Snapshots supplies the data served by /api/snapshot.
	Snapshots Snapshotter
	// RefreshInterval tells the page how often to poll.
	RefreshInterval time.Duration
	// AllowedHosts are the exact Host header values accepted, such as
	// "127.0.0.1:8123". Any other Host is refused, which blocks DNS
	// rebinding from a web page.
	AllowedHosts []string
}

type asset struct {
	path        string
	contentType string
}

var assets = map[string]asset{
	"/":        {"web/index.html", "text/html; charset=utf-8"},
	"/app.js":  {"web/app.js", "text/javascript; charset=utf-8"},
	"/app.css": {"web/app.css", "text/css; charset=utf-8"},
}

type handler struct {
	opts  HandlerOptions
	files map[string][]byte
}

// NewHandler returns the dashboard's HTTP handler. The page and its
// assets hold no data and are served to any allowed Host; data is only
// served to requests that carry the token.
func NewHandler(opts HandlerOptions) (http.Handler, error) {
	switch {
	case len(opts.Token) < MinTokenLength:
		return nil, errors.New("dashboard: token too short")
	case opts.Snapshots == nil:
		return nil, errors.New("dashboard: no snapshot source")
	case len(opts.AllowedHosts) == 0:
		return nil, errors.New("dashboard: no allowed hosts")
	case opts.RefreshInterval <= 0:
		return nil, errors.New("dashboard: refresh interval must be positive")
	}
	h := &handler{opts: opts, files: map[string][]byte{}}
	for route, a := range assets {
		b, err := web.ReadFile(a.path)
		if err != nil {
			return nil, err
		}
		h.files[route] = b
	}
	return h, nil
}

// GenerateToken returns a random URL-safe access token.
func GenerateToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hdr := w.Header()
	hdr.Set("Content-Security-Policy", contentSecurityPolicy)
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Referrer-Policy", "no-referrer")
	hdr.Set("X-Frame-Options", "DENY")
	hdr.Set("Cross-Origin-Resource-Policy", "same-origin")
	hdr.Set("Cache-Control", "no-store")

	if !slices.Contains(h.opts.AllowedHosts, r.Host) {
		http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		hdr.Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/api/snapshot" {
		h.serveSnapshot(w, r)
		return
	}
	a, ok := assets[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	hdr.Set("Content-Type", a.contentType)
	_, _ = w.Write(h.files[r.URL.Path])
}

func (h *handler) serveSnapshot(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := json.Marshal(struct {
		RefreshMS int64     `json:"refresh_ms"`
		Snapshot  *Snapshot `json:"snapshot"`
	}{h.opts.RefreshInterval.Milliseconds(), h.opts.Snapshots.Snapshot(r.Context())})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// authorized accepts only "Authorization: Bearer <token>", compared in
// constant time. Tokens in the URL are never read: they would end up in
// logs and history.
func (h *handler) authorized(r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(h.opts.Token)) == 1
}
