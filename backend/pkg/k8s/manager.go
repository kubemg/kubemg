package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// TTL bounds accepted for generated credentials. The Kubernetes API server may
// enforce a shorter ceiling of its own, in which case the token it returns
// expires earlier than requested.
//
// There are two ceilings on purpose. DefaultMaxTTL is what an install allows
// with nobody having said otherwise, and it is a day because a credential
// sitting on a laptop is the one KubeMG cannot see being used. MaxTTL is the
// absolute bound an administrator cannot configure past: a quarter, which is
// the longest window anyone asking for "a few months" means, and past it a
// bearer token is not access control but a permanent key.
//
// The effective ceiling between the two is resolved from the settings and
// enforced in the API layer, which is where the policy lives. This package
// enforces only the absolute bound — its job is minting.
const (
	MinTTL        = 10 * time.Minute
	DefaultMaxTTL = 24 * time.Hour
	MaxTTL        = 90 * 24 * time.Hour
	DefaultTTL    = time.Hour
)

// ErrInvalidTTL is returned for a TTL outside [MinTTL, MaxTTL].
var ErrInvalidTTL = fmt.Errorf("ttl must be between %s and %s", MinTTL, MaxTTL)

// TokenRequest describes the short-lived credential to mint on a cluster.
type TokenRequest struct {
	ServiceAccount          string
	ServiceAccountNamespace string
	TTL                     time.Duration
	Audiences               []string
}

// IssuedToken is a minted service account token and its true expiry, as
// reported by the target API server.
type IssuedToken struct {
	Token     string
	ExpiresAt time.Time
}

// Issuer mints short-lived credentials on a registered cluster.
type Issuer interface {
	IssueToken(ctx context.Context, cluster *db.Cluster, req TokenRequest) (*IssuedToken, error)
}

// HealthReport is the outcome of probing a registered cluster.
type HealthReport struct {
	Reachable bool
	Version   string
	// Message explains an unreachable cluster in terms an operator can act on.
	Message string
}

// Checker probes whether a registered cluster is reachable.
type Checker interface {
	CheckHealth(ctx context.Context, cluster *db.Cluster) HealthReport
}

// UpstreamError wraps a failure encountered while talking to a target cluster,
// letting the HTTP layer distinguish "their cluster is unhappy" from "we have a
// bug".
type UpstreamError struct {
	Cluster string
	Op      string
	Err     error
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("cluster %q: %s: %v", e.Cluster, e.Op, e.Err)
}

func (e *UpstreamError) Unwrap() error { return e.Err }

// Manager holds one cached clientset per registered cluster and mints tokens
// through the Kubernetes TokenRequest API.
type Manager struct {
	// newClient is swapped out in tests; it defaults to the real client-go
	// constructor.
	newClient func(*rest.Config) (kubernetes.Interface, error)

	mu      sync.Mutex
	clients map[string]kubernetes.Interface
}

// NewManager builds a connection manager backed by client-go.
func NewManager() *Manager {
	return &Manager{
		newClient: func(cfg *rest.Config) (kubernetes.Interface, error) {
			return kubernetes.NewForConfig(cfg)
		},
		clients: map[string]kubernetes.Interface{},
	}
}

// ClientFor returns a clientset for the cluster, building and caching one on
// first use. The cache key covers the connection parameters, so re-registering
// a cluster with new credentials transparently replaces the cached client.
func (m *Manager) ClientFor(cluster *db.Cluster) (kubernetes.Interface, error) {
	cfg, err := RestConfig(cluster)
	if err != nil {
		return nil, err
	}
	key := clientCacheKey(cluster)

	m.mu.Lock()
	defer m.mu.Unlock()

	if client, ok := m.clients[key]; ok {
		return client, nil
	}

	client, err := m.newClient(cfg)
	if err != nil {
		return nil, &UpstreamError{Cluster: cluster.Name, Op: "build client", Err: err}
	}
	m.clients[key] = client
	return client, nil
}

// IssueToken mints a short-lived token for the requested service account,
// creating the service account first if the cluster does not have it yet.
func (m *Manager) IssueToken(ctx context.Context, cluster *db.Cluster, req TokenRequest) (*IssuedToken, error) {
	if req.TTL < MinTTL || req.TTL > MaxTTL {
		return nil, ErrInvalidTTL
	}
	if req.ServiceAccount == "" || req.ServiceAccountNamespace == "" {
		return nil, errors.New("service account name and namespace are required")
	}

	client, err := m.ClientFor(cluster)
	if err != nil {
		return nil, err
	}

	issued, err := createToken(ctx, client, req)
	if apierrors.IsNotFound(err) {
		// First use of this identity on this cluster: create the service
		// account, then mint again.
		if err := ensureServiceAccount(ctx, client, req); err != nil {
			return nil, &UpstreamError{Cluster: cluster.Name, Op: "create service account", Err: err}
		}
		issued, err = createToken(ctx, client, req)
		if err != nil {
			return nil, &UpstreamError{Cluster: cluster.Name, Op: "token request", Err: err}
		}
		return issued, nil
	}
	if err != nil {
		return nil, &UpstreamError{Cluster: cluster.Name, Op: "token request", Err: err}
	}
	return issued, nil
}

// CheckHealth asks the target API server for its version. A cluster that
// answers is reachable and its credentials still work, which is what an
// operator actually wants to know. Failures are reported, not returned as
// errors, because "unreachable" is a valid answer.
func (m *Manager) CheckHealth(ctx context.Context, cluster *db.Cluster) HealthReport {
	client, err := m.ClientFor(cluster)
	if err != nil {
		return HealthReport{Message: probeMessage(err)}
	}

	info, err := client.Discovery().ServerVersion()
	if err != nil {
		return HealthReport{Message: probeMessage(err)}
	}

	version := info.GitVersion
	if version == "" {
		version = info.String()
	}
	return HealthReport{Reachable: true, Version: version}
}

// probeMessage keeps failure text short and free of credentials.
func probeMessage(err error) string {
	switch {
	case errors.Is(err, ErrMissingCredentials):
		return "registration is missing an API URL or service account token"
	case apierrors.IsUnauthorized(err):
		return "the stored service account token was rejected"
	case apierrors.IsForbidden(err):
		return "the stored service account token lacks permission to read the API server version"
	default:
		return "the API server did not respond"
	}
}

func createToken(ctx context.Context, client kubernetes.Interface, req TokenRequest) (*IssuedToken, error) {
	expirationSeconds := int64(req.TTL.Seconds())
	out, err := client.CoreV1().
		ServiceAccounts(req.ServiceAccountNamespace).
		CreateToken(ctx, req.ServiceAccount, &authv1.TokenRequest{
			Spec: authv1.TokenRequestSpec{
				Audiences:         req.Audiences,
				ExpirationSeconds: &expirationSeconds,
			},
		}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	if out.Status.Token == "" {
		return nil, errors.New("api server returned an empty token")
	}

	// The API server may cap the lifetime below what we asked for; trust its
	// timestamp when present.
	expiresAt := out.Status.ExpirationTimestamp.Time
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(req.TTL)
	}
	return &IssuedToken{Token: out.Status.Token, ExpiresAt: expiresAt}, nil
}

func ensureServiceAccount(ctx context.Context, client kubernetes.Interface, req TokenRequest) error {
	_, err := client.CoreV1().
		ServiceAccounts(req.ServiceAccountNamespace).
		Create(ctx, &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      req.ServiceAccount,
				Namespace: req.ServiceAccountNamespace,
				Labels:    map[string]string{"app.kubernetes.io/managed-by": "kubemg"},
			},
		}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func clientCacheKey(cluster *db.Cluster) string {
	sum := sha256.Sum256([]byte(cluster.ServiceAccountToken + "|" + cluster.CACertData))
	return fmt.Sprintf("%d|%s|%s", cluster.ID, cluster.APIURL, hex.EncodeToString(sum[:8]))
}
