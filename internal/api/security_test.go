package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/william-lbn/ops-observability-platform/internal/config"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCurrentAuthorizationAndCookieOrigin(t *testing.T) {
	token := strings.Repeat("a", 48)
	h := sha256.Sum256([]byte(token))
	path := filepath.Join(t.TempDir(), "config.json")
	c := config.Config{PublicURL: "https://ops.example", Principals: []config.Principal{{TokenSHA256: hex.EncodeToString(h[:]), Subject: "alice", Tenant: "team-a", Role: "viewer"}}, Sources: []config.Source{{ID: "a", Tenant: "team-a", Enabled: true}, {ID: "b", Tenant: "team-b", Enabled: true}}}
	save := func() {
		b, _ := json.Marshal(c)
		if e := os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	save()
	s, e := New(nil, path, http.NotFoundHandler())
	if e != nil {
		t.Fatal(e)
	}
	request := func(method, url, bearer, origin string, cookie bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, url, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		if cookie {
			r.AddCookie(&http.Cookie{Name: "ops_session", Value: token})
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/api/v1/sources", token, "", false); w.Code != 200 || strings.Contains(w.Body.String(), `"id":"b"`) {
		t.Fatalf("cross-tenant source leakage: %d %s", w.Code, w.Body)
	}
	if w := request("POST", "/api/v1/incidents/ack", "", "https://attacker.example", true); w.Code != 403 {
		t.Fatalf("cookie CSRF accepted: %d", w.Code)
	}
	if w := request("POST", "/api/v1/incidents/ack", token, "", false); w.Code != 403 {
		t.Fatalf("viewer mutation accepted: %d", w.Code)
	}
	c.Principals[0].Role = "admin"
	save()
	if w := request("GET", "/api/v1/platform/dashboard", token, "", false); w.Code != 403 {
		t.Fatalf("tenant administrator obtained platform data: %d", w.Code)
	}
	c.Principals[0].Disabled = true
	save()
	if w := request("GET", "/api/v1/me", token, "", false); w.Code != 401 {
		t.Fatalf("revoked principal remains authorized: %d", w.Code)
	}
	if w := request("GET", "/api/v1/events", "", "", true); w.Code != 401 {
		t.Fatalf("revoked browser remains authorized: %d", w.Code)
	}
}
