package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * GitOps sync state — which objects have drifted from what Argo CD or Flux
 * declares, read from the controller's own records rather than from Git.
 *
 * KubeMG does not clone repositories. It cannot reproduce the controller's
 * render (kustomize plugins, config management plugins, `ignoreDifferences`,
 * `postBuild` substitution, values from ConfigMaps), so a comparison of its own
 * would report every difference between two renders as drift, on every object.
 * The controller has already compared, and wrote the answer down.
 *
 * Whose identity reads it is the decision everything else follows from.
 * Applications live in Argo CD's namespace and Kustomizations in
 * `flux-system`; a namespace-scoped grant covers neither, so a read as the
 * caller would hide drift from exactly the people whose namespaces drifted. The
 * records are therefore read as KubeMG's own service identity
 * (`kubemg:gitops-reader`, the alarm and event watchers' posture: the
 * `kubemg:cluster-admin` group, so no agent re-apply is needed) and narrowed
 * here, to the caller's namespaces, before anything is answered — the
 * observability path's rule: when KubeMG is the reader, authorization cannot be
 * delegated to the cluster. The narrowing is narrowGitopsRecord, and the shape
 * it narrows is an allowlist (gitops_records.go).
 *
 * The per-object read is the other way round: the object itself is read as the
 * caller first, so the cluster's RBAC has already said they may see it before
 * the record tracking it is looked up — and the record is found from the
 * object's own tracking metadata, never from anything the caller names.
 */

// gitopsReaderUser is the identity the controller records are read as. It is
// its own name so the trail can tell these reads from the watchers'.
const gitopsReaderUser = "kubemg:gitops-reader"

// argoDefaultNamespace is where Argo CD's install puts its Applications. A
// tracking id written for an Application there does not name the namespace,
// so it is tried first before listing every Application in the cluster.
const argoDefaultNamespace = "argocd"

// gitopsServiceReader reads as kubemg:gitops-reader. Unlike tunnelReader it
// hands a gateway refusal back as an error: a missing tunnel or a spent budget
// is the request's failure, not one tool's.
type gitopsServiceReader struct {
	s       *server
	c       *gin.Context
	cluster *db.Cluster
}

func (r gitopsServiceReader) get(path string) (int, []byte, error) {
	user := &db.User{Username: gitopsReaderUser}
	grant := db.UserClusterAccess{ClusterID: r.cluster.ID, K8sRole: db.K8sRoleClusterAdmin}
	resp, err := r.s.proxy.Call(r.c.Request.Context(), user, r.cluster, grant,
		http.MethodGet, path, nil, nil)
	if err != nil {
		return 0, nil, err
	}
	return resp.Status, resp.Body, nil
}

// gitopsToolStatus says whether one controller's list could be read.
type gitopsToolStatus struct {
	Tool      string `json:"tool"`
	Installed bool   `json:"installed"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// gitopsRead is every record the cluster holds, before narrowing.
type gitopsRead struct {
	records   []gitopsRecord
	tools     []gitopsToolStatus
	truncated bool
}

// readGitopsRecords lists every tool's records. A tool that is not installed,
// or whose list the cluster refuses, is reported on its own and the others are
// still answered; only a transport failure fails the read.
func readGitopsRecords(read trafficReader) (gitopsRead, error) {
	out := gitopsRead{records: []gitopsRecord{}, tools: []gitopsToolStatus{}}
	for _, tool := range gitopsTools {
		records, status, truncated, err := readGitopsTool(read, tool)
		if err != nil {
			return gitopsRead{}, err
		}
		out.records = append(out.records, records...)
		out.tools = append(out.tools, status)
		out.truncated = out.truncated || truncated
	}
	return out, nil
}

func readGitopsTool(read trafficReader, tool gitopsTool) ([]gitopsRecord, gitopsToolStatus, bool, error) {
	status := gitopsToolStatus{Tool: tool.name}
	var transport error

	for i, path := range tool.paths {
		fetch := func(limit int, cont string) (int, []byte, bool) {
			code, body, err := read.get(pagedPath(path, limit, cont))
			if err != nil {
				transport = err
				return 0, nil, false
			}
			return code, body, true
		}

		var raw []json.RawMessage
		walk, ok := walkListPages(fetch, "", &raw)
		if !ok {
			return nil, status, false, transport
		}
		if walk.status == http.StatusNotFound && i < len(tool.paths)-1 {
			continue
		}

		switch {
		case walk.status == http.StatusNotFound:
			status.Reason = gitopsToolLabel(tool.name) + " is not installed in this cluster"
			return nil, status, false, nil
		case walk.status != 0:
			status.Installed = true
			status.Reason = kubeErrorMessage(walk.body, walk.status)
			return nil, status, false, nil
		case walk.unreadable:
			status.Installed = true
			status.Reason = "the cluster returned an unreadable list"
			return nil, status, false, nil
		}

		status.Installed, status.Available = true, true
		records := make([]gitopsRecord, 0, len(raw))
		for _, item := range raw {
			if record, ok := tool.decode(item); ok {
				records = append(records, record)
			}
		}
		return records, status, walk.truncated, nil
	}
	return nil, status, false, nil
}

func gitopsToolLabel(tool string) string {
	switch tool {
	case gitopsArgoCD:
		return "Argo CD"
	case gitopsFluxKustomize:
		return "Flux's kustomize-controller"
	case gitopsFluxHelmRelease:
		return "Flux's helm-controller"
	}
	return tool
}

/*
 * narrowGitopsRecord decides what of a record a grant sees.
 *
 *   - An unscoped grant sees all of it.
 *   - A record deploying to another cluster is invisible to a scoped grant: its
 *     resources' namespaces are that cluster's, and matching them against this
 *     cluster's grant would be a coincidence of names.
 *   - Otherwise a scoped grant sees the resource entries in its namespaces and
 *     a count of the rest. Cluster-scoped entries are never in a namespace, so
 *     they are always in the count.
 *   - The record is visible when any entry survived, or when it lives in or
 *     deploys to one of the caller's namespaces.
 *   - A view grant never sees a Secret entry, scoped or not. The cluster's own
 *     view role refuses Secrets, and these records were read as KubeMG, so the
 *     name would otherwise leave through a read the caller could not make.
 *   - When anything was cut the message goes too: Argo CD's sync failure lists
 *     the objects that failed, and those may be the cut ones.
 */
func narrowGitopsRecord(record gitopsRecord, grant db.UserClusterAccess) (gitopsRecord, bool) {
	allowed := grant.NamespaceList()
	if len(allowed) > 0 && record.Remote != "" {
		return gitopsRecord{}, false
	}
	hideSecrets := grant.K8sRole == "" || grant.K8sRole == db.K8sRoleView

	kept := make([]gitopsResource, 0, len(record.Resources))
	for _, resource := range record.Resources {
		if hideSecrets && resource.Group == "" && resource.Kind == "Secret" {
			continue
		}
		if len(allowed) == 0 || (resource.Namespace != "" && slices.Contains(allowed, resource.Namespace)) {
			kept = append(kept, resource)
		}
	}
	hidden := len(record.Resources) - len(kept)

	visible := len(allowed) == 0 || len(kept) > 0 ||
		slices.Contains(allowed, record.Namespace) ||
		(record.destinationNamespace != "" && slices.Contains(allowed, record.destinationNamespace))
	if !visible {
		return gitopsRecord{}, false
	}

	record.Resources = kept
	record.ResourcesHidden += hidden
	if hidden > 0 {
		record.Message = ""
	}
	return record, true
}

// gitopsSummary counts what the page leads with.
type gitopsSummary struct {
	Records   int `json:"records"`
	OutOfSync int `json:"out_of_sync"`
	Degraded  int `json:"degraded"`
}

// listGitopsRecords answers GET /clusters/:id/gitops/applications.
func (s *server) listGitopsRecords(c *gin.Context) {
	_, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}

	read, err := readGitopsRecords(gitopsServiceReader{s: s, c: c, cluster: cluster})
	if err != nil {
		writeGitopsReadError(c, err)
		return
	}

	records := make([]gitopsRecord, 0, len(read.records))
	summary := gitopsSummary{}
	for _, record := range read.records {
		narrowed, visible := narrowGitopsRecord(record, grant)
		if !visible {
			continue
		}
		narrowed.finishResources()
		records = append(records, narrowed)

		summary.Records++
		if narrowed.Sync == syncOutOfSync {
			summary.OutOfSync++
		}
		if narrowed.Health == healthDegraded || narrowed.Health == healthMissing {
			summary.Degraded++
		}
	}
	sortGitopsRecords(records)

	available := slices.ContainsFunc(read.tools, func(t gitopsToolStatus) bool { return t.Available })
	if read.truncated {
		noteTruncated(c)
	}
	listResponse(c, gin.H{
		"available": available,
		"tools":     read.tools,
		"records":   records,
		"summary":   summary,
	})
}

func writeGitopsReadError(c *gin.Context, err error) {
	var callErr *bastion.CallError
	if errors.As(err, &callErr) {
		c.JSON(callErr.Status, gin.H{"error": callErr.Message})
		return
	}
	c.JSON(http.StatusBadGateway, gin.H{"error": "could not read from the cluster"})
}

/* ------------------------------------------------------------ one object --- */

// gitopsObjectView answers "is this object tracked, and how is it doing".
type gitopsObjectView struct {
	Tracked   bool           `json:"tracked"`
	ManagedBy *managedByView `json:"managed_by"`
	// Record is the tracking Application/Kustomization/HelmRelease, narrowed
	// exactly as the list narrows it. Nil when it could not be found here.
	Record *gitopsRecord `json:"record"`
	// Entry is this object's own line in the record. Nil when the record does
	// not list it (helm-controller lists nothing; Argo CD omits what it ignores).
	Entry  *gitopsResource `json:"entry"`
	Reason string          `json:"reason,omitempty"`
}

type gitopsObjectMeta struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
	Metadata   struct {
		Name            string            `json:"name"`
		Namespace       string            `json:"namespace"`
		Labels          map[string]string `json:"labels"`
		Annotations     map[string]string `json:"annotations"`
		OwnerReferences []ownerRef        `json:"ownerReferences"`
	} `json:"metadata"`
}

// showGitopsObject answers GET /clusters/:id/gitops/object?kind=&namespace=&name=.
func (s *server) showGitopsObject(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	kind, name, namespace, ok := s.resourceObjectTarget(c, grant)
	if !ok {
		return
	}

	// As the caller: the cluster decides whether they may see the object
	// before anything about it is read as KubeMG.
	body, ok := s.readObject(c, user, cluster, grant, kind, namespace, name)
	if !ok {
		return
	}
	var object gitopsObjectMeta
	if err := json.Unmarshal(body, &object); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "the cluster returned an unreadable object"})
		return
	}

	view, err := resolveGitopsObject(gitopsServiceReader{s: s, c: c, cluster: cluster}, object, grant)
	if err != nil {
		writeGitopsReadError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// resolveGitopsObject finds the record tracking an object from the object's own
// metadata and narrows it for the caller.
func resolveGitopsObject(read trafficReader, object gitopsObjectMeta, grant db.UserClusterAccess) (gitopsObjectView, error) {
	meta := object.Metadata
	managed := managedBy(object.Kind, meta.Labels, meta.Annotations, meta.OwnerReferences)
	view := gitopsObjectView{ManagedBy: managed}
	if managed == nil || (managed.Manager != managerArgoCD && managed.Manager != managerFlux) {
		return view, nil
	}
	view.Tracked = true

	tool := gitopsArgoCD
	if managed.Manager == managerFlux {
		tool = gitopsFluxKustomize
		if managed.Kind == "HelmRelease" {
			tool = gitopsFluxHelmRelease
		}
	}

	target := gitopsResource{
		Group:     apiGroupOf(object.APIVersion),
		Kind:      object.Kind,
		Namespace: meta.Namespace,
		Name:      meta.Name,
	}
	record, found, reason, err := findGitopsRecord(read, tool, managed, target)
	if err != nil {
		return view, err
	}
	if !found {
		view.Reason = reason
		return view, nil
	}

	narrowed, visible := narrowGitopsRecord(record, grant)
	if !visible {
		// The caller can read the object, so its namespace is in their grant
		// and its entry would have kept the record visible; only an entry the
		// record does not list gets here. It reads exactly as a record that is
		// not here, so the answer is no oracle for records outside the grant.
		view.Reason = gitopsNotHere(tool, managed.Name)
		return view, nil
	}
	for i := range narrowed.Resources {
		if sameGitopsResource(narrowed.Resources[i], target) {
			entry := narrowed.Resources[i]
			view.Entry = &entry
			break
		}
	}
	narrowed.finishResources()
	view.Record = &narrowed
	if view.Entry == nil {
		switch tool {
		case gitopsFluxHelmRelease:
			view.Reason = "helm-controller does not report the state of the objects a release made"
		default:
			view.Reason = "the record does not list this object"
		}
	}
	return view, nil
}

// findGitopsRecord reads the one record named by the object's tracking
// metadata. A namespace the metadata names is read directly; Argo CD's default
// namespace is tried next; only then is every record listed and matched by
// name, preferring one that lists the object — two Applications of one name in
// different namespaces is legal under apps-in-any-namespace.
func findGitopsRecord(read trafficReader, toolName string, managed *managedByView,
	target gitopsResource,
) (gitopsRecord, bool, string, error) {
	tool := gitopsToolByName(toolName)
	notHere := gitopsNotHere(toolName, managed.Name)

	if !alarmObjectName.MatchString(managed.Name) ||
		(managed.Namespace != "" && !validNamespaceName(managed.Namespace)) {
		return gitopsRecord{}, false, "the tracking metadata on this object does not name a valid " +
			gitopsKindLabel(toolName), nil
	}

	namespaces := []string{}
	if managed.Namespace != "" {
		namespaces = append(namespaces, managed.Namespace)
	} else if toolName == gitopsArgoCD {
		namespaces = append(namespaces, argoDefaultNamespace)
	}
	for _, namespace := range namespaces {
		for _, collection := range tool.paths {
			code, body, err := read.get(namedRecordPath(collection, namespace, managed.Name))
			if err != nil {
				return gitopsRecord{}, false, "", err
			}
			if code == http.StatusNotFound {
				continue
			}
			if code < 200 || code >= 300 {
				// Read as KubeMG, so the cluster's own words describe KubeMG's
				// access rather than the caller's; they are not handed on.
				return gitopsRecord{}, false, "the " + gitopsKindLabel(toolName) +
					" tracking this object could not be read", nil
			}
			if record, ok := tool.decode(body); ok {
				return record, true, "", nil
			}
			return gitopsRecord{}, false, "the cluster returned an unreadable " + gitopsKindLabel(toolName), nil
		}
	}
	if managed.Namespace != "" {
		return gitopsRecord{}, false, notHere, nil
	}

	records, status, _, err := readGitopsTool(read, tool)
	if err != nil {
		return gitopsRecord{}, false, "", err
	}
	if !status.Available {
		if !status.Installed {
			return gitopsRecord{}, false, notHere, nil
		}
		return gitopsRecord{}, false, "the " + gitopsKindLabel(toolName) +
			" tracking this object could not be read", nil
	}
	var match *gitopsRecord
	for i := range records {
		if records[i].Name != managed.Name {
			continue
		}
		if slices.ContainsFunc(records[i].Resources, func(r gitopsResource) bool {
			return sameGitopsResource(r, target)
		}) {
			return records[i], true, "", nil
		}
		if match == nil {
			match = &records[i]
		}
	}
	if match != nil {
		return *match, true, "", nil
	}
	return gitopsRecord{}, false, notHere, nil
}

// gitopsNotHere is the one answer for a record the caller cannot have: absent
// from this cluster, or present and outside their grant.
func gitopsNotHere(tool, name string) string {
	reason := "no " + gitopsKindLabel(tool) + " named " + name + " in this cluster"
	if tool == gitopsArgoCD {
		reason += " — Argo CD may run in another cluster and deploy here"
	}
	return reason
}

func gitopsToolByName(name string) gitopsTool {
	for _, tool := range gitopsTools {
		if tool.name == name {
			return tool
		}
	}
	return gitopsTool{}
}

func gitopsKindLabel(tool string) string {
	switch tool {
	case gitopsFluxKustomize:
		return "Kustomization"
	case gitopsFluxHelmRelease:
		return "HelmRelease"
	}
	return "Application"
}

// namedRecordPath turns a collection path (`/apis/G/V/plural`) into one named
// object's path. Both names were validated by the caller.
func namedRecordPath(collection, namespace, name string) string {
	prefix, plural, _ := cutLast(collection, "/")
	return prefix + "/namespaces/" + url.PathEscape(namespace) + "/" + plural + "/" + url.PathEscape(name)
}

func cutLast(s, sep string) (string, string, bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}

func apiGroupOf(apiVersion string) string {
	group, _, found := strings.Cut(apiVersion, "/")
	if !found {
		return ""
	}
	return group
}

func sameGitopsResource(a, b gitopsResource) bool {
	return a.Group == b.Group && a.Kind == b.Kind && a.Namespace == b.Namespace && a.Name == b.Name
}
