package authz

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"github.com/william-lbn/ops-observability-platform/internal/config"
	"net/http"
	"strings"
)

var ErrUnauthorized = errors.New("unauthorized")

func Authenticate(r *http.Request, c config.Config) (config.Principal, error) {
	var empty config.Principal
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return empty, ErrUnauthorized
	}
	token := strings.TrimPrefix(h, "Bearer ")
	if len(token) < 32 || len(token) > 512 {
		return empty, ErrUnauthorized
	}
	hash := sha256.Sum256([]byte(token))
	digest := hex.EncodeToString(hash[:])
	for _, p := range c.Principals {
		if !p.Disabled && subtle.ConstantTimeCompare([]byte(digest), []byte(p.TokenSHA256)) == 1 {
			return p, nil
		}
	}
	return empty, ErrUnauthorized
}
func Can(p config.Principal, action string) bool {
	if p.Role == "admin" || p.Role == "platform_admin" {
		return true
	}
	if p.Role == "ingest" {
		return action == "ingest"
	}
	return (p.Role == "viewer" || p.Role == "platform_viewer") && action == "read"
}
func PlatformRead(p config.Principal) bool {
	return p.Role == "platform_admin" || p.Role == "platform_viewer"
}
func Sources(p config.Principal, c config.Config) []config.Source {
	out := []config.Source{}
	for _, s := range c.Sources {
		if s.Enabled && s.Tenant == p.Tenant {
			out = append(out, s)
		}
	}
	return out
}
