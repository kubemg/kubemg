package api

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// The sidebar's custom-resource sections are derived from the cluster, so the
// only thing standing between a developer and a hundred of an operator's
// internal kinds is this curation. What has to hold is that it narrows the
// *navigation* and nothing else, and that an administrator can still see — and
// therefore undo — what they hid.

func TestApplyCRDVisibilityNarrowsForEverybodyButTheAdmin(t *testing.T) {
	views := []crdView{
		{Group: "networking.istio.io", Plural: "virtualservices"},
		{Group: "cert-manager.io", Plural: "certificates"},
		{Group: "acme.io", Plural: "widgets"},
	}
	hidden := map[string]bool{"certificates.cert-manager.io": true}

	t.Run("a developer never sees the row", func(t *testing.T) {
		got := applyCRDVisibility(views, hidden, false)
		if len(got) != 2 {
			t.Fatalf("got %d kinds, want the two that were left on the list: %+v", len(got), got)
		}
		for _, view := range got {
			if view.Plural == "certificates" {
				t.Fatal("a hidden kind must not reach a caller who cannot put it back")
			}
		}
	})

	t.Run("an admin sees it marked", func(t *testing.T) {
		got := applyCRDVisibility(views, hidden, true)
		if len(got) != len(views) {
			t.Fatalf("got %d kinds, want all %d: an admin has to be able to undo this",
				len(got), len(views))
		}
		for _, view := range got {
			want := view.Plural == "certificates"
			if view.Hidden != want {
				t.Fatalf("%s.%s hidden = %v, want %v", view.Plural, view.Group, view.Hidden, want)
			}
		}
	})

	t.Run("a cluster nobody curated is untouched", func(t *testing.T) {
		if got := applyCRDVisibility(views, nil, false); len(got) != len(views) {
			t.Fatalf("got %d kinds, want all %d: the default is shown", len(got), len(views))
		}
	})
}

func TestNormalizeHiddenCRDs(t *testing.T) {
	got, err := normalizeHiddenCRDs([]string{
		"widgets.acme.io", "", "certificates.cert-manager.io", "widgets.acme.io",
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	want := []string{"certificates.cert-manager.io", "widgets.acme.io"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v — deduplicated and ordered", got, want)
	}
}

// A resource with no group is a built-in, and a built-in is not something this
// panel curates: the fixed inventory is what every cluster is browsed through.
// Anything that is not plural.group is refused rather than stored and quietly
// never matched.
func TestNormalizeHiddenCRDsRefusesWhatIsNotAResourceName(t *testing.T) {
	for _, bad := range []string{"pods", "Widgets.acme.io", "widgets.acme.io/v1", "widgets acme"} {
		if _, err := normalizeHiddenCRDs([]string{bad}); err == nil {
			t.Fatalf("%q was accepted, want a refusal naming the plural.group form", bad)
		}
	}
}

func TestCRDVisibilityIsReadableByAnyoneGrantedAndWrittenByAdmins(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	dev := env.store.addUser("dev", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(dev.ID, cluster.ID, "view", nil)

	path := "/api/v1/clusters/" + itoa(cluster.ID) + "/crd-visibility"
	body := map[string]any{"hidden": []string{"widgets.acme.io"}}

	// A developer may read it: they have to be able to tell "this cluster does
	// not run Istio" from "somebody took Istio off the list".
	rec := env.do(t, http.MethodGet, path, env.tokenFor(t, dev), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d reading, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if decode[struct {
		Editable bool `json:"editable"`
	}](t, rec).Editable {
		t.Fatal("a developer must not be told they can edit the curation")
	}

	if rec = env.do(t, http.MethodPut, path, env.tokenFor(t, dev), body); rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d writing as a developer, got %d (%s)",
			http.StatusForbidden, rec.Code, rec.Body.String())
	}

	if rec = env.do(t, http.MethodPut, path, env.tokenFor(t, admin), body); rec.Code != http.StatusOK {
		t.Fatalf("expected status %d writing as an admin, got %d (%s)",
			http.StatusOK, rec.Code, rec.Body.String())
	}
	if got := env.store.hiddenCRDs[cluster.ID]; !reflect.DeepEqual(got, []string{"widgets.acme.io"}) {
		t.Fatalf("stored %v, want the submitted set", got)
	}

	// The submitted set replaces the stored one, so clearing it is submitting
	// nothing rather than a delete route of its own.
	rec = env.do(t, http.MethodPut, path, env.tokenFor(t, admin), map[string]any{"hidden": []string{}})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d clearing, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if got := env.store.hiddenCRDs[cluster.ID]; len(got) != 0 {
		t.Fatalf("stored %v, want the curation cleared", got)
	}
}

func TestCRDVisibilityRefusesAnUngrantedCluster(t *testing.T) {
	env := newTestEnv(t)
	dev := env.store.addUser("dev", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/crd-visibility", env.tokenFor(t, dev), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d without a grant, got %d (%s)",
			http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestCRDVisibilityRefusesAMalformedResourceName(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	rec := env.do(t, http.MethodPut,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/crd-visibility", env.tokenFor(t, admin),
		map[string]any{"hidden": []string{"pods"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	if body := decode[map[string]string](t, rec); !strings.Contains(body["error"], "plural.group") {
		t.Fatalf("error = %q, want it to name the form a resource is written in", body["error"])
	}
}
