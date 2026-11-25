package api

import (
	"database/sql"
	"encoding/json"
	"github.com/william-lbn/ops-observability-platform/internal/authz"
	"github.com/william-lbn/ops-observability-platform/internal/config"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var platformQueries = map[string]string{
	"node_cpu":              "1-avg by(instance,node)(rate(node_cpu_seconds_total{ops_tenant=\"platform\",mode=\"idle\"}[5m]))",
	"node_memory":           "1-node_memory_MemAvailable_bytes{ops_tenant=\"platform\"}/node_memory_MemTotal_bytes{ops_tenant=\"platform\"}",
	"disk_used":             "1-node_filesystem_avail_bytes{ops_tenant=\"platform\",fstype!~\"tmpfs|overlay|squashfs\",mountpoint!~\"^/(dev|proc|sys|run)($|/).*\"}/node_filesystem_size_bytes{ops_tenant=\"platform\",fstype!~\"tmpfs|overlay|squashfs\",mountpoint!~\"^/(dev|proc|sys|run)($|/).*\"}",
	"network_drops":         "sum by(instance)(rate(node_network_receive_drop_total{ops_tenant=\"platform\"}[5m])+rate(node_network_transmit_drop_total{ops_tenant=\"platform\"}[5m]))",
	"disk_io":               "rate(node_disk_io_time_seconds_total{ops_tenant=\"platform\",device!~\"^(loop|ram).*\"}[5m])",
	"collection_up":         "up{ops_tenant=\"platform\"}",
	"dns_failures":          "sum(rate(coredns_dns_responses_total{ops_tenant=\"platform\",rcode=~\"SERVFAIL|REFUSED\"}[5m]))",
	"collector_refused":     "sum by(job)(rate(otelcol_receiver_refused_log_records_total{ops_tenant=\"platform\"}[5m]))",
	"collector_send_failed": "sum by(job)(rate(otelcol_exporter_send_failed_log_records_total{ops_tenant=\"platform\"}[5m]))",
	"collector_queue":       "max by(job)(otelcol_exporter_queue_size{ops_tenant=\"platform\"}/otelcol_exporter_queue_capacity{ops_tenant=\"platform\"})",
}

func (s *Server) platformQuery(w http.ResponseWriter, r *http.Request, p config.Principal, cfg config.Config, id string) {
	if !authz.PlatformRead(p) {
		failure(w, 403, "platform_scope_denied", "当前角色没有平台级数据读取权限", id)
		return
	}
	expr, ok := platformQueries[r.URL.Query().Get("metric")]
	if !ok {
		failure(w, 422, "unsupported", "当前平台档位未提供此诊断指标", id)
		return
	}
	expr = strings.ReplaceAll(expr, `ops_tenant="platform"`, `ops_tenant=`+strconv.Quote(p.Tenant))
	data, e := s.Query.Get(r.Context(), cfg.Prometheus, "/api/v1/query", url.Values{"query": {expr}, "timeout": {"5s"}})
	if e != nil {
		failure(w, 502, "backend_unavailable", "平台指标暂时无法读取，请保留当前诊断范围", id)
		return
	}
	reply(w, 200, map[string]any{"scope": "platform", "as_of": time.Now().UTC(), "coverage": "partial", "data": data, "message": "缺少样本、未启用与正常零值分别解释；平台指标不替代租户业务成功证明"})
}
func (s *Server) platformDashboard(w http.ResponseWriter, r *http.Request, p config.Principal, cfg config.Config, id string) {
	if !authz.PlatformRead(p) {
		failure(w, 403, "platform_scope_denied", "当前角色没有平台级数据读取权限", id)
		return
	}
	sources := []string{}
	expected := map[string]bool{}
	for _, source := range cfg.Sources {
		if source.Enabled && source.Tenant == p.Tenant && source.Kind == "kubernetes_cluster" {
			sources = append(sources, source.ID)
			for _, uid := range source.ExpectedNodeUIDs {
				expected[uid] = true
			}
		}
	}
	items := []map[string]any{}
	qualities := []map[string]any{}
	diagnostics := []map[string]any{}
	freshNodes, readyNodes, unknownNodes := 0, 0, 0
	seen := map[string]bool{}
	unregistered := []string{}
	e := s.Store.Tx(r.Context(), p.Tenant, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(r.Context(), `SELECT c.source,c.uid,c.name,c.kind,c.snapshot,c.observed_at,COALESCE(q.state,'unknown'),COALESCE(q.observed_set ? c.uid,false) FROM catalog c LEFT JOIN source_quality q ON q.tenant=c.tenant AND q.source=c.source WHERE c.tenant=$1 AND c.source=ANY($2::text[]) ORDER BY c.kind,c.name LIMIT 500`, p.Tenant, sources)
		if e != nil {
			return e
		}
		for rows.Next() {
			var source, uid, name, kind, quality string
			var raw []byte
			var at time.Time
			var observed bool
			if e = rows.Scan(&source, &uid, &name, &kind, &raw, &at, &quality, &observed); e != nil {
				rows.Close()
				return e
			}
			var snapshot map[string]any
			if e = json.Unmarshal(raw, &snapshot); e != nil {
				rows.Close()
				return e
			}
			stale := time.Since(at) > 90*time.Second || quality == "identity_changed" || quality == "unknown" || (kind == "kubernetes.node" && !observed)
			item := map[string]any{"source": source, "uid": uid, "name": name, "kind": kind, "snapshot": snapshot, "observedAt": at, "stale": stale}
			items = append(items, item)
			if kind == "kubernetes.node" {
				seen[uid] = true
				ready := false
				conditions, _ := snapshot["conditions"].([]any)
				for _, value := range conditions {
					condition, _ := value.(map[string]any)
					if condition["type"] == "Ready" && condition["status"] == "True" {
						ready = true
					}
					if condition["status"] == "True" && (condition["type"] == "MemoryPressure" || condition["type"] == "DiskPressure" || condition["type"] == "PIDPressure" || condition["type"] == "NetworkUnavailable") {
						diagnostics = append(diagnostics, map[string]any{"title": name + "：观察到节点压力", "severity": "warning", "fact": condition["type"], "sourceUID": uid, "observedAt": at, "runbook": "node-pressure", "nextStep": "核对节点资源、存储和网络的当前指标，再关联受影响服务；不要直接重启共享节点"})
					}
				}
				if !expected[uid] {
					unregistered = append(unregistered, uid)
				} else if stale {
					unknownNodes++
				} else {
					freshNodes++
					if ready {
						readyNodes++
					} else {
						diagnostics = append(diagnostics, map[string]any{"title": name + "：节点就绪条件未满足", "severity": "critical", "sourceUID": uid, "observedAt": at, "runbook": "node-not-ready", "nextStep": "先确认节点连通、压力条件和相关系统服务，再核对租户影响；不从节点状态推断原业务操作结果"})
					}
				}
			}
			if kind == "kubernetes.namespace.health" && !stale {
				crashes, _ := snapshot["crashLoops"].(float64)
				pending, _ := snapshot["pendingPVCs"].(float64)
				if crashes > 0 || pending > 0 {
					diagnostics = append(diagnostics, map[string]any{"title": name + "：需要核对工作负载或存储", "severity": "warning", "sourceUID": uid, "observedAt": at, "runbook": "namespace-workload", "nextStep": "检查当前启动失败与待绑定存储；历史失败记录、已完成任务和当前服务影响分别判断"})
				}
				failures, _ := snapshot["controllerFailures"].(float64)
				if failures > 0 {
					diagnostics = append(diagnostics, map[string]any{"title": name + "：控制器创建副本失败", "severity": "warning", "sourceUID": uid, "observedAt": at, "runbook": "rollout-budget", "nextStep": "先比较命名空间配额 hard/used 与滚动更新临时副本需求，再核对控制器事件和调度原因；不能仅统计现存 Pod 来判断部署成功"})
				}
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		rows, e = tx.QueryContext(r.Context(), `SELECT source,state,last_attempt_at,last_success_at,error_code,expected_set,observed_set FROM source_quality WHERE tenant=$1 ORDER BY source LIMIT 100`, p.Tenant)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var source, state, code string
			var attempt time.Time
			var success sql.NullTime
			var expectedRaw, observedRaw []byte
			if e = rows.Scan(&source, &state, &attempt, &success, &code, &expectedRaw, &observedRaw); e != nil {
				return e
			}
			var last any
			if success.Valid {
				last = success.Time
			}
			qualities = append(qualities, map[string]any{"source": source, "state": state, "lastAttemptAt": attempt, "lastSuccessAt": last, "stale": !success.Valid || time.Since(success.Time) > 90*time.Second, "errorCode": code, "expectedSet": json.RawMessage(expectedRaw), "observedSet": json.RawMessage(observedRaw)})
		}
		return rows.Err()
	})
	if e != nil {
		failure(w, 503, "storage_unavailable", "平台运行视图暂时无法读取", id)
		return
	}
	missing := []string{}
	for uid := range expected {
		if !seen[uid] {
			missing = append(missing, uid)
		}
	}
	unknownNodes += len(missing)
	if unknownNodes > 0 {
		diagnostics = append(diagnostics, map[string]any{"title": "存在过期或未观察到的预期节点", "severity": "unknown", "runbook": "source-gap", "nextStep": "检查来源身份、Worker 权限和采集链路；保留预期节点分母，不能将失联节点从统计中自动移除"})
	}
	pool := s.Store.DB.Stats()
	reply(w, 200, map[string]any{"scope": "platform", "as_of": time.Now().UTC(), "coverage": "partial", "expectedNodes": len(expected), "freshNodes": freshNodes, "readyNodes": readyNodes, "unknownNodes": unknownNodes, "unregisteredNodeUIDs": unregistered, "missingExpectedUIDs": missing, "inventory": items, "sources": qualities, "diagnostics": diagnostics, "databasePool": map[string]any{"open": pool.OpenConnections, "inUse": pool.InUse, "waitCount": pool.WaitCount}, "neonIntegrationEnabled": false})
}
