package worker

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPodObservationDoesNotCopyFreeTextOrCredentials(t *testing.T) {
	input := map[string]any{"phase": "Running", "message": "password=DO_NOT_COPY", "conditions": []any{map[string]any{"type": "Ready", "status": "True", "message": "token=DO_NOT_COPY"}}, "containerStatuses": []any{map[string]any{"name": "api", "ready": true, "restartCount": float64(2), "state": map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff", "message": "secret=DO_NOT_COPY"}}}}}
	raw, _ := json.Marshal(safePodStatus(input))
	if strings.Contains(string(raw), "DO_NOT_COPY") || !strings.Contains(string(raw), "CrashLoopBackOff") {
		t.Fatal("observation copied unbounded sensitive text or lost actionable reason", string(raw))
	}
}
