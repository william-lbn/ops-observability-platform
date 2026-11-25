package query

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/william-lbn/ops-observability-platform/internal/config"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

var ErrUnsupported = errors.New("unsupported query")
var ErrNotFound = errors.New("requested evidence not found in this security domain")
var label = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,128}$`)
var traceIdentity = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)

func (c *Client) Trace(ctx context.Context, cfg config.Config, s config.Source, id string) (json.RawMessage, error) {
	if !traceIdentity.MatchString(id) || !s.Supports("traces") {
		return nil, ErrUnsupported
	}
	tenant := s.TraceStorageTenant
	if tenant == "" {
		tenant = s.StorageTenant
	}
	if tenant == "" {
		tenant = s.Tenant
	}
	return c.get(ctx, cfg.Tempo, "/api/traces/"+id, nil, tenant)
}

type Client struct{ HTTP *http.Client }

func New() *Client {
	return &Client{&http.Client{Timeout: config.QueryTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil, MaxIdleConns: 20, MaxConnsPerHost: 8, ResponseHeaderTimeout: 5 * time.Second}}}
}
func (c *Client) Get(ctx context.Context, base, path string, params url.Values) (json.RawMessage, error) {
	return c.get(ctx, base, path, params, "")
}
func (c *Client) get(ctx context.Context, base, path string, params url.Values, tenant string) (json.RawMessage, error) {
	if base == "" {
		return nil, ErrUnsupported
	}
	u, e := url.Parse(base)
	if e != nil {
		return nil, e
	}
	u.Path = path
	u.RawQuery = params.Encode()
	r, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if e != nil {
		return nil, e
	}
	r.Header.Set("Accept", "application/json")
	if tenant != "" {
		r.Header.Set("X-Scope-OrgID", tenant)
	}
	res, e := c.HTTP.Do(r)
	if e != nil {
		return nil, e
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if res.StatusCode != 200 {
		return nil, errors.New("backend unavailable")
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, 2*1024*1024+1))
	if e != nil || len(b) > 2*1024*1024 || !json.Valid(b) {
		return nil, errors.New("invalid backend response")
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(b, &envelope) != nil || envelope == nil || string(envelope["status"]) == `"error"` {
		return nil, errors.New("invalid backend envelope")
	}
	return json.RawMessage(b), nil
}
func (c *Client) Execute(ctx context.Context, cfg config.Config, s config.Source, signal, metric string) (json.RawMessage, error) {
	if !s.Supports(signal) {
		return nil, ErrUnsupported
	}
	if !label.MatchString(s.Service) || !label.MatchString(s.ID) || !label.MatchString(s.Tenant) {
		return nil, ErrUnsupported
	}
	now := time.Now()
	lookback := 15 * time.Minute
	if s.Kind == "legacy_archive" {
		lookback = 48 * time.Hour
	}
	service := strconv.Quote(s.Service)
	storageTenant := s.StorageTenant
	if storageTenant == "" {
		storageTenant = s.Tenant
	}
	traceTenant := s.TraceStorageTenant
	if traceTenant == "" {
		traceTenant = storageTenant
	}
	switch signal {
	case "metrics":
		expr, e := metricExpression(s, metric)
		if e != nil {
			return nil, e
		}
		return c.Get(ctx, cfg.Prometheus, "/api/v1/query", url.Values{"query": {expr}, "timeout": {"5s"}})
	case "logs":
		return c.get(ctx, cfg.Loki, "/loki/api/v1/query_range", url.Values{"query": {"{service_name=" + service + "}"}, "start": {strconv.FormatInt(now.Add(-lookback).UnixNano(), 10)}, "end": {strconv.FormatInt(now.UnixNano(), 10)}, "limit": {"100"}}, storageTenant)
	case "traces":
		return c.get(ctx, cfg.Tempo, "/api/search", url.Values{"q": {"{resource.service.name = " + service + "}"}, "start": {strconv.FormatInt(now.Add(-lookback).Unix(), 10)}, "end": {strconv.FormatInt(now.Unix(), 10)}, "limit": {"20"}}, traceTenant)
	}
	return nil, ErrUnsupported
}
func metricExpression(s config.Source, metric string) (string, error) {
	if !s.Supports("metrics") || (metric != "up" && s.QueryProfile != "reference-http-v1") {
		return "", ErrUnsupported
	}
	if !label.MatchString(s.ID) || !label.MatchString(s.Tenant) {
		return "", ErrUnsupported
	}
	selector := `job=` + strconv.Quote(s.ID) + `,ops_tenant=` + strconv.Quote(s.Tenant)
	switch metric {
	case "up":
		return `up{` + selector + `}`, nil
	case "requests":
		return `sum(rate(reference_requests_total{` + selector + `}[5m]))`, nil
	case "errors":
		return `sum(rate(reference_requests_total{` + selector + `,code="500"}[5m]))`, nil
	case "error_ratio":
		return `sum(rate(reference_requests_total{` + selector + `,code="500"}[5m]))/sum(rate(reference_requests_total{` + selector + `}[5m]))`, nil
	case "latency_p95":
		return `histogram_quantile(0.95,sum by(le)(rate(reference_request_duration_seconds_bucket{` + selector + `}[5m])))`, nil
	}
	return "", ErrUnsupported
}
func (c *Client) Range(ctx context.Context, cfg config.Config, s config.Source, metric, window string) (json.RawMessage, error) {
	expr, e := metricExpression(s, metric)
	if e != nil {
		return nil, e
	}
	duration := 15 * time.Minute
	step := "30s"
	if window == "1h" {
		duration = time.Hour
		step = "60s"
	} else if window != "15m" {
		return nil, ErrUnsupported
	}
	now := time.Now()
	return c.Get(ctx, cfg.Prometheus, "/api/v1/query_range", url.Values{"query": {expr}, "start": {strconv.FormatInt(now.Add(-duration).Unix(), 10)}, "end": {strconv.FormatInt(now.Unix(), 10)}, "step": {step}, "timeout": {"5s"}})
}
