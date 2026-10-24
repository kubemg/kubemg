package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"sigs.k8s.io/yaml"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/objdiff"
	"github.com/kubemg/kubemg/backend/pkg/observability"
)

/*
 * Live against desired, for one object Argo CD tracks.
 *
 * The record says an object is out of sync; this says how. The desired state is
 * Argo CD's — its managed-resources answer, normalized the way its own diff view
 * shows it — so KubeMG renders nothing and holds no repository credential. The
 * order of the steps is the security of the route, and each can end it:
 *
 *  1. the grant decides the namespace (resourceObjectTarget);
 *  2. a Secret is refused by kind before anything is read — Argo CD masks
 *     Secret data, but a Secret's values leave KubeMG only through the audited
 *     reveal route, never on the assumption that somebody else masked them;
 *  3. the live object is read as the caller, so the cluster decides whether
 *     they may see it, and a refusal ends the request before Argo CD is asked;
 *  4. the Application is the one the object's own tracking metadata names,
 *     resolved and narrowed exactly as the per-object panel does — never one
 *     the caller names;
 *  5. only then is Argo CD read, for that one object.
 */

// maxDiffSide bounds each rendered side. A diff is read by a person; past this
// the YAML is cut at a line and said to be.
const maxDiffSide = 256 << 10

// gitopsDiffApp names the Application the desired state came from.
type gitopsDiffApp struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// gitopsDiffView is the route's answer. Available false carries a reason and
// nothing else: the sync state the panel already shows does not depend on it.
type gitopsDiffView struct {
	Available   bool           `json:"available"`
	Reason      string         `json:"reason,omitempty"`
	Application *gitopsDiffApp `json:"application,omitempty"`
	// Modified is Argo CD's own verdict on this object.
	Modified bool `json:"modified"`
	// Diff is live → desired: what a sync would change, path by path, in the
	// same shape and from the same function as the editor's pre-write diff.
	Diff             *objdiff.Result `json:"diff,omitempty"`
	Live             string          `json:"live"`
	Desired          string          `json:"desired"`
	LiveTruncated    bool            `json:"live_truncated"`
	DesiredTruncated bool            `json:"desired_truncated"`
}

// gitopsDiffOffer tells the per-object panel whether a diff can be asked for,
// so the button is absent rather than a request that fails.
type gitopsDiffOffer struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

const (
	diffSecretRefusal = "a Secret's desired state is not shown: its values leave KubeMG only through the audited reveal"
	diffNoSource      = "no Argo CD datasource is registered for this cluster; an administrator can add one " +
		"under the cluster's datasources to compare live and desired state"
	diffSourceOff = "this cluster's Argo CD datasource is turned off"
)

// isSecretKind is the refusal in step 2, on the inventory key before a read and
// on the object itself after one.
func isSecretKind(key string) bool { return key == "secrets" }

// gitopsDiffSteps are the reads the route makes, as values so the order they
// run in is what a test pins.
type gitopsDiffSteps struct {
	// readLive reads the object as the caller. False means it has answered the
	// request itself, in the cluster's words.
	readLive func() ([]byte, bool)
	// service reads Application records as KubeMG's service identity.
	service trafficReader
	source  func() (*db.ObservabilitySource, error)
	argo    func(db.ObservabilitySource, observability.ArgoObject) (observability.ArgoState, error)
}

// run walks the steps. A zero status means readLive already answered.
func (steps gitopsDiffSteps) run(kindKey string, grant db.UserClusterAccess) (int, any, error) {
	if isSecretKind(kindKey) {
		return http.StatusConflict, gin.H{"error": diffSecretRefusal}, nil
	}

	body, ok := steps.readLive()
	if !ok {
		return 0, nil, nil
	}
	var object gitopsObjectMeta
	if err := json.Unmarshal(body, &object); err != nil {
		return http.StatusBadGateway, gin.H{"error": "the cluster returned an unreadable object"}, nil
	}
	group := apiGroupOf(object.APIVersion)
	if group == "" && object.Kind == "Secret" {
		return http.StatusConflict, gin.H{"error": diffSecretRefusal}, nil
	}

	view, err := resolveGitopsObject(steps.service, object, grant)
	if err != nil {
		return 0, nil, err
	}
	if view.ManagedBy == nil || view.ManagedBy.Manager != managerArgoCD {
		return http.StatusConflict, gin.H{
			"error": "only an object Argo CD tracks has a desired state KubeMG can read",
		}, nil
	}
	if view.Record == nil {
		return http.StatusConflict, gin.H{"error": view.Reason}, nil
	}

	offer := gitopsDiffOfferFor(steps.source)
	if !offer.Available {
		return http.StatusOK, gitopsDiffView{Reason: offer.Reason}, nil
	}
	source, _ := steps.source()

	app := &gitopsDiffApp{Namespace: view.Record.Namespace, Name: view.Record.Name}
	state, err := steps.argo(*source, observability.ArgoObject{
		App:          app.Name,
		AppNamespace: app.Namespace,
		Group:        group,
		Kind:         object.Kind,
		Namespace:    object.Metadata.Namespace,
		Name:         object.Metadata.Name,
	})
	if err != nil {
		return http.StatusOK, gitopsDiffView{Application: app, Reason: argoReason(err)}, nil
	}
	return http.StatusOK, renderGitopsDiff(app, state), nil
}

// gitopsDiffOfferFor answers whether a source exists to ask. It does not dial
// Argo CD: the panel asks it on every open, and a stored, enabled source is
// what an administrator promised. A failed store read says so rather than
// reading as "none registered".
func gitopsDiffOfferFor(lookup func() (*db.ObservabilitySource, error)) gitopsDiffOffer {
	source, err := lookup()
	switch {
	case errors.Is(err, db.ErrNotFound):
		return gitopsDiffOffer{Reason: diffNoSource}
	case err != nil:
		return gitopsDiffOffer{Reason: "the cluster's Argo CD datasource could not be loaded"}
	case !source.Enabled:
		return gitopsDiffOffer{Reason: diffSourceOff}
	}
	return gitopsDiffOffer{Available: true}
}

func argoReason(err error) string {
	var argoErr *observability.ArgoError
	switch {
	case errors.As(err, &argoErr):
		return argoErr.Message
	case errors.Is(err, observability.ErrArgoNoResource):
		return err.Error()
	}
	return "Argo CD could not be reached: " + err.Error()
}

// renderGitopsDiff strips both sides of what is the cluster's rather than the
// object's, diffs them and renders each side as YAML.
func renderGitopsDiff(app *gitopsDiffApp, state observability.ArgoState) gitopsDiffView {
	view := gitopsDiffView{Available: true, Application: app, Modified: state.Modified}
	live := strippedState(state.NormalizedLive)
	desired := strippedState(state.PredictedLive)

	diff := objdiff.Diff(live, desired)
	if diff.Changes == nil {
		diff.Changes = []objdiff.Change{}
	}
	view.Diff = &diff

	view.Live, view.LiveTruncated = diffSideYAML(live)
	view.Desired, view.DesiredTruncated = diffSideYAML(desired)
	if live == nil && desired == nil {
		view.Available = false
		view.Reason = "Argo CD holds no state for this object yet"
	}
	return view
}

// strippedState decodes one side and removes what the cluster writes: status,
// the server-set metadata, and the last-applied copy of the desired state that
// would otherwise show the same difference twice. Argo CD writes `null` for a
// side it has none of.
func strippedState(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		return nil
	}
	delete(object, "status")
	if metadata, ok := object["metadata"].(map[string]any); ok {
		for _, key := range []string{"managedFields", "resourceVersion", "generation", "uid",
			"creationTimestamp", "selfLink"} {
			delete(metadata, key)
		}
		if annotations, ok := metadata["annotations"].(map[string]any); ok {
			delete(annotations, lastAppliedAnnotation)
			if len(annotations) == 0 {
				delete(metadata, "annotations")
			}
		}
	}
	return object
}

// diffSideYAML renders one side, cut at a line once past maxDiffSide.
func diffSideYAML(object map[string]any) (string, bool) {
	if object == nil {
		return "", false
	}
	out, err := yaml.Marshal(object)
	if err != nil {
		return "", false
	}
	if len(out) <= maxDiffSide {
		return string(out), false
	}
	cut := out[:maxDiffSide]
	if at := strings.LastIndexByte(string(cut), '\n'); at > 0 {
		cut = cut[:at+1]
	}
	return string(cut), true
}

// showGitopsDiff is GET .../gitops/diff.
func (s *server) showGitopsDiff(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	kind, name, namespace, ok := s.resourceObjectTarget(c, grant)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	steps := gitopsDiffSteps{
		readLive: func() ([]byte, bool) {
			return s.readObject(c, user, cluster, grant, kind, namespace, name)
		},
		service: gitopsServiceReader{s: s, c: c, cluster: cluster},
		source:  s.gitopsSourceLookup(ctx, cluster),
		argo: func(source db.ObservabilitySource, object observability.ArgoObject) (observability.ArgoState, error) {
			return observability.ArgoManagedResource(ctx, observability.TargetOf(source),
				s.gitopsTunnel(cluster), object)
		},
	}
	status, body, err := steps.run(strings.TrimSpace(c.Query("kind")), grant)
	if err != nil {
		writeGitopsReadError(c, err)
		return
	}
	if status != 0 {
		c.JSON(status, body)
	}
}

// gitopsSourceLookup reads the cluster's Argo CD datasource once per request,
// however many times a step asks.
func (s *server) gitopsSourceLookup(ctx context.Context, cluster *db.Cluster) func() (*db.ObservabilitySource, error) {
	var (
		source *db.ObservabilitySource
		err    error
		done   bool
	)
	return func() (*db.ObservabilitySource, error) {
		if !done {
			source, err = s.store.ObservabilitySource(ctx, cluster.ID, db.SourceGitOps)
			done = true
		}
		return source, err
	}
}

// gitopsTunnel reaches an in-cluster Argo CD as the GitOps service identity,
// the same one the records are read as. Nil on a direct-mode cluster, where
// only a direct source can work and the probe has said so on save.
func (s *server) gitopsTunnel(cluster *db.Cluster) observability.TunnelCall {
	return s.tunnelCall(&db.User{Username: gitopsReaderUser}, cluster)
}
