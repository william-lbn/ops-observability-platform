package main

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var ok, failed, dropped atomic.Uint64
var queue = make(chan map[string]any, 256)
var durationCount atomic.Uint64
var durationMicros atomic.Uint64
var durationBuckets [8]atomic.Uint64
var durationBounds = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 1, 5}
var durationMu sync.Mutex

func attr(k, v string) map[string]any {
	return map[string]any{"key": k, "value": map[string]string{"stringValue": v}}
}
func main() {
	base := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	service := os.Getenv("OTEL_SERVICE_NAME")
	if service == "" {
		service = "reference-service"
	}
	transport := &http.Transport{Proxy: nil}
	if caFile := os.Getenv("OTEL_EXPORTER_OTLP_CERTIFICATE"); caFile != "" {
		ca, e := os.ReadFile(caFile)
		roots := x509.NewCertPool()
		if e != nil || !roots.AppendCertsFromPEM(ca) {
			panic("telemetry trust configuration rejected")
		}
		cert, e := tls.LoadX509KeyPair(os.Getenv("OTEL_EXPORTER_OTLP_CLIENT_CERTIFICATE"), os.Getenv("OTEL_EXPORTER_OTLP_CLIENT_KEY"))
		if e != nil {
			panic("telemetry client identity rejected")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		if !strings.HasPrefix(base, "https://") {
			panic("encrypted telemetry endpoint required")
		}
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	go func() {
		for item := range queue {
			for path, body := range item {
				b, _ := json.Marshal(body)
				request, e := http.NewRequest("POST", base+path, bytes.NewReader(b))
				if e != nil {
					dropped.Add(1)
					continue
				}
				request.Header.Set("Content-Type", "application/json")
				for _, pair := range strings.Split(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"), ",") {
					key, value, found := strings.Cut(pair, "=")
					if found {
						decoded, error := url.QueryUnescape(value)
						if error == nil {
							request.Header.Set(strings.TrimSpace(key), decoded)
						}
					}
				}
				r, e := client.Do(request)
				if e != nil {
					dropped.Add(1)
					continue
				}
				io.Copy(io.Discard, io.LimitReader(r.Body, 4096))
				r.Body.Close()
				if r.StatusCode/100 != 2 {
					dropped.Add(1)
				}
			}
		}
	}()
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "reference_requests_total{code=\"200\"} %d\nreference_requests_total{code=\"500\"} %d\nreference_telemetry_dropped_total %d\n", ok.Load(), failed.Load(), dropped.Load())
		durationMu.Lock()
		count, sum := durationCount.Load(), durationMicros.Load()
		var buckets [8]uint64
		for i := range buckets {
			buckets[i] = durationBuckets[i].Load()
		}
		durationMu.Unlock()
		for i, bound := range durationBounds {
			fmt.Fprintf(w, "reference_request_duration_seconds_bucket{le=%q} %d\n", strconv.FormatFloat(bound, 'g', -1, 64), buckets[i])
		}
		fmt.Fprintf(w, "reference_request_duration_seconds_bucket{le=\"+Inf\"} %d\nreference_request_duration_seconds_count %d\nreference_request_duration_seconds_sum %f\n", count, count, float64(sum)/1e6)
	})
	http.HandleFunc("/work", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		defer func() {
			elapsed := time.Since(start)
			durationMu.Lock()
			defer durationMu.Unlock()
			durationCount.Add(1)
			durationMicros.Add(uint64(elapsed.Microseconds()))
			for i, bound := range durationBounds {
				if elapsed.Seconds() <= bound {
					durationBuckets[i].Add(1)
				}
			}
		}()
		if delay, e := strconv.Atoi(r.URL.Query().Get("delay_ms")); e == nil && delay > 0 && delay <= 3000 {
			time.Sleep(time.Duration(delay) * time.Millisecond)
		}
		trace := make([]byte, 16)
		span := make([]byte, 8)
		rand.Read(trace)
		rand.Read(span)
		traceID := hex.EncodeToString(trace)
		spanID := hex.EncodeToString(span)
		code := 200
		msg := "参考请求已完成"
		if r.URL.Query().Get("fail") == "1" {
			code = 500
			msg = "参考故障请求，供独立验收使用"
			failed.Add(1)
		} else {
			ok.Add(1)
		}
		resource := map[string]any{"attributes": []any{attr("service.name", service), attr("service.version", "0.1.0"), attr("deployment.environment.name", "acceptance")}}
		ts := strconv.FormatInt(start.UnixNano(), 10)
		traceBody := map[string]any{"resourceSpans": []any{map[string]any{"resource": resource, "scopeSpans": []any{map[string]any{"scope": map[string]string{"name": "ops-reference"}, "spans": []any{map[string]any{"traceId": traceID, "spanId": spanID, "name": "参考服务请求", "kind": 2, "startTimeUnixNano": ts, "endTimeUnixNano": strconv.FormatInt(time.Now().UnixNano(), 10), "attributes": []any{attr("http.request.method", "GET")}, "status": map[string]int{"code": map[bool]int{true: 2, false: 1}[code == 500]}}}}}}}}
		logs := map[string]any{"resourceLogs": []any{map[string]any{"resource": resource, "scopeLogs": []any{map[string]any{"scope": map[string]string{"name": "ops-reference"}, "logRecords": []any{map[string]any{"timeUnixNano": ts, "observedTimeUnixNano": ts, "severityNumber": map[bool]int{true: 17, false: 9}[code == 500], "severityText": map[bool]string{true: "ERROR", false: "INFO"}[code == 500], "body": map[string]string{"stringValue": msg}, "traceId": traceID, "spanId": spanID}}}}}}}
		select {
		case queue <- map[string]any{"/v1/traces": traceBody, "/v1/logs": logs}:
		default:
			dropped.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Trace-ID", traceID)
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]any{"message": msg, "traceId": traceID, "service": service, "observedAt": time.Now().UTC()})
	})
	s := &http.Server{Addr: ":8081", ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 10 * time.Second}
	if e := s.ListenAndServe(); e != nil {
		println("reference server stopped")
	}
}
