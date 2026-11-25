package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTenantSecurityDomainEnrollment(t *testing.T) {
	base := Config{Sources: []Source{{ID: "orders-a", Tenant: "tenant-a", Service: "orders", Enabled: true}, {ID: "orders-b", Tenant: "tenant-b", Service: "orders", Enabled: true}}}
	check := func(c Config, valid bool) {
		t.Helper()
		raw, _ := json.Marshal(c)
		path := filepath.Join(t.TempDir(), "config.json")
		os.WriteFile(path, raw, 0600)
		_, e := Load(path)
		if (e == nil) != valid {
			t.Fatalf("valid=%v error=%v", valid, e)
		}
	}
	check(base, true)
	base.Sources[1].StorageTenant = "tenant-a"
	check(base, false)
	base.Sources[1].StorageTenant = "tenant-b"
	base.Sources[1].ID = "orders-a"
	check(base, false)
	base.Sources[1].ID = "orders-b"
	base.Sources[1].Kind = "kubernetes_cluster"
	check(base, false)
	base.Sources = nil
	base.Principals = []Principal{{Subject: "a", Tenant: "tenant-a", Role: "platform_admin", OIDCSubject: "x"}}
	check(base, false)
}
