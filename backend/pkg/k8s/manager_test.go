package k8s

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

func testCluster() *db.Cluster {
	return &db.Cluster{
		ID:                  1,
		Name:                "prod-eu",
		APIURL:              "https://prod-eu.example.com:6443",
		CACertData:          testPEM,
		ServiceAccountToken: "cluster-sa-token",
	}
}

// fakeCluster is a scripted stand-in for a target API server.
type fakeCluster struct {
	client *fake.Clientset

	saExists      bool
	tokenCalls    int
	createdSAs    []string
	lastExpirySec int64
	expiry        time.Time
	emptyToken    bool
	failWith      error
}

func newFakeCluster(saExists bool) *fakeCluster {
	f := &fakeCluster{
		client:   fake.NewClientset(),
		saExists: saExists,
		expiry:   time.Now().Add(time.Hour).Truncate(time.Second),
	}

	f.client.PrependReactor("create", "serviceaccounts", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create, ok := action.(k8stesting.CreateAction)
		if !ok {
			return false, nil, nil
		}

		if action.GetSubresource() != "token" {
			sa := create.GetObject().(*corev1.ServiceAccount)
			f.createdSAs = append(f.createdSAs, sa.Namespace+"/"+sa.Name)
			f.saExists = true
			return true, sa, nil
		}

		f.tokenCalls++
		if f.failWith != nil {
			return true, nil, f.failWith
		}
		if !f.saExists {
			return true, nil, apierrors.NewNotFound(corev1.Resource("serviceaccounts"), "kubemg-devops")
		}

		req := create.GetObject().(*authv1.TokenRequest)
		if req.Spec.ExpirationSeconds != nil {
			f.lastExpirySec = *req.Spec.ExpirationSeconds
		}

		status := authv1.TokenRequestStatus{Token: "issued-token"}
		if f.emptyToken {
			status.Token = ""
		}
		if !f.expiry.IsZero() {
			status.ExpirationTimestamp = metav1.NewTime(f.expiry)
		}
		return true, &authv1.TokenRequest{Status: status}, nil
	})

	return f
}

// manager returns a Manager wired to this fake cluster, plus a counter of how
// many clientsets were constructed.
func (f *fakeCluster) manager() (*Manager, *int) {
	builds := 0
	m := &Manager{
		clients: map[string]kubernetes.Interface{},
		newClient: func(*rest.Config) (kubernetes.Interface, error) {
			builds++
			return f.client, nil
		},
	}
	return m, &builds
}

func validRequest() TokenRequest {
	return TokenRequest{
		ServiceAccount:          "kubemg-devops",
		ServiceAccountNamespace: "kubemg-system",
		TTL:                     time.Hour,
	}
}

func TestIssueTokenReturnsTokenAndExpiry(t *testing.T) {
	f := newFakeCluster(true)
	m, _ := f.manager()

	issued, err := m.IssueToken(context.Background(), testCluster(), validRequest())
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if issued.Token != "issued-token" {
		t.Fatalf("unexpected token %q", issued.Token)
	}
	if !issued.ExpiresAt.Equal(f.expiry) {
		t.Fatalf("expected expiry %s from the API server, got %s", f.expiry, issued.ExpiresAt)
	}
	if f.lastExpirySec != 3600 {
		t.Fatalf("expected expirationSeconds 3600, got %d", f.lastExpirySec)
	}
	if len(f.createdSAs) != 0 {
		t.Fatalf("existing service account must not be recreated, got %v", f.createdSAs)
	}
}

func TestIssueTokenHonoursRequestedTTL(t *testing.T) {
	f := newFakeCluster(true)
	m, _ := f.manager()

	if _, err := m.IssueToken(context.Background(), testCluster(), TokenRequest{
		ServiceAccount:          "kubemg-devops",
		ServiceAccountNamespace: "kubemg-system",
		TTL:                     8 * time.Hour,
	}); err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if f.lastExpirySec != 28800 {
		t.Fatalf("expected expirationSeconds 28800, got %d", f.lastExpirySec)
	}
}

func TestIssueTokenCreatesMissingServiceAccount(t *testing.T) {
	f := newFakeCluster(false)
	m, _ := f.manager()

	issued, err := m.IssueToken(context.Background(), testCluster(), validRequest())
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if issued.Token != "issued-token" {
		t.Fatalf("unexpected token %q", issued.Token)
	}
	if f.tokenCalls != 2 {
		t.Fatalf("expected the token request to be retried once, got %d calls", f.tokenCalls)
	}
	if len(f.createdSAs) != 1 || f.createdSAs[0] != "kubemg-system/kubemg-devops" {
		t.Fatalf("unexpected created service accounts: %v", f.createdSAs)
	}
}

func TestIssueTokenFallsBackToRequestedExpiry(t *testing.T) {
	f := newFakeCluster(true)
	f.expiry = time.Time{}
	m, _ := f.manager()

	before := time.Now()
	issued, err := m.IssueToken(context.Background(), testCluster(), validRequest())
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if issued.ExpiresAt.Before(before.Add(59 * time.Minute)) {
		t.Fatalf("expected a ~1h fallback expiry, got %s", issued.ExpiresAt)
	}
}

func TestIssueTokenRejectsEmptyToken(t *testing.T) {
	f := newFakeCluster(true)
	f.emptyToken = true
	m, _ := f.manager()

	if _, err := m.IssueToken(context.Background(), testCluster(), validRequest()); err == nil {
		t.Fatal("expected an error when the API server returns no token")
	}
}

func TestIssueTokenWrapsUpstreamFailure(t *testing.T) {
	f := newFakeCluster(true)
	f.failWith = apierrors.NewForbidden(corev1.Resource("serviceaccounts"), "kubemg-devops", errors.New("nope"))
	m, _ := f.manager()

	_, err := m.IssueToken(context.Background(), testCluster(), validRequest())

	var upstream *UpstreamError
	if !errors.As(err, &upstream) {
		t.Fatalf("expected an UpstreamError, got %v", err)
	}
	if upstream.Cluster != "prod-eu" || upstream.Op != "token request" {
		t.Fatalf("unexpected upstream error: %+v", upstream)
	}
}

func TestIssueTokenValidatesTTL(t *testing.T) {
	f := newFakeCluster(true)
	m, _ := f.manager()

	for _, ttl := range []time.Duration{0, time.Minute, 48 * time.Hour, -time.Hour} {
		req := validRequest()
		req.TTL = ttl
		if _, err := m.IssueToken(context.Background(), testCluster(), req); !errors.Is(err, ErrInvalidTTL) {
			t.Fatalf("ttl %s: expected ErrInvalidTTL, got %v", ttl, err)
		}
	}
	if f.tokenCalls != 0 {
		t.Fatal("invalid TTLs must not reach the cluster")
	}
}

func TestIssueTokenRequiresServiceAccountIdentity(t *testing.T) {
	f := newFakeCluster(true)
	m, _ := f.manager()

	for name, req := range map[string]TokenRequest{
		"no name":      {ServiceAccountNamespace: "kubemg-system", TTL: time.Hour},
		"no namespace": {ServiceAccount: "kubemg-devops", TTL: time.Hour},
	} {
		if _, err := m.IssueToken(context.Background(), testCluster(), req); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}

func TestIssueTokenRejectsClusterWithoutCredentials(t *testing.T) {
	f := newFakeCluster(true)
	m, _ := f.manager()

	cluster := testCluster()
	cluster.ServiceAccountToken = ""

	if _, err := m.IssueToken(context.Background(), cluster, validRequest()); !errors.Is(err, ErrMissingCredentials) {
		t.Fatalf("expected ErrMissingCredentials, got %v", err)
	}
}

func TestClientForCachesPerCluster(t *testing.T) {
	f := newFakeCluster(true)
	m, builds := f.manager()
	cluster := testCluster()

	for range 3 {
		if _, err := m.ClientFor(cluster); err != nil {
			t.Fatalf("client for: %v", err)
		}
	}
	if *builds != 1 {
		t.Fatalf("expected 1 clientset build, got %d", *builds)
	}
}

func TestClientForRebuildsWhenCredentialsChange(t *testing.T) {
	f := newFakeCluster(true)
	m, builds := f.manager()

	cluster := testCluster()
	if _, err := m.ClientFor(cluster); err != nil {
		t.Fatalf("client for: %v", err)
	}

	rotated := testCluster()
	rotated.ServiceAccountToken = "rotated-token"
	if _, err := m.ClientFor(rotated); err != nil {
		t.Fatalf("client for: %v", err)
	}

	if *builds != 2 {
		t.Fatalf("expected a rebuild after credential rotation, got %d builds", *builds)
	}
}

func TestCheckHealthReportsReachable(t *testing.T) {
	f := newFakeCluster(true)
	f.client.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.31.4+k3s1",
	}
	m, _ := f.manager()

	report := m.CheckHealth(context.Background(), testCluster())
	if !report.Reachable {
		t.Fatalf("expected the cluster to be reachable: %+v", report)
	}
	if report.Version != "v1.31.4+k3s1" {
		t.Fatalf("unexpected version %q", report.Version)
	}
	if report.Message != "" {
		t.Fatalf("a reachable cluster must not carry a message, got %q", report.Message)
	}
}

func TestCheckHealthReportsMissingCredentials(t *testing.T) {
	f := newFakeCluster(true)
	m, _ := f.manager()

	cluster := testCluster()
	cluster.ServiceAccountToken = ""

	report := m.CheckHealth(context.Background(), cluster)
	if report.Reachable {
		t.Fatal("expected an unreachable report")
	}
	if !strings.Contains(report.Message, "missing an API URL") {
		t.Fatalf("unexpected message %q", report.Message)
	}
}

func TestCheckHealthNeverLeaksCredentials(t *testing.T) {
	f := newFakeCluster(true)
	f.client.Discovery().(*fakediscovery.FakeDiscovery).PrependReactor(
		"get", "version",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewUnauthorized("token cluster-sa-token rejected")
		},
	)
	m, _ := f.manager()

	report := m.CheckHealth(context.Background(), testCluster())
	if report.Reachable {
		t.Fatal("expected an unreachable report")
	}
	if strings.Contains(report.Message, "cluster-sa-token") {
		t.Fatalf("probe message leaked the service account token: %q", report.Message)
	}
}

func TestNewManagerUsesRealClientConstructor(t *testing.T) {
	m := NewManager()
	if m.newClient == nil {
		t.Fatal("expected a client constructor")
	}
	if m.clients == nil {
		t.Fatal("expected an initialized client cache")
	}
}
