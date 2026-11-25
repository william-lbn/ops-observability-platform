package worker

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/william-lbn/ops-observability-platform/internal/config"
	"github.com/william-lbn/ops-observability-platform/internal/query"
	"github.com/william-lbn/ops-observability-platform/internal/store"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type Worker struct {
	Store         *store.Store
	ConfigPath    string
	Query         *query.Client
	Observations  map[string][]string
	observationMu sync.Mutex
	LastAttempt   atomic.Int64
	LastSuccess   atomic.Int64
	Failures      atomic.Uint64
}

func (w *Worker) Run(ctx context.Context) error {
	w.Query = query.New()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		w.LastAttempt.Store(time.Now().Unix())
		if e := w.Tick(ctx); e != nil {
			w.Failures.Add(1)
			println("worker cycle failed; retrying without advancing failed transactions")
		} else {
			w.LastSuccess.Store(time.Now().Unix())
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func (w *Worker) Metrics(out http.ResponseWriter, r *http.Request) {
	out.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(out, "ops_worker_last_attempt_timestamp_seconds %d\nops_worker_last_success_timestamp_seconds %d\nops_worker_failed_cycles_total %d\n", w.LastAttempt.Load(), w.LastSuccess.Load(), w.Failures.Load())
}
func (w *Worker) Tick(ctx context.Context) error {
	c, e := config.Load(w.ConfigPath)
	if e != nil {
		return e
	}
	tenants := map[string]bool{}
	w.observationMu.Lock()
	w.Observations = map[string][]string{}
	w.observationMu.Unlock()
	w.Store.DB.ExecContext(ctx, `DELETE FROM auth_sessions WHERE hash IN (SELECT hash FROM auth_sessions WHERE expires_at<now() ORDER BY expires_at LIMIT 100)`)
	w.Store.DB.ExecContext(ctx, `DELETE FROM auth_login_states WHERE hash IN (SELECT hash FROM auth_login_states WHERE expires_at<now() ORDER BY expires_at LIMIT 100)`)
	for _, s := range c.Sources {
		if s.Enabled {
			tenants[s.Tenant] = true
		}
	}
	var failures []error
	probeErrors := make(chan error, len(c.Sources)*2)
	slots := make(chan struct{}, 4)
	var probes sync.WaitGroup
	for _, src := range c.Sources {
		if !src.Enabled || src.Kind == "legacy_archive" {
			continue
		}
		src := src
		probes.Add(1)
		go func() {
			defer probes.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				probeErrors <- ctx.Err()
				return
			}
			probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			var e error
			if src.Kind == "telemetry_only" {
				e = w.telemetry(probeCtx, c, src)
			} else {
				e = w.catalog(probeCtx, src)
			}
			cancel()
			qualityCtx, finish := context.WithTimeout(ctx, 3*time.Second)
			qualityError := w.quality(qualityCtx, src, e)
			finish()
			if e != nil {
				probeErrors <- e
			}
			if qualityError != nil {
				probeErrors <- qualityError
			}
		}()
	}
	for tenant := range tenants {
		if e = w.project(ctx, tenant); e != nil {
			failures = append(failures, e)
		}
		if e = w.alerts(ctx, c, tenant); e != nil {
			failures = append(failures, e)
		}
	}
	probes.Wait()
	close(probeErrors)
	for e := range probeErrors {
		failures = append(failures, e)
	}
	return errors.Join(failures...)
}
func (w *Worker) project(ctx context.Context, tenant string) error {
	return w.Store.Tx(ctx, tenant, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT id,audit_sequence,payload FROM outbox WHERE tenant=$1 AND delivered_at IS NULL ORDER BY id LIMIT 100 FOR UPDATE SKIP LOCKED`, tenant)
		if e != nil {
			return e
		}
		type item struct {
			id, seq int64
			payload []byte
		}
		items := []item{}
		for rows.Next() {
			var i item
			if e = rows.Scan(&i.id, &i.seq, &i.payload); e != nil {
				rows.Close()
				return e
			}
			items = append(items, i)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, i := range items {
			if _, e = tx.ExecContext(ctx, `INSERT INTO projections(tenant,audit_sequence,payload) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, tenant, i.seq, i.payload); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `UPDATE outbox SET delivered_at=now(),attempts=attempts+1 WHERE tenant=$1 AND id=$2`, tenant, i.id); e != nil {
				return e
			}
		}
		return nil
	})
}
func (w *Worker) catalog(ctx context.Context, src config.Source) error {
	if src.Kind == "kubernetes_cluster" {
		return w.clusterCatalog(ctx, src)
	}
	return w.namespaceCatalog(ctx, src)
}
func (w *Worker) alerts(ctx context.Context, c config.Config, tenant string) error {
	b, e := w.Query.Get(ctx, c.Prometheus, "/api/v1/alerts", nil)
	if e != nil {
		return e
	}
	var response struct {
		Status string
		Data   struct {
			Alerts []struct {
				Labels      map[string]string
				Annotations map[string]string
				State       string
			}
		}
	}
	if e = json.Unmarshal(b, &response); e != nil {
		return e
	}
	if response.Status != "success" {
		return errors.New("alert query failed")
	}
	return w.Store.Tx(ctx, tenant, func(tx *sql.Tx) error {
		seen := []string{}
		for _, a := range response.Data.Alerts {
			if a.State != "firing" {
				continue
			}
			allowed := false
			for _, src := range c.Sources {
				if src.Enabled && src.Tenant == tenant && a.Labels["source"] == src.ID && a.Labels["ops_tenant"] == tenant {
					allowed = true
				}
			}
			if !allowed {
				continue
			}
			labels, _ := json.Marshal(a.Labels)
			hash := sha256.Sum256(labels)
			fingerprint := hex.EncodeToString(hash[:])
			seen = append(seen, fingerprint)
			title := a.Annotations["summary"]
			if title == "" {
				title = "观测规则触发，请核对用户影响"
			}
			if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "ops.incident:"+tenant+":"+fingerprint); e != nil {
				return e
			}
			var previous string
			previousError := tx.QueryRowContext(ctx, `SELECT state FROM incidents WHERE tenant=$1 AND fingerprint=$2 FOR UPDATE`, tenant, fingerprint).Scan(&previous)
			if previousError != nil && !errors.Is(previousError, sql.ErrNoRows) {
				return previousError
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO incidents(tenant,fingerprint,title,state,labels) VALUES($1,$2,$3,'firing',$4) ON CONFLICT(tenant,fingerprint) DO UPDATE SET title=EXCLUDED.title,labels=EXCLUDED.labels,state=CASE WHEN incidents.state='acknowledged' THEN 'acknowledged' ELSE 'firing' END,updated_at=now()`, tenant, fingerprint, title, labels)
			if e != nil {
				return e
			}
			if previous == "" || previous == "resolved" {
				if _, e = tx.ExecContext(ctx, `INSERT INTO notifications(tenant,fingerprint,transition_at,title,body) VALUES($1,$2,now(),$3,$4)`, tenant, fingerprint, title, "规则发现需要核验的症状。请核对服务影响、时间范围和最近证据；时间相近的变更不自动构成原因。"); e != nil {
					return e
				}
				if e = store.Audit(ctx, tx, tenant, "platform-worker", "incident.firing", fingerprint, "rule-observation", map[string]string{"title": title}); e != nil {
					return e
				}
			}
		}
		rows, e := tx.QueryContext(ctx, `UPDATE incidents SET state='resolved',updated_at=now() WHERE tenant=$1 AND state IN ('firing','acknowledged') AND NOT (fingerprint=ANY($2::text[])) RETURNING fingerprint,title`, tenant, seen)
		if e != nil {
			return e
		}
		type resolved struct{ fingerprint, title string }
		finished := []resolved{}
		for rows.Next() {
			var item resolved
			if e = rows.Scan(&item.fingerprint, &item.title); e != nil {
				rows.Close()
				return e
			}
			finished = append(finished, item)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, item := range finished {
			if _, e = tx.ExecContext(ctx, `INSERT INTO notifications(tenant,fingerprint,transition_at,title,body) VALUES($1,$2,now(),$3,$4)`, tenant, item.fingerprint, "规则恢复："+item.title, "当前规则已不再触发。服务恢复须结合请求样本确认；原操作结果和历史证据缺口保持独立。"); e != nil {
				return e
			}
			if e = store.Audit(ctx, tx, tenant, "platform-worker", "incident.rule_resolved", item.fingerprint, "rule-observation", nil); e != nil {
				return e
			}
		}
		return nil
	})
}
