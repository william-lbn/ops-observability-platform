package identity

import (
	"context"
	"database/sql"
	"errors"
	"github.com/william-lbn/ops-observability-platform/internal/config"
	"github.com/william-lbn/ops-observability-platform/internal/store"
	"net/http"
	"time"
)

var errStateCapacity = errors.New("pending login capacity reached")

func (c *Manager) saveState(ctx context.Context, hash string, s pending) error {
	tx, e := c.Store.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(741852964)`); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM auth_login_states WHERE expires_at<=now()`); e != nil {
		return e
	}
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM auth_login_states`).Scan(&count); e != nil {
		return e
	}
	if count >= 512 {
		return errStateCapacity
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO auth_login_states(hash,nonce,verifier,expires_at) VALUES($1,$2,$3,$4)`, hash, s.Nonce, s.Verifier, s.Expires); e != nil {
		return e
	}
	return tx.Commit()
}
func (c *Manager) consumeState(ctx context.Context, hash string) (pending, error) {
	var saved pending
	e := c.Store.DB.QueryRowContext(ctx, `DELETE FROM auth_login_states WHERE hash=$1 RETURNING nonce,verifier,expires_at`, hash).Scan(&saved.Nonce, &saved.Verifier, &saved.Expires)
	return saved, e
}
func (c *Manager) EmergencySession(ctx context.Context, p config.Principal, requestID string) (string, error) {
	session := random()
	e := c.Store.Tx(ctx, p.Tenant, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `INSERT INTO auth_sessions(hash,subject,expires_at,auth_method,credential_hash) VALUES($1,$2,$3,'personal_credential',$4)`, digest(session), p.Subject, time.Now().Add(time.Hour), p.TokenSHA256); e != nil {
			return e
		}
		return store.Audit(ctx, tx, p.Tenant, p.Subject, "identity.login", p.Subject, requestID, map[string]string{"method": "personal_credential"})
	})
	return session, e
}
func protectedSession(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: age})
}
