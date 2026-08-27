package api

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/helm"
)

/*
 * Removing a Helm release.
 *
 * The console could read a release, edit its values, see its history and roll it
 * back, and could not remove it — so the one lifecycle verb Explore has for
 * every other object was the one it lacked for the object that produced half of
 * them.
 *
 * It is more than deleting the Secret, and that is the whole of the design. The
 * release's own record carries the **rendered manifest** of everything it
 * installed. That manifest is already decoded server-side for the upgrade and
 * rollback paths and is deliberately never returned to a client — charts put
 * generated passwords in there — so an uninstall is that same manifest parsed
 * here, and its objects deleted one at a time through the same impersonated
 * tunnel. Each is its own call, its own RBAC answer and its own audit record:
 * the selection-delete rule, applied to a set the release named rather than one
 * an operator ticked.
 *
 * Order and failure are the two things worth being exact about.
 *
 * Objects go in **reverse** of the order they were written, so a dependant is
 * removed before what it depends on. The release's own Secrets go **last**, and
 * only if every object went: a partial failure then leaves a release that still
 * exists and still describes what is there, which is a state an operator can
 * retry or finish by hand. Deleting the Secrets first — or regardless — would
 * leave a set of objects nothing accounts for, and no way to find out what they
 * were.
 *
 * Two honest limits, stated on the surface itself for the same reason the
 * values write states its own:
 *
 *   - **`pre-delete` and `post-delete` hooks do not run.** They are chart
 *     templates, and KubeMG has no chart to render them from at this point —
 *     the release records its rendered manifest, not the templates behind it.
 *   - **An object created outside the manifest stays.** A PVC a StatefulSet
 *     expanded, anything a controller made, anything from the chart's `crds/`
 *     directory (which Helm does not record on the release and `helm uninstall`
 *     leaves behind too) is not in the manifest and is not removed.
 *
 * `--keep-history` is deliberately not a mode here: a release with no objects
 * and a history is a row nobody can act on.
 */

// helmUninstallHookNotice is carried on every uninstall response. It is not
// conditional on the release declaring hooks, because the release's recorded
// hooks are the *install* ones — whether a chart has a `pre-delete` is a fact
// about templates this path never sees, so the honest statement is that they
// were not run at all.
const helmUninstallHookNotice = "Chart delete hooks (pre-delete, post-delete) were not run: a release " +
	"records its rendered manifest, not the templates behind it, so KubeMG has nothing to render them " +
	"from. Objects the chart did not render — anything a controller created, and any CustomResourceDefinition " +
	"from the chart's crds/ directory — are not part of the recorded manifest and stay."

// uninstallHelmRelease removes a release and the objects it installed.
func (s *server) uninstallHelmRelease(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	namespace, name, ok := s.helmReleaseTarget(c, grant)
	if !ok {
		return
	}

	revisions, ok := s.helmRevisions(c, user, cluster, grant, namespace, name)
	if !ok {
		return
	}
	current, ok := parsedRelease(c, revisions[0])
	if !ok {
		return
	}

	objects, err := helm.ManifestObjects(current.Manifest, namespace)
	if err != nil || len(objects) == 0 {
		// Refused rather than reduced to "delete the Secrets". A release whose
		// manifest KubeMG cannot read is one whose objects it cannot name, and
		// removing the record while leaving them running is the worst of the
		// available outcomes.
		c.JSON(http.StatusConflict, gin.H{
			"error": fmt.Sprintf("revision %d of %s records no readable manifest, so kubemg cannot "+
				"tell what it installed. Remove it with `helm uninstall`, which reads the same record.",
				revisions[0].revision, name),
		})
		return
	}

	// Reverse of the order they were written: a dependant goes before what it
	// depends on, which is the same rule `removalsOf` applies on an upgrade.
	slices.Reverse(objects)

	if !s.planUninstall(c, grant, objects) {
		return
	}
	discovery, ok := s.discoverCluster(c, user, cluster, grant)
	if !ok {
		return
	}

	reports := make([]objectReport, 0, len(objects)+len(revisions))
	complete := true
	for _, object := range objects {
		// removeObject is the same delete the upgrade path uses for an object a
		// revision dropped: Background propagation, and a 404 read as the
		// desired state reached by somebody else rather than as a failure.
		report := s.removeObject(c, user, cluster, grant, discovery, object)
		if report.Action != actionDeleted {
			complete = false
		}
		reports = append(reports, report)
	}

	if complete {
		reports = append(reports,
			s.removeHelmRevisions(c, user, cluster, grant, namespace, revisions, &complete)...)
	} else {
		for _, revision := range revisions {
			reports = append(reports, objectReport{
				Kind: "Secret", Name: helmSecretName(revision.secret), Namespace: namespace,
				Action: actionSkipped,
				Message: "kept — the release still records what it installed, and something " +
					"above could not be removed",
			})
		}
	}

	body := gin.H{
		"release":     name,
		"namespace":   namespace,
		"objects":     reports,
		"removed":     complete,
		"hook_notice": helmUninstallHookNotice,
	}
	if complete {
		body["message"] = name + " uninstalled — its objects are marked for deletion"
	} else {
		body["message"] = name + " was not fully removed, so its release record was kept"
		body["error"] = uninstallFailureMessage(reports)
	}
	c.JSON(http.StatusOK, body)
}

// planUninstall applies the two refusals a scoped grant earns, before anything
// has been deleted.
//
// They are the ones `planApply` makes for an install, in this path's own words
// and for the same reason: a chart is not a manifest the operator wrote, so
// being told up front — with the object named — is the difference between a
// message they can act on and a half-removed release. Unlike the install, an
// unresolvable Kind is *not* refused here: a CRD whose definition has already
// gone means the object has gone with it, which `removeObject` reports as
// skipped rather than treating as a reason to stop.
func (s *server) planUninstall(c *gin.Context, grant db.UserClusterAccess, objects []helm.Object) bool {
	allowed := grant.NamespaceList()
	if len(allowed) == 0 {
		return true
	}

	for _, object := range objects {
		// Namespace is read off the rendered object rather than from discovery,
		// which is what makes this pre-flight: an object with no namespace in a
		// manifest is one the chart installed cluster-wide.
		if object.Namespace == "" {
			c.JSON(http.StatusForbidden, gin.H{
				"error": fmt.Sprintf("this release installed %s, which is cluster-scoped, and your "+
					"access to this cluster is limited to %s", object.Ref(), strings.Join(allowed, ", ")),
			})
			return false
		}
		if !slices.Contains(allowed, object.Namespace) {
			c.JSON(http.StatusForbidden, gin.H{
				"error": fmt.Sprintf("this release installed %s into namespace %s, which is outside "+
					"your granted scope", object.Ref(), object.Namespace),
			})
			return false
		}
	}
	return true
}

// removeHelmRevisions deletes the Secrets the release is stored in — every
// revision, not just the current one, because a history left behind is a
// release `helm list` still finds.
func (s *server) removeHelmRevisions(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, namespace string, revisions []helmStoredRevision, complete *bool,
) []objectReport {
	reports := make([]objectReport, 0, len(revisions))
	for _, revision := range revisions {
		name := helmSecretName(revision.secret)
		report := objectReport{Kind: "Secret", Name: name, Namespace: namespace}
		if name == "" {
			report.Action, report.Message = actionSkipped, "this revision's secret has no name"
			*complete = false
			reports = append(reports, report)
			continue
		}

		where := secretNamespace(revision.secret)
		if where == "" {
			where = namespace
		}
		path := fmt.Sprintf("/api/v1/namespaces/%s/secrets/%s",
			url.PathEscape(where), url.PathEscape(name))

		resp, err := s.proxy.Call(c.Request.Context(), user, cluster, grant,
			http.MethodDelete, path, nil, nil)
		switch {
		case err != nil:
			report.Action, report.Message = actionFailed, callFailureMessage(err)
		case resp.Status == http.StatusNotFound:
			report.Action = actionDeleted
		case resp.Status < 200 || resp.Status >= 300:
			report.Action, report.Message = actionFailed, kubeErrorMessage(resp.Body, resp.Status)
		default:
			report.Action = actionDeleted
		}
		if report.Action != actionDeleted {
			*complete = false
		}
		reports = append(reports, report)
	}
	return reports
}

// helmSecretName reads a stored revision's Secret name.
func helmSecretName(secret map[string]any) string {
	metadata, _ := secret["metadata"].(map[string]any)
	name, _ := metadata["name"].(string)
	return name
}

// uninstallFailureMessage names what stopped, in the cluster's own words. Every
// object is attempted — unlike an install, where a failure stops the run,
// because the objects being removed do not depend on each other's success and
// stopping would leave more behind, not less.
func uninstallFailureMessage(reports []objectReport) string {
	for _, report := range reports {
		if report.Action == actionDeleted {
			continue
		}
		return fmt.Sprintf("%s/%s could not be removed: %s",
			report.Kind, report.Name, report.Message)
	}
	return "something could not be removed"
}
