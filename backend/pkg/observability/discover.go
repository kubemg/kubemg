package observability

import (
	"slices"
	"strconv"
	"strings"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Finding the datasource that is already there.
 *
 * Practically every cluster KubeMG gets registered against is already running
 * something that stores series — a kube-prometheus-stack, a VictoriaMetrics
 * single, a Loki. Making an operator go and read a Service list to fill in a
 * form is the difference between metrics that get connected and metrics that do
 * not, so KubeMG looks first and offers what it found.
 *
 * A match is a suggestion and never a configuration: it is scored, shown with
 * the reason it matched, and only stored once someone picks it.
 */

// ServicePort is one port a Service exposes.
type ServicePort struct {
	Name string
	Port int32
}

// ServiceRef is one Service in the cluster, reduced to what matching needs.
type ServiceRef struct {
	Namespace string
	Name      string
	Ports     []ServicePort
}

// Candidate is a datasource KubeMG believes is already running, in the shape the
// registration form takes.
type Candidate struct {
	Kind     string `json:"kind"`
	Provider string `json:"provider"`

	ServiceNamespace string `json:"service_namespace"`
	ServiceName      string `json:"service_name"`
	ServicePort      string `json:"service_port"`
	ServiceScheme    string `json:"service_scheme"`
	PathPrefix       string `json:"path_prefix,omitempty"`

	// Score orders the list; the port matching the provider's own default is
	// what separates "this is it" from "this is named like it".
	Score int `json:"score"`
	// Reason is why this row is here, so a wrong guess is visibly a guess.
	Reason string `json:"reason"`
}

// signature is one provider's fingerprint in a Service list.
type signature struct {
	kind     string
	provider string
	// names are substrings of a Service name that indicate this provider.
	names []string
	// ports are the ports it conventionally serves its query API on.
	ports []int32
	// prefix is the path its query API lives under, when it is not the root.
	// vmselect and Mimir both serve Prometheus's API under a prefix, and getting
	// that wrong is the single most common reason a correct address 404s.
	prefix func(serviceName string) string
}

// signatures are ordered from most specific to least: a Service named
// "vmselect" is VictoriaMetrics before it is anything else.
var signatures = []signature{
	{
		kind:     db.SourceMetrics,
		provider: db.ProviderVictoriaMetrics,
		names:    []string{"vmselect", "vmsingle", "victoria-metrics", "victoriametrics", "vmauth"},
		ports:    []int32{8481, 8428, 8427},
		prefix: func(name string) string {
			// vmselect serves the Prometheus API per tenant; single-node serves
			// it at the root. Tenant 0 is the default a cluster-wide install
			// writes to.
			if strings.Contains(name, "vmselect") || strings.Contains(name, "vmauth") {
				return "/select/0/prometheus"
			}
			return ""
		},
	},
	{
		kind:     db.SourceMetrics,
		provider: db.ProviderThanos,
		names:    []string{"thanos-query", "thanos-querier"},
		ports:    []int32{9090, 10902, 10901},
	},
	{
		kind:     db.SourceMetrics,
		provider: db.ProviderMimir,
		names:    []string{"mimir-query-frontend", "mimir-nginx", "mimir-gateway"},
		ports:    []int32{8080, 80},
		prefix:   func(string) string { return "/prometheus" },
	},
	{
		kind:     db.SourceMetrics,
		provider: db.ProviderPrometheus,
		names:    []string{"prometheus"},
		ports:    []int32{9090},
	},
	{
		kind:     db.SourceLogs,
		provider: db.ProviderVictoriaLogs,
		names:    []string{"vlselect", "vlsingle", "victoria-logs", "victorialogs", "vlogs"},
		ports:    []int32{9428, 9471},
	},
	{
		kind:     db.SourceLogs,
		provider: db.ProviderLoki,
		names:    []string{"loki"},
		ports:    []int32{3100, 80, 8080},
	},
}

// excluded are Services that carry a provider's name without serving its query
// API. Offering a node-exporter as "the metrics backend" would be worse than
// offering nothing: it answers on /metrics, so it looks alive and returns
// nothing anyone asked for.
var excluded = []string{
	"node-exporter",
	"kube-state-metrics",
	"alertmanager",
	"pushgateway",
	"operator",
	"operated",
	"headless",
	"canary",
	"agent",
	"vmagent",
	"vminsert",
	"vlinsert",
	"promtail",
	"grafana",
	"exporter",
	"metrics-server",
	"vmalert",
	"ruler",
	"compactor",
	"distributor",
	"ingester",
	"store-gateway",
	"memberlist",
}

// Discover matches a cluster's Services against the providers KubeMG knows,
// highest confidence first.
func Discover(services []ServiceRef) []Candidate {
	out := []Candidate{}

	for _, service := range services {
		name := strings.ToLower(service.Name)
		if isExcluded(name) {
			continue
		}

		for _, sig := range signatures {
			if !matchesName(name, sig.names) {
				continue
			}
			port, exact := pickPort(service.Ports, sig.ports)
			if port == "" {
				continue
			}

			candidate := Candidate{
				Kind:             sig.kind,
				Provider:         sig.provider,
				ServiceNamespace: service.Namespace,
				ServiceName:      service.Name,
				ServicePort:      port,
				ServiceScheme:    "http",
				Score:            1,
				Reason: "the Service name matches " + providerLabel(sig.provider) +
					", but not on its usual port — confirm the port before saving",
			}
			if sig.prefix != nil {
				candidate.PathPrefix = sig.prefix(name)
			}
			if exact {
				candidate.Score = 2
				candidate.Reason = providerLabel(sig.provider) + " on its conventional port " + port
			}
			out = append(out, candidate)
			// One Service is one provider; the first signature that claims it is
			// the most specific one.
			break
		}
	}

	slices.SortStableFunc(out, func(a, b Candidate) int {
		if a.Score != b.Score {
			return b.Score - a.Score
		}
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		return strings.Compare(a.ServiceNamespace+"/"+a.ServiceName, b.ServiceNamespace+"/"+b.ServiceName)
	})
	return out
}

func isExcluded(name string) bool {
	for _, fragment := range excluded {
		if strings.Contains(name, fragment) {
			return true
		}
	}
	return false
}

func matchesName(name string, fragments []string) bool {
	for _, fragment := range fragments {
		if strings.Contains(name, fragment) {
			return true
		}
	}
	return false
}

// pickPort prefers the provider's conventional port, falling back to the
// Service's first port so a non-standard install is still offered — flagged as
// the guess it is.
func pickPort(ports []ServicePort, wanted []int32) (string, bool) {
	if len(ports) == 0 {
		return "", false
	}
	for _, port := range ports {
		if slices.Contains(wanted, port.Port) {
			return strconv.FormatInt(int64(port.Port), 10), true
		}
	}
	return strconv.FormatInt(int64(ports[0].Port), 10), false
}
