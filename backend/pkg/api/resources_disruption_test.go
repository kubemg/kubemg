package api

import (
	"encoding/json"
	"testing"
)

/*
 * PodDisruptionBudgets.
 *
 * `minAvailable` and `maxUnavailable` are IntOrString, and coercing them into
 * numbers is the way this list would quietly turn `"50%"` into 50 — a
 * completely different budget. That, and `disruptionsAllowed: 0`, which is the
 * state a drain hangs on while every other list looks healthy, are what these
 * pin.
 */

func TestIntOrStringKeepsWhicheverWasWritten(t *testing.T) {
	cases := []struct {
		document string
		want     string
	}{
		{`{"v": 2}`, "2"},
		{`{"v": "50%"}`, "50%"},
		{`{"v": "3"}`, "3"},
		// Neither shape: read as unset rather than failing the whole list. One
		// malformed budget must not cost the operator every other one.
		{`{"v": {"nope": true}}`, ""},
	}

	for _, test := range cases {
		var wrapper struct {
			V *intOrString `json:"v"`
		}
		if err := json.Unmarshal([]byte(test.document), &wrapper); err != nil {
			t.Fatalf("unmarshal %s: %v", test.document, err)
		}
		if got := wrapper.V.String(); got != test.want {
			t.Fatalf("%s -> %q, want %q", test.document, got, test.want)
		}
	}

	// An absent field is empty, not a nil dereference.
	var absent *intOrString
	if got := absent.String(); got != "" {
		t.Fatalf("absent = %q, want empty", got)
	}
}

func TestPodDisruptionBudgetViewCarriesWhatADrainBlocksOn(t *testing.T) {
	var object pdbObject
	if err := json.Unmarshal([]byte(`{
		"metadata": {"name": "api", "namespace": "shop"},
		"spec": {
			"minAvailable": "50%",
			"selector": {"matchLabels": {"app": "api"}}
		},
		"status": {
			"currentHealthy": 3, "desiredHealthy": 3,
			"disruptionsAllowed": 0, "expectedPods": 3
		}
	}`), &object); err != nil {
		t.Fatalf("invalid fixture: %v", err)
	}

	view := object.view()
	if view.MinAvailable != "50%" {
		t.Fatalf("minAvailable = %q, want the percentage kept as one", view.MinAvailable)
	}
	if view.MaxUnavailable != "" {
		t.Fatalf("maxUnavailable = %q, want it absent — the budget did not declare it", view.MaxUnavailable)
	}
	if view.Selector != "app=api" {
		t.Fatalf("selector = %q, want the rendered selector", view.Selector)
	}
	// The column the whole list is for: nothing may be evicted, and everything
	// else about this budget looks healthy.
	if view.DisruptionsAllowed != 0 || view.CurrentHealthy != 3 {
		t.Fatalf("view = %+v, want the status carried across", view)
	}
}

func TestPodDisruptionBudgetWithNoSelectorSaysSo(t *testing.T) {
	// A PDB with no selector protects no pods at all. That is a real and broken
	// state, and it has to read as empty rather than as a crash.
	var object pdbObject
	if err := json.Unmarshal([]byte(`{"metadata": {"name": "api"}, "spec": {}}`), &object); err != nil {
		t.Fatalf("invalid fixture: %v", err)
	}
	if got := object.view().Selector; got != "" {
		t.Fatalf("selector = %q, want empty", got)
	}
}
