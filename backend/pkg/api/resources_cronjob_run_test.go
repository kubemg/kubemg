package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

/*
 * Firing a CronJob now.
 *
 * What is pinned here is everything the route decides before the tunnel sees
 * it: that the Job is the CronJob's own jobTemplate rather than anything off
 * the wire, that it is deliberately unowned, that the name is generated, and
 * that a CronJob which is not the shape it has to be is refused rather than
 * turned into a Job that runs something else.
 */

func TestJobFromCronJobUsesTheStoredTemplate(t *testing.T) {
	cronjob := map[string]any{
		"metadata": map[string]any{"name": "nightly-report", "namespace": "shop"},
		"spec": map[string]any{
			"schedule": "0 2 * * *",
			"jobTemplate": map[string]any{
				"metadata": map[string]any{
					"labels":      map[string]any{"app": "report"},
					"annotations": map[string]any{"owner": "data"},
				},
				"spec": map[string]any{"backoffLimit": float64(3)},
			},
		},
	}

	job, reason := jobFromCronJob(cronjob, "nightly-report")
	if reason != "" {
		t.Fatalf("jobFromCronJob refused a well-formed CronJob: %s", reason)
	}
	if job["apiVersion"] != "batch/v1" || job["kind"] != "Job" {
		t.Fatalf("job = %v, want a batch/v1 Job", job)
	}

	// The spec is the template's, carried across rather than rebuilt: a manual
	// run has to run what the schedule runs.
	spec, _ := job["spec"].(map[string]any)
	if spec == nil || spec["backoffLimit"] != float64(3) {
		t.Fatalf("spec = %v, want the jobTemplate's own spec", spec)
	}

	metadata, _ := job["metadata"].(map[string]any)
	labels, _ := metadata["labels"].(map[string]any)
	if labels["app"] != "report" {
		t.Fatalf("labels = %v, want the template's labels carried across", labels)
	}
	annotations, _ := metadata["annotations"].(map[string]any)
	if annotations["owner"] != "data" {
		t.Fatalf("annotations = %v, want the template's annotations kept", annotations)
	}
	if annotations[cronJobInstantiateAnnotation] != "manual" {
		t.Fatalf("annotations = %v, want the manual-instantiate marker kubectl writes", annotations)
	}

	// The name is the cluster's to generate, and nothing names it outright.
	if metadata["name"] != nil {
		t.Fatalf("metadata = %v, want no fixed name", metadata)
	}
	if got := metadata["generateName"]; got != "nightly-report"+manualJobSuffix {
		t.Fatalf("generateName = %v, want it derived from the CronJob's name", got)
	}

	// The whole point of the item: an owned Job is reaped by the CronJob's
	// history limits, and a run somebody triggered by hand must not be.
	if _, owned := metadata["ownerReferences"]; owned {
		t.Fatal("a manually started Job must not be owned by the CronJob")
	}
}

func TestJobFromCronJobWorksWithoutTemplateMetadata(t *testing.T) {
	cronjob := map[string]any{
		"spec": map[string]any{
			"jobTemplate": map[string]any{"spec": map[string]any{}},
		},
	}

	job, reason := jobFromCronJob(cronjob, "cleanup")
	if reason != "" {
		t.Fatalf("jobFromCronJob refused a template with no metadata: %s", reason)
	}
	metadata, _ := job["metadata"].(map[string]any)
	annotations, _ := metadata["annotations"].(map[string]any)
	if annotations[cronJobInstantiateAnnotation] != "manual" {
		t.Fatalf("annotations = %v, want the marker created alongside", annotations)
	}
	if _, labelled := metadata["labels"]; labelled {
		t.Fatalf("metadata = %v, want no labels invented", metadata)
	}
}

func TestJobFromCronJobRefusesWhatItCannotRun(t *testing.T) {
	cases := []struct {
		name    string
		cronjob map[string]any
	}{
		{"no spec", map[string]any{"metadata": map[string]any{"name": "x"}}},
		{"no jobTemplate", map[string]any{"spec": map[string]any{"schedule": "* * * * *"}}},
		{"no template spec", map[string]any{
			"spec": map[string]any{"jobTemplate": map[string]any{"metadata": map[string]any{}}},
		}},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if job, reason := jobFromCronJob(test.cronjob, "x"); reason == "" {
				t.Fatalf("jobFromCronJob built %v from a CronJob it cannot run", job)
			}
		})
	}
}

func TestManualJobPrefixStaysANameTheAPIServerTakes(t *testing.T) {
	long := strings.Repeat("a", 80)
	prefix := manualJobPrefix(long)
	// The API server appends five characters to a generateName and caps the
	// whole thing at 63, so the prefix has to leave room for both.
	if len(prefix) > 63-5 {
		t.Fatalf("prefix = %q (%d chars), want room for the generated suffix", prefix, len(prefix))
	}

	// A cut landing on a separator would make `x--manual-`, which is not a
	// name at all.
	if got := manualJobPrefix(strings.Repeat("b", manualJobNamePrefixLimit) + "-tail"); strings.Contains(got, "--") {
		t.Fatalf("prefix = %q, want no doubled separator", got)
	}
	if got := manualJobPrefix("-----"); got != "job"+manualJobSuffix {
		t.Fatalf("prefix = %q, want a usable fallback when nothing is left", got)
	}
}

func TestCreatedJobNameFallsBackRatherThanInventing(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"metadata": map[string]any{"name": "nightly-manual-x7k2p"}})
	if got := createdJobName(body); got != "nightly-manual-x7k2p" {
		t.Fatalf("createdJobName = %q, want the name the cluster generated", got)
	}
	// The Job exists — the API server said so — so an unreadable body is not a
	// failure and must not produce a name nobody can find.
	if got := createdJobName([]byte("not json")); got != "the job" {
		t.Fatalf("createdJobName = %q, want a fallback rather than an invented name", got)
	}
}

/* ------------------------------------------------------------ the route --- */

func TestRunCronJobRefusesANamespaceOutsideTheGrant(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "edit", []string{"team-a"})
	token := env.tokenFor(t, user)

	rec := env.do(t, http.MethodPost,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/cronjob/run", token,
		map[string]any{"name": "nightly", "namespace": "team-b"})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestRunCronJobNeedsAName(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodPost,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/cronjob/run", token,
		map[string]any{"namespace": "shop"})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}
