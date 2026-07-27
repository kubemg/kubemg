// Package kube is the agent's client for its own cluster's API server.
//
// It deliberately does not use client-go: the agent forwards opaque HTTP calls
// and never decodes a Kubernetes object, so the entire typed API surface would
// be dead weight in a binary whose whole selling point is that it is small.
package kube

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// In-cluster service account paths, mounted by the kubelet.
const (
	tokenPath     = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	caPath        = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	namespacePath = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

// DefaultAPIURL is the in-cluster address of the API server.
const DefaultAPIURL = "https://kubernetes.default.svc"

// maxResponseBody caps what the agent will read back from the API server, so a
// runaway response cannot push the pod past its memory limit.
const maxResponseBody = 8 << 20

// Client talks to the local Kubernetes API server as the agent's own service
// account.
type Client struct {
	baseURL string
	http    *http.Client
	// stream is the same transport without an overall timeout. A watch that is
	// deliberately quiet for an hour is not a stuck request, and the 55s
	// deadline on the ordinary client would kill it.
	stream *http.Client
	// dialer upgrades to a WebSocket for exec and attach sessions.
	dialer *websocket.Dialer
	// tls is retained so the dialer and both clients share one configuration.
	tls *tls.Config
	// tokenPath is re-read per request: projected service account tokens are
	// rotated in place, and a cached one stops working after an hour.
	tokenPath string
}

// Options configure the client.
type Options struct {
	// APIURL defaults to DefaultAPIURL.
	APIURL string
	// InsecureSkipVerify is for running the agent outside a cluster during
	// development. It is never what you want in production.
	InsecureSkipVerify bool
	// TokenPath and CAPath default to the in-cluster mount points.
	TokenPath string
	CAPath    string
}

// New builds a client for the local API server.
func New(opts Options) (*Client, error) {
	if opts.APIURL == "" {
		opts.APIURL = DefaultAPIURL
	}
	if opts.TokenPath == "" {
		opts.TokenPath = tokenPath
	}
	if opts.CAPath == "" {
		opts.CAPath = caPath
	}

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if opts.InsecureSkipVerify {
		tlsConfig.InsecureSkipVerify = true
	} else {
		pem, err := os.ReadFile(opts.CAPath)
		if err != nil {
			return nil, fmt.Errorf("read cluster CA from %s: %w", opts.CAPath, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("cluster CA at %s is not valid PEM", opts.CAPath)
		}
		tlsConfig.RootCAs = pool
	}

	transport := &http.Transport{
		TLSClientConfig:     tlsConfig,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     90 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}

	return &Client{
		baseURL:   strings.TrimRight(opts.APIURL, "/"),
		tokenPath: opts.TokenPath,
		tls:       tlsConfig,
		http:      &http.Client{Timeout: 55 * time.Second, Transport: transport},
		// No Timeout: the caller's context is what ends a stream.
		stream: &http.Client{Transport: transport},
		dialer: &websocket.Dialer{
			TLSClientConfig:  tlsConfig,
			HandshakeTimeout: 20 * time.Second,
		},
	}, nil
}

// Namespace reports the namespace the agent is running in.
func Namespace() string {
	raw, err := os.ReadFile(namespacePath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// Do replays a proxied request against the API server. The caller's headers
// arrive already sanitised by the bastion — including the impersonation headers
// that decide who this call acts as — so the agent adds only its own bearer
// token and forwards the rest untouched.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	token, err := c.token()
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return c.http.Do(req)
}

// DoStream replays a request whose response body keeps arriving. It differs
// from Do only in having no overall deadline; the request's context ends it.
func (c *Client) DoStream(req *http.Request) (*http.Response, error) {
	token, err := c.token()
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return c.stream.Do(req)
}

// DialUpgrade opens an interactive session — exec or attach — against the API
// server, negotiating one of the Kubernetes channel subprotocols. It returns
// the socket and the subprotocol that was agreed.
func (c *Client) DialUpgrade(
	ctx context.Context,
	path string,
	header map[string][]string,
	subprotocols []string,
) (*websocket.Conn, string, error) {
	endpoint, err := c.wsURL(path)
	if err != nil {
		return nil, "", err
	}

	token, err := c.token()
	if err != nil {
		return nil, "", err
	}

	out := http.Header{}
	for name, values := range header {
		// The dialer sets these itself; duplicating them makes the handshake
		// invalid rather than merely redundant.
		if isHandshakeHeader(name) {
			continue
		}
		for _, value := range values {
			out.Add(name, value)
		}
	}
	out.Set("Authorization", "Bearer "+token)

	dialer := *c.dialer
	dialer.Subprotocols = subprotocols

	conn, resp, err := dialer.DialContext(ctx, endpoint, out)
	if err != nil {
		if resp != nil {
			return nil, "", fmt.Errorf("api server refused the session: %s", resp.Status)
		}
		return nil, "", fmt.Errorf("open session: %w", err)
	}
	return conn, conn.Subprotocol(), nil
}

// handshakeHeaders are owned by the WebSocket dialer.
var handshakeHeaders = []string{
	"Upgrade", "Connection", "Sec-Websocket-Key", "Sec-Websocket-Version",
	"Sec-Websocket-Protocol", "Sec-Websocket-Extensions", "Authorization",
}

func isHandshakeHeader(name string) bool {
	for _, header := range handshakeHeaders {
		if strings.EqualFold(header, name) {
			return true
		}
	}
	return false
}

// wsURL turns an API path into the ws(s) endpoint for an upgraded session.
func (c *Client) wsURL(path string) (string, error) {
	parsed, err := url.Parse(c.URL(path))
	if err != nil {
		return "", fmt.Errorf("build session URL: %w", err)
	}
	if parsed.Scheme == "https" {
		parsed.Scheme = "wss"
	} else {
		parsed.Scheme = "ws"
	}
	return parsed.String(), nil
}

// token reads the service account token fresh, because projected tokens are
// rotated in place.
func (c *Client) token() (string, error) {
	raw, err := os.ReadFile(c.tokenPath)
	if err != nil {
		return "", fmt.Errorf("read service account token: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// URL builds an absolute API server URL from a proxied path.
func (c *Client) URL(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return c.baseURL + path
}

// ReadBody reads a response body under the size cap.
func ReadBody(body io.ReadCloser) ([]byte, error) {
	defer body.Close()

	limited := io.LimitReader(body, maxResponseBody+1)
	out, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(out) > maxResponseBody {
		return nil, errors.New("response from the API server is too large to tunnel")
	}
	return out, nil
}

// versionInfo is the subset of /version the agent reports in its handshake.
type versionInfo struct {
	GitVersion string `json:"gitVersion"`
}

// ServerVersion asks the API server what it is running, so KubeMG can display
// the cluster's version without opening a connection of its own.
func (c *Client) ServerVersion() (string, error) {
	req, err := http.NewRequest(http.MethodGet, c.URL("/version"), nil)
	if err != nil {
		return "", err
	}

	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("api server returned %s for /version", resp.Status)
	}

	var info versionInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&info); err != nil {
		return "", fmt.Errorf("decode /version: %w", err)
	}
	return info.GitVersion, nil
}
