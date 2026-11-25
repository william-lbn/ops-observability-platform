package config

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"regexp"
	"time"
)

type Principal struct {
	TokenSHA256 string `json:"tokenSHA256"`
	Subject     string `json:"subject"`
	Tenant      string `json:"tenant"`
	Role        string `json:"role"`
	Disabled    bool   `json:"disabled"`
	OIDCSubject string `json:"oidcSubject"`
}
type Source struct {
	ID                 string   `json:"id"`
	Tenant             string   `json:"tenant"`
	Service            string   `json:"service"`
	Namespace          string   `json:"namespace"`
	Enabled            bool     `json:"enabled"`
	Kind               string   `json:"kind,omitempty"`
	StorageTenant      string   `json:"storageTenant,omitempty"`
	TraceStorageTenant string   `json:"traceStorageTenant,omitempty"`
	NamespaceUID       string   `json:"namespaceUID,omitempty"`
	ClusterUID         string   `json:"clusterUID,omitempty"`
	ExpectedNodeUIDs   []string `json:"expectedNodeUIDs,omitempty"`
	ExpectedTargets    []string `json:"expectedTargets,omitempty"`
	Signals            []string `json:"signals,omitempty"`
	QueryProfile       string   `json:"queryProfile,omitempty"`
	DisplayName        string   `json:"displayName,omitempty"`
}
type Config struct {
	DatabaseURL      string      `json:"databaseURL"`
	Listen           string      `json:"listen"`
	PublicURL        string      `json:"publicURL"`
	TLSCert          string      `json:"tlsCert"`
	TLSKey           string      `json:"tlsKey"`
	Prometheus       string      `json:"prometheus"`
	Loki             string      `json:"loki"`
	Tempo            string      `json:"tempo"`
	Collector        string      `json:"collector"`
	Principals       []Principal `json:"principals"`
	Sources          []Source    `json:"sources"`
	NeonEnabled      bool        `json:"neonEnabled"`
	OIDCIssuer       string      `json:"oidcIssuer"`
	OIDCClientID     string      `json:"oidcClientID"`
	OIDCClientSecret string      `json:"oidcClientSecret"`
	OIDCBackend      string      `json:"oidcBackend"`
	PlatformTenant   string      `json:"platformTenant"`
}

func Load(path string) (Config, error) {
	var c Config
	b, e := os.ReadFile(path)
	if e != nil {
		return c, e
	}
	e = json.Unmarshal(b, &c)
	if e != nil {
		return c, e
	}
	if c.NeonEnabled {
		return c, errors.New("Neon provider is outside this release scope")
	}
	if c.Listen == "" {
		c.Listen = ":8443"
	}
	if c.PlatformTenant == "" {
		c.PlatformTenant = "platform"
	}
	for _, v := range []string{c.Prometheus, c.Loki, c.Tempo, c.Collector, c.OIDCBackend, c.OIDCIssuer} {
		if v == "" {
			continue
		}
		u, e := url.Parse(v)
		if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return c, errors.New("invalid backend endpoint")
		}
	}
	if c.OIDCIssuer != "" && (c.OIDCBackend == "" || c.OIDCClientID == "" || c.OIDCClientSecret == "" || c.OIDCIssuer != c.PublicURL+"/identity") {
		return c, errors.New("incomplete or mismatched identity configuration")
	}
	seenOIDC := map[string]bool{}
	seenToken := map[string]bool{}
	for _, p := range c.Principals {
		if (len(p.TokenSHA256) != 64 && p.OIDCSubject == "") || p.Tenant == "" || p.Subject == "" || (p.Role != "admin" && p.Role != "viewer" && p.Role != "ingest" && p.Role != "platform_admin" && p.Role != "platform_viewer") {
			return c, errors.New("invalid principal")
		}
		if (p.Role == "platform_admin" || p.Role == "platform_viewer") && p.Tenant != c.PlatformTenant {
			return c, errors.New("platform privilege cannot be bound to a business tenant")
		}
		if p.OIDCSubject != "" {
			if seenOIDC[p.OIDCSubject] {
				return c, errors.New("ambiguous identity binding")
			}
			seenOIDC[p.OIDCSubject] = true
		}
		if p.TokenSHA256 != "" {
			if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(p.TokenSHA256) || seenToken[p.TokenSHA256] {
				return c, errors.New("invalid or ambiguous credential binding")
			}
			seenToken[p.TokenSHA256] = true
		}
	}
	identifier := regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,128}$`)
	sources := map[string]bool{}
	partitions := map[string]string{}
	for _, s := range c.Sources {
		if !identifier.MatchString(s.ID) || !identifier.MatchString(s.Tenant) || sources[s.ID] {
			return c, errors.New("invalid or duplicate registered source identity")
		}
		sources[s.ID] = true
		for _, partition := range []string{s.StorageTenant, s.TraceStorageTenant} {
			if partition == "" {
				partition = s.Tenant
			}
			if !identifier.MatchString(partition) {
				return c, errors.New("invalid storage security domain")
			}
			if owner, ok := partitions[partition]; ok && owner != s.Tenant {
				return c, errors.New("storage security domain is shared by distinct tenants")
			}
			partitions[partition] = s.Tenant
		}
		if s.Kind == "kubernetes_cluster" && s.Tenant != c.PlatformTenant {
			return c, errors.New("cluster inventory belongs to the platform security domain")
		}
		switch s.Kind {
		case "", "legacy_archive":
		case "telemetry_only":
			if len(s.ExpectedTargets) == 0 {
				return c, errors.New("telemetry enrollment requires fixed collection targets")
			}
		case "kubernetes_namespace":
			if s.Namespace == "" || s.NamespaceUID == "" {
				return c, errors.New("namespace enrollment requires its native identity")
			}
		case "kubernetes_cluster":
			if s.ClusterUID == "" || len(s.ExpectedNodeUIDs) == 0 {
				return c, errors.New("cluster enrollment requires frozen native identities")
			}
		default:
			return c, errors.New("unsupported source kind")
		}
	}
	for i := range c.Sources {
		if c.Sources[i].QueryProfile == "" {
			c.Sources[i].QueryProfile = "readonly-v1"
		}
		if c.Sources[i].QueryProfile != "readonly-v1" && c.Sources[i].QueryProfile != "reference-http-v1" {
			return c, errors.New("unsupported query capability profile")
		}
		for _, signal := range c.Sources[i].Signals {
			if signal != "metrics" && signal != "logs" && signal != "traces" && signal != "inventory" {
				return c, errors.New("unsupported source capability")
			}
		}
	}
	return c, nil
}

func (s Source) Supports(signal string) bool {
	if len(s.Signals) > 0 {
		for _, v := range s.Signals {
			if v == signal {
				return true
			}
		}
		return false
	}
	switch s.Kind {
	case "kubernetes_cluster":
		return signal == "inventory"
	case "legacy_archive":
		return signal == "logs" || signal == "traces"
	case "telemetry_only":
		return signal == "metrics"
	default:
		return signal == "inventory" || signal == "metrics" || signal == "logs" || signal == "traces"
	}
}

const QueryTimeout = 8 * time.Second
