package query

import (
	"context"
	"github.com/william-lbn/ops-observability-platform/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBackendFailureAndSelectorIsolation(t *testing.T) {
	var calls int
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(503)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer backend.Close()
	c := New()
	cfg := config.Config{Prometheus: backend.URL}
	src := config.Source{ID: "orders-a", Tenant: "team-a", Service: `reference"} or up{job=~".*`}
	if _, e := c.Execute(context.Background(), cfg, src, "metrics", "up"); e != ErrUnsupported || calls != 0 {
		t.Fatal("untrusted selector accepted")
	}
	src.Service = "reference-service"
	if _, e := c.Execute(context.Background(), cfg, src, "metrics", "up"); e == nil {
		t.Fatal("backend failure became empty success")
	}
	if calls != 1 {
		t.Fatal("backend failure test did not reach the registered backend")
	}
	if _, e := c.Execute(context.Background(), cfg, src, "metrics", "arbitrary-query"); e != ErrUnsupported {
		t.Fatal("arbitrary metric query accepted")
	}
}
func TestSameServiceNamesUseDistinctSecurityDomains(t *testing.T) {
	type observation struct{ query, tenant, path string }
	seen := []observation{}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, observation{r.URL.Query().Get("query"), r.Header.Get("X-Scope-OrgID"), r.URL.Path})
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"success","data":{"result":[]},"traces":[]}`))
	}))
	defer backend.Close()
	c := New()
	cfg := config.Config{Prometheus: backend.URL, Loki: backend.URL, Tempo: backend.URL}
	for _, tenant := range []string{"team-a", "team-b"} {
		s := config.Source{ID: "orders-" + tenant, Tenant: tenant, Service: "orders"}
		for _, signal := range []string{"metrics", "logs", "traces"} {
			if _, e := c.Execute(context.Background(), cfg, s, signal, "up"); e != nil {
				t.Fatal(e)
			}
		}
	}
	if len(seen) != 6 {
		t.Fatal("expected all registered backend queries")
	}
	for i, item := range seen {
		tenant := "team-a"
		if i >= 3 {
			tenant = "team-b"
		}
		if item.path == "/api/v1/query" {
			if !strings.Contains(item.query, `ops_tenant="`+tenant+`"`) || !strings.Contains(item.query, `job="orders-`+tenant+`"`) {
				t.Fatal("metrics security selector missing", item.query)
			}
		} else if item.tenant != tenant {
			t.Fatal("backend tenant boundary missing", item)
		}
	}
}
func TestRedirectDoesNotEscapeRegisteredBackend(t *testing.T) {
	var leaked bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer target.Close()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer backend.Close()
	if _, e := New().Get(context.Background(), backend.URL, "/api/v1/query", nil); e == nil || leaked {
		t.Fatal("backend redirect followed")
	}
}
