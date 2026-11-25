package identity

import (
	"context"
	"github.com/william-lbn/ops-observability-platform/internal/config"
	"github.com/william-lbn/ops-observability-platform/internal/store"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoginStatePKCEAndExpiry(t *testing.T) {
	dsn := os.Getenv("OPS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("real PostgreSQL is required; CI always supplies OPS_TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, e := store.Open(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.DB.Close()
	if e = db.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	m := New(db)
	cfg := config.Config{PublicURL: "https://ops.example", OIDCIssuer: "https://ops.example/identity", OIDCBackend: "http://identity.internal/identity", OIDCClientID: "platform", OIDCClientSecret: "test-secret"}
	w := httptest.NewRecorder()
	if !m.Handle(w, httptest.NewRequest("GET", "/api/v1/auth/start", nil), cfg) || w.Code != http.StatusSeeOther {
		t.Fatal("login did not start")
	}
	u, e := url.Parse(w.Header().Get("Location"))
	if e != nil {
		t.Fatal(e)
	}
	query := u.Query()
	if query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" || query.Get("nonce") == "" {
		t.Fatal("login lacks PKCE or nonce")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatal("login state cookie is not protected")
	}
	r := httptest.NewRequest("GET", "/api/v1/auth/callback?state="+strings.Repeat("f", 64), nil)
	r.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	m.Handle(w, r, cfg)
	if w.Code != 400 {
		t.Fatal("mismatched browser state accepted")
	}
	state := query.Get("state")
	var verifier string
	if e = db.DB.QueryRowContext(ctx, `SELECT verifier FROM auth_login_states WHERE hash=$1`, digest(state)).Scan(&verifier); e != nil || verifier == "" {
		t.Fatal("PKCE verifier was not durably stored", e)
	}
	if _, e = db.DB.ExecContext(ctx, `UPDATE auth_login_states SET expires_at=now()-interval '1 second' WHERE hash=$1`, digest(state)); e != nil {
		t.Fatal(e)
	}
	m = New(db) // A different API instance consumes the shared, single-use login state.
	for attempt := 0; attempt < 2; attempt++ {
		r = httptest.NewRequest("GET", "/api/v1/auth/callback?state="+state, nil)
		r.AddCookie(cookies[0])
		w = httptest.NewRecorder()
		m.Handle(w, r, cfg)
		if w.Code != 400 {
			t.Fatal("expired or consumed state accepted")
		}
	}
}
