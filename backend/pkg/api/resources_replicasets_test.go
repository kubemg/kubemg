package api

import (
	"encoding/json"
	"testing"
)

/*
 * ReplicaSets.
 *
 * The two columns that make this list worth having separately from the workload
 * one are the owner and the revision, and the owner is the one that is quiet
 * when wrong: a ReplicaSet can carry several owner references and only one of
 * them is the controller deciding its size.
 */

func TestReplicaSetViewNamesTheControllingOwner(t *testing.T) {
	var object replicaSetObject
	if err := json.Unmarshal([]byte(`{
		"metadata": {
			"name": "api-7d4f9", "namespace": "shop",
			"annotations": {"deployment.kubernetes.io/revision": "4"},
			"ownerReferences": [
				{"kind": "SomethingElse", "name": "watcher", "controller": false},
				{"kind": "Deployment", "name": "api", "controller": true}
			]
		},
		"spec": {"replicas": 0, "template": {"spec": {"containers": [{"image": "api:1.4"}]}}},
		"status": {"replicas": 0, "readyReplicas": 0}
	}`), &object); err != nil {
		t.Fatalf("invalid fixture: %v", err)
	}

	view := object.view()
	if view.Owner != "api" || view.OwnerKind != "Deployment" {
		t.Fatalf("owner = %s/%s, want the controlling reference", view.OwnerKind, view.Owner)
	}
	// The number `kubectl rollout undo --to-revision` takes, and the only thing
	// that orders one ReplicaSet against another.
	if view.Revision != "4" {
		t.Fatalf("revision = %q, want the Deployment's revision annotation", view.Revision)
	}
	// Zero desired is the ordinary state of every superseded ReplicaSet, and it
	// has to be reported rather than mistaken for "unset".
	if view.Desired != 0 || len(view.Images) != 1 {
		t.Fatalf("view = %+v, want a scaled-down ReplicaSet with its image", view)
	}
}

func TestReplicaSetViewLeavesAnUnownedOneBlank(t *testing.T) {
	// A ReplicaSet somebody created directly has no controller, which is
	// unusual enough to be worth seeing as a blank rather than filled in.
	var object replicaSetObject
	if err := json.Unmarshal([]byte(`{
		"metadata": {"name": "manual", "ownerReferences": [
			{"kind": "Deployment", "name": "api", "controller": false}
		]},
		"spec": {"replicas": 2}, "status": {"replicas": 2, "readyReplicas": 1}
	}`), &object); err != nil {
		t.Fatalf("invalid fixture: %v", err)
	}

	view := object.view()
	if view.Owner != "" || view.OwnerKind != "" {
		t.Fatalf("owner = %s/%s, want no owner named", view.OwnerKind, view.Owner)
	}
	if view.Desired != 2 || view.Ready != 1 {
		t.Fatalf("view = %+v, want the spec's desired and the status' ready", view)
	}
}
