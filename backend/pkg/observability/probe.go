package observability

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// probeTimeout bounds a datasource check. A backend that cannot answer a
// one-sample query in ten seconds is not one an operator should be told is fine.
const probeTimeout = 10 * time.Second

// maxProbeBody caps what a probe reads back. Nothing here needs more than the
// first few hundred bytes, and a datasource is an address an operator typed —
// it should not be able to stream a gigabyte into the check handler.
const maxProbeBody = 64 << 10

// TunnelCall performs one call through a cluster's agent tunnel. The API layer
// supplies it as a closure over the bastion proxy, which keeps this package free
// of the proxy's identity and audit machinery while still going through it.
type TunnelCall func(ctx context.Context, method, path string, body []byte) (status int, out []byte, err error)

// Result is the outcome of a datasource check, written for someone who is
// looking at a form they just filled in.
type Result struct {
	Reachable bool   `json:"reachable"`
	Message   string `json:"message"`
	// Version is what the backend said about itself, when it says anything.
	Version string `json:"version,omitempty"`
	// Endpoint and Path together are exactly what was asked for, so a failure
	// can be reproduced with curl rather than guessed at.
	Endpoint string `json:"endpoint"`
	Path     string `json:"path"`
}

// probeSpec is how one provider answers the two questions a check asks.
type probeSpec struct {
	// ping is a real read: it proves a backend of this kind is there, not just
	// that something is listening on the port.
	ping string
	// version is best-effort; a provider that does not serve one is not less
	// healthy for it.
	version string
}

// specFor maps a provider onto its probe paths. The metrics four all speak the
// Prometheus query API — that shared surface is why they can share a query path
// later, not just a check.
func specFor(provider string) probeSpec {
	switch provider {
	case db.ProviderVictoriaMetrics, db.ProviderPrometheus, db.ProviderThanos, db.ProviderMimir:
		return probeSpec{ping: "/api/v1/query?query=1", version: "/api/v1/status/buildinfo"}
	case db.ProviderVictoriaLogs:
		return probeSpec{ping: "/select/logsql/query?limit=1&query=%2A"}
	case db.ProviderLoki:
		return probeSpec{ping: "/loki/api/v1/labels", version: "/loki/api/v1/status/buildinfo"}
	default:
		return probeSpec{ping: "/"}
	}
}

// Probe checks that a datasource is really there and really is what it claims.
// An in-cluster target needs a tunnel; passing none for one is the caller's
// error and is reported as such rather than silently reading nothing.
func Probe(ctx context.Context, target Target, tunnel TunnelCall) Result {
	spec := specFor(target.Provider)
	result := Result{Endpoint: target.Endpoint(), Path: spec.ping}

	if err := target.Validate(); err != nil {
		result.Message = err.Error()
		return result
	}
	if target.AccessMode == db.AccessInCluster && tunnel == nil {
		result.Message = "an in-cluster datasource can only be checked through a connected agent"
		return result
	}

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	status, body, err := call(ctx, target, spec.ping, tunnel)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	if status < 200 || status >= 300 {
		result.Message = explain(target, status, body)
		return result
	}

	result.Reachable = true
	result.Message = fmt.Sprintf("%s answered.", providerLabel(target.Provider))

	if spec.version != "" {
		if _, versionBody, verr := call(ctx, target, spec.version, tunnel); verr == nil {
			result.Version = parseVersion(versionBody)
		}
	}
	if result.Version != "" {
		result.Message = fmt.Sprintf("%s %s answered.", providerLabel(target.Provider), result.Version)
	}
	return result
}

// call performs one probe request in whichever of the two shapes applies.
func call(ctx context.Context, target Target, path string, tunnel TunnelCall) (int, []byte, error) {
	return callLimited(ctx, target, path, tunnel, maxProbeBody, probeTimeout)
}

// callLimited is the same request with the caller's own ceilings. A probe reads
// a few hundred bytes and a query reads a result set, so the two cannot share one
// limit — but they must share one code path, because that path is where the
// in-cluster and direct shapes diverge and where the credential is applied.
func callLimited(ctx context.Context, target Target, path string, tunnel TunnelCall,
	maxBody int64, timeout time.Duration,
) (int, []byte, error) {
	requestPath := target.requestPath(path)

	if target.AccessMode == db.AccessInCluster {
		status, body, err := tunnel(ctx, http.MethodGet, requestPath, nil)
		if err != nil {
			return 0, nil, fmt.Errorf("could not reach the Service through the cluster: %w", err)
		}
		return status, body, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestPath, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("that address cannot be requested: %w", err)
	}
	applyAuth(req.Header, target)

	resp, err := directClientWithTimeout(target, timeout).Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("KubeMG could not reach %s: %w", target.Endpoint(), err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("the datasource closed the connection mid-answer: %w", err)
	}
	return resp.StatusCode, body, nil
}

// applyAuth adds the datasource's credential. In in-cluster mode the API server
// is the one making the onward call, so there is nowhere to put a header — a
// credentialled in-cluster source is therefore a direct-mode source in disguise
// and the form says so.
func applyAuth(header http.Header, target Target) {
	switch target.AuthMode {
	case db.AuthBearer:
		if target.Credential != "" {
			header.Set("Authorization", "Bearer "+target.Credential)
		}
	case db.AuthBasic:
		if target.Username != "" {
			pair := target.Username + ":" + target.Credential
			header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(pair)))
		}
	}
	header.Set("Accept", "application/json")
}

// directClient dials a datasource KubeMG can route to itself. Skipping
// verification is an explicit per-source choice, never a default: an internal
// certificate is a reason to paste a CA, not a reason to stop checking.
func directClient(target Target) *http.Client {
	return directClientWithTimeout(target, probeTimeout)
}

func directClientWithTimeout(target Target, timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if target.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true, //nolint:gosec // operator opt-in
		}
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

// explain turns a non-2xx answer into the next thing to try. A bare status code
// in a form is a dead end; naming the likely cause is the difference between a
// datasource that gets connected and one that gets abandoned.
func explain(target Target, status int, body []byte) string {
	detail := strings.TrimSpace(string(body))
	if len(detail) > 200 {
		detail = detail[:200] + "…"
	}

	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		if target.AuthMode == db.AuthNone {
			return fmt.Sprintf("%s requires authentication (HTTP %d). Add a bearer token or basic credentials.",
				target.Endpoint(), status)
		}
		return fmt.Sprintf("%s rejected the credential (HTTP %d).", target.Endpoint(), status)
	case http.StatusNotFound:
		return fmt.Sprintf("%s answered, but not on %s (HTTP 404). "+
			"Check the path prefix, and that this really is %s.",
			target.Endpoint(), specFor(target.Provider).ping, providerLabel(target.Provider))
	case http.StatusServiceUnavailable, http.StatusBadGateway:
		return fmt.Sprintf("%s is reachable but not serving (HTTP %d). %s", target.Endpoint(), status, detail)
	}
	if detail == "" {
		return fmt.Sprintf("%s answered HTTP %d.", target.Endpoint(), status)
	}
	return fmt.Sprintf("%s answered HTTP %d: %s", target.Endpoint(), status, detail)
}

// parseVersion pulls a version out of whichever buildinfo shape came back.
// Prometheus-family backends wrap it in a status envelope; Loki answers flat.
func parseVersion(body []byte) string {
	var envelope struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	if envelope.Data.Version != "" {
		return envelope.Data.Version
	}
	return envelope.Version
}

// providerLabel is how a provider is named in a sentence.
func providerLabel(provider string) string {
	switch provider {
	case db.ProviderVictoriaMetrics:
		return "VictoriaMetrics"
	case db.ProviderPrometheus:
		return "Prometheus"
	case db.ProviderThanos:
		return "Thanos"
	case db.ProviderMimir:
		return "Mimir"
	case db.ProviderVictoriaLogs:
		return "VictoriaLogs"
	case db.ProviderLoki:
		return "Loki"
	default:
		return provider
	}
}

// Label is the display name of a provider.
func Label(provider string) string { return providerLabel(provider) }
