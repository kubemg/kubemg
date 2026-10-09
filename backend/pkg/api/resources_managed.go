package api

import "strings"

/*
 * Who else writes this object.
 *
 * Every write the console offers — the manifest editor, scale, restart, suspend,
 * delete, a rollback — lands on an object that something other than the person
 * at the keyboard may be reconciling. An Argo CD application or a Flux
 * Kustomization puts back what Git says on its next sync; an operator rewrites
 * the StatefulSet it made from its custom resource within seconds; the next
 * `helm upgrade` renders over a hand edit. The write succeeds, reports what it
 * set, and is quietly undone, with nothing anywhere having said it would be.
 *
 * So the object is asked, and only the object: every signal below is a label,
 * an annotation or an owner reference the reconciler itself wrote, read off the
 * metadata a list or a describe already carries. Nothing extra is fetched — in
 * particular not the Argo CD Application, whose `selfHeal` is the only thing
 * that would turn "may be reverted" into "will be": it is a different object in
 * a different namespace the caller is usually not granted, and a notice is not
 * worth a read the cluster might refuse.
 *
 * It is a **notice, never a refusal** — the autoscaler notice's rule. Patching a
 * GitOps-managed object by hand during an incident is legitimate, and the
 * manifest editor could always do it anyway. What this prevents is the surprise.
 *
 * Deliberately not signals:
 *   - `app.kubernetes.io/instance` on its own. It was Argo CD's default tracking
 *     label once, and it is also what every Helm chart stamps on everything, so
 *     reading it as Argo would warn about half of every cluster. A notice that is
 *     wrong often is a notice nobody reads.
 *   - `managedFields` manager names. Which manager an apply was recorded under
 *     depends on the reconciler's version and apply mode; the labels and
 *     annotations below are each tool's documented tracking contract.
 *   - The owner chains Kubernetes itself runs that an operator already expects:
 *     a Pod under its ReplicaSet/StatefulSet/DaemonSet/Job, a Job under its
 *     CronJob. Deleting a Deployment's pod *to have it replaced* is the point,
 *     and warning on every pod would bury the notice that matters.
 */

// Reconcilers, as the console names them.
const (
	managerController = "controller"
	managerArgoCD     = "argocd"
	managerFlux       = "flux"
	managerHelm       = "helm"
)

const (
	argoInstanceLabel      = "argocd.argoproj.io/instance"
	argoTrackingAnnotation = "argocd.argoproj.io/tracking-id"

	fluxKustomizeName      = "kustomize.toolkit.fluxcd.io/name"
	fluxKustomizeNamespace = "kustomize.toolkit.fluxcd.io/namespace"
	fluxHelmName           = "helm.toolkit.fluxcd.io/name"
	fluxHelmNamespace      = "helm.toolkit.fluxcd.io/namespace"
	// fluxReconcileAnnotation set to "disabled" is Flux's own off switch for one
	// object: the controller leaves it alone from then on.
	fluxReconcileAnnotation = "kustomize.toolkit.fluxcd.io/reconcile"

	helmReleaseNameAnnotation      = "meta.helm.sh/release-name"
	helmReleaseNamespaceAnnotation = "meta.helm.sh/release-namespace"
)

// managedByView names what reconciles an object besides the people editing it.
type managedByView struct {
	// Manager is one of controller, argocd, flux, helm.
	Manager string `json:"manager"`
	// Kind is the managing object's kind where one is known: the owner's Kind for
	// a controller, Kustomization or HelmRelease for Flux.
	Kind string `json:"kind,omitempty"`
	// Name is the managing object: the owner, the Argo CD application, the Flux
	// object or the Helm release.
	Name string `json:"name"`
	// Namespace is where the managing object lives, when the tracking metadata
	// says so. For Argo CD it is the application's namespace, which only
	// apps-in-any-namespace writes into the tracking id.
	Namespace string `json:"namespace,omitempty"`
	// Reverts is true when a hand edit is certain to be undone — a controlling
	// owner, or a Flux Kustomization, whose drift correction is not optional.
	// False means "may be": Argo CD's self-heal and Flux helm-controller's drift
	// detection are settings on an object this does not read, and a Helm release
	// is only rewritten when somebody next upgrades it.
	Reverts bool `json:"reverts"`
}

// builtinOwnedKinds are the owner chains Kubernetes runs itself and an operator
// relies on, keyed by the owned Kind. See the file comment for why they do not
// earn a notice.
var builtinOwnedKinds = map[string][]string{
	"Pod": {"ReplicaSet", "StatefulSet", "DaemonSet", "Job", "Node"},
	"Job": {"CronJob"},
}

// managedBy reads who reconciles an object from its own metadata. Precedence is
// nearest first: a controlling owner rewrites the object directly, while a
// GitOps tool may only be tracking it through the parent the owner was made
// from — operators commonly copy their resource's labels onto what they create.
// Flux before Helm, because helm-controller's objects carry both.
func managedBy(kind string, labels, annotations map[string]string, owners []ownerRef) *managedByView {
	if owner := controllerOf(owners); owner.Kind != "" && !isBuiltinOwner(kind, owner.Kind) {
		return &managedByView{Manager: managerController, Kind: owner.Kind, Name: owner.Name, Reverts: true}
	}

	if view := argoManaged(labels, annotations); view != nil {
		return view
	}

	if name := labels[fluxKustomizeName]; name != "" {
		return &managedByView{
			Manager:   managerFlux,
			Kind:      "Kustomization",
			Name:      name,
			Namespace: labels[fluxKustomizeNamespace],
			Reverts:   annotations[fluxReconcileAnnotation] != "disabled",
		}
	}
	if name := labels[fluxHelmName]; name != "" {
		return &managedByView{
			Manager:   managerFlux,
			Kind:      "HelmRelease",
			Name:      name,
			Namespace: labels[fluxHelmNamespace],
		}
	}

	if name := annotations[helmReleaseNameAnnotation]; name != "" {
		return &managedByView{
			Manager:   managerHelm,
			Name:      name,
			Namespace: annotations[helmReleaseNamespaceAnnotation],
		}
	}
	return nil
}

func isBuiltinOwner(kind, ownerKind string) bool {
	for _, builtin := range builtinOwnedKinds[kind] {
		if builtin == ownerKind {
			return true
		}
	}
	return false
}

// argoManaged reads Argo CD's two tracking methods. The annotation is the
// default since Argo CD 3.0 and is written alongside the label in
// annotation+label mode, so it is read first; the label is what installs that
// chose label tracking with Argo's own key write.
//
// The tracking id is `<app>:<group>/<kind>:<namespace>/<name>`, and only the
// part before the first colon is the application; a value without that shape
// is not read as one. Either way `<app>` is `<app-namespace>_<app-name>` for an
// application outside Argo CD's own namespace — an underscore cannot appear in
// either name, so the split is unambiguous.
func argoManaged(labels, annotations map[string]string) *managedByView {
	app := ""
	if id := annotations[argoTrackingAnnotation]; id != "" {
		if prefix, _, found := strings.Cut(id, ":"); found {
			app = prefix
		}
	}
	if app == "" {
		app = labels[argoInstanceLabel]
	}
	if app == "" {
		return nil
	}
	view := &managedByView{Manager: managerArgoCD, Name: app}
	if namespace, name, ok := strings.Cut(app, "_"); ok && namespace != "" && name != "" {
		view.Namespace, view.Name = namespace, name
	}
	return view
}

// ownerRefsOf reads `metadata.ownerReferences` off an object decoded as a plain
// map, the shape describe works in.
func ownerRefsOf(metadata map[string]any) []ownerRef {
	entries, _ := metadata["ownerReferences"].([]any)
	refs := make([]ownerRef, 0, len(entries))
	for _, entry := range entries {
		fields, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		ref := ownerRef{}
		ref.Kind, _ = fields["kind"].(string)
		ref.Name, _ = fields["name"].(string)
		if controller, ok := fields["controller"].(bool); ok {
			ref.Controller = &controller
		}
		refs = append(refs, ref)
	}
	return refs
}

// managedObjectMeta is objectMeta plus the owner references, for the list reads
// whose rows carry a managed-by answer — the rows a selection acts on, which
// have no describe behind them to ask.
type managedObjectMeta struct {
	objectMeta
	OwnerReferences []ownerRef `json:"ownerReferences"`
}

func (m managedObjectMeta) managedBy(kind string) *managedByView {
	return managedBy(kind, m.Labels, m.Annotations, m.OwnerReferences)
}
