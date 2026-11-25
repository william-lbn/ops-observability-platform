package api

import "testing"

func TestPlatformWarningsAreScopedToInfrastructure(t *testing.T) {
	for _, namespace := range []string{"ops-observability", "kube-system", "cert-manager"} {
		if !platformWorkloadMonitored(namespace) {
			t.Errorf("missing managed infrastructure namespace %q", namespace)
		}
	}
	for _, namespace := range []string{"", "neon", "neon-controller", "neon-reinstall", "ops-tenant-a", "kube-system-migration"} {
		if platformWorkloadMonitored(namespace) {
			t.Errorf("business or maintenance workload reported as platform fault: %q", namespace)
		}
	}
}
