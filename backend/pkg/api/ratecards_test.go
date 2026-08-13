package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * The rate card.
 *
 * What is pinned here is the validation and the resolution rule, because both
 * decide what every cost figure in the console says. The validation exists to
 * catch a unit error rather than to police pricing: nobody pays five thousand
 * dollars for a core-hour, and somebody does type a monthly figure into an
 * hourly field — after which the report is absurd in a way that is much harder
 * to diagnose from the far end than a refusal on the way in.
 */

func validCard() rateCardRequest {
	return rateCardRequest{
		Provider: db.RateProviderAWS, Currency: "usd",
		CPUCoreHour: 0.0353, MemoryGiBHour: 0.0038,
		StorageGiBMonth: 0.08, LoadBalancerMonth: 16.43,
	}
}

func TestRateCardRequestNormalizesWhatItAccepts(t *testing.T) {
	got, err := validCard().toRateCard(7)
	if err != nil {
		t.Fatalf("valid card refused: %v", err)
	}
	if got.Currency != "USD" {
		t.Errorf("currency = %q, want it upper-cased", got.Currency)
	}
	if got.ClusterID != 7 {
		t.Errorf("cluster = %d, want 7", got.ClusterID)
	}
}

func TestRateCardRequestDefaultsAnUnstatedProviderToCustom(t *testing.T) {
	req := validCard()
	req.Provider = ""

	got, err := req.toRateCard(db.RateCardScopeDefault)
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if got.Provider != db.RateProviderCustom {
		t.Errorf("provider = %q, want %q", got.Provider, db.RateProviderCustom)
	}
}

func TestRateCardRequestRefusesWhatCannotBeRight(t *testing.T) {
	cases := map[string]func(*rateCardRequest){
		"an unknown provider":   func(r *rateCardRequest) { r.Provider = "digitalocean" },
		"a two-letter currency": func(r *rateCardRequest) { r.Currency = "US" },
		"a numeric currency":    func(r *rateCardRequest) { r.Currency = "US1" },
		"a negative rate":       func(r *rateCardRequest) { r.CPUCoreHour = -1 },
		// The unit error this bound exists for: a month's price in an hourly
		// field.
		"an hourly rate that is plainly monthly": func(r *rateCardRequest) {
			r.CPUCoreHour = 25000
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			req := validCard()
			mutate(&req)
			if _, err := req.toRateCard(0); err == nil {
				t.Error("expected a refusal")
			}
		})
	}
}

func TestRateCardOfZeroesPricesNothing(t *testing.T) {
	// A card where everything is zero prices nothing, and the reports read that
	// as "no rates configured" rather than as "this fleet is free".
	if (db.RateCard{Currency: "USD"}).Priced() {
		t.Error("a card of zeroes must not read as priced")
	}
	// One rate known and the rest not is a legitimate, partial card.
	if !(db.RateCard{Currency: "USD", StorageGiBMonth: 0.08}).Priced() {
		t.Error("a card with one rate set must read as priced")
	}
}

/* --------------------------------------------------------- the resolution -- */

func TestClusterRateCardFallsBackToTheInstallationDefault(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)

	env.store.rateCards = map[uint]db.RateCard{
		db.RateCardScopeDefault: {Currency: "USD", CPUCoreHour: 0.03},
	}

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/rate-card", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	// The flag is what distinguishes "inheriting" from "has a card that happens
	// to match", and only one of the two changes when the default does.
	if !strings.Contains(rec.Body.String(), `"inherited":true`) {
		t.Errorf("body = %s, want inherited:true", rec.Body.String())
	}
}

func TestAClusterOverrideIsNotInherited(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)

	env.store.rateCards = map[uint]db.RateCard{
		db.RateCardScopeDefault: {Currency: "USD", CPUCoreHour: 0.03},
		cluster.ID:              {ClusterID: cluster.ID, Currency: "EUR", CPUCoreHour: 0.05},
	}

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/rate-card", token, nil)

	body := rec.Body.String()
	if !strings.Contains(body, `"inherited":false`) || !strings.Contains(body, `"EUR"`) {
		t.Errorf("body = %s, want the cluster's own EUR card", body)
	}
}

/* -------------------------------------------------------------- the guard -- */

func TestTheInstallationRateCardIsAdministrative(t *testing.T) {
	// It decides what every cost figure in the console says.
	env := newTestEnv(t)
	user := env.store.addUser("dev", "secret123", "user")
	token := env.tokenFor(t, user)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		rec := env.do(t, method, "/api/v1/settings/rate-card", token, nil)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403 for a non-admin", method, rec.Code)
		}
	}
}

func TestPresetsTravelWithTheirProvenance(t *testing.T) {
	// A preset is offered as something to replace, never as something to
	// accept, so every one of them says what it actually is.
	for _, preset := range ratePresets {
		if preset.Note == "" {
			t.Errorf("%s carries no note saying what these numbers are", preset.Label)
		}
		if preset.Provider == db.RateProviderCustom {
			continue
		}
		if !strings.Contains(preset.Note, "Replace it with your own rates") {
			t.Errorf("%s does not tell the operator to replace it", preset.Label)
		}
		if preset.CPUCoreHour <= 0 || preset.MemoryGiBHour <= 0 {
			t.Errorf("%s prices no compute", preset.Label)
		}
	}
}
