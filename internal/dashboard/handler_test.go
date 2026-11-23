package dashboard

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testToken = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFG"
	testHost  = "127.0.0.1:8123"
)

type stubSnapshots struct{ calls int }

func (s *stubSnapshots) Snapshot(context.Context) *Snapshot {
	s.calls++
	return &Snapshot{
		CapturedAt: time.Date(2026, 11, 23, 21, 0, 0, 0, time.UTC),
		Tools:      map[string]json.RawMessage{"memory_usage": json.RawMessage(`{"used_percent":32.06}`)},
		Services:   map[string]json.RawMessage{},
		Errors:     map[string]string{"disk_usage": "tool timed out"},
	}
}

func newTestHandler(t *testing.T) (http.Handler, *stubSnapshots) {
	t.Helper()
	snaps := &stubSnapshots{}
	h, err := NewHandler(HandlerOptions{
		Token:           testToken,
		Snapshots:       snaps,
		RefreshInterval: 5 * time.Second,
		AllowedHosts:    []string{testHost, "localhost:8123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, snaps
}

func do(h http.Handler, method, target, host, auth string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestStaticAssetsAreServedWithoutToken(t *testing.T) {
	h, snaps := newTestHandler(t)
	tests := []struct {
		path, contentType, contains string
	}{
		{"/", "text/html; charset=utf-8", `<script src="app.js"></script>`},
		{"/app.js", "text/javascript; charset=utf-8", "/api/snapshot"},
		{"/app.css", "text/css; charset=utf-8", "--accent"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := do(h, http.MethodGet, tt.path, testHost, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != tt.contentType {
				t.Errorf("content-type = %q, want %q", got, tt.contentType)
			}
			if !strings.Contains(rec.Body.String(), tt.contains) {
				t.Errorf("body does not contain %q", tt.contains)
			}
		})
	}
	if snaps.calls != 0 {
		t.Fatalf("static assets triggered %d snapshots", snaps.calls)
	}
}

func TestSnapshotRequiresTheBearerToken(t *testing.T) {
	h, snaps := newTestHandler(t)
	tests := []struct {
		name, target, auth string
	}{
		{"no header", "/api/snapshot", ""},
		{"wrong token", "/api/snapshot", "Bearer " + strings.Repeat("x", len(testToken))},
		{"token prefix only", "/api/snapshot", "Bearer " + testToken[:10]},
		{"token with suffix", "/api/snapshot", "Bearer " + testToken + "x"},
		{"wrong scheme", "/api/snapshot", "Basic " + testToken},
		{"token in query string", "/api/snapshot?token=" + testToken, ""},
		{"token in access_token parameter", "/api/snapshot?access_token=" + testToken, ""},
		{"lowercase scheme", "/api/snapshot", "bearer " + testToken},
		{"token as basic password", "/api/snapshot", "Basic " + base64.StdEncoding.EncodeToString([]byte("x:"+testToken))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(h, http.MethodGet, tt.target, testHost, tt.auth)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Errorf("WWW-Authenticate = %q, want Bearer", got)
			}
			if strings.Contains(rec.Body.String(), "memory_usage") {
				t.Error("unauthorized response leaked snapshot data")
			}
		})
	}
	if snaps.calls != 0 {
		t.Fatalf("unauthorized requests triggered %d snapshots", snaps.calls)
	}
}

func TestSnapshotWithTokenReturnsDataAndRefreshInterval(t *testing.T) {
	h, snaps := newTestHandler(t)

	rec := do(h, http.MethodGet, "/api/snapshot", testHost, "Bearer "+testToken)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("content-type = %q", got)
	}
	var body struct {
		RefreshMS int64    `json:"refresh_ms"`
		Snapshot  Snapshot `json:"snapshot"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.RefreshMS != 5000 {
		t.Errorf("refresh_ms = %d, want 5000", body.RefreshMS)
	}
	if string(body.Snapshot.Tools["memory_usage"]) != `{"used_percent":32.06}` {
		t.Errorf("memory_usage = %s", body.Snapshot.Tools["memory_usage"])
	}
	if body.Snapshot.Errors["disk_usage"] != "tool timed out" {
		t.Errorf("errors = %v", body.Snapshot.Errors)
	}
	if snaps.calls != 1 {
		t.Errorf("snapshots = %d, want 1", snaps.calls)
	}
}

func TestUnknownHostIsRejectedOnEveryRoute(t *testing.T) {
	h, snaps := newTestHandler(t)
	for _, path := range []string{"/", "/app.js", "/api/snapshot"} {
		for _, host := range []string{"evil.example:8123", "127.0.0.1:9999", "", "127.0.0.1", "LOCALHOST:8123", "localhost.:8123"} {
			rec := do(h, http.MethodGet, path, host, "Bearer "+testToken)
			if rec.Code != http.StatusMisdirectedRequest {
				t.Errorf("GET %s Host=%q: status = %d, want 421", path, host, rec.Code)
			}
		}
	}
	if snaps.calls != 0 {
		t.Fatalf("rejected hosts triggered %d snapshots", snaps.calls)
	}
}

func TestHostIsCheckedBeforeMethodAndPath(t *testing.T) {
	h, _ := newTestHandler(t)
	for _, c := range []struct{ method, path string }{{http.MethodPut, "/"}, {http.MethodGet, "/missing"}} {
		if rec := do(h, c.method, c.path, "evil.example", ""); rec.Code != http.StatusMisdirectedRequest {
			t.Errorf("%s %s with foreign Host: status = %d, want 421", c.method, c.path, rec.Code)
		}
	}
}

func TestAllowedHostAliasesAreAccepted(t *testing.T) {
	h, _ := newTestHandler(t)
	if rec := do(h, http.MethodGet, "/", "localhost:8123", ""); rec.Code != http.StatusOK {
		t.Fatalf("localhost alias: status = %d, want 200", rec.Code)
	}
}

func TestOnlyGetAndHeadAreAllowed(t *testing.T) {
	h, _ := newTestHandler(t)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		rec := do(h, m, "/api/snapshot", testHost, "Bearer "+testToken)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", m, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("%s: Allow = %q", m, got)
		}
	}
	if rec := do(h, http.MethodHead, "/", testHost, ""); rec.Code != http.StatusOK {
		t.Errorf("HEAD /: status = %d, want 200", rec.Code)
	}
}

func TestUnknownPathIsNotFound(t *testing.T) {
	h, _ := newTestHandler(t)
	for _, p := range []string{"/index.html", "/api", "/api/snapshot/", "/../etc/passwd", "/web/app.js"} {
		if rec := do(h, http.MethodGet, p, testHost, "Bearer "+testToken); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", p, rec.Code)
		}
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	h, _ := newTestHandler(t)
	want := map[string]string{
		"Content-Security-Policy":      "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
		"X-Content-Type-Options":       "nosniff",
		"Referrer-Policy":              "no-referrer",
		"X-Frame-Options":              "DENY",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Cache-Control":                "no-store",
	}
	cases := []struct{ method, path, host, auth string }{
		{http.MethodGet, "/", testHost, ""},
		{http.MethodGet, "/api/snapshot", testHost, "Bearer " + testToken},
		{http.MethodGet, "/api/snapshot", testHost, ""},
		{http.MethodGet, "/missing", testHost, ""},
		{http.MethodPost, "/", testHost, ""},
		{http.MethodGet, "/", "evil.example", ""},
	}
	for _, c := range cases {
		rec := do(h, c.method, c.path, c.host, c.auth)
		for k, v := range want {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("%s %s (%d): %s = %q, want %q", c.method, c.path, rec.Code, k, got, v)
			}
		}
	}
}

func TestNewHandlerRejectsWeakConfiguration(t *testing.T) {
	snaps := &stubSnapshots{}
	tests := []struct {
		name string
		opts HandlerOptions
	}{
		{"short token", HandlerOptions{Token: "short", Snapshots: snaps, RefreshInterval: time.Second, AllowedHosts: []string{testHost}}},
		{"no snapshots", HandlerOptions{Token: testToken, RefreshInterval: time.Second, AllowedHosts: []string{testHost}}},
		{"no hosts", HandlerOptions{Token: testToken, Snapshots: snaps, RefreshInterval: time.Second}},
		{"no interval", HandlerOptions{Token: testToken, Snapshots: snaps, AllowedHosts: []string{testHost}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewHandler(tt.opts); err == nil {
				t.Fatal("NewHandler accepted a weak configuration")
			}
		})
	}
}

func TestGenerateTokenIsLongAndUnique(t *testing.T) {
	a, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) < MinTokenLength {
		t.Fatalf("token length = %d, want >= %d", len(a), MinTokenLength)
	}
	if a == b {
		t.Fatal("two tokens are equal")
	}
	if strings.ContainsAny(a, "+/=#?&") {
		t.Fatalf("token %q is not URL-fragment safe", a)
	}
}
