package api

import (
	"database/sql"
	"encoding/json"
	"github.com/william-lbn/ops-observability-platform/internal/config"
	"net/http"
	"time"
)

func (s *Server) history(w http.ResponseWriter, r *http.Request, p config.Principal, id string) {
	uid := r.URL.Query().Get("uid")
	source := r.URL.Query().Get("source")
	if len(uid) > 256 || len(source) > 256 {
		failure(w, 400, "invalid_request", "资源范围无效", id)
		return
	}
	items := []map[string]any{}
	e := s.Store.Tx(r.Context(), p.Tenant, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(r.Context(), `SELECT snapshot,observed_at,recorded_at FROM catalog_history WHERE tenant=$1 AND source=$2 AND uid=$3 ORDER BY observed_at DESC LIMIT 100`, p.Tenant, source, uid)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var at, recorded time.Time
			if e = rows.Scan(&raw, &at, &recorded); e != nil {
				return e
			}
			items = append(items, map[string]any{"snapshot": json.RawMessage(raw), "observedAt": at, "recordedAt": recorded})
		}
		return rows.Err()
	})
	if e != nil {
		failure(w, 503, "storage_unavailable", "历史观察暂时无法读取", id)
		return
	}
	reply(w, 200, map[string]any{"items": items, "scope": p.Tenant, "coverage": "partial", "message": "只保存已观察到的变化，轮询间隔内的转换尚未证明完整"})
}
func (s *Server) notifications(w http.ResponseWriter, r *http.Request, p config.Principal, id string) {
	items := []map[string]any{}
	e := s.Store.Tx(r.Context(), p.Tenant, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(r.Context(), `SELECT id::text,title,body,fingerprint,created_at FROM notifications WHERE tenant=$1 ORDER BY id DESC LIMIT 100`, p.Tenant)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var sequence, title, body, fingerprint string
			var at time.Time
			if e = rows.Scan(&sequence, &title, &body, &fingerprint, &at); e != nil {
				return e
			}
			items = append(items, map[string]any{"id": sequence, "title": title, "body": body, "fingerprint": fingerprint, "at": at})
		}
		return rows.Err()
	})
	if e != nil {
		failure(w, 503, "storage_unavailable", "站内通知暂时无法读取", id)
		return
	}
	reply(w, 200, map[string]any{"items": items, "scope": p.Tenant, "channel": "in_application", "message": "站内通知已持久保存；外部邮件或消息渠道需另行配置与验收"})
}
