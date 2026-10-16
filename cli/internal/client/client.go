// Package client speaks to a kubemg server's CLI routes: the device sign-in,
// the cluster list, and the fifteen-minute kubectl credential.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrSessionEnded is the server saying the CLI session is over — expired,
// ended in the console, or its account gone. Only a new `kubemg login` helps.
var ErrSessionEnded = errors.New("the CLI session has ended")

// ErrDenied is the sign-in refused in the browser.
var ErrDenied = errors.New("the sign-in was denied in the browser")

// ErrExpired is a sign-in that waited too long, or was already used.
var ErrExpired = errors.New("the sign-in expired before it was approved")

// Client is one server.
type Client struct {
	Server string
	HTTP   *http.Client
	// Sleep waits between polls. Tests replace it so a device flow runs
	// instantly.
	Sleep func(context.Context, time.Duration) error
}

// New builds a client for a server. A non-empty caPEM is the only trust pool
// used, so a self-signed bastion is trusted exactly and nothing else is added;
// without one the system roots apply.
//
// The server must be https. The refresh token is a credential and a plaintext
// server would carry it in the clear — and nothing is lost by refusing: kubectl
// will not send a bearer token over http either, so a plaintext kubemg could
// not serve the credentials this CLI exists to fetch.
func New(server string, caPEM []byte) (*Client, error) {
	server = strings.TrimRight(strings.TrimSpace(server), "/")
	if !strings.HasPrefix(server, "https://") || len(server) == len("https://") {
		return nil, fmt.Errorf("the server must be an https URL, like https://kubemg.example.com")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if len(caPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, errors.New("the CA file holds no PEM certificate")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &Client{
		Server: server,
		HTTP:   &http.Client{Transport: transport, Timeout: 30 * time.Second},
		Sleep:  sleep,
	}, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// apiError is a refusal the server explained.
type apiError struct {
	Status  int
	Message string
	Code    string
}

func (e *apiError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("the server answered %d", e.Status)
}

func (c *Client) do(ctx context.Context, method, path, bearer string, body, into any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Server+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach %s: %w", c.Server, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var refusal struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(data, &refusal)
		out := &apiError{Status: resp.StatusCode, Message: refusal.Error}
		if refusal.Description != "" {
			out.Code, out.Message = refusal.Error, refusal.Description
		}
		return out
	}
	if into == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, into)
}

// ClientInfo is what the CLI says about itself, shown on the approval page.
type ClientInfo struct {
	Name    string `json:"client_name"`
	Host    string `json:"client_host"`
	OS      string `json:"client_os"`
	Version string `json:"client_version"`
}

// Device is a started sign-in.
type Device struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// StartLogin opens a device sign-in.
func (c *Client) StartLogin(ctx context.Context, info ClientInfo) (Device, error) {
	var device Device
	err := c.do(ctx, http.MethodPost, "/api/v1/auth/cli/device", "", info, &device)
	return device, err
}

// Session is what an approved sign-in hands over.
type Session struct {
	RefreshToken string    `json:"refresh_token"`
	SessionID    string    `json:"session_id"`
	ExpiresAt    time.Time `json:"expires_at"`
	User         struct {
		Username string `json:"username"`
	} `json:"user"`
}

// WaitForApproval polls until the sign-in is approved, denied or expired, at
// the interval the server set — five seconds more each time it says slow_down,
// as RFC 8628 has it.
func (c *Client) WaitForApproval(ctx context.Context, device Device) (Session, error) {
	interval := time.Duration(max(device.Interval, 1)) * time.Second
	deadline := time.Now().Add(time.Duration(max(device.ExpiresIn, 1)) * time.Second)
	for {
		if err := c.Sleep(ctx, interval); err != nil {
			return Session{}, err
		}
		var session Session
		err := c.do(ctx, http.MethodPost, "/api/v1/auth/cli/token", "", map[string]string{"device_code": device.DeviceCode}, &session)
		if err == nil {
			return session, nil
		}
		var refusal *apiError
		if !errors.As(err, &refusal) {
			// A network blip is not an answer; keep asking until the window shuts.
			if time.Now().After(deadline) {
				return Session{}, err
			}
			continue
		}
		switch refusal.Code {
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied":
			return Session{}, ErrDenied
		case "expired_token":
			return Session{}, ErrExpired
		default:
			return Session{}, err
		}
		if time.Now().After(deadline) {
			return Session{}, ErrExpired
		}
	}
}

// Cluster is one cluster as the server describes it for a kubeconfig.
type Cluster struct {
	ID             uint     `json:"id"`
	Name           string   `json:"name"`
	Environment    string   `json:"environment"`
	ConnectionMode string   `json:"connection_mode"`
	Role           string   `json:"role"`
	Namespaces     []string `json:"namespaces"`
	Server         string   `json:"server"`
	ContextName    string   `json:"context_name"`
	CAData         string   `json:"ca_data"`
	Note           string   `json:"note"`
}

// Clusters lists what the session's account can reach.
func (c *Client) Clusters(ctx context.Context, refresh string) ([]Cluster, error) {
	var body struct {
		Clusters []Cluster `json:"clusters"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/auth/cli/clusters", refresh, nil, &body)
	return body.Clusters, sessionErr(err)
}

// Credential is one kubectl token.
type Credential struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Credential mints a fifteen-minute token for one cluster.
func (c *Client) Credential(ctx context.Context, refresh string, clusterID uint) (Credential, error) {
	var credential Credential
	err := c.do(ctx, http.MethodPost, "/api/v1/auth/cli/credential", refresh,
		map[string]any{"cluster_id": clusterID}, &credential)
	return credential, sessionErr(err)
}

// StaticKubeconfig is the console download, from the CLI: a whole kubeconfig
// carrying a token of the given lifetime, registered like any download.
type StaticKubeconfig struct {
	Kubeconfig string    `json:"kubeconfig"`
	Context    string    `json:"context"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// Static asks for a static kubeconfig.
func (c *Client) Static(ctx context.Context, refresh string, clusterID uint, namespace string, ttl time.Duration) (StaticKubeconfig, error) {
	var out StaticKubeconfig
	err := c.do(ctx, http.MethodPost, "/api/v1/auth/cli/credential", refresh, map[string]any{
		"cluster_id": clusterID, "namespace": namespace, "static": true, "ttl_seconds": int64(ttl.Seconds()),
	}, &out)
	return out, sessionErr(err)
}

// Logout ends the session on the server.
func (c *Client) Logout(ctx context.Context, refresh string) error {
	return sessionErr(c.do(ctx, http.MethodPost, "/api/v1/auth/cli/logout", refresh, nil, nil))
}

// sessionErr turns the server's 401 on a CLI route into ErrSessionEnded,
// keeping its sentence.
func sessionErr(err error) error {
	var refusal *apiError
	if errors.As(err, &refusal) && refusal.Status == http.StatusUnauthorized {
		return fmt.Errorf("%w: %s", ErrSessionEnded, refusal.Message)
	}
	return err
}
