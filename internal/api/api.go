package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/william-lbn/ops-observability-platform/contracts"
	"github.com/william-lbn/ops-observability-platform/internal/authz"
	"github.com/william-lbn/ops-observability-platform/internal/config"
	"github.com/william-lbn/ops-observability-platform/internal/identity"
	"github.com/william-lbn/ops-observability-platform/internal/query"
	"github.com/william-lbn/ops-observability-platform/internal/store"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Server struct {
	Store       *store.Store
	ConfigPath  string
	Query       *query.Client
	Schema      *jsonschema.Schema
	Web         http.Handler
	Requests    atomic.Uint64
	Draining    atomic.Bool
	Slots       chan struct{}
	Identity    *identity.Manager
	TenantSlots sync.Map
}

func New(s *store.Store, path string, web http.Handler) (*Server, error) {
	schema, e := contracts.EventSchema()
	if e != nil {
		return nil, e
	}
	return &Server{Store: s, ConfigPath: path, Query: query.New(), Schema: schema, Web: web, Slots: make(chan struct{}, 32), Identity: identity.New(s)}, nil
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, status int, code, msg, id string) {
	reply(w, status, map[string]any{"code": code, "message": msg, "messageKey": "error." + code, "supportId": id})
}
func identifier() string { b := make([]byte, 16); rand.Read(b); return hex.EncodeToString(b) }
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Requests.Add(1)
	id := identifier()
	w.Header().Set("X-Request-ID", id)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if r.URL.Path == "/healthz" {
		reply(w, 200, map[string]bool{"alive": true})
		return
	}
	if r.URL.Path == "/readyz" {
		if s.Draining.Load() {
			failure(w, 503, "draining", "本副本正在退出，请由服务入口重试", id)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if s.Store.DB.PingContext(ctx) != nil {
			failure(w, 503, "database_unavailable", "平台存储暂时不可读取", id)
			return
		}
		reply(w, 200, map[string]bool{"ready": true})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/identity/") {
		cfg, e := config.Load(s.ConfigPath)
		if e != nil {
			failure(w, 503, "configuration_unavailable", "身份入口暂时不可读取", id)
			return
		}
		s.Identity.Proxy(w, r, cfg)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		s.Web.ServeHTTP(w, r)
		return
	}
	cfg, e := config.Load(s.ConfigPath)
	if e != nil {
		failure(w, 503, "configuration_unavailable", "平台配置暂时不可读取", id)
		return
	}
	if s.Identity.Handle(w, r, cfg) {
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		if origin := r.Header.Get("Origin"); origin != "" && origin != cfg.PublicURL {
			failure(w, 403, "origin_denied", "此页面来源无权提交请求", id)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") && r.URL.Path != "/api/v1/attachments" {
			failure(w, 415, "invalid_content_type", "请使用规定的数据格式", id)
			return
		}
	}
	if r.URL.Path == "/api/v1/login" && r.Method == "POST" {
		s.login(w, r, cfg, id)
		return
	}
	if r.URL.Path == "/api/v1/logout" && r.Method == "POST" {
		if r.Header.Get("Origin") != cfg.PublicURL && r.Header.Get("Authorization") == "" {
			failure(w, 403, "origin_denied", "请从当前工作台退出", id)
			return
		}
		if e = s.Identity.Logout(r, w, cfg, id); e != nil {
			failure(w, 503, "session_revocation_failed", "会话尚未完成撤销，请稍后重试", id)
			return
		}
		reply(w, 200, map[string]bool{"loggedOut": true})
		return
	}
	bearer := r.Header.Get("Authorization") != ""
	p, e := authz.Authenticate(r, cfg)
	if e != nil && !bearer {
		current, identityError := s.Identity.Principal(r, cfg)
		if identityError != nil {
			failure(w, 503, "identity_unavailable", "当前身份暂时无法核验，请稍后重试", id)
			return
		}
		if current != nil {
			p = *current
			e = nil
		}
	}
	if e != nil {
		failure(w, 401, "unauthorized", "请先登录，或联系管理员更新访问凭据", id)
		return
	}
	if !bearer && r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("Origin") != cfg.PublicURL {
		failure(w, 403, "origin_denied", "页面验证失败，请重新登录", id)
		return
	}
	action := "read"
	if r.Method != "GET" && r.Method != "HEAD" {
		action = "manage"
		if r.URL.Path == "/api/v1/events" || r.URL.Path == "/api/v1/attachments" {
			action = "ingest"
		}
	}
	if !authz.Can(p, action) {
		failure(w, 403, "forbidden", "当前角色无法执行此操作", id)
		return
	}
	currentSlots, _ := s.TenantSlots.LoadOrStore(p.Tenant, make(chan struct{}, 4))
	tenantSlots := currentSlots.(chan struct{})
	select {
	case tenantSlots <- struct{}{}:
		defer func() { <-tenantSlots }()
	default:
		failure(w, 429, "tenant_query_budget", "当前租户并发读取已达上限，请稍后重试", id)
		return
	}
	select {
	case s.Slots <- struct{}{}:
		defer func() { <-s.Slots }()
	default:
		failure(w, 429, "query_budget", "查询已达并发上限，请稍后重试", id)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	switch {
	case r.URL.Path == "/api/v1/me" && r.Method == "GET":
		reply(w, 200, map[string]any{"subject": p.Subject, "tenant": p.Tenant, "role": p.Role, "platformAccess": authz.PlatformRead(p)})
	case r.URL.Path == "/api/v1/platform/dashboard" && r.Method == "GET":
		s.platformDashboard(w, r, p, cfg, id)
	case r.URL.Path == "/api/v1/platform/query" && r.Method == "GET":
		s.platformQuery(w, r, p, cfg, id)
	case r.URL.Path == "/api/v1/sources" && r.Method == "GET":
		reply(w, 200, map[string]any{"items": authz.Sources(p, cfg), "neon": map[string]any{"enabled": false, "qualification": "NOT_RUN", "message": "未接入，等待后续指令"}, "as_of": time.Now().UTC(), "coverage": "partial"})
	case r.URL.Path == "/api/v1/sources/quality" && r.Method == "GET":
		s.sourceQuality(w, r, p, id)
	case r.URL.Path == "/api/v1/query" && r.Method == "GET":
		var src *config.Source
		for _, v := range authz.Sources(p, cfg) {
			if v.ID == r.URL.Query().Get("source") {
				v := v
				src = &v
				break
			}
		}
		if src == nil {
			failure(w, 404, "source_unavailable", "此范围没有可读取的数据来源", id)
			return
		}
		data, err := s.Query.Execute(ctx, cfg, *src, r.URL.Query().Get("signal"), r.URL.Query().Get("metric"))
		if r.URL.Query().Get("signal") == "metrics" && r.URL.Query().Get("window") != "" {
			data, err = s.Query.Range(ctx, cfg, *src, r.URL.Query().Get("metric"), r.URL.Query().Get("window"))
		}
		if r.URL.Query().Get("signal") == "traces" && r.URL.Query().Get("traceID") != "" {
			data, err = s.Query.Trace(ctx, cfg, *src, r.URL.Query().Get("traceID"))
		}
		if err != nil {
			status := 502
			code := "backend_unavailable"
			msg := "来源暂时无法读取，已保留查询范围"
			if errors.Is(err, query.ErrUnsupported) {
				status = 422
				code = "unsupported"
				msg = "当前来源未提供此查询能力"
			}
			if errors.Is(err, query.ErrNotFound) {
				status = 404
				code = "evidence_not_found"
				msg = "当前授权范围未找到此明细，请核对标识与保留时间"
			}
			failure(w, status, code, msg, id)
			return
		}
		reply(w, 200, map[string]any{"scope": p.Tenant, "source": src.ID, "as_of": time.Now().UTC(), "coverage": "partial", "sampling": "unknown", "projectionVersion": "1", "data": data})
	case r.URL.Path == "/api/v1/events" && r.Method == "POST":
		s.ingest(w, r, p, cfg, id)
	case r.URL.Path == "/api/v1/events" && r.Method == "GET":
		s.events(w, r, p, id)
	case strings.HasPrefix(r.URL.Path, "/api/v1/events/") && r.Method == "GET":
		s.original(w, r, p, id)
	case r.URL.Path == "/api/v1/audits" && r.Method == "GET":
		s.audits(w, r, p, id)
	case r.URL.Path == "/api/v1/catalog" && r.Method == "GET":
		s.catalog(w, r, p, id)
	case r.URL.Path == "/api/v1/catalog/history" && r.Method == "GET":
		s.history(w, r, p, id)
	case r.URL.Path == "/api/v1/notifications" && r.Method == "GET":
		s.notifications(w, r, p, id)
	case r.URL.Path == "/api/v1/incidents" && r.Method == "GET":
		s.incidents(w, r, p, id)
	case r.URL.Path == "/api/v1/incidents/ack" && r.Method == "POST":
		s.ack(w, r, p, id)
	case r.URL.Path == "/api/v1/attachments" && r.Method == "POST":
		s.attachment(w, r, p, id)
	case strings.HasPrefix(r.URL.Path, "/api/v1/attachments/") && r.Method == "GET":
		s.downloadAttachment(w, r, p, id)
	case r.URL.Path == "/api/v1/export" && r.Method == "GET":
		s.export(w, r, p, id)
	default:
		failure(w, 404, "not_found", "未找到此功能入口", id)
	}
}
func (s *Server) login(w http.ResponseWriter, r *http.Request, c config.Config, id string) {
	var b struct {
		Token string `json:"token"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&b) != nil {
		failure(w, 400, "invalid_request", "请输入有效的访问凭据", id)
		return
	}
	r.Header.Set("Authorization", "Bearer "+b.Token)
	p, e := authz.Authenticate(r, c)
	if e != nil {
		time.Sleep(100 * time.Millisecond)
		failure(w, 401, "unauthorized", "访问凭据无效或已停用", id)
		return
	}
	if p.Role == "ingest" {
		failure(w, 403, "forbidden", "采集凭据不能用于交互登录", id)
		return
	}
	session, sessionError := s.Identity.EmergencySession(r.Context(), p, id)
	if sessionError != nil {
		failure(w, 503, "storage_unavailable", "登录记录尚未保存，请稍后重试", id)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "ops_session", Value: session, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 3600})
	http.SetCookie(w, &http.Cookie{Name: "ops_oidc_session", Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	reply(w, 200, map[string]string{"message": "已登录", "subject": p.Subject})
}
func (s *Server) ingest(w http.ResponseWriter, r *http.Request, p config.Principal, c config.Config, id string) {
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 256*1024))
	if e != nil {
		failure(w, 413, "payload_too_large", "事件超过允许大小", id)
		return
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil || s.Schema.Validate(doc) != nil {
		failure(w, 422, "invalid_event", "事件格式不符合公共合同", id)
		return
	}
	source, _ := doc["sourceInstance"].(string)
	allowed := false
	for _, v := range authz.Sources(p, c) {
		if v.ID == source {
			allowed = true
		}
	}
	entity, _ := doc["entityRef"].(map[string]any)
	signal, _ := doc["signalKind"].(string)
	if !allowed || entity["sourceInstance"] != source {
		failure(w, 403, "source_denied", "此凭据无权写入指定来源", id)
		return
	}
	if signal != "snapshot" && signal != "telemetry" {
		failure(w, 422, "unqualified_evidence", "此来源尚未取得可靠领域证据资格", id)
		return
	}
	if doc["exampleMetadata"] != nil {
		failure(w, 422, "synthetic_denied", "运行入口不接收合成演示数据", id)
		return
	}
	when, e := time.Parse(time.RFC3339Nano, fmt.Sprint(doc["observedAt"]))
	if e != nil || when.After(time.Now().Add(5*time.Minute)) {
		failure(w, 422, "invalid_time", "来源观测时间无效", id)
		return
	}
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	eventID := fmt.Sprint(doc["eventId"])
	conflict := false
	duplicate := false
	e = s.Store.Tx(r.Context(), p.Tenant, func(tx *sql.Tx) error {
		var existing string
		err := tx.QueryRowContext(r.Context(), `INSERT INTO events(tenant,source,id,digest,raw,observed_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING RETURNING digest`, p.Tenant, source, eventID, hash, raw, when).Scan(&existing)
		if errors.Is(err, sql.ErrNoRows) {
			if err = tx.QueryRowContext(r.Context(), `SELECT digest FROM events WHERE tenant=$1 AND source=$2 AND id=$3`, p.Tenant, source, eventID).Scan(&existing); err != nil {
				return err
			}
			duplicate = true
			conflict = existing != hash
			if conflict {
				return store.Audit(r.Context(), tx, p.Tenant, p.Subject, "event.conflict", eventID, id, map[string]string{"source": source})
			}
			return nil
		}
		if err != nil {
			return err
		}
		return store.Audit(r.Context(), tx, p.Tenant, p.Subject, "event.ingested", eventID, id, map[string]string{"source": source, "digest": hash})
	})
	if e != nil {
		failure(w, 503, "storage_unavailable", "事件尚未持久保存，请使用相同标识及原始字节重试", id)
		return
	}
	if conflict {
		failure(w, 409, "event_conflict", "相同事件标识对应不同原件，冲突已记录", id)
		return
	}
	reply(w, 200, map[string]any{"eventId": eventID, "durable": true, "duplicate": duplicate, "sha256": hash, "coverage": "partial", "message": "原件已持久保存，来源历史完整性仍未确认"})
}
func (s *Server) events(w http.ResponseWriter, r *http.Request, p config.Principal, id string) {
	before := r.URL.Query().Get("before")
	if before == "" {
		before = time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)
	}
	if _, e := time.Parse(time.RFC3339Nano, before); e != nil {
		failure(w, 400, "invalid_cursor", "分页时间无效", id)
		return
	}
	items := []map[string]any{}
	e := s.Store.Tx(r.Context(), p.Tenant, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(r.Context(), `SELECT source,id::text,digest,raw,received_at FROM events WHERE tenant=$1 AND received_at<$2 ORDER BY received_at DESC,id DESC LIMIT 100`, p.Tenant, before)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var src, event, hash string
			var raw []byte
			var at time.Time
			if e = rows.Scan(&src, &event, &hash, &raw, &at); e != nil {
				return e
			}
			items = append(items, map[string]any{"source": src, "id": event, "sha256": hash, "event": json.RawMessage(raw), "receivedAt": at})
		}
		return rows.Err()
	})
	if e != nil {
		failure(w, 503, "storage_unavailable", "事件暂时无法读取", id)
		return
	}
	reply(w, 200, map[string]any{"items": items, "scope": p.Tenant, "as_of": time.Now().UTC(), "coverage": "partial", "pagination": "receivedAt-before; maximum 100"})
}

var uuid = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)

func (s *Server) original(w http.ResponseWriter, r *http.Request, p config.Principal, id string) {
	event := strings.TrimPrefix(r.URL.Path, "/api/v1/events/")
	src := r.URL.Query().Get("source")
	if !uuid.MatchString(event) {
		failure(w, 404, "not_found", "未找到可读取的原件", id)
		return
	}
	var raw []byte
	var digest string
	e := s.Store.Tx(r.Context(), p.Tenant, func(tx *sql.Tx) error {
		if e := tx.QueryRowContext(r.Context(), `SELECT raw,digest FROM events WHERE tenant=$1 AND source=$2 AND id=$3`, p.Tenant, src, event).Scan(&raw, &digest); e != nil {
			return e
		}
		return store.Audit(r.Context(), tx, p.Tenant, p.Subject, "evidence.download", event, id, map[string]string{"sha256": digest})
	})
	if e != nil {
		failure(w, 404, "not_found", "未找到可读取的原件", id)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="event-`+event+`.json"`)
	w.Header().Set("X-Content-SHA256", digest)
	w.Write(raw)
}
func (s *Server) audits(w http.ResponseWriter, r *http.Request, p config.Principal, id string) {
	items := []map[string]any{}
	e := s.Store.Tx(r.Context(), p.Tenant, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(r.Context(), `SELECT sequence::text,actor,action,object_id,detail,created_at FROM audits WHERE tenant=$1 ORDER BY sequence DESC LIMIT 100`, p.Tenant)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var seq, actor, act, obj string
			var detail []byte
			var at time.Time
			if e = rows.Scan(&seq, &actor, &act, &obj, &detail, &at); e != nil {
				return e
			}
			items = append(items, map[string]any{"sequence": seq, "actor": actor, "action": act, "object": obj, "detail": json.RawMessage(detail), "at": at})
		}
		return rows.Err()
	})
	if e != nil {
		failure(w, 503, "storage_unavailable", "审计暂时无法读取", id)
		return
	}
	reply(w, 200, map[string]any{"items": items, "scope": p.Tenant})
}
func (s *Server) catalog(w http.ResponseWriter, r *http.Request, p config.Principal, id string) {
	items := []map[string]any{}
	e := s.Store.Tx(r.Context(), p.Tenant, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(r.Context(), `SELECT source,uid,name,kind,snapshot,observed_at FROM catalog WHERE tenant=$1 ORDER BY name LIMIT 200`, p.Tenant)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var src, uid, name, kind string
			var data []byte
			var at time.Time
			if e = rows.Scan(&src, &uid, &name, &kind, &data, &at); e != nil {
				return e
			}
			items = append(items, map[string]any{"source": src, "uid": uid, "name": name, "kind": kind, "snapshot": json.RawMessage(data), "observedAt": at, "stale": time.Since(at) > time.Minute, "operationResult": "unsupported", "resourceEffects": "unknown", "evidenceCoverage": "partial"})
		}
		return rows.Err()
	})
	if e != nil {
		failure(w, 503, "storage_unavailable", "目录暂时无法读取", id)
		return
	}
	reply(w, 200, map[string]any{"items": items, "scope": p.Tenant, "as_of": time.Now().UTC()})
}
func (s *Server) incidents(w http.ResponseWriter, r *http.Request, p config.Principal, id string) {
	items := []map[string]any{}
	e := s.Store.Tx(r.Context(), p.Tenant, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(r.Context(), `SELECT fingerprint,title,state,labels,updated_at FROM incidents WHERE tenant=$1 ORDER BY updated_at DESC LIMIT 100`, p.Tenant)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var f, title, state string
			var labels []byte
			var at time.Time
			if e = rows.Scan(&f, &title, &state, &labels, &at); e != nil {
				return e
			}
			items = append(items, map[string]any{"fingerprint": f, "title": title, "state": state, "labels": json.RawMessage(labels), "at": at})
		}
		return rows.Err()
	})
	if e != nil {
		failure(w, 503, "storage_unavailable", "告警暂时无法读取", id)
		return
	}
	reply(w, 200, map[string]any{"items": items, "scope": p.Tenant})
}
func (s *Server) ack(w http.ResponseWriter, r *http.Request, p config.Principal, id string) {
	var b struct {
		Fingerprint string `json:"fingerprint"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&b) != nil || b.Fingerprint == "" || len(b.Fingerprint) > 256 {
		failure(w, 400, "invalid_request", "请选择有效的告警", id)
		return
	}
	e := s.Store.Tx(r.Context(), p.Tenant, func(tx *sql.Tx) error {
		res, e := tx.ExecContext(r.Context(), `UPDATE incidents SET state='acknowledged',updated_at=now() WHERE tenant=$1 AND fingerprint=$2 AND state='firing'`, p.Tenant, b.Fingerprint)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return sql.ErrNoRows
		}
		return store.Audit(r.Context(), tx, p.Tenant, p.Subject, "incident.acknowledged", b.Fingerprint, id, map[string]string{"message": "人工确认不等于服务恢复"})
	})
	if e != nil {
		failure(w, 409, "incident_changed", "告警已变化或不可处理，请重新读取", id)
		return
	}
	reply(w, 200, map[string]string{"message": "已记录处理责任，服务是否恢复仍需核验"})
}
func (s *Server) attachment(w http.ResponseWriter, r *http.Request, p config.Principal, id string) {
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024*1024))
	if e != nil {
		failure(w, 413, "payload_too_large", "附件超过一兆字节限制", id)
		return
	}
	h := sha256.Sum256(b)
	digest := hex.EncodeToString(h[:])
	e = s.Store.Tx(r.Context(), p.Tenant, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(r.Context(), `INSERT INTO attachments(tenant,digest,bytes,content_type) VALUES($1,$2,$3,'application/octet-stream') ON CONFLICT DO NOTHING`, p.Tenant, digest, b)
		if e != nil {
			return e
		}
		return store.Audit(r.Context(), tx, p.Tenant, p.Subject, "attachment.saved", digest, id, map[string]int{"bytes": len(b)})
	})
	if e != nil {
		failure(w, 503, "storage_unavailable", "附件尚未持久保存，请稍后重试", id)
		return
	}
	reply(w, 200, map[string]any{"sha256": digest, "durable": true, "size": len(b)})
}
func (s *Server) downloadAttachment(w http.ResponseWriter, r *http.Request, p config.Principal, id string) {
	digest := strings.TrimPrefix(r.URL.Path, "/api/v1/attachments/")
	if len(digest) != 64 {
		failure(w, 404, "not_found", "未找到可读取的附件", id)
		return
	}
	var b []byte
	e := s.Store.Tx(r.Context(), p.Tenant, func(tx *sql.Tx) error {
		if e := tx.QueryRowContext(r.Context(), `SELECT bytes FROM attachments WHERE tenant=$1 AND digest=$2`, p.Tenant, digest).Scan(&b); e != nil {
			return e
		}
		return store.Audit(r.Context(), tx, p.Tenant, p.Subject, "attachment.download", digest, id, map[string]int{"bytes": len(b)})
	})
	if e != nil {
		failure(w, 404, "not_found", "未找到可读取的附件", id)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="evidence-`+digest+`.bin"`)
	w.Header().Set("X-Content-SHA256", digest)
	w.Write(b)
}
func (s *Server) export(w http.ResponseWriter, r *http.Request, p config.Principal, id string) {
	items := []map[string]any{}
	counts := map[string]int64{}
	e := s.Store.Tx(r.Context(), p.Tenant, func(tx *sql.Tx) error {
		for key, table := range map[string]string{"来源事件": "events", "资源快照": "catalog", "告警记录": "incidents", "站内通知": "notifications"} {
			var count int64
			if e := tx.QueryRowContext(r.Context(), `SELECT count(*) FROM `+table+` WHERE tenant=$1`, p.Tenant).Scan(&count); e != nil {
				return e
			}
			counts[key] = count
		}
		rows, e := tx.QueryContext(r.Context(), `SELECT source,id::text,digest,received_at FROM events WHERE tenant=$1 ORDER BY received_at DESC LIMIT 100`, p.Tenant)
		if e != nil {
			return e
		}
		for rows.Next() {
			var source, event, digest string
			var at time.Time
			if e = rows.Scan(&source, &event, &digest, &at); e != nil {
				rows.Close()
				return e
			}
			items = append(items, map[string]any{"来源": source, "事件标识": event, "原件SHA256": digest, "接收时间": at, "授权下载路径": "/api/v1/events/" + url.PathEscape(event) + "?source=" + url.QueryEscape(source)})
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		return store.Audit(r.Context(), tx, p.Tenant, p.Subject, "summary.export", "tenant", id, map[string]any{"type": "derived_chinese_summary", "includedEvents": len(items)})
	})
	if e != nil {
		failure(w, 503, "storage_unavailable", "导出审计尚未保存，请稍后重试", id)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="operations-summary.json"`)
	reply(w, 200, map[string]any{"文档类型": "中文派生摘要，不是来源原件", "组织范围": p.Tenant, "生成时间": time.Now().In(time.FixedZone("Asia/Shanghai", 8*3600)).Format(time.RFC3339), "查看时区": "Asia/Shanghai", "证据覆盖": "部分证据可读；来源完整历史未获证明", "原请求结果": "该通用来源未提供", "资源效果": "待核验", "语言版本": "zh-CN/1", "最大事件引用数": 100, "范围内存量统计": counts, "最近事件引用": items})
}

func (s *Server) sourceQuality(w http.ResponseWriter, r *http.Request, p config.Principal, id string) {
	items := []map[string]any{}
	e := s.Store.Tx(r.Context(), p.Tenant, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(r.Context(), `SELECT source,state,last_attempt_at,last_success_at,error_code,expected_set,observed_set FROM source_quality WHERE tenant=$1 ORDER BY source LIMIT 100`, p.Tenant)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var source, state, code string
			var attempt time.Time
			var success sql.NullTime
			var expected, observed []byte
			if e = rows.Scan(&source, &state, &attempt, &success, &code, &expected, &observed); e != nil {
				return e
			}
			var last any
			if success.Valid {
				last = success.Time
			}
			items = append(items, map[string]any{"source": source, "state": state, "lastAttemptAt": attempt, "lastSuccessAt": last, "errorCode": code, "expectedSet": json.RawMessage(expected), "observedSet": json.RawMessage(observed), "stale": !success.Valid || time.Since(success.Time) > 90*time.Second})
		}
		return rows.Err()
	})
	if e != nil {
		failure(w, 503, "storage_unavailable", "采集质量暂时无法读取", id)
		return
	}
	reply(w, 200, map[string]any{"items": items, "scope": p.Tenant, "as_of": time.Now().UTC(), "coverage": "partial", "message": "快照身份核验与遥测投递是不同质量维度；未登记或未启用的信号不记作正常"})
}
