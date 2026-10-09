package api

import (
	"encoding/json"
	"reflect"
	"testing"
)

/*
 * Who else writes an object.
 *
 * The notice is only worth anything if it is right: one that fires on half the
 * cluster is one nobody reads. So the table below pins both directions — each
 * reconciler's own tracking metadata is recognised, and the look-alikes that
 * are not tracking metadata are not.
 */

func TestManagedByReadsEachReconcilersOwnMetadata(t *testing.T) {
	yes := true
	no := false

	cases := []struct {
		name        string
		kind        string
		labels      map[string]string
		annotations map[string]string
		owners      []ownerRef
		want        *managedByView
	}{
		{
			name: "nothing manages a hand-made object",
			kind: "Deployment",
			want: nil,
		},
		{
			name:   "an operator's controlling owner is certain to revert",
			kind:   "StatefulSet",
			owners: []ownerRef{{Kind: "PostgresCluster", Name: "orders", Controller: &yes}},
			want:   &managedByView{Manager: "controller", Kind: "PostgresCluster", Name: "orders", Reverts: true},
		},
		{
			name:   "an owner that is not the controller is not one",
			kind:   "ConfigMap",
			owners: []ownerRef{{Kind: "Deployment", Name: "api", Controller: &no}},
			want:   nil,
		},
		{
			name:   "a ReplicaSet's Deployment is a controller worth naming",
			kind:   "ReplicaSet",
			owners: []ownerRef{{Kind: "Deployment", Name: "api", Controller: &yes}},
			want:   &managedByView{Manager: "controller", Kind: "Deployment", Name: "api", Reverts: true},
		},
		{
			name:   "a pod under its ReplicaSet is the ordinary case, not a notice",
			kind:   "Pod",
			owners: []ownerRef{{Kind: "ReplicaSet", Name: "api-7d4f9", Controller: &yes}},
			want:   nil,
		},
		{
			name:   "a Job under its CronJob is the ordinary case, not a notice",
			kind:   "Job",
			owners: []ownerRef{{Kind: "CronJob", Name: "nightly", Controller: &yes}},
			want:   nil,
		},
		{
			name:   "a pod an operator made directly is a notice",
			kind:   "Pod",
			owners: []ownerRef{{Kind: "Kafka", Name: "events", Controller: &yes}},
			want:   &managedByView{Manager: "controller", Kind: "Kafka", Name: "events", Reverts: true},
		},
		{
			name:   "Argo CD's instance label",
			kind:   "Deployment",
			labels: map[string]string{"argocd.argoproj.io/instance": "shop"},
			want:   &managedByView{Manager: "argocd", Name: "shop"},
		},
		{
			name: "Argo CD's tracking annotation, the 3.0 default",
			kind: "Deployment",
			annotations: map[string]string{
				"argocd.argoproj.io/tracking-id": "shop:apps/Deployment:shop/api",
			},
			want: &managedByView{Manager: "argocd", Name: "shop"},
		},
		{
			name: "an application outside Argo CD's namespace carries it in the id",
			kind: "Deployment",
			annotations: map[string]string{
				"argocd.argoproj.io/tracking-id": "team-a_shop:apps/Deployment:shop/api",
			},
			want: &managedByView{Manager: "argocd", Name: "shop", Namespace: "team-a"},
		},
		{
			name:        "a tracking id without its shape is not read as one",
			kind:        "Deployment",
			annotations: map[string]string{"argocd.argoproj.io/tracking-id": "garbage"},
			want:        nil,
		},
		{
			name:   "app.kubernetes.io/instance alone is every Helm chart, not Argo CD",
			kind:   "Deployment",
			labels: map[string]string{"app.kubernetes.io/instance": "shop"},
			want:   nil,
		},
		{
			name: "a Flux Kustomization corrects drift",
			kind: "Deployment",
			labels: map[string]string{
				"kustomize.toolkit.fluxcd.io/name":      "apps",
				"kustomize.toolkit.fluxcd.io/namespace": "flux-system",
			},
			want: &managedByView{Manager: "flux", Kind: "Kustomization", Name: "apps", Namespace: "flux-system", Reverts: true},
		},
		{
			name:        "Flux's own off switch for one object is honoured",
			kind:        "Deployment",
			labels:      map[string]string{"kustomize.toolkit.fluxcd.io/name": "apps"},
			annotations: map[string]string{"kustomize.toolkit.fluxcd.io/reconcile": "disabled"},
			want:        &managedByView{Manager: "flux", Kind: "Kustomization", Name: "apps"},
		},
		{
			name: "helm-controller's objects are Flux's before they are Helm's",
			kind: "Deployment",
			labels: map[string]string{
				"helm.toolkit.fluxcd.io/name":      "redis",
				"helm.toolkit.fluxcd.io/namespace": "cache",
			},
			annotations: map[string]string{"meta.helm.sh/release-name": "cache-redis"},
			want:        &managedByView{Manager: "flux", Kind: "HelmRelease", Name: "redis", Namespace: "cache"},
		},
		{
			name: "a plain Helm release",
			kind: "Deployment",
			annotations: map[string]string{
				"meta.helm.sh/release-name":      "redis",
				"meta.helm.sh/release-namespace": "cache",
			},
			want: &managedByView{Manager: "helm", Name: "redis", Namespace: "cache"},
		},
		{
			name:   "an operator's owner wins over the GitOps labels it copied down",
			kind:   "StatefulSet",
			labels: map[string]string{"argocd.argoproj.io/instance": "data"},
			owners: []ownerRef{{Kind: "PostgresCluster", Name: "orders", Controller: &yes}},
			want:   &managedByView{Manager: "controller", Kind: "PostgresCluster", Name: "orders", Reverts: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := managedBy(tc.kind, tc.labels, tc.annotations, tc.owners)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("managedBy = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// The describe path decodes metadata as a plain map, so the owner references
// arrive through ownerRefsOf rather than a typed struct — the one place the
// answer could silently go missing.
func TestDescribeReportsWhatManagesTheObject(t *testing.T) {
	var object map[string]any
	if err := json.Unmarshal([]byte(`{
		"kind": "StatefulSet",
		"metadata": {
			"name": "orders-db", "namespace": "shop",
			"ownerReferences": [{"apiVersion": "postgres.example.com/v1", "kind": "PostgresCluster", "name": "orders", "controller": true}]
		}
	}`), &object); err != nil {
		t.Fatalf("invalid fixture: %v", err)
	}

	view := describeObject(object, "orders-db", "shop")
	want := &managedByView{Manager: "controller", Kind: "PostgresCluster", Name: "orders", Reverts: true}
	if !reflect.DeepEqual(view.ManagedBy, want) {
		t.Fatalf("managed_by = %+v, want %+v", view.ManagedBy, want)
	}
}

// A row a selection acts on has no describe behind it, so the list has to carry
// the answer itself.
func TestListRowsCarryWhatManagesThem(t *testing.T) {
	var object replicaSetObject
	if err := json.Unmarshal([]byte(`{
		"metadata": {
			"name": "api-7d4f9", "namespace": "shop",
			"ownerReferences": [{"kind": "Deployment", "name": "api", "controller": true}]
		}
	}`), &object); err != nil {
		t.Fatalf("invalid fixture: %v", err)
	}
	view := object.view()
	if view.ManagedBy == nil || view.ManagedBy.Name != "api" || !view.ManagedBy.Reverts {
		t.Fatalf("managed_by = %+v, want the controlling Deployment", view.ManagedBy)
	}
	// The owner columns read the same references and must not have moved.
	if view.Owner != "api" || view.OwnerKind != "Deployment" {
		t.Fatalf("owner = %s/%s, want the controlling reference", view.OwnerKind, view.Owner)
	}

	var meta managedObjectMeta
	if err := json.Unmarshal([]byte(`{
		"name": "api", "namespace": "shop",
		"labels": {"argocd.argoproj.io/instance": "shop"}
	}`), &meta); err != nil {
		t.Fatalf("invalid fixture: %v", err)
	}
	if got := meta.managedBy("Deployment"); got == nil || got.Manager != "argocd" {
		t.Fatalf("managed_by = %+v, want the Argo CD application", got)
	}
}
