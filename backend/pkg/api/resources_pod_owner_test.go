package api

import (
	"encoding/json"
	"testing"
)

// The namespace header reads a pod list per workload, so a pod has to carry
// the workload an operator would name — without a second read to resolve it.

func podViewFrom(t *testing.T, raw string) podView {
	t.Helper()
	var pod podObject
	if err := json.Unmarshal([]byte(raw), &pod); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return pod.view()
}

func TestPodOwnerResolvesADeploymentsReplicaSet(t *testing.T) {
	view := podViewFrom(t, `{
		"metadata": {
			"name": "checkout-7f9c5-abcde", "namespace": "shop",
			"labels": {"pod-template-hash": "7f9c5"},
			"ownerReferences": [{"kind": "ReplicaSet", "name": "checkout-7f9c5", "controller": true}]
		},
		"spec": {}, "status": {}
	}`)
	if view.Owner == nil || view.Owner.Kind != "Deployment" || view.Owner.Name != "checkout" {
		t.Fatalf("owner = %+v, want Deployment/checkout", view.Owner)
	}
}

// A ReplicaSet whose name does not end in its pods' hash was not made by a
// Deployment, and is not guessed up to one.
func TestPodOwnerKeepsAHandMadeReplicaSet(t *testing.T) {
	view := podViewFrom(t, `{
		"metadata": {
			"name": "legacy-x1", "labels": {"pod-template-hash": "7f9c5"},
			"ownerReferences": [{"kind": "ReplicaSet", "name": "legacy", "controller": true}]
		},
		"spec": {}, "status": {}
	}`)
	if view.Owner == nil || view.Owner.Kind != "ReplicaSet" || view.Owner.Name != "legacy" {
		t.Fatalf("owner = %+v, want ReplicaSet/legacy", view.Owner)
	}
}

func TestPodOwnerTakesOnlyTheControllingReference(t *testing.T) {
	view := podViewFrom(t, `{
		"metadata": {
			"name": "db-0",
			"ownerReferences": [
				{"kind": "ConfigMap", "name": "unrelated"},
				{"kind": "StatefulSet", "name": "db", "controller": true}
			]
		},
		"spec": {}, "status": {}
	}`)
	if view.Owner == nil || view.Owner.Kind != "StatefulSet" || view.Owner.Name != "db" {
		t.Fatalf("owner = %+v, want StatefulSet/db", view.Owner)
	}

	bare := podViewFrom(t, `{"metadata": {"name": "scratch"}, "spec": {}, "status": {}}`)
	if bare.Owner != nil {
		t.Fatalf("bare pod owner = %+v, want none", bare.Owner)
	}
}

// A container OOM-killed and Running again by the time anybody looks still
// says so: the evidence is how its previous run ended.
func TestPodViewCarriesTheLastTerminationReason(t *testing.T) {
	view := podViewFrom(t, `{
		"metadata": {"name": "api-1"},
		"spec": {"containers": [{"name": "app", "image": "app:1"}, {"name": "side", "image": "side:1"}]},
		"status": {"containerStatuses": [
			{"name": "app", "restartCount": 3, "state": {"running": {}},
			 "lastState": {"terminated": {"reason": "OOMKilled", "exitCode": 137}}},
			{"name": "side", "state": {"running": {}}}
		]}
	}`)
	if got := view.Containers[0].LastTerminationReason; got != "OOMKilled" {
		t.Fatalf("app last termination = %q, want OOMKilled", got)
	}
	if got := view.Containers[1].LastTerminationReason; got != "" {
		t.Fatalf("side last termination = %q, want empty", got)
	}
}
