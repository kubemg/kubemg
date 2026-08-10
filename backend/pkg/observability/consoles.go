package observability

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Links out to the other consoles a cluster is operated from.
 *
 * Everything here builds a URL and nothing here fetches one. That is the whole
 * boundary: KubeMG holds no session for Grafana or Argo CD, sends nothing to
 * them, and learns nothing back — the operator follows the link and
 * authenticates as themselves. A deep link is built only where the target's URL
 * scheme is documented and stable enough to be worth it, and a bare link is the
 * honest answer everywhere else.
 *
 * The one asymmetry worth naming: the Grafana Explore link carries a *query*,
 * and in KubeMG a query is never the caller's — the browser names a chart from
 * a fixed catalogue and the server writes the PromQL around the caller's own
 * scope (see query.go). So the Explore link is built here, server-side, out of
 * the same expression the server just ran. A browser assembling its own would be
 * the browser writing a query, which is exactly the thing that path exists to
 * prevent.
 */

// maxConsoleURL bounds a stored console address. It is a base URL somebody
// types, not a document.
const maxConsoleURL = 512

// NormalizeConsoleURL validates a console address and renders it in the one form
// everything downstream can append to: scheme, host, optional path, no trailing
// slash.
//
// Userinfo is refused rather than stripped. A URL carrying a password is a
// credential, this table stores none, and quietly dropping the half that makes
// it work would produce a link that fails with nothing to explain it.
func NormalizeConsoleURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("a console needs an address")
	}
	if len(trimmed) > maxConsoleURL {
		return "", fmt.Errorf("that address is longer than %d characters", maxConsoleURL)
	}

	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf(
			"a console needs an absolute http:// or https:// address, " +
				"for example https://grafana.example.com")
	}
	if parsed.User != nil {
		return "", fmt.Errorf(
			"leave the username and password out of the address — KubeMG stores no " +
				"credential for another console, and you sign in to it as yourself")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf(
			"give the console's base address only; KubeMG adds the query it needs")
	}

	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}

// maxConsoleRef bounds the identifier a console needs to open on the right
// thing. It is a name in another system, so it is length-checked and otherwise
// carried verbatim — it is escaped where it is used, never trusted raw.
const maxConsoleRef = 190

// NormalizeConsoleRef trims and bounds a console's identifier.
func NormalizeConsoleRef(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) > maxConsoleRef {
		return "", fmt.Errorf("that identifier is longer than %d characters", maxConsoleRef)
	}
	if strings.ContainsAny(trimmed, "/?#") {
		return "", fmt.Errorf("an identifier is a name, not a path")
	}
	return trimmed, nil
}

/* ------------------------------------------------------------------ Grafana --- */

// grafanaDatasourceType maps a KubeMG provider onto the Grafana datasource type
// its queries are written for. Grafana resolves a datasource by uid, so this is
// a hint rather than the identifier — but a query object without one is not
// something every Grafana version fills in for itself.
func grafanaDatasourceType(provider string) string {
	switch provider {
	case db.ProviderLoki:
		return "loki"
	case db.ProviderVictoriaLogs:
		return "victorialogs-datasource"
	default:
		// The metrics four all speak the Prometheus query API, which is the
		// datasource type a Grafana is overwhelmingly likely to have them under.
		return "prometheus"
	}
}

// GrafanaExplore builds a link into Grafana's Explore, carrying one query
// against one datasource over one window.
//
// The format is the documented `panes` + `schemaVersion` one, which is what
// Grafana's own "generate Explore URLs from external tools" describes. An empty
// datasource uid returns an empty string rather than a link to Explore with
// somebody else's default datasource selected: a PromQL expression handed to a
// Loki datasource is an error message, not a chart, and a link that lands wrong
// is worse than no link.
func GrafanaExplore(base, datasourceUID, provider, expr string, from, to time.Time) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	datasourceUID = strings.TrimSpace(datasourceUID)
	expr = strings.TrimSpace(expr)
	if base == "" || datasourceUID == "" || expr == "" {
		return ""
	}

	pane := map[string]any{
		"datasource": datasourceUID,
		"queries": []map[string]any{{
			"refId":      "A",
			"datasource": map[string]string{"uid": datasourceUID, "type": grafanaDatasourceType(provider)},
			"expr":       expr,
		}},
		"range": map[string]string{
			"from": strconv.FormatInt(from.UnixMilli(), 10),
			"to":   strconv.FormatInt(to.UnixMilli(), 10),
		},
	}
	// The pane key is an arbitrary identifier scoped to the URL; naming it says
	// where the link came from when it turns up in somebody's history.
	encoded, err := json.Marshal(map[string]any{"kubemg": pane})
	if err != nil {
		return ""
	}

	query := url.Values{}
	query.Set("schemaVersion", "1")
	query.Set("panes", string(encoded))
	// orgId is deliberately not pinned: Grafana opens in the org the viewer is
	// already in, and asserting org 1 would send half a fleet to the wrong one.
	return base + "/explore?" + query.Encode()
}

/* ------------------------------------------------------------------ Argo CD --- */

// ArgoApplication builds a link to one Argo CD application. Argo's application
// view is a path — `/applications/{name}`, which redirects to the namespaced
// form — so this is stable in a way a query string is not.
//
// The name is escaped rather than validated against a pattern: it comes off a
// Kubernetes label written by Argo itself, so what matters is that it cannot
// leave the path segment it was put in.
func ArgoApplication(base, name string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	name = strings.TrimSpace(name)
	if base == "" || name == "" {
		return ""
	}
	return base + "/applications/" + url.PathEscape(name)
}

/* --------------------------------------------------- the datasource's own UI --- */

// providerUIPath is the path a provider serves its own query UI on, relative to
// the base a datasource is registered at. A provider absent from this table has
// no UI of its own worth linking to — Mimir and Loki are queried through
// Grafana, and pointing at their API root would open a 404 with KubeMG's name
// on it.
func providerUIPath(provider string) (string, bool) {
	switch provider {
	case db.ProviderVictoriaMetrics:
		return "/vmui", true
	case db.ProviderVictoriaLogs:
		return "/select/vmui", true
	case db.ProviderPrometheus, db.ProviderThanos:
		return "/graph", true
	default:
		return "", false
	}
}

// DatasourceUI renders the browser-reachable address of a datasource's own query
// UI, or "" when there is none to reach.
//
// An **in-cluster** source has no such address by construction and never gets
// one: it is reached by asking the API server to proxy to a Service, which is a
// call KubeMG makes down the tunnel under the caller's identity — not a URL a
// browser can open. Offering a link there would be offering a link that cannot
// work from the operator's laptop, which is the failure this whole item exists
// to remove.
func DatasourceUI(source db.ObservabilitySource) string {
	if source.AccessMode != db.AccessDirect {
		return ""
	}
	path, ok := providerUIPath(source.Provider)
	if !ok {
		return ""
	}

	base := strings.TrimRight(strings.TrimSpace(source.URL), "/")
	if base == "" {
		return ""
	}
	prefix := strings.Trim(strings.TrimSpace(source.PathPrefix), "/")
	if prefix == "" {
		return base + path
	}
	// A VictoriaMetrics cluster serves its API under /select/{account}/prometheus
	// and its UI under /select/{account}/vmui — the same tenant prefix with the
	// last segment swapped, which is the one case where the prefix is part of the
	// UI's address rather than in front of it.
	if source.Provider == db.ProviderVictoriaMetrics && strings.HasSuffix(prefix, "/prometheus") {
		return base + "/" + strings.TrimSuffix(prefix, "/prometheus") + "/vmui"
	}
	return base + "/" + prefix + path
}
