package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

/*
 * HorizontalPodAutoscalers.
 *
 * The list normalisation is the fixed-inventory pattern and needs no more
 * pinning than every other list has. What is pinned here is the part that is
 * quiet when wrong: the `minReplicas` default, the pairing of a declared metric
 * with the reading recorded for it, and the reason an autoscaler gives when it
 * cannot read its metric at all — which is the state that looks identical to a
 * healthy one on a table of replica counts.
 */

func hpaFrom(t *testing.T, document string) hpaObject {
	t.Helper()
	var object hpaObject
	if err := json.Unmarshal([]byte(document), &object); err != nil {
		t.Fatalf("invalid fixture: %v", err)
	}
	return object
}

func TestAutoscalerViewDefaultsMinReplicasToOne(t *testing.T) {
	// No minReplicas: the API's documented default is 1, and reporting 0 would
	// say this workload may be scaled away entirely.
	view := hpaFrom(t, `{
		"metadata": {"name": "api", "namespace": "shop"},
		"spec": {"maxReplicas": 10, "scaleTargetRef": {"kind": "Deployment", "name": "api"}},
		"status": {"currentReplicas": 3, "desiredReplicas": 4}
	}`).view()

	if view.MinReplicas != 1 {
		t.Fatalf("minReplicas = %d, want the documented default of 1", view.MinReplicas)
	}
	if view.TargetKind != "Deployment" || view.TargetName != "api" {
		t.Fatalf("target = %s/%s, want the scaleTargetRef", view.TargetKind, view.TargetName)
	}
	if view.CurrentReplicas != 3 || view.DesiredReplicas != 4 {
		t.Fatalf("replicas = %d/%d, want the status", view.CurrentReplicas, view.DesiredReplicas)
	}
}

func TestAutoscalerMetricsPairTargetWithItsOwnReading(t *testing.T) {
	// The spec and status lists are usually parallel and are not promised to
	// be. Here they are deliberately in opposite orders: a reading shown
	// against the wrong target is worse than no reading at all.
	view := hpaFrom(t, `{
		"metadata": {"name": "api"},
		"spec": {
			"maxReplicas": 10,
			"metrics": [
				{"type": "Resource", "resource": {"name": "cpu",
					"target": {"type": "Utilization", "averageUtilization": 80}}},
				{"type": "Resource", "resource": {"name": "memory",
					"target": {"type": "AverageValue", "averageValue": "512Mi"}}}
			]
		},
		"status": {
			"currentMetrics": [
				{"type": "Resource", "resource": {"name": "memory",
					"current": {"averageValue": "300Mi"}}},
				{"type": "Resource", "resource": {"name": "cpu",
					"current": {"averageUtilization": 43}}}
			]
		}
	}`).view()

	if len(view.Metrics) != 2 {
		t.Fatalf("metrics = %+v, want one row per declared metric", view.Metrics)
	}
	if view.Metrics[0] != (autoscalerMetricView{Name: "cpu", Target: "80%", Current: "43%"}) {
		t.Fatalf("cpu = %+v, want its own reading", view.Metrics[0])
	}
	if view.Metrics[1] != (autoscalerMetricView{Name: "memory", Target: "512Mi", Current: "300Mi"}) {
		t.Fatalf("memory = %+v, want its own reading", view.Metrics[1])
	}
}

func TestAutoscalerMetricWithNoReadingYetIsNotZero(t *testing.T) {
	// An HPA that has not scraped once reports nothing, and 0% would read as
	// "the workload is idle" — the opposite of "nobody knows".
	view := hpaFrom(t, `{
		"metadata": {"name": "api"},
		"spec": {"maxReplicas": 5, "metrics": [
			{"type": "Resource", "resource": {"name": "cpu",
				"target": {"type": "Utilization", "averageUtilization": 60}}}
		]}
	}`).view()

	if len(view.Metrics) != 1 || view.Metrics[0].Current != "" {
		t.Fatalf("metrics = %+v, want the reading absent rather than zero", view.Metrics)
	}
}

func TestAutoscalerSkipsAMetricShapeItCannotRead(t *testing.T) {
	// The same rule objectKinds follows for an unknown kind: fail towards
	// saying nothing rather than towards a blank row that means something.
	view := hpaFrom(t, `{
		"metadata": {"name": "api"},
		"spec": {"maxReplicas": 5, "metrics": [
			{"type": "Wormhole"},
			{"type": "Resource"},
			{"type": "Pods", "pods": {"metric": {"name": "queue_depth"},
				"target": {"type": "AverageValue", "averageValue": "30"}}}
		]}
	}`).view()

	if len(view.Metrics) != 1 || view.Metrics[0].Name != "queue_depth" {
		t.Fatalf("metrics = %+v, want only the row that could be read", view.Metrics)
	}
}

func TestAutoscalerReportsWhyItIsNotScaling(t *testing.T) {
	view := hpaFrom(t, `{
		"metadata": {"name": "api"},
		"spec": {"maxReplicas": 5},
		"status": {"conditions": [
			{"type": "AbleToScale", "status": "True"},
			{"type": "ScalingActive", "status": "False", "reason": "FailedGetResourceMetric",
			 "message": "did not receive metrics for any ready pods"}
		]}
	}`).view()

	if view.Reason == "" {
		t.Fatal("an autoscaler that cannot read its metric has to say so")
	}
}

func TestAutoscalerNoticeNamesTheBoundsThatWillWin(t *testing.T) {
	notice := autoscalerNotice(autoscalerView{
		listMeta: listMeta{Name: "api-hpa"}, MinReplicas: 2, MaxReplicas: 10,
	})
	for _, want := range []string{"api-hpa", "2", "10"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("notice = %q, want it to name %q", notice, want)
		}
	}
}

/* ------------------------------------------------------------ the route --- */

func TestWorkloadAutoscalerRefusesAKindThatCannotBeAutoscaled(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)

	// A DaemonSet has no `scale` subresource, so no HPA can ever own it —
	// asking is a mistake worth naming rather than an empty answer.
	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+
			"/resources/workload/autoscaler?kind=daemonsets&name=agent&namespace=shop", token, nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestWorkloadAutoscalerNeedsAName(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+
			"/resources/workload/autoscaler?kind=deployments&namespace=shop", token, nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}
