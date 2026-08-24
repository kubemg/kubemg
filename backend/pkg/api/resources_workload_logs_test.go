package api

import "testing"

// A workload's logs are its pods' logs, and which pods those are comes down
// entirely to this one string. What is pinned here is that it never widens: a
// selector that cannot be encoded faithfully has to be refused, because the
// alternative — dropping a term — returns logs from pods the workload does not
// own, and an operator reading a rollout would have no way to tell.

func TestEncodeLabelSelectorRendersMatchLabelsSorted(t *testing.T) {
	got, err := encodeLabelSelector(labelSelector{
		MatchLabels: map[string]string{"app": "api", "app.kubernetes.io/instance": "prod"},
	})
	if err != nil {
		t.Fatalf("expected matchLabels to encode: %v", err)
	}
	// Sorted, so the same selector is one query in the audit trail rather than
	// two spellings of one.
	if want := "app=api,app.kubernetes.io/instance=prod"; got != want {
		t.Fatalf("selector = %q, want %q", got, want)
	}
}

func TestEncodeLabelSelectorRendersEveryOperator(t *testing.T) {
	selector := labelSelector{MatchLabels: map[string]string{"app": "api"}}
	selector.MatchExpressions = []struct {
		Key      string   `json:"key"`
		Operator string   `json:"operator"`
		Values   []string `json:"values"`
	}{
		{Key: "tier", Operator: "In", Values: []string{"web", "worker"}},
		{Key: "canary", Operator: "NotIn", Values: []string{"true"}},
		{Key: "track", Operator: "Exists"},
		{Key: "retired", Operator: "DoesNotExist"},
	}

	got, err := encodeLabelSelector(selector)
	if err != nil {
		t.Fatalf("expected matchExpressions to encode: %v", err)
	}
	want := "app=api,tier in (web,worker),canary notin (true),track,!retired"
	if got != want {
		t.Fatalf("selector = %q, want %q", got, want)
	}
}

func TestEncodeLabelSelectorRefusesAnythingItCannotRepresent(t *testing.T) {
	type expression = struct {
		Key      string   `json:"key"`
		Operator string   `json:"operator"`
		Values   []string `json:"values"`
	}

	cases := []struct {
		name     string
		selector labelSelector
	}{
		// An empty selector matches every pod in the namespace. Those are not the
		// workload's logs, so it is a refusal rather than a wide read.
		{"empty", labelSelector{}},
		{"empty match labels", labelSelector{MatchLabels: map[string]string{}}},
		{
			"unknown operator",
			labelSelector{MatchExpressions: []expression{{Key: "tier", Operator: "Matches"}}},
		},
		{
			"set operator with no set",
			labelSelector{MatchExpressions: []expression{{Key: "tier", Operator: "In"}}},
		},
		// Not valid Kubernetes to begin with; refused here so a hand-written or
		// migrated object cannot smuggle a second term past the encoder.
		{"comma in a value", labelSelector{MatchLabels: map[string]string{"app": "api,other"}}},
		{"equals in a key", labelSelector{MatchLabels: map[string]string{"app=x": "api"}}},
		{
			"parenthesis in a set value",
			labelSelector{MatchExpressions: []expression{
				{Key: "tier", Operator: "In", Values: []string{"web)"}},
			}},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got, err := encodeLabelSelector(test.selector); err == nil {
				t.Fatalf("expected a refusal, got selector %q", got)
			}
		})
	}
}

// A CronJob has no selector of its own — it owns Jobs, and a Job's pods are
// found through the Job's UID — so the one thing standing between an operator
// and somebody else's pods is this match staying exact.

func TestJobsOwnedByCronJobMatchesOnKindAndUID(t *testing.T) {
	job := func(uid, ownerKind, ownerUID string) ownedJob {
		var j ownedJob
		j.Metadata.UID = uid
		if ownerKind != "" {
			j.Metadata.OwnerReferences = []struct {
				Kind string `json:"kind"`
				UID  string `json:"uid"`
			}{{Kind: ownerKind, UID: ownerUID}}
		}
		return j
	}

	jobs := []ownedJob{
		job("job-mine-1", "CronJob", "cron-uid"),
		job("job-mine-2", "CronJob", "cron-uid"),
		// Same UID string, wrong owner kind — a Job owned directly (not by a
		// CronJob) must never match just because some other object shares a UID.
		job("job-other-kind", "Job", "cron-uid"),
		// Right kind, different CronJob.
		job("job-other-cron", "CronJob", "some-other-uid"),
		// No owner at all — a hand-created Job.
		job("job-orphan", "", ""),
	}

	got := jobsOwnedByCronJob("cron-uid", jobs)
	want := []string{"job-mine-1", "job-mine-2"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("jobsOwnedByCronJob = %v, want %v", got, want)
	}
}

func TestJobsOwnedByCronJobFindsNoneForAnUnrelatedList(t *testing.T) {
	jobs := []ownedJob{}
	if got := jobsOwnedByCronJob("cron-uid", jobs); len(got) != 0 {
		t.Fatalf("expected no owned jobs, got %v", got)
	}
}

func TestWorkloadPodKindsCoverTheWorkloadsThatOwnPods(t *testing.T) {
	for _, key := range []string{"deployments", "statefulsets", "daemonsets", "jobs"} {
		if _, ok := workloadPodKinds[key]; !ok {
			t.Fatalf("%s owns pods but cannot be resolved to them", key)
		}
	}
	// A Service has a selector too, and it points at pods it does not own. Only
	// controllers belong in this table.
	for _, key := range []string{"services", "configmaps", "secrets", "pods"} {
		if _, ok := workloadPodKinds[key]; ok {
			t.Fatalf("%s is not a pod-owning workload", key)
		}
	}
}
