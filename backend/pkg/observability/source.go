package observability

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Talking to a cluster's metrics or logs backend.
 *
 * Two shapes exist and they are genuinely different. An *in-cluster* source is
 * reached by asking the cluster's own API server to proxy to a Service, which
 * travels down the agent tunnel: nothing has to be exposed outside the cluster,
 * and the call is impersonated and audited exactly like a kubectl read. A
 * *direct* source is dialled from KubeMG, which is what a central Thanos or a
 * hosted Mimir looks like — the series live outside the cluster they describe.
 *
 * Everything below is transport. Query semantics are the provider's own, and
 * the only ones KubeMG needs today are "is this really there" and "what is it".
 */

// Target is a resolved datasource: everything one call needs, decoupled from
// how it was stored.
type Target struct {
	Kind       string
	Provider   string
	AccessMode string

	// URL is the base address in direct mode.
	URL string

	// The Service the API server proxies to, in in-cluster mode.
	ServiceNamespace string
	ServiceName      string
	ServicePort      string
	ServiceScheme    string

	PathPrefix string

	AuthMode   string
	Username   string
	Credential string

	InsecureSkipVerify bool
}

// TargetOf renders a stored source as a Target.
func TargetOf(source db.ObservabilitySource) Target {
	return Target{
		Kind:               source.Kind,
		Provider:           source.Provider,
		AccessMode:         source.AccessMode,
		URL:                source.URL,
		ServiceNamespace:   source.ServiceNamespace,
		ServiceName:        source.ServiceName,
		ServicePort:        source.ServicePort,
		ServiceScheme:      source.ServiceScheme,
		PathPrefix:         source.PathPrefix,
		AuthMode:           source.AuthMode,
		Username:           source.Username,
		Credential:         source.Credential,
		InsecureSkipVerify: source.InsecureSkipVerify,
	}
}

// Validate reports what is wrong with a target, in the words an operator filling
// in the form needs to read. It is deliberately strict about the two shapes not
// overlapping: a source that names both a URL and a Service is one whose author
// did not decide which one KubeMG should use.
func (t Target) Validate() error {
	if !db.ValidSourceKind(t.Kind) {
		return fmt.Errorf("%q is not a datasource kind KubeMG stores", t.Kind)
	}
	if !db.ValidProvider(t.Kind, t.Provider) {
		return fmt.Errorf("%q cannot serve %s; supported: %s",
			t.Provider, t.Kind, strings.Join(db.ProvidersFor(t.Kind), ", "))
	}
	if !db.ValidAccessMode(t.AccessMode) {
		return fmt.Errorf("%q is not an access mode", t.AccessMode)
	}
	if !db.ValidAuthMode(t.AuthMode) {
		return fmt.Errorf("%q is not an authentication mode", t.AuthMode)
	}

	switch t.AccessMode {
	case db.AccessDirect:
		parsed, err := url.Parse(t.URL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf(
				"a direct datasource needs an absolute http:// or https:// address, " +
					"for example https://vmselect.example.com:8481")
		}
	case db.AccessInCluster:
		if t.ServiceNamespace == "" || t.ServiceName == "" {
			return fmt.Errorf("an in-cluster datasource needs the namespace and name of its Service")
		}
		if t.ServicePort == "" {
			return fmt.Errorf("an in-cluster datasource needs the Service port to talk to")
		}
		if t.ServiceScheme != "" && t.ServiceScheme != "http" && t.ServiceScheme != "https" {
			return fmt.Errorf("the Service scheme must be http or https")
		}
	}

	if t.AuthMode == db.AuthBasic && t.Username == "" {
		return fmt.Errorf("basic authentication needs a username")
	}
	return nil
}

// scheme answers http when nothing says otherwise: an in-cluster metrics backend
// on plain HTTP is the overwhelmingly common case, and guessing https would make
// the default configuration fail.
func (t Target) scheme() string {
	if t.ServiceScheme == "" {
		return "http"
	}
	return t.ServiceScheme
}

// prefix normalises the path sitting in front of the provider's own API.
func (t Target) prefix() string {
	trimmed := strings.Trim(strings.TrimSpace(t.PathPrefix), "/")
	if trimmed == "" {
		return ""
	}
	return "/" + trimmed
}

// requestPath renders a provider-relative path (which may carry a query string)
// into the path that actually gets requested.
//
// In in-cluster mode that is the API server's Service proxy subresource, which
// is what makes this work at all without exposing anything: the agent already
// holds a tunnel to the API server, and the API server can reach the Service.
func (t Target) requestPath(path string) string {
	if t.AccessMode == db.AccessInCluster {
		service := fmt.Sprintf("%s:%s:%s", t.scheme(),
			t.ServiceName, t.ServicePort)
		return fmt.Sprintf("/api/v1/namespaces/%s/services/%s/proxy%s%s",
			url.PathEscape(t.ServiceNamespace), url.PathEscape(service), t.prefix(), path)
	}
	return strings.TrimRight(t.URL, "/") + t.prefix() + path
}

// Endpoint renders the address a target resolves to, for display. It is not a
// URL anyone dials — in in-cluster mode there is nothing dialable from here,
// which is the point — but it is what an operator recognises.
func (t Target) Endpoint() string {
	if t.AccessMode == db.AccessInCluster {
		return fmt.Sprintf("%s://%s.%s.svc:%s%s",
			t.scheme(), t.ServiceName, t.ServiceNamespace, t.ServicePort, t.prefix())
	}
	return strings.TrimRight(t.URL, "/") + t.prefix()
}
