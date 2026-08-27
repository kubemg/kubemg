package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

/*
 * Firing a CronJob now.
 *
 * A CronJob's only control until now was `suspend`, and the console could say to
 * the second when the next run lands — which is the wrong half of what somebody
 * woken at 2am wants. The thing they want is to run it *now* and watch it, and
 * that has meant leaving for a terminal and `kubectl create job --from=cronjob`.
 *
 * It is a **create with no new reach**: the Job is built server-side out of the
 * CronJob's own `spec.jobTemplate` and posted to the Jobs collection down the
 * same impersonated tunnel every other write uses, so the namespace check, the
 * guardrails, the cluster's own RBAC and the `create` audit record are all the
 * ones that were already there. Nothing about the Job's shape comes off the
 * wire: the caller names a CronJob, and the cluster's own copy of it is what
 * gets read and turned into a Job.
 *
 * Two decisions are worth stating because both are easy to get backwards.
 *
 * The Job is deliberately **not owned by the CronJob** — no `ownerReferences`.
 * kubectl does the same, and the reason is `successfulJobsHistoryLimit`: an
 * owned Job counts against the CronJob's history and is reaped out from under
 * whoever triggered it, which is precisely the run nobody wants garbage
 * collected. It carries the `cronjob.kubernetes.io/instantiate: manual`
 * annotation instead, which is how kubectl marks a hand-started run and how an
 * operator reading the Job later can tell it apart from a scheduled one.
 *
 * The name comes from `generateName`, not from the caller. A manual run has no
 * natural name, the cluster is the only thing that can guarantee one is free,
 * and letting a caller name it would turn "run it now" into a second create
 * route with a name to collide.
 */

// cronJobInstantiateAnnotation marks a Job somebody started by hand. It is
// kubectl's own annotation and value; a Job created from here is meant to be
// indistinguishable from one `kubectl create job --from=cronjob/x` made.
const cronJobInstantiateAnnotation = "cronjob.kubernetes.io/instantiate"

// manualJobNamePrefixLimit bounds the base a generated name is built from. A
// Job name is a DNS subdomain the API server caps at 63 characters and it
// appends five of its own to a `generateName`, so a long CronJob name has to be
// cut somewhere — better here, deterministically, than by a 422 nobody can read.
const manualJobNamePrefixLimit = 45

// manualJobSuffix separates the CronJob's name from what the cluster generates,
// and says which kind of run this was in the one place an operator always sees:
// the Job's name in a list.
const manualJobSuffix = "-manual-"

// cronJobRunRequest names the CronJob to fire. There is nothing else to say: a
// manual run is the CronJob's own jobTemplate, unmodified.
type cronJobRunRequest struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// cronJobRunResult is what came back — the Job the cluster named, so the UI can
// take the operator straight to it rather than making them find it in a list.
type cronJobRunResult struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	CronJob   string `json:"cronjob"`
	Message   string `json:"message"`
}

// runCronJob creates a Job from a CronJob's template, now.
func (s *server) runCronJob(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxManifestBody)
	var req cronJobRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the request could not be read"})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a CronJob name is required"})
		return
	}
	namespace, ok := s.scopedNamespace(c, grant, req.Namespace)
	if !ok {
		return
	}

	cronJobs := resourceListPath{"/apis/batch/v1", "cronjobs"}
	resp, callOK := s.callResource(c, user, cluster, grant,
		cronJobs.namespaced(namespace)+"/"+url.PathEscape(req.Name))
	if !callOK {
		return
	}
	var cronjob map[string]any
	if !s.decodeResource(c, resp, &cronjob) {
		return
	}

	job, reason := jobFromCronJob(cronjob, req.Name)
	if reason != "" {
		c.JSON(http.StatusConflict, gin.H{"error": reason})
		return
	}
	document, err := json.Marshal(job)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the job could not be encoded"})
		return
	}

	jobs := resourceListPath{"/apis/batch/v1", "jobs"}
	resp, callOK = s.callResourceWith(c, user, cluster, grant,
		http.MethodPost, jobs.namespaced(namespace), document, "could not write to the cluster")
	if !callOK {
		return
	}
	if resp.Status < 200 || resp.Status >= 300 {
		c.JSON(resp.Status, gin.H{"error": kubeErrorMessage(resp.Body, resp.Status)})
		return
	}

	name := createdJobName(resp.Body)
	c.JSON(http.StatusCreated, cronJobRunResult{
		Name:      name,
		Namespace: namespace,
		CronJob:   req.Name,
		Message:   name + " started from " + req.Name,
	})
}

// jobFromCronJob builds the Job a manual run posts.
//
// It refuses rather than guessing where the CronJob is not the shape it has to
// be: a jobTemplate is the only thing here that says what to run, and inventing
// one would produce a Job that runs something other than the schedule does.
func jobFromCronJob(cronjob map[string]any, name string) (map[string]any, string) {
	spec, _ := cronjob["spec"].(map[string]any)
	if spec == nil {
		return nil, "the cluster returned a CronJob with no spec"
	}
	template, _ := spec["jobTemplate"].(map[string]any)
	if template == nil {
		return nil, "the cluster returned a CronJob with no jobTemplate, so there is nothing to run"
	}
	jobSpec, _ := template["spec"].(map[string]any)
	if jobSpec == nil {
		return nil, "this CronJob's jobTemplate declares no spec, so there is nothing to run"
	}

	// The template's own labels and annotations travel with the run: they are
	// what the CronJob would have stamped on a scheduled Job, and a manual run
	// that lost them would be selected by different things from every other run
	// of the same schedule.
	metadata := map[string]any{}
	if declared, ok := template["metadata"].(map[string]any); ok {
		if labels, ok := declared["labels"].(map[string]any); ok && len(labels) > 0 {
			metadata["labels"] = labels
		}
		if annotations, ok := declared["annotations"].(map[string]any); ok && len(annotations) > 0 {
			metadata["annotations"] = annotations
		}
	}
	annotations, _ := metadata["annotations"].(map[string]any)
	if annotations == nil {
		annotations = map[string]any{}
		metadata["annotations"] = annotations
	}
	annotations[cronJobInstantiateAnnotation] = "manual"
	metadata["generateName"] = manualJobPrefix(name)

	// No ownerReferences: see the file header. An owned Job is reaped by the
	// CronJob's history limits, and this is the one run that must not be.
	return map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata":   metadata,
		"spec":       jobSpec,
	}, ""
}

// manualJobPrefix renders the generateName a manual run is named from.
func manualJobPrefix(name string) string {
	if len(name) > manualJobNamePrefixLimit {
		name = name[:manualJobNamePrefixLimit]
	}
	// A cut can land on a separator, and `x--manual-` or `x.-manual-` is not a
	// name the API server will take.
	name = strings.TrimRight(name, "-.")
	if name == "" {
		return "job" + manualJobSuffix
	}
	return name + manualJobSuffix
}

// createdJobName reads back the name the cluster generated. A response that
// cannot be read is not a failure — the Job exists, the API server said so —
// so the fallback says what is true rather than inventing a name.
func createdJobName(body []byte) string {
	var created struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.Metadata.Name == "" {
		return "the job"
	}
	return created.Metadata.Name
}
