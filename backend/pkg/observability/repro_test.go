package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

func TestReproClusterMemory(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{},"values":[[1785315600,"123456"],[1785315615,"123999"]]}]}}`))
	}))
	defer server.Close()

	target := Target{
		Kind:       db.SourceMetrics,
		Provider:   db.ProviderVictoriaMetrics,
		AccessMode: db.AccessDirect,
		URL:        server.URL,
		AuthMode:   db.AuthNone,
	}

	for _, kind := range []MetricKind{MetricClusterMemory, MetricClusterCPU, MetricPodMemory} {
		req := MetricRequest{Kind: kind}
		if kind == MetricPodMemory {
			req.Namespace = "shop"
			req.Pod = "checkout"
		}
		result, err := QueryMetrics(context.Background(), target, nil, Scope{}, req)
		if err != nil {
			t.Errorf("%s FAILED: %v", kind, err)
			continue
		}
		t.Logf("%s OK: %d series, query=%s", kind, len(result.Series), result.Query)
		t.Logf("   path=%s", gotPath)
	}
}
