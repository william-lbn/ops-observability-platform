package worker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/william-lbn/ops-observability-platform/internal/config"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

var ErrSourceIdentity = errors.New("source native identity changed")
var ErrSourceIncomplete = errors.New("expected collection targets are missing")

func (w *Worker) telemetry(ctx context.Context, c config.Config, src config.Source) error {
	raw, e := w.Query.Get(ctx, c.Prometheus, "/api/v1/query", url.Values{"query": {`up{job=` + strconv.Quote(src.ID) + `,ops_tenant=` + strconv.Quote(src.Tenant) + `}`}, "timeout": {"5s"}})
	if e != nil {
		return e
	}
	var response struct {
		Status string
		Data   struct {
			Result []struct {
				Metric map[string]string
				Value  []any
			}
		}
	}
	if e = json.Unmarshal(raw, &response); e != nil {
		return e
	}
	if response.Status != "success" {
		return errors.New("collection health query failed")
	}
	observed := []string{}
	seen := map[string]bool{}
	for _, result := range response.Data.Result {
		if len(result.Value) < 2 || result.Value[1] != "1" {
			continue
		}
		target := result.Metric["node"]
		if target == "" {
			target = result.Metric["instance"]
		}
		observed = append(observed, target)
		seen[target] = true
	}
	w.observe(src.ID, observed)
	for _, expected := range src.ExpectedTargets {
		if !seen[expected] {
			return ErrSourceIncomplete
		}
	}
	return nil
}

type kubeObject struct {
	Metadata struct{ UID, Name, Namespace string }
	Status   map[string]any
}
type kubeList struct{ Items []kubeObject }

func readKubernetes(ctx context.Context, path string, result any) error {
	const sa = "/var/run/secrets/kubernetes.io/serviceaccount/"
	token, e := os.ReadFile(sa + "token")
	if e != nil {
		return e
	}
	ca, e := os.ReadFile(sa + "ca.crt")
	if e != nil {
		return e
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return errors.New("invalid cluster trust root")
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	req, e := http.NewRequestWithContext(ctx, "GET", "https://kubernetes.default.svc"+path, nil)
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	response, e := client.Do(req)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("cluster read status %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 16*1024*1024)).Decode(result)
}
func (w *Worker) saveSnapshot(ctx context.Context, tx *sql.Tx, src config.Source, uid, namespace, name, kind string, snapshot any) error {
	raw, e := json.Marshal(snapshot)
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO catalog_history(tenant,source,uid,snapshot,observed_at) SELECT tenant,source,uid,snapshot,observed_at FROM catalog WHERE tenant=$1 AND source=$2 AND uid=$3 AND snapshot IS DISTINCT FROM $4::jsonb ON CONFLICT DO NOTHING`, src.Tenant, src.ID, uid, raw); e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO catalog(tenant,source,uid,namespace,name,kind,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(tenant,source,uid) DO UPDATE SET snapshot=EXCLUDED.snapshot,observed_at=now()`, src.Tenant, src.ID, uid, namespace, name, kind, raw)
	return e
}
func safeConditions(raw any) []map[string]any {
	result := []map[string]any{}
	items, _ := raw.([]any)
	for _, item := range items {
		if c, ok := item.(map[string]any); ok {
			result = append(result, map[string]any{"type": c["type"], "status": c["status"], "reason": c["reason"], "lastTransitionTime": c["lastTransitionTime"]})
		}
	}
	return result
}
func safePodStatus(status map[string]any) map[string]any {
	result := map[string]any{"phase": status["phase"], "reason": status["reason"], "conditions": safeConditions(status["conditions"])}
	containers := []map[string]any{}
	items, _ := status["containerStatuses"].([]any)
	for _, item := range items {
		c, ok := item.(map[string]any)
		if !ok {
			continue
		}
		container := map[string]any{"name": c["name"], "ready": c["ready"], "restartCount": c["restartCount"]}
		state, _ := c["state"].(map[string]any)
		if waiting, ok := state["waiting"].(map[string]any); ok {
			container["waitingReason"] = waiting["reason"]
		}
		if terminated, ok := state["terminated"].(map[string]any); ok {
			container["exitCode"] = terminated["exitCode"]
			container["finishedAt"] = terminated["finishedAt"]
		}
		containers = append(containers, container)
	}
	result["containers"] = containers
	return result
}
func (w *Worker) namespaceCatalog(ctx context.Context, src config.Source) error {
	var namespace kubeObject
	if e := readKubernetes(ctx, "/api/v1/namespaces/"+url.PathEscape(src.Namespace), &namespace); e != nil {
		return e
	}
	if src.NamespaceUID != "" && src.NamespaceUID != namespace.Metadata.UID {
		return ErrSourceIdentity
	}
	var pods kubeList
	path := "/api/v1/namespaces/" + url.PathEscape(src.Namespace) + "/pods"
	if src.Kind == "" {
		path += "?labelSelector=" + url.QueryEscape("app.kubernetes.io/part-of=ops-observability-platform")
	}
	if e := readKubernetes(ctx, path, &pods); e != nil {
		return e
	}
	w.observe(src.ID, []string{namespace.Metadata.UID})
	return w.Store.Tx(ctx, src.Tenant, func(tx *sql.Tx) error {
		for _, pod := range pods.Items {
			if e := w.saveSnapshot(ctx, tx, src, pod.Metadata.UID, pod.Metadata.Namespace, pod.Metadata.Name, "kubernetes.pod", safePodStatus(pod.Status)); e != nil {
				return e
			}
		}
		return nil
	})
}
func (w *Worker) clusterCatalog(ctx context.Context, src config.Source) error {
	var systemNamespace kubeObject
	if e := readKubernetes(ctx, "/api/v1/namespaces/kube-system", &systemNamespace); e != nil {
		return e
	}
	if src.ClusterUID != "" && src.ClusterUID != systemNamespace.Metadata.UID {
		return ErrSourceIdentity
	}
	var nodes, namespaces, pods, pvcs, deployments, quotas kubeList
	for path, target := range map[string]*kubeList{"/api/v1/nodes": &nodes, "/api/v1/namespaces": &namespaces, "/api/v1/pods": &pods, "/api/v1/persistentvolumeclaims": &pvcs, "/apis/apps/v1/deployments": &deployments, "/api/v1/resourcequotas": &quotas} {
		if e := readKubernetes(ctx, path, target); e != nil {
			return e
		}
	}
	var usage struct {
		Items []struct {
			Metadata  struct{ Name string }
			Timestamp string
			Usage     map[string]string
		}
	}
	usageError := readKubernetes(ctx, "/apis/metrics.k8s.io/v1beta1/nodes", &usage)
	usageByNode := map[string]any{}
	usageFresh := map[string]bool{}
	if usageError == nil {
		for _, item := range usage.Items {
			usageByNode[item.Metadata.Name] = map[string]any{"values": item.Usage, "asOf": item.Timestamp, "source": "metrics-server"}
			at, e := time.Parse(time.RFC3339Nano, item.Timestamp)
			usageFresh[item.Metadata.Name] = e == nil && time.Since(at) <= 90*time.Second && time.Until(at) < 5*time.Second
		}
	}
	type nsHealth struct {
		Pods, Ready, Pending, Failed, Completed, Restarts, CrashLoops int
		PVCs, PendingPVCs                                             int
		ControllerFailures                                            int
		Quotas                                                        []map[string]any
	}
	health := map[string]*nsHealth{}
	for _, ns := range namespaces.Items {
		health[ns.Metadata.Name] = &nsHealth{}
	}
	for _, p := range pods.Items {
		h := health[p.Metadata.Namespace]
		if h == nil {
			continue
		}
		h.Pods++
		phase, _ := p.Status["phase"].(string)
		switch phase {
		case "Pending":
			h.Pending++
		case "Failed":
			h.Failed++
		case "Succeeded":
			h.Completed++
		}
		for _, condition := range safeConditions(p.Status["conditions"]) {
			if condition["type"] == "Ready" && condition["status"] == "True" {
				h.Ready++
			}
		}
		items, _ := p.Status["containerStatuses"].([]any)
		for _, item := range items {
			c, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if n, ok := c["restartCount"].(float64); ok {
				h.Restarts += int(n)
			}
			state, _ := c["state"].(map[string]any)
			waiting, _ := state["waiting"].(map[string]any)
			if waiting["reason"] == "CrashLoopBackOff" || waiting["reason"] == "ImagePullBackOff" || waiting["reason"] == "ErrImagePull" {
				h.CrashLoops++
			}
		}
	}
	for _, pvc := range pvcs.Items {
		if h := health[pvc.Metadata.Namespace]; h != nil {
			h.PVCs++
			if pvc.Status["phase"] == "Pending" {
				h.PendingPVCs++
			}
		}
	}
	for _, deployment := range deployments.Items {
		if h := health[deployment.Metadata.Namespace]; h != nil {
			for _, condition := range safeConditions(deployment.Status["conditions"]) {
				if condition["type"] == "ReplicaFailure" && condition["status"] == "True" {
					h.ControllerFailures++
				}
			}
		}
	}
	for _, quota := range quotas.Items {
		if h := health[quota.Metadata.Namespace]; h != nil {
			h.Quotas = append(h.Quotas, map[string]any{"name": quota.Metadata.Name, "hard": quota.Status["hard"], "used": quota.Status["used"]})
		}
	}
	observed := []string{}
	for _, node := range nodes.Items {
		observed = append(observed, node.Metadata.UID)
	}
	w.observe(src.ID, observed)
	err := w.Store.Tx(ctx, src.Tenant, func(tx *sql.Tx) error {
		for _, node := range nodes.Items {
			data := map[string]any{"conditions": safeConditions(node.Status["conditions"]), "capacity": node.Status["capacity"], "allocatable": node.Status["allocatable"], "nodeInfo": node.Status["nodeInfo"], "usage": usageByNode[node.Metadata.Name], "usageAvailable": usageError == nil && usageFresh[node.Metadata.Name]}
			if e := w.saveSnapshot(ctx, tx, src, node.Metadata.UID, "", node.Metadata.Name, "kubernetes.node", data); e != nil {
				return e
			}
		}
		for _, ns := range namespaces.Items {
			h := health[ns.Metadata.Name]
			summary := map[string]any{"pods": h.Pods, "ready": h.Ready, "pending": h.Pending, "historicalFailed": h.Failed, "completed": h.Completed, "restarts": h.Restarts, "crashLoops": h.CrashLoops, "pvcs": h.PVCs, "pendingPVCs": h.PendingPVCs, "controllerFailures": h.ControllerFailures, "quotas": h.Quotas}
			if e := w.saveSnapshot(ctx, tx, src, ns.Metadata.UID, ns.Metadata.Name, ns.Metadata.Name, "kubernetes.namespace.health", summary); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, uid := range observed {
		seen[uid] = true
	}
	for _, uid := range src.ExpectedNodeUIDs {
		if !seen[uid] {
			return ErrSourceIncomplete
		}
	}
	return nil
}
func (w *Worker) quality(ctx context.Context, src config.Source, probeError error) error {
	expected := src.ExpectedNodeUIDs
	if src.Kind == "telemetry_only" {
		expected = src.ExpectedTargets
	}
	if src.Kind != "kubernetes_cluster" && src.Kind != "telemetry_only" {
		expected = []string{src.NamespaceUID}
		if src.NamespaceUID == "" {
			expected = []string{src.ID}
		}
	}
	w.observationMu.Lock()
	observed := append([]string{}, w.Observations[src.ID]...)
	w.observationMu.Unlock()
	if observed == nil {
		observed = []string{}
	}
	expectedRaw, _ := json.Marshal(expected)
	observedRaw, _ := json.Marshal(observed)
	state, code := "observed_partial", ""
	var success any = time.Now().UTC()
	if probeError != nil {
		state = "unavailable"
		code = "source.read_unavailable"
		success = nil
		if errors.Is(probeError, ErrSourceIdentity) {
			state = "identity_changed"
			code = "source.identity_changed"
		}
		if errors.Is(probeError, ErrSourceIncomplete) {
			state = "observed_incomplete"
			code = "source.expected_targets_missing"
			success = time.Now().UTC()
		}
	}
	return w.Store.Tx(ctx, src.Tenant, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `INSERT INTO source_quality(tenant,source,state,last_success_at,error_code,expected_set,observed_set) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(tenant,source) DO UPDATE SET state=EXCLUDED.state,last_attempt_at=now(),last_success_at=COALESCE(EXCLUDED.last_success_at,source_quality.last_success_at),error_code=EXCLUDED.error_code,expected_set=EXCLUDED.expected_set,observed_set=CASE WHEN EXCLUDED.last_success_at IS NULL THEN source_quality.observed_set ELSE EXCLUDED.observed_set END`, src.Tenant, src.ID, state, success, code, expectedRaw, observedRaw)
		return e
	})
}
func (w *Worker) observe(source string, ids []string) {
	w.observationMu.Lock()
	defer w.observationMu.Unlock()
	w.Observations[source] = append([]string{}, ids...)
}
