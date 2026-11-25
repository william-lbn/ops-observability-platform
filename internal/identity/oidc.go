package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/william-lbn/ops-observability-platform/internal/config"
	"github.com/william-lbn/ops-observability-platform/internal/store"
	"golang.org/x/oauth2"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

type pending struct {
	Nonce, Verifier string
	Expires         time.Time
}
type Manager struct {
	Store  *store.Store
	mu     sync.Mutex
	keys   map[string]oidc.KeySet
	slots  chan struct{}
	client *http.Client
}

func New(s *store.Store) *Manager {
	return &Manager{Store: s, keys: map[string]oidc.KeySet{}, slots: make(chan struct{}, 16), client: &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func random() string             { b := make([]byte, 32); rand.Read(b); return hex.EncodeToString(b) }
func digest(value string) string { h := sha256.Sum256([]byte(value)); return hex.EncodeToString(h[:]) }
func Enabled(c config.Config) bool {
	return c.OIDCIssuer != "" && c.OIDCBackend != "" && c.OIDCClientID != "" && c.OIDCClientSecret != ""
}
func (c *Manager) oauth(cfg config.Config) *oauth2.Config {
	return &oauth2.Config{ClientID: cfg.OIDCClientID, ClientSecret: cfg.OIDCClientSecret, RedirectURL: cfg.PublicURL + "/api/v1/auth/callback", Scopes: []string{oidc.ScopeOpenID, "profile", "email"}, Endpoint: oauth2.Endpoint{AuthURL: cfg.OIDCIssuer + "/auth", TokenURL: cfg.OIDCBackend + "/token"}}
}
func respond(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"message": message})
}
func (c *Manager) Handle(w http.ResponseWriter, r *http.Request, cfg config.Config) bool {
	if r.URL.Path == "/api/v1/auth/options" && r.Method == "GET" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"oidcEnabled": Enabled(cfg)})
		return true
	}
	if r.URL.Path != "/api/v1/auth/start" && r.URL.Path != "/api/v1/auth/callback" {
		return false
	}
	if r.Method != "GET" {
		respond(w, 405, "请从登录入口重新发起身份验证")
		return true
	}
	if !Enabled(cfg) {
		respond(w, 422, "统一身份登录尚未配置，请使用已授权的应急入口")
		return true
	}
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	default:
		respond(w, 429, "身份请求较多，请稍后重试")
		return true
	}
	if r.URL.Path == "/api/v1/auth/start" {
		state := random()
		nonce := random()
		verifier := oauth2.GenerateVerifier()
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		e := c.saveState(ctx, digest(state), pending{nonce, verifier, time.Now().Add(5 * time.Minute)})
		cancel()
		if e != nil {
			if errors.Is(e, errStateCapacity) {
				respond(w, 429, "待处理登录已达上限，请稍后重试")
			} else {
				respond(w, 503, "登录状态尚未保存，请稍后重试")
			}
			return true
		}
		http.SetCookie(w, &http.Cookie{Name: "ops_oidc_state", Value: state, Path: "/api/v1/auth", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: 300})
		http.Redirect(w, r, c.oauth(cfg).AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusSeeOther)
		return true
	}
	state := r.URL.Query().Get("state")
	cookie, e := r.Cookie("ops_oidc_state")
	if e != nil || len(state) != 64 || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		respond(w, 400, "登录状态验证失败，请重新开始")
		return true
	}
	stateCtx, finish := context.WithTimeout(r.Context(), 3*time.Second)
	saved, stateError := c.consumeState(stateCtx, digest(state))
	finish()
	if stateError != nil && !errors.Is(stateError, sql.ErrNoRows) {
		respond(w, 503, "登录状态暂时无法核验，请稍后重试")
		return true
	}
	if stateError != nil || time.Now().After(saved.Expires) {
		respond(w, 400, "登录已过期或已经使用，请重新开始")
		return true
	}
	http.SetCookie(w, &http.Cookie{Name: "ops_oidc_state", Value: "", Path: "/api/v1/auth", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, oauth2.HTTPClient, c.client)
	token, e := c.oauth(cfg).Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(saved.Verifier))
	if e != nil {
		respond(w, 401, "身份验证未完成，请重新登录")
		return true
	}
	raw, _ := token.Extra("id_token").(string)
	if raw == "" {
		respond(w, 401, "身份源没有提供有效的身份凭证")
		return true
	}
	c.mu.Lock()
	keySet := c.keys[cfg.OIDCBackend]
	if keySet == nil {
		keySet = oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), c.client), cfg.OIDCBackend+"/keys")
		c.keys[cfg.OIDCBackend] = keySet
	}
	c.mu.Unlock()
	verified, e := oidc.NewVerifier(cfg.OIDCIssuer, keySet, &oidc.Config{ClientID: cfg.OIDCClientID, SupportedSigningAlgs: []string{"RS256"}}).Verify(ctx, raw)
	if e != nil || subtle.ConstantTimeCompare([]byte(verified.Nonce), []byte(saved.Nonce)) != 1 {
		respond(w, 401, "身份凭证的签名、范围或登录绑定无效")
		return true
	}
	var principal *config.Principal
	for _, p := range cfg.Principals {
		if !p.Disabled && p.OIDCSubject == verified.Subject {
			p := p
			principal = &p
			break
		}
	}
	if principal == nil || principal.Role == "ingest" {
		respond(w, 403, "身份已确认，但当前用户没有此工作台的访问权限")
		return true
	}
	session := random()
	expires := time.Now().Add(time.Hour)
	if verified.Expiry.Before(expires) {
		expires = verified.Expiry
	}
	e = c.Store.Tx(ctx, principal.Tenant, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `INSERT INTO auth_sessions(hash,subject,expires_at) VALUES($1,$2,$3)`, digest(session), verified.Subject, expires); e != nil {
			return e
		}
		return store.Audit(ctx, tx, principal.Tenant, principal.Subject, "identity.login", principal.Subject, digest(state), map[string]string{"method": "oidc"})
	})
	if e != nil {
		respond(w, 503, "登录记录尚未保存，请稍后重试")
		return true
	}
	http.SetCookie(w, &http.Cookie{Name: "ops_oidc_session", Value: session, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: int(time.Until(expires).Seconds())})
	protectedSession(w, "ops_session", "", -1)
	http.Redirect(w, r, "/", http.StatusSeeOther)
	return true
}
func (c *Manager) Principal(r *http.Request, cfg config.Config) (*config.Principal, error) {
	cookie, e := r.Cookie("ops_oidc_session")
	if e != nil {
		cookie, e = r.Cookie("ops_session")
	}
	if e != nil || len(cookie.Value) != 64 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	var subject, method, credential string
	if e := c.Store.DB.QueryRowContext(ctx, `SELECT subject,auth_method,credential_hash FROM auth_sessions WHERE hash=$1 AND expires_at>now()`, digest(cookie.Value)).Scan(&subject, &method, &credential); e != nil {
		if e == sql.ErrNoRows {
			return nil, nil
		}
		return nil, e
	}
	for _, p := range cfg.Principals {
		bound := (method == "oidc" && Enabled(cfg) && p.OIDCSubject == subject) || (method == "personal_credential" && p.Subject == subject && subtle.ConstantTimeCompare([]byte(p.TokenSHA256), []byte(credential)) == 1)
		if !p.Disabled && bound && p.Role != "ingest" {
			p := p
			return &p, nil
		}
	}
	return nil, nil
}
func (c *Manager) Logout(r *http.Request, w http.ResponseWriter, cfg config.Config, requestID string) error {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	hashes := []string{}
	for _, name := range []string{"ops_oidc_session", "ops_session"} {
		if cookie, e := r.Cookie(name); e == nil && len(cookie.Value) == 64 {
			hashes = append(hashes, digest(cookie.Value))
		}
	}
	if len(hashes) > 0 {
		p, e := c.Principal(r, cfg)
		if e != nil {
			return e
		}
		if p != nil {
			e = c.Store.Tx(ctx, p.Tenant, func(tx *sql.Tx) error {
				if _, e := tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE hash=ANY($1::text[])`, hashes); e != nil {
					return e
				}
				return store.Audit(ctx, tx, p.Tenant, p.Subject, "identity.logout", p.Subject, requestID, map[string]string{"session": "revoked"})
			})
		} else {
			_, e = c.Store.DB.ExecContext(ctx, `DELETE FROM auth_sessions WHERE hash=ANY($1::text[])`, hashes)
		}
		if e != nil {
			return e
		}
	}
	protectedSession(w, "ops_oidc_session", "", -1)
	protectedSession(w, "ops_session", "", -1)
	return nil
}
func (c *Manager) Proxy(w http.ResponseWriter, r *http.Request, cfg config.Config) {
	if !Enabled(cfg) || !strings.HasPrefix(r.URL.Path, "/identity/") {
		http.NotFound(w, r)
		return
	}
	u, e := url.Parse(cfg.OIDCBackend)
	if e != nil || u.Host == "" {
		respond(w, 503, "身份入口配置不可用")
		return
	}
	u.Path = ""
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.Transport = c.client.Transport
	original := proxy.Director
	proxy.Director = func(request *http.Request) {
		original(request)
		request.Header.Set("X-Forwarded-Proto", "https")
		request.Header.Del("Authorization")
		request.Header.Del("Cookie")
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) {
		respond(w, 503, "身份来源暂时不可读取，请稍后重试")
	}
	proxy.ServeHTTP(w, r)
}
