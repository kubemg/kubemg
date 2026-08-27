package api

import (
	"github.com/gin-gonic/gin"
)

/*
 * ReplicaSets.
 *
 * They are already read on the rollout-history path, which is the clue that
 * they belong in the inventory: a stuck rollout is a fact about a ReplicaSet,
 * not about the Deployment above it. The Deployment says how many replicas it
 * wants and, during a rollout, two ReplicaSets are between that and the pods —
 * the one that will not scale up and the one that will not scale down. Neither
 * was visible here.
 *
 * The view is the workload one plus the two columns that make it worth having
 * separately: the controller that owns it, so it is obvious which Deployment a
 * row belongs to when a namespace holds forty of them, and the revision, which
 * is what `kubectl rollout history` numbers a rollout by and the only thing
 * that orders one ReplicaSet against another.
 */

type replicaSetView struct {
	listMeta
	// Desired is `spec.replicas` — what the owning Deployment currently asks
	// of this ReplicaSet. Zero is the ordinary state of every superseded one.
	Desired int32 `json:"desired"`
	Current int32 `json:"current"`
	Ready   int32 `json:"ready"`
	// Owner names the controller that made it, empty for a ReplicaSet somebody
	// created directly — which is unusual enough to be worth seeing as a blank
	// rather than filled in with a guess.
	Owner     string   `json:"owner,omitempty"`
	OwnerKind string   `json:"owner_kind,omitempty"`
	Revision  string   `json:"revision,omitempty"`
	Images    []string `json:"images,omitempty"`
}

type replicaSetObject struct {
	Metadata struct {
		objectMeta
		OwnerReferences []struct {
			Kind       string `json:"kind"`
			Name       string `json:"name"`
			Controller *bool  `json:"controller"`
		} `json:"ownerReferences"`
	} `json:"metadata"`
	Spec struct {
		Replicas *int32 `json:"replicas"`
		Template struct {
			Spec struct {
				Containers []struct {
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
	Status struct {
		Replicas      int32 `json:"replicas"`
		ReadyReplicas int32 `json:"readyReplicas"`
	} `json:"status"`
}

func (r replicaSetObject) view() replicaSetView {
	view := replicaSetView{
		listMeta: r.Metadata.meta(),
		Current:  r.Status.Replicas,
		Ready:    r.Status.ReadyReplicas,
		Revision: r.Metadata.Annotations[deploymentRevisionAnnotation],
	}
	if r.Spec.Replicas != nil {
		view.Desired = *r.Spec.Replicas
	}
	// The *controlling* reference, not the first one: a ReplicaSet can carry
	// several owners and only one of them is the controller deciding its size.
	for _, owner := range r.Metadata.OwnerReferences {
		if owner.Controller != nil && *owner.Controller {
			view.Owner, view.OwnerKind = owner.Name, owner.Kind
			break
		}
	}
	for _, container := range r.Spec.Template.Spec.Containers {
		view.Images = append(view.Images, container.Image)
	}
	return view
}

func (s *server) listReplicaSets(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	out := []replicaSetView{}
	for _, path := range scope.paths(resourceListPath{"/apis/apps/v1", "replicasets"}) {
		var items []replicaSetObject
		if !fetchList(s, c, user, cluster, grant, path, &items) {
			return
		}
		for _, item := range items {
			out = append(out, item.view())
		}
	}

	sortResources(out)
	listResponse(c, gin.H{
		"replicasets":    out,
		"namespace":      scope.Namespace,
		"all_namespaces": scope.All,
	})
}
