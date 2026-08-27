package api

import (
	"encoding/json"
	"testing"
)

/*
 * ResourceQuotas and LimitRanges.
 *
 * Both normalisations exist to stop an operator reading two maps against each
 * other by eye, and both have a state that is quiet when wrong: a quota the
 * controller has not counted yet, and a LimitRange resource that declares only
 * a default. Those are what these pin.
 */

func quotaFrom(t *testing.T, document string) quotaObject {
	t.Helper()
	var object quotaObject
	if err := json.Unmarshal([]byte(document), &object); err != nil {
		t.Fatalf("invalid fixture: %v", err)
	}
	return object
}

func TestQuotaViewPairsHardWithUsed(t *testing.T) {
	view := quotaFrom(t, `{
		"metadata": {"name": "team", "namespace": "shop"},
		"status": {
			"hard": {"requests.cpu": "4", "limits.memory": "8Gi", "pods": "20"},
			"used": {"requests.cpu": "3500m", "limits.memory": "6Gi", "pods": "17"}
		}
	}`).view()

	if len(view.Entries) != 3 {
		t.Fatalf("entries = %+v, want one row per bounded resource", view.Entries)
	}
	// Sorted, so the same quota reads the same way twice — a map's order is not
	// an order, and a table that reshuffles is a table nobody trusts.
	if view.Entries[0].Resource != "limits.memory" || view.Entries[2].Resource != "requests.cpu" {
		t.Fatalf("entries = %+v, want them sorted by resource", view.Entries)
	}
	if view.Entries[0].Hard != "8Gi" || view.Entries[0].Used != "6Gi" {
		t.Fatalf("entry = %+v, want the hard and used values paired", view.Entries[0])
	}
}

func TestQuotaViewLeavesAnUncountedResourceBlank(t *testing.T) {
	// A quota the controller has not run against yet carries no `used`, and a
	// zero would say the namespace is empty rather than uncounted.
	view := quotaFrom(t, `{
		"metadata": {"name": "team"},
		"status": {"hard": {"pods": "20"}}
	}`).view()

	if len(view.Entries) != 1 || view.Entries[0].Used != "" {
		t.Fatalf("entries = %+v, want the usage absent rather than zero", view.Entries)
	}
}

func TestQuotaViewNamesEveryScopeItIsNarrowedBy(t *testing.T) {
	// A quota with scopes does not bound everything in the namespace, and a
	// table that did not say so would be read as if it did. Both shapes count:
	// the legacy `scopes` list and the `scopeSelector` that replaced it.
	view := quotaFrom(t, `{
		"metadata": {"name": "team"},
		"spec": {
			"scopes": ["NotTerminating"],
			"scopeSelector": {"matchExpressions": [
				{"scopeName": "PriorityClass"},
				{"scopeName": "NotTerminating"}
			]}
		},
		"status": {"hard": {"pods": "20"}}
	}`).view()

	if len(view.Scopes) != 2 {
		t.Fatalf("scopes = %v, want both shapes merged without a duplicate", view.Scopes)
	}
}

/* ------------------------------------------------------------ LimitRange --- */

func limitRangeFrom(t *testing.T, document string) limitRangeObject {
	t.Helper()
	var object limitRangeObject
	if err := json.Unmarshal([]byte(document), &object); err != nil {
		t.Fatalf("invalid fixture: %v", err)
	}
	return object
}

func TestLimitRangeViewFlattensToOneRowPerResource(t *testing.T) {
	view := limitRangeFrom(t, `{
		"metadata": {"name": "limits", "namespace": "shop"},
		"spec": {"limits": [
			{"type": "Container",
			 "max": {"cpu": "2", "memory": "2Gi"},
			 "min": {"cpu": "100m"},
			 "default": {"cpu": "500m"},
			 "defaultRequest": {"cpu": "200m"}},
			{"type": "PersistentVolumeClaim", "max": {"storage": "50Gi"}}
		]}
	}`).view()

	if len(view.Entries) != 3 {
		t.Fatalf("entries = %+v, want one row per (type, resource) pair", view.Entries)
	}
	// Sorted by type then resource, so Container rows sit together above the
	// PVC one whatever order the object declared them in.
	if view.Entries[0].Type != "Container" || view.Entries[0].Resource != "cpu" {
		t.Fatalf("first entry = %+v, want Container/cpu", view.Entries[0])
	}
	if view.Entries[0].Max != "2" || view.Entries[0].Default != "500m" ||
		view.Entries[0].DefaultRequest != "200m" || view.Entries[0].Min != "100m" {
		t.Fatalf("cpu = %+v, want all four bounds it declared", view.Entries[0])
	}

	// memory declared only a max: the other three are absent rather than zero,
	// because `min: 0` and "no minimum" are different statements.
	memory := view.Entries[1]
	if memory.Resource != "memory" || memory.Min != "" || memory.Default != "" {
		t.Fatalf("memory = %+v, want the undeclared bounds left empty", memory)
	}
	if last := view.Entries[2]; last.Type != "PersistentVolumeClaim" || last.Resource != "storage" {
		t.Fatalf("last entry = %+v, want the PVC row", last)
	}
}

func TestLimitRangeViewKeepsAResourceThatOnlyHasADefault(t *testing.T) {
	// A default with no bounds is exactly the case somebody is trying to
	// explain: it is what got written onto a pod that declared nothing.
	view := limitRangeFrom(t, `{
		"metadata": {"name": "limits"},
		"spec": {"limits": [{"type": "Container", "default": {"memory": "256Mi"}}]}
	}`).view()

	if len(view.Entries) != 1 || view.Entries[0].Default != "256Mi" {
		t.Fatalf("entries = %+v, want the default-only resource kept", view.Entries)
	}
}
