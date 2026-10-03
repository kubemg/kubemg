package observability

import (
	"encoding/json"
	"strings"
	"testing"
)

func roundTrip(t *testing.T, spec AlarmSpec, namespace string) Alarm {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"name": spec.Name, "namespace": namespace,
			"labels": spec.Labels, "annotations": spec.Annotations,
		},
		"spec": spec.Spec,
	})
	if err != nil {
		t.Fatal(err)
	}
	var object PrometheusRuleObject
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	alarm, ok := ParseAlarm(object)
	if !ok {
		t.Fatal("a rule KubeMG wrote must read back as one")
	}
	return alarm
}

func TestBuildAlarmWritesTheRuleAroundValidatedNames(t *testing.T) {
	threshold := 5.0
	spec, err := BuildAlarm(AlarmRequest{
		Namespace: "shop", Kind: "deployments", Name: "api.v2", Condition: "restarts",
		Threshold: &threshold, For: "5m", Severity: "critical", Note: "page payments",
		Actor: "jane", RuleLabels: map[string]string{"release": "kube-prom"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != "kubemg-deployment-api.v2-restarts" {
		t.Fatalf("name = %q", spec.Name)
	}
	// The dot is escaped for the regex; the suffix is the Deployment pod shape.
	want := `sum(increase(kube_pod_container_status_restarts_total{namespace="shop",pod=~"api\\.v2-[a-z0-9]{1,10}-[a-z0-9]{5}"}[15m])) > 5`
	if spec.Expr != want {
		t.Fatalf("expr =\n%s\nwant\n%s", spec.Expr, want)
	}
	if spec.Labels["release"] != "kube-prom" || spec.Labels[ManagedByLabel] != ManagedByValue {
		t.Fatalf("labels = %v, want the rule selector's and KubeMG's own", spec.Labels)
	}

	alarm := roundTrip(t, spec, "shop")
	if alarm.Kind != "deployments" || alarm.Target != "api.v2" || alarm.Condition != "restarts" ||
		alarm.For != "5m" || alarm.Severity != "critical" || *alarm.Threshold != 5 ||
		alarm.CreatedBy != "jane" || alarm.Note != "page payments" || alarm.ConditionLabel != "Pods restarting" {
		t.Fatalf("read back = %+v", alarm)
	}
}

func TestBuildAlarmDefaultsAndOmitsAZeroFor(t *testing.T) {
	spec, err := BuildAlarm(AlarmRequest{Namespace: "shop", Kind: "jobs", Name: "nightly", Condition: "failed"})
	if err != nil {
		t.Fatal(err)
	}
	groups := spec.Spec["groups"].([]any)
	rule := groups[0].(map[string]any)["rules"].([]any)[0].(map[string]any)
	if _, ok := rule["for"]; ok {
		t.Fatalf("a 0m duration is written as no `for` at all: %v", rule)
	}
	labels := rule["labels"].(map[string]string)
	if labels["severity"] != "warning" || labels["job_name"] != "nightly" || labels[KubeMGRuleLabel] != spec.Name {
		t.Fatalf("rule labels = %v", labels)
	}
	if rule["alert"] != "KubeMGJobFailed" {
		t.Fatalf("alert = %v", rule["alert"])
	}
}

func TestBuildAlarmRefusesWhatCannotBeWritten(t *testing.T) {
	big := 101.0
	for name, req := range map[string]AlarmRequest{
		"matcher syntax": {Namespace: "shop", Kind: "pods", Name: `a"} or up{`, Condition: "not-ready"},
		"bad namespace":  {Namespace: "Shop", Kind: "pods", Name: "a", Condition: "not-ready"},
		"no condition":   {Namespace: "shop", Kind: "pods", Name: "a", Condition: "filling"},
		"unknown kind":   {Namespace: "shop", Kind: "secrets", Name: "a", Condition: "not-ready"},
		"duration":       {Namespace: "shop", Kind: "pods", Name: "a", Condition: "not-ready", For: "2m"},
		"severity":       {Namespace: "shop", Kind: "pods", Name: "a", Condition: "not-ready", Severity: "page"},
		"threshold":      {Namespace: "shop", Kind: "persistentvolumeclaims", Name: "a", Condition: "filling", Threshold: &big},
		"note":           {Namespace: "shop", Kind: "pods", Name: "a", Condition: "not-ready", Note: strings.Repeat("x", 501)},
	} {
		if _, err := BuildAlarm(req); err == nil {
			t.Fatalf("%s: built, want a refusal", name)
		}
	}
}

func TestEveryCatalogueEntryRenders(t *testing.T) {
	for kind, conditions := range AlarmCatalogue() {
		for _, condition := range conditions {
			spec, err := BuildAlarm(AlarmRequest{Namespace: "ns", Kind: kind, Name: "obj", Condition: condition.Key})
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, condition.Key, err)
			}
			if !strings.Contains(spec.Expr, `namespace="ns"`) || strings.Contains(spec.Expr, "$") {
				t.Fatalf("%s/%s: expr %q is not scoped or has an unfilled term", kind, condition.Key, spec.Expr)
			}
		}
	}
}

func TestAlarmObjectNameStaysWithinTheAPILimit(t *testing.T) {
	long := strings.Repeat("a", 250)
	first := AlarmObjectName("deployments", long, "unavailable")
	second := AlarmObjectName("deployments", long+"b", "unavailable")
	if len(first) > 253 || first == second {
		t.Fatalf("long names: %d chars, distinct = %v", len(first), first != second)
	}
}

func TestParseAlarmIgnoresRulesKubeMGDidNotWrite(t *testing.T) {
	var object PrometheusRuleObject
	object.Metadata.Labels = map[string]string{"release": "kube-prom"}
	if _, ok := ParseAlarm(object); ok {
		t.Fatal("a rule without KubeMG's managed-by label is somebody else's")
	}
}
