package api

import (
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

func costRatesPath(clusterID uint) string {
	return "/api/v1/clusters/" + itoa(clusterID) + "/cost-rates"
}

func TestNormalizeCostRates(t *testing.T) {
	ok, err := normalizeCostRates(costRatesRequest{
		Currency: " eur ", CPUCoreHour: floatRef(0.04), MemoryGiBHour: floatRef(0),
		LoadBalancerHour: floatRef(0),
	})
	if err != nil {
		t.Fatalf("a valid form was refused: %v", err)
	}
	// Zero is a price — "costs nothing" — and absent is "not priced".
	if ok.Currency != "EUR" || ok.MemoryGiBHour != 0 || ok.LoadBalancerHour == nil || ok.StorageGiBMonth != nil {
		t.Errorf("normalised = %+v", ok)
	}

	refused := map[string]costRatesRequest{
		"currency":       {Currency: "euro", CPUCoreHour: floatRef(1), MemoryGiBHour: floatRef(1)},
		"cpu_core_hour":  {Currency: "USD", CPUCoreHour: floatRef(-1), MemoryGiBHour: floatRef(1)},
		"memory_gib":     {Currency: "USD", CPUCoreHour: floatRef(1)},
		"storage_gib":    {Currency: "USD", CPUCoreHour: floatRef(1), MemoryGiBHour: floatRef(1), StorageGiBMonth: floatRef(math.Inf(1))},
		"load_balancer_": {Currency: "USD", CPUCoreHour: floatRef(1), MemoryGiBHour: floatRef(1), LoadBalancerHour: floatRef(maxCostRate + 1)},
	}
	for name, req := range refused {
		if _, err := normalizeCostRates(req); err == nil {
			t.Errorf("%s: expected a refusal", name)
		} else if name != "currency" && name != "memory_gib" && !strings.Contains(err.Error(), name) {
			t.Errorf("%s: the refusal names the field, got %q", name, err)
		}
	}
}

func TestCostRatesAreReadableByTheGrantedAndSetByAdmins(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	dev := env.store.addUser("dev", "pw", db.RoleUser)
	cluster := env.store.addAgentCluster("edge", db.EnvProd, "agent-token")
	env.store.grant(dev.ID, cluster.ID, db.K8sRoleView, nil)
	adminToken, devToken := env.tokenFor(t, admin), env.tokenFor(t, dev)

	type answer struct {
		Rates    *db.ClusterCostRates `json:"rates"`
		Editable bool                 `json:"editable"`
	}

	rec := env.do(t, http.MethodGet, costRatesPath(cluster.ID), devToken, nil)
	if body := decode[answer](t, rec); rec.Code != http.StatusOK || body.Rates != nil || body.Editable {
		t.Fatalf("none set: %d %+v", rec.Code, body)
	}

	form := map[string]any{"currency": "usd", "cpu_core_hour": 0.031, "memory_gib_hour": 0.004}
	if rec := env.do(t, http.MethodPut, costRatesPath(cluster.ID), devToken, form); rec.Code != http.StatusForbidden {
		t.Fatalf("a non-admin sets no rates, got %d", rec.Code)
	}
	if rec := env.do(t, http.MethodPut, costRatesPath(cluster.ID), adminToken, form); rec.Code != http.StatusOK {
		t.Fatalf("admin put: %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := env.do(t, http.MethodPut, costRatesPath(cluster.ID), adminToken,
		map[string]any{"currency": "usd", "cpu_core_hour": 0.031}); rec.Code != http.StatusBadRequest {
		t.Fatalf("CPU without memory is refused, got %d", rec.Code)
	}

	rec = env.do(t, http.MethodGet, costRatesPath(cluster.ID), devToken, nil)
	body := decode[answer](t, rec)
	if body.Rates == nil || body.Rates.Currency != "USD" || body.Rates.CPUCoreHour != 0.031 {
		t.Fatalf("read back = %+v", body.Rates)
	}

	if rec := env.do(t, http.MethodDelete, costRatesPath(cluster.ID), devToken, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("a non-admin removes no rates, got %d", rec.Code)
	}
	if rec := env.do(t, http.MethodDelete, costRatesPath(cluster.ID), adminToken, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("admin delete: %d", rec.Code)
	}
	if rec := env.do(t, http.MethodDelete, costRatesPath(cluster.ID), adminToken, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("a second delete is 404, got %d", rec.Code)
	}
}

func TestCostRatesNeedAGrant(t *testing.T) {
	env := newTestEnv(t)
	dev := env.store.addUser("dev", "pw", db.RoleUser)
	cluster := env.store.addAgentCluster("edge", db.EnvProd, "agent-token")
	rec := env.do(t, http.MethodGet, costRatesPath(cluster.ID), env.tokenFor(t, dev), nil)
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Fatalf("expected a refusal without a grant, got %d", rec.Code)
	}
}
