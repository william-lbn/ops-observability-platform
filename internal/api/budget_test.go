package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/william-lbn/ops-observability-platform/internal/config"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTenantBudgetDoesNotConsumeAnotherTenantsReservedAccess(t *testing.T) {
	arrived := make(chan struct{}, 4)
	release := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		<-release
		w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
	}))
	defer backend.Close()
	tokens := []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}
	cfg := config.Config{Prometheus: backend.URL, Sources: []config.Source{{ID: "orders-a", Tenant: "a", Service: "orders", Enabled: true}}}
	for i, tenant := range []string{"a", "b"} {
		hash := sha256.Sum256([]byte(tokens[i]))
		cfg.Principals = append(cfg.Principals, config.Principal{Subject: tenant, Tenant: tenant, Role: "viewer", TokenSHA256: hex.EncodeToString(hash[:])})
	}
	raw, _ := json.Marshal(cfg)
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, raw, 0600)
	s, e := New(nil, path, http.NotFoundHandler())
	if e != nil {
		t.Fatal(e)
	}
	request := func(path, token string) int {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w.Code
	}
	var jobs sync.WaitGroup
	codes := make(chan int, 4)
	for i := 0; i < 4; i++ {
		jobs.Add(1)
		go func() {
			defer jobs.Done()
			codes <- request("/api/v1/query?source=orders-a&signal=metrics&metric=up", tokens[0])
		}()
	}
	defer func() {
		close(release)
		jobs.Wait()
		close(codes)
		for code := range codes {
			if code != 200 {
				t.Errorf("held backend request failed: %d", code)
			}
		}
	}()
	for i := 0; i < 4; i++ {
		select {
		case <-arrived:
		case <-time.After(4 * time.Second):
			t.Fatal("registered backend requests did not hold the tenant budget")
		}
	}
	if code := request("/api/v1/query?source=orders-a&signal=metrics&metric=up", tokens[0]); code != 429 {
		t.Fatalf("tenant burst accepted: %d", code)
	}
	if code := request("/api/v1/me", tokens[1]); code != 200 {
		t.Fatalf("another tenant lost reserved access: %d", code)
	}
}
