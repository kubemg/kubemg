package api

import (
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

/*
 * The GitOps controllers' own records, decoded and normalised.
 *
 * Every struct here is an allowlist. An Application's `spec.source.helm.values`,
 * `valuesObject`, `parameters` and `plugin.env`, a Kustomization's
 * `postBuild.substitute` and a HelmRelease's `spec.values` can all carry
 * secrets, and none of them has a field below — so they are never decoded,
 * never held and cannot be written back out by a later edit that forgets to
 * strip them. A test pins that by name.
 *
 * KubeMG does not decide whether anything drifted. Argo CD has already compared
 * the render against the cluster and written the answer per resource; Flux has
 * written which revision it last applied and which it last attempted. What is
 * below translates those answers into one vocabulary and adds nothing of its
 * own — in particular a Flux object's per-resource state is `unknown`, because
 * Flux corrects drift rather than recording it and saying `synced` would be a
 * claim nobody made.
 */

const (
	gitopsArgoCD          = "argocd"
	gitopsFluxKustomize   = "flux-kustomization"
	gitopsFluxHelmRelease = "flux-helmrelease"

	syncSynced    = "synced"
	syncOutOfSync = "out_of_sync"
	syncUnknown   = "unknown"

	healthHealthy     = "healthy"
	healthProgressing = "progressing"
	healthDegraded    = "degraded"
	healthSuspended   = "suspended"
	healthMissing     = "missing"
	healthUnknown     = "unknown"

	// maxGitopsResources caps the resource entries one record carries. An
	// Application managing a whole platform can list thousands; the drifted and
	// unhealthy ones sort first, so the cut only ever drops healthy, synced rows.
	maxGitopsResources = 500

	// maxGitopsMessage bounds a condition or operation message. Argo CD's sync
	// failure messages concatenate one line per failed object.
	maxGitopsMessage = 512

	// argoInClusterServer is how an Application names the cluster Argo CD
	// itself runs in.
	argoInClusterServer = "https://kubernetes.default.svc"
	argoInClusterName   = "in-cluster"
)

// gitopsToolNames lists the tools a drift rule may name.
func gitopsToolNames() []string {
	names := make([]string, 0, len(gitopsTools))
	for _, tool := range gitopsTools {
		names = append(names, tool.name)
	}
	return names
}

// gitopsTool is one controller's list: which API serves it, newest version
// first, and how its objects decode.
type gitopsTool struct {
	name   string
	paths  []string
	decode func(raw json.RawMessage) (gitopsRecord, bool)
}

var gitopsTools = []gitopsTool{
	{
		name:   gitopsArgoCD,
		paths:  []string{"/apis/argoproj.io/v1alpha1/applications"},
		decode: decodeArgoApplication,
	},
	{
		name: gitopsFluxKustomize,
		paths: []string{
			"/apis/kustomize.toolkit.fluxcd.io/v1/kustomizations",
			"/apis/kustomize.toolkit.fluxcd.io/v1beta2/kustomizations",
		},
		decode: decodeFluxKustomization,
	},
	{
		name: gitopsFluxHelmRelease,
		paths: []string{
			"/apis/helm.toolkit.fluxcd.io/v2/helmreleases",
			"/apis/helm.toolkit.fluxcd.io/v2beta2/helmreleases",
			"/apis/helm.toolkit.fluxcd.io/v2beta1/helmreleases",
		},
		decode: decodeFluxHelmRelease,
	},
}

// gitopsSource is where a record says its desired state comes from.
type gitopsSource struct {
	// Repo is the repository URL with any userinfo removed, or for Flux the
	// source object it names (`GitRepository/flux-system`) — reading the
	// source itself would be another list for a label.
	Repo           string `json:"repo,omitempty"`
	Path           string `json:"path,omitempty"`
	Chart          string `json:"chart,omitempty"`
	TargetRevision string `json:"target_revision,omitempty"`
}

// gitopsResource is one object a record manages, as the controller reports it.
type gitopsResource struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Sync      string `json:"sync"`
	Health    string `json:"health,omitempty"`

	// version is the API version Argo CD reports the entry at. It is what the
	// drift poller asks discovery for the entry's resource name with, and is
	// never sent.
	version string
}

// gitopsRecord is one Application, Kustomization or HelmRelease.
type gitopsRecord struct {
	Tool      string         `json:"tool"`
	Namespace string         `json:"namespace"`
	Name      string         `json:"name"`
	Project   string         `json:"project,omitempty"`
	Sources   []gitopsSource `json:"sources"`
	// Revision is what was last applied (Argo: compared, Flux: applied).
	Revision string `json:"revision,omitempty"`
	// AttemptedRevision is Flux's last attempt, set only when it differs from
	// what was applied — the one shape of Flux drift Flux itself records.
	AttemptedRevision string `json:"attempted_revision,omitempty"`
	Sync              string `json:"sync"`
	Health            string `json:"health"`
	// AutoHeal says whether a hand edit is put back: Argo CD's selfHeal, true
	// for a Kustomization (drift correction is not optional), helm-controller's
	// drift detection in `enabled` mode. Nil when the record does not say.
	AutoHeal     *bool      `json:"auto_heal"`
	Suspended    bool       `json:"suspended"`
	ReconciledAt *time.Time `json:"reconciled_at,omitempty"`
	Message      string     `json:"message,omitempty"`
	// Remote names the destination when the record deploys to a cluster other
	// than the one it lives in. Its resources are that cluster's, not this one's.
	Remote string `json:"remote,omitempty"`
	// ConsoleURL opens this record in Argo CD's own console, where Sync and
	// Refresh live. Set only on an Argo CD record, only after narrowing, and
	// only when the cluster has an `argocd` console row.
	ConsoleURL string `json:"console_url,omitempty"`

	Resources          []gitopsResource `json:"resources"`
	ResourcesHidden    int              `json:"resources_hidden"`
	ResourcesTruncated bool             `json:"resources_truncated"`

	// destinationNamespace is where the record deploys by default. It decides
	// visibility for a scoped grant alongside the resources, and is never sent.
	destinationNamespace string
	// lastSyncAt is when Argo CD last finished a sync operation. A write to a
	// tracked object after it is what drifted the object; it bounds the audit
	// read that names who made it, and is never sent.
	lastSyncAt *time.Time
}

type gitopsMeta struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type gitopsCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

func findCondition(conditions []gitopsCondition, kind string) *gitopsCondition {
	for i := range conditions {
		if conditions[i].Type == kind {
			return &conditions[i]
		}
	}
	return nil
}

/* --------------------------------------------------------------- Argo CD --- */

type argoSource struct {
	RepoURL        string `json:"repoURL"`
	Path           string `json:"path"`
	Chart          string `json:"chart"`
	TargetRevision string `json:"targetRevision"`
}

type argoApplication struct {
	Metadata gitopsMeta `json:"metadata"`
	Spec     struct {
		Project     string       `json:"project"`
		Source      *argoSource  `json:"source"`
		Sources     []argoSource `json:"sources"`
		Destination struct {
			Server    string `json:"server"`
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"destination"`
		SyncPolicy *struct {
			Automated *struct {
				SelfHeal bool `json:"selfHeal"`
			} `json:"automated"`
		} `json:"syncPolicy"`
	} `json:"spec"`
	Status struct {
		Sync struct {
			Status    string   `json:"status"`
			Revision  string   `json:"revision"`
			Revisions []string `json:"revisions"`
		} `json:"sync"`
		Health struct {
			Status string `json:"status"`
		} `json:"health"`
		Resources []struct {
			Group     string `json:"group"`
			Version   string `json:"version"`
			Kind      string `json:"kind"`
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
			Status    string `json:"status"`
			Health    *struct {
				Status string `json:"status"`
			} `json:"health"`
		} `json:"resources"`
		ReconciledAt   *time.Time `json:"reconciledAt"`
		OperationState *struct {
			Phase      string     `json:"phase"`
			Message    string     `json:"message"`
			FinishedAt *time.Time `json:"finishedAt"`
		} `json:"operationState"`
		Conditions []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"conditions"`
	} `json:"status"`
}

func decodeArgoApplication(raw json.RawMessage) (gitopsRecord, bool) {
	var app argoApplication
	if err := json.Unmarshal(raw, &app); err != nil || app.Metadata.Name == "" {
		return gitopsRecord{}, false
	}

	record := gitopsRecord{
		Tool:                 gitopsArgoCD,
		Namespace:            app.Metadata.Namespace,
		Name:                 app.Metadata.Name,
		Project:              app.Spec.Project,
		Sources:              []gitopsSource{},
		Sync:                 argoSync(app.Status.Sync.Status),
		Health:               argoHealth(app.Status.Health.Status),
		ReconciledAt:         app.Status.ReconciledAt,
		destinationNamespace: app.Spec.Destination.Namespace,
	}

	sources := app.Spec.Sources
	if app.Spec.Source != nil {
		sources = append([]argoSource{*app.Spec.Source}, sources...)
	}
	for _, source := range sources {
		record.Sources = append(record.Sources, gitopsSource{
			Repo:           redactRepoURL(source.RepoURL),
			Path:           source.Path,
			Chart:          source.Chart,
			TargetRevision: source.TargetRevision,
		})
	}

	record.Revision = app.Status.Sync.Revision
	if record.Revision == "" && len(app.Status.Sync.Revisions) > 0 {
		record.Revision = strings.Join(app.Status.Sync.Revisions, ", ")
	}

	heal := false
	if policy := app.Spec.SyncPolicy; policy != nil && policy.Automated != nil {
		heal = policy.Automated.SelfHeal
	}
	record.AutoHeal = &heal

	if destination := app.Spec.Destination; !argoInCluster(destination.Server, destination.Name) {
		record.Remote = destination.Name
		if record.Remote == "" {
			record.Remote = destination.Server
		}
	}

	// A comparison or sync error explains an Unknown/OutOfSync better than the
	// last operation does, so conditions are read first.
	for _, condition := range app.Status.Conditions {
		if strings.HasSuffix(condition.Type, "Error") && condition.Message != "" {
			record.Message = condition.Message
			break
		}
	}
	if op := app.Status.OperationState; record.Message == "" && op != nil &&
		(op.Phase == "Failed" || op.Phase == "Error") {
		record.Message = op.Message
	}
	record.Message = boundMessage(record.Message)
	if op := app.Status.OperationState; op != nil {
		record.lastSyncAt = op.FinishedAt
	}

	for _, entry := range app.Status.Resources {
		resource := gitopsResource{
			Group:     entry.Group,
			Kind:      entry.Kind,
			Namespace: entry.Namespace,
			Name:      entry.Name,
			Sync:      argoSync(entry.Status),
			version:   entry.Version,
		}
		if entry.Health != nil {
			resource.Health = argoHealth(entry.Health.Status)
		}
		record.Resources = append(record.Resources, resource)
	}
	return record, true
}

// argoInCluster reports whether an Application deploys into the cluster it
// lives in. An empty destination is Argo CD's own validation failure, not a
// remote cluster, and is read as local.
func argoInCluster(server, name string) bool {
	if server == "" && name == "" {
		return true
	}
	return strings.TrimRight(server, "/") == argoInClusterServer || name == argoInClusterName
}

func argoSync(status string) string {
	switch status {
	case "Synced":
		return syncSynced
	case "OutOfSync":
		return syncOutOfSync
	}
	return syncUnknown
}

func argoHealth(status string) string {
	switch status {
	case "Healthy":
		return healthHealthy
	case "Progressing":
		return healthProgressing
	case "Degraded":
		return healthDegraded
	case "Suspended":
		return healthSuspended
	case "Missing":
		return healthMissing
	}
	return healthUnknown
}

/* ------------------------------------------------------------------ Flux --- */

type fluxSourceRef struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

func (r fluxSourceRef) String() string {
	if r.Name == "" {
		return ""
	}
	return r.Kind + "/" + r.Name
}

type fluxKustomization struct {
	Metadata gitopsMeta `json:"metadata"`
	Spec     struct {
		Path            string         `json:"path"`
		SourceRef       fluxSourceRef  `json:"sourceRef"`
		Suspend         bool           `json:"suspend"`
		TargetNamespace string         `json:"targetNamespace"`
		KubeConfig      *fluxRemoteRef `json:"kubeConfig"`
	} `json:"spec"`
	Status struct {
		LastAppliedRevision   string            `json:"lastAppliedRevision"`
		LastAttemptedRevision string            `json:"lastAttemptedRevision"`
		Conditions            []gitopsCondition `json:"conditions"`
		Inventory             *struct {
			Entries []struct {
				ID string `json:"id"`
			} `json:"entries"`
		} `json:"inventory"`
	} `json:"status"`
}

// fluxRemoteRef is `spec.kubeConfig`: present means the object applies to
// another cluster. Only the Secret's name is read, never the Secret.
type fluxRemoteRef struct {
	SecretRef *struct {
		Name string `json:"name"`
	} `json:"secretRef"`
}

func (r *fluxRemoteRef) describe() string {
	if r == nil {
		return ""
	}
	if r.SecretRef != nil && r.SecretRef.Name != "" {
		return "kubeconfig " + r.SecretRef.Name
	}
	return "another cluster"
}

func decodeFluxKustomization(raw json.RawMessage) (gitopsRecord, bool) {
	var ks fluxKustomization
	if err := json.Unmarshal(raw, &ks); err != nil || ks.Metadata.Name == "" {
		return gitopsRecord{}, false
	}

	heal := true
	record := gitopsRecord{
		Tool:      gitopsFluxKustomize,
		Namespace: ks.Metadata.Namespace,
		Name:      ks.Metadata.Name,
		Sources: []gitopsSource{{
			Repo: ks.Spec.SourceRef.String(),
			Path: ks.Spec.Path,
		}},
		Revision:             ks.Status.LastAppliedRevision,
		AutoHeal:             &heal,
		Suspended:            ks.Spec.Suspend,
		Remote:               ks.Spec.KubeConfig.describe(),
		destinationNamespace: ks.Spec.TargetNamespace,
	}
	if attempted := ks.Status.LastAttemptedRevision; attempted != "" && attempted != record.Revision {
		record.AttemptedRevision = attempted
	}
	record.Sync, record.Health, record.Message = fluxState(ks.Status.Conditions, ks.Spec.Suspend,
		record.AttemptedRevision != "")

	if ks.Status.Inventory != nil {
		for _, entry := range ks.Status.Inventory.Entries {
			if resource, ok := parseFluxInventoryID(entry.ID); ok {
				record.Resources = append(record.Resources, resource)
			}
		}
	}
	return record, true
}

// parseFluxInventoryID reads `<namespace>_<name>_<group>_<kind>`. None of the
// four can contain an underscore — names and groups are DNS labels, kinds are
// identifiers — so the split is unambiguous; an empty namespace is a
// cluster-scoped object and an empty group the core API.
func parseFluxInventoryID(id string) (gitopsResource, bool) {
	parts := strings.Split(id, "_")
	if len(parts) != 4 || parts[1] == "" || parts[3] == "" {
		return gitopsResource{}, false
	}
	return gitopsResource{
		Namespace: parts[0],
		Name:      parts[1],
		Group:     parts[2],
		Kind:      parts[3],
		Sync:      syncUnknown,
	}, true
}

type fluxHelmRelease struct {
	Metadata gitopsMeta `json:"metadata"`
	Spec     struct {
		Chart *struct {
			Spec struct {
				Chart     string        `json:"chart"`
				Version   string        `json:"version"`
				SourceRef fluxSourceRef `json:"sourceRef"`
			} `json:"spec"`
		} `json:"chart"`
		ChartRef        *fluxSourceRef `json:"chartRef"`
		Suspend         bool           `json:"suspend"`
		TargetNamespace string         `json:"targetNamespace"`
		KubeConfig      *fluxRemoteRef `json:"kubeConfig"`
		DriftDetection  *struct {
			Mode string `json:"mode"`
		} `json:"driftDetection"`
	} `json:"spec"`
	Status struct {
		LastAttemptedRevision string            `json:"lastAttemptedRevision"`
		LastAppliedRevision   string            `json:"lastAppliedRevision"`
		Conditions            []gitopsCondition `json:"conditions"`
		History               []struct {
			ChartName    string `json:"chartName"`
			ChartVersion string `json:"chartVersion"`
		} `json:"history"`
	} `json:"status"`
}

func decodeFluxHelmRelease(raw json.RawMessage) (gitopsRecord, bool) {
	var hr fluxHelmRelease
	if err := json.Unmarshal(raw, &hr); err != nil || hr.Metadata.Name == "" {
		return gitopsRecord{}, false
	}

	source := gitopsSource{}
	switch {
	case hr.Spec.Chart != nil:
		source.Repo = hr.Spec.Chart.Spec.SourceRef.String()
		source.Chart = hr.Spec.Chart.Spec.Chart
		source.TargetRevision = hr.Spec.Chart.Spec.Version
	case hr.Spec.ChartRef != nil:
		source.Repo = hr.Spec.ChartRef.String()
	}

	record := gitopsRecord{
		Tool:      gitopsFluxHelmRelease,
		Namespace: hr.Metadata.Namespace,
		Name:      hr.Metadata.Name,
		Sources:   []gitopsSource{source},
		Suspended: hr.Spec.Suspend,
		Remote:    hr.Spec.KubeConfig.describe(),
		// A release installs into its own namespace unless told otherwise.
		destinationNamespace: hr.Metadata.Namespace,
		// helm-controller does not report the objects a release made, so there
		// is no inventory to list; the page says so rather than showing none.
		Resources: []gitopsResource{},
	}
	if hr.Spec.TargetNamespace != "" {
		record.destinationNamespace = hr.Spec.TargetNamespace
	}

	// v2 writes history newest first; v2beta1 only lastAppliedRevision.
	if len(hr.Status.History) > 0 {
		record.Revision = hr.Status.History[0].ChartVersion
	} else {
		record.Revision = hr.Status.LastAppliedRevision
	}
	if attempted := hr.Status.LastAttemptedRevision; attempted != "" && record.Revision != "" &&
		attempted != record.Revision {
		record.AttemptedRevision = attempted
	}

	// Drift detection is off unless asked for; `warn` reports and leaves the
	// edit, `enabled` puts the release back.
	heal := hr.Spec.DriftDetection != nil && hr.Spec.DriftDetection.Mode == "enabled"
	record.AutoHeal = &heal

	record.Sync, record.Health, record.Message = fluxState(hr.Status.Conditions, hr.Spec.Suspend,
		record.AttemptedRevision != "")
	return record, true
}

// fluxState reads a Flux object's Ready condition. A failed apply means the
// cluster does not hold what Git declares, which is the one out-of-sync Flux
// records; a revision attempted but not applied is the same fact from the
// other side.
func fluxState(conditions []gitopsCondition, suspended, attemptPending bool) (sync, health, message string) {
	sync, health = syncUnknown, healthUnknown
	if ready := findCondition(conditions, "Ready"); ready != nil {
		switch ready.Status {
		case "True":
			sync, health = syncSynced, healthHealthy
		case "False":
			sync, health = syncOutOfSync, healthDegraded
			message = ready.Message
		default:
			health = healthProgressing
		}
	}
	if attemptPending && sync == syncSynced {
		sync = syncOutOfSync
	}
	if suspended {
		health = healthSuspended
	}
	return sync, health, boundMessage(message)
}

/* ---------------------------------------------------------------- shared --- */

// scpLikeRepo is git's `user@host:path` form, which url.Parse refuses. Its user
// is an SSH login (`git`), never a secret; the key is elsewhere.
var scpLikeRepo = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^/:@][^@]*$`)

// redactRepoURL drops credentials an operator put in a repository URL. Argo CD
// advises against it and accepts it anyway; the URL is shown to everyone the
// record is visible to, and a token in it would leave with the page.
//
// It fails closed: a value that parses neither as a URL nor as git's scp form
// is cut back to what follows its last `@`, or dropped if that leaves nothing
// recognisable. The query and fragment go too — a token parameter is the other
// place a credential rides.
func redactRepoURL(raw string) string {
	if scpLikeRepo.MatchString(raw) {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		if at := strings.LastIndex(raw, "@"); at >= 0 {
			raw = raw[at+1:]
		}
		if raw == "" || strings.ContainsAny(raw, "?#") {
			return ""
		}
		return raw
	}
	parsed.User = nil
	parsed.RawQuery, parsed.ForceQuery = "", false
	parsed.Fragment, parsed.RawFragment = "", ""
	return parsed.String()
}

func boundMessage(message string) string {
	message = strings.TrimSpace(message)
	if len(message) <= maxGitopsMessage {
		return message
	}
	return strings.ToValidUTF8(message[:maxGitopsMessage], "") + "…"
}

// syncRank orders the drifted first, then the unknown, then the settled.
func syncRank(sync string) int {
	switch sync {
	case syncOutOfSync:
		return 0
	case syncUnknown:
		return 1
	}
	return 2
}

func healthRank(health string) int {
	switch health {
	case healthDegraded, healthMissing:
		return 0
	case healthProgressing, healthUnknown:
		return 1
	case healthSuspended:
		return 2
	}
	return 3
}

// sortGitopsRecords puts what needs a look first: out of sync, then unhealthy,
// then by name.
func sortGitopsRecords(records []gitopsRecord) {
	slices.SortStableFunc(records, func(a, b gitopsRecord) int {
		if d := syncRank(a.Sync) - syncRank(b.Sync); d != 0 {
			return d
		}
		if d := healthRank(a.Health) - healthRank(b.Health); d != 0 {
			return d
		}
		if d := strings.Compare(a.Namespace, b.Namespace); d != 0 {
			return d
		}
		if d := strings.Compare(a.Name, b.Name); d != 0 {
			return d
		}
		return strings.Compare(a.Tool, b.Tool)
	})
}

// finishResources orders a record's entries and applies the cap, after
// narrowing, so the cut is of what the caller could have seen.
func (r *gitopsRecord) finishResources() {
	if r.Resources == nil {
		r.Resources = []gitopsResource{}
	}
	slices.SortStableFunc(r.Resources, func(a, b gitopsResource) int {
		if d := syncRank(a.Sync) - syncRank(b.Sync); d != 0 {
			return d
		}
		if d := healthRank(a.Health) - healthRank(b.Health); d != 0 {
			return d
		}
		if d := strings.Compare(a.Kind, b.Kind); d != 0 {
			return d
		}
		if d := strings.Compare(a.Namespace, b.Namespace); d != 0 {
			return d
		}
		return strings.Compare(a.Name, b.Name)
	})
	if len(r.Resources) > maxGitopsResources {
		r.Resources = r.Resources[:maxGitopsResources]
		r.ResourcesTruncated = true
	}
}
