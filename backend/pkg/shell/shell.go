/*
 * Package shell describes the browser shell's pod, and nothing else.
 *
 * A KubeMG shell is a pod KubeMG creates in the target cluster, holding a
 * terminal with `kubectl` on the path. The load-bearing rule for the
 * whole feature is in this package's shape rather than in its code: **the pod
 * holds no cluster credential of its own**. It runs with no mounted service
 * account token and under a service account with no bindings, so a shell that
 * escaped every other control could still not name a single object in the
 * cluster it is standing in. Everything it can do arrives later, as a kubeconfig
 * written into its home directory over an exec — a KubeMG proxy credential
 * scoped to the caller, which means every command it runs is impersonated as
 * that person, answered by the cluster's own RBAC, held to their namespace
 * scope, and in the audit trail exactly like a command typed anywhere else.
 *
 * A shell holding a cluster credential of its own would undo the access model in
 * one feature, which is why the pod is deliberately powerless and the credential
 * deliberately transient.
 *
 * This package is pure: it renders manifests and reads pod status. It never
 * calls a cluster — pkg/api owns the tunnel, the lifecycle and the routes.
 */
package shell

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DefaultImage is the shell image this build was cut against. It is KubeMG's
// own, pinned and minimal: busybox and kubectl on a distroless base, with no
// package manager in it — an operator's shell that can install software is a
// supply chain rather than a terminal. helm was in it and was removed; see the
// docblock in shell/Dockerfile for why.
const DefaultImage = "ghcr.io/kubemg/kubemg-shell:0.9.0"

// RunnerUser is the identity KubeMG impersonates to manage shell pods.
//
// It is a name rather than an account, the alarm watcher's rule: creating and
// deleting a shell pod is KubeMG acting on its own behalf, and attributing it to
// whoever happened to click would put a pod create in the trail under an
// operator whose grant may be read-only. Authorization comes from a Role bound
// to this *user* in the agent's own namespace (see the agent manifests), which
// is what confines it to one namespace and five verbs — the impersonated groups
// that ride along are the ordinary read-only ones every KubeMG call carries.
const RunnerUser = "kubemg:shell-runner"

// ServiceAccount is the account the pod runs as: it exists so the pod does not
// inherit `default`, which on many clusters has bindings nobody remembers
// adding. It is granted nothing, and its token is not mounted either — belt and
// braces, because the two failures are independent.
const ServiceAccount = "kubemg-shell"

// Label and annotation keys. The component label is what the reaper lists by, so
// it is the one thing a shell pod must always carry.
const (
	LabelName      = "app.kubernetes.io/name"
	LabelPartOf    = "app.kubernetes.io/part-of"
	LabelManagedBy = "app.kubernetes.io/managed-by"
	LabelComponent = "app.kubernetes.io/component"
	// LabelUserID is the numeric KubeMG user id. It is a label rather than an
	// annotation because it is a selector: "is there already a shell for this
	// person" is a list, not a read of every pod's annotations.
	LabelUserID = "kubemg.io/shell-user-id"

	// ComponentValue names a shell pod among KubeMG's other objects.
	ComponentValue = "shell"

	// AnnotationUsername is who the shell belongs to, in the form a person reads.
	// The label above cannot hold it: a username is not a valid label value once
	// it comes from an identity provider and contains an @.
	AnnotationUsername = "kubemg.io/shell-username"
	// AnnotationLastActivity is when this shell last carried a keystroke, RFC 3339.
	//
	// It lives on the pod rather than in KubeMG's database on purpose. The pod is
	// the state — it is what a reaper has to find and what an operator sees with
	// kubectl — and putting the clock anywhere else means two records that can
	// disagree, with the wrong one deleting somebody's session. It also makes the
	// reaper stateless across replicas: any KubeMG can read it.
	AnnotationLastActivity = "kubemg.io/shell-last-activity"
	// AnnotationCredentialExpires is when the kubeconfig written into the pod runs
	// out. Its presence is also what says the pod has been seeded at all, which is
	// how a second create avoids minting a second credential for a shell that
	// already holds a working one.
	AnnotationCredentialExpires = "kubemg.io/shell-credential-expires"
	// AnnotationCredentialID is the register row the kubeconfig inside this pod
	// was written as. It is what lets ending a shell also *withdraw* what was in
	// it: the pod is gone either way, but the token it held would otherwise stay
	// valid for hours with no copy of it anywhere, which is a credential nobody
	// can account for.
	AnnotationCredentialID = "kubemg.io/shell-credential-id"
	// AnnotationExpiresAt is when the absolute deadline lands, so the console can
	// count down without re-deriving it from the pod's start time and a setting
	// that may have changed since.
	AnnotationExpiresAt = "kubemg.io/shell-expires-at"
)

// Lifetime bounds.
//
// Two clocks, because they answer different questions. The idle timeout is
// "nobody is using this", and it is the one that reclaims almost every shell.
// The maximum lifetime is "this has been open long enough", and it exists
// because an idle timer alone can be held open forever by a single command that
// keeps printing — `kubectl get pods -w` in a spare tab is not a session
// somebody is in, but it is never idle.
const (
	DefaultIdleTimeout = time.Hour
	MinIdleTimeout     = 5 * time.Minute
	MaxIdleTimeout     = 24 * time.Hour

	DefaultMaxLifetime = 8 * time.Hour
	MinMaxLifetime     = time.Hour
	MaxMaxLifetime     = 7 * 24 * time.Hour
)

// Resource bounds for the pod. These are deliberately modest: the shell runs a
// person typing, not a workload. The limits are what stop a runaway loop in
// somebody's terminal from being the target cluster's problem.
const (
	cpuRequest    = "50m"
	cpuLimit      = "500m"
	memoryRequest = "64Mi"
	memoryLimit   = "256Mi"
	// scratchLimit bounds both writable mounts. A shell needs somewhere to put a
	// values file and a kubeconfig; it does not need somewhere to stage an image.
	scratchLimit = "64Mi"
)

// HomeDir is where the shell's writable home is mounted, and KubeconfigPath is
// the file the caller's credential is written to. The image sets KUBECONFIG to
// the same path, so kubectl finds it without the operator doing anything.
const (
	HomeDir        = "/home/shell"
	KubeconfigPath = HomeDir + "/.kube/config"
)

// runAsUser is the unprivileged uid the image ships with. It matches distroless
// `nonroot`, which is what the base image's own /etc/passwd knows about.
const runAsUser = 65532

// PodName is the shell pod's name for a user. One shell per person per cluster,
// and the name is derived rather than generated so that finding it again is a
// read rather than a search — a second KubeMG replica answering the next request
// has to arrive at the same name with nothing shared between them.
func PodName(userID uint) string {
	return fmt.Sprintf("kubemg-shell-%d", userID)
}

// PodSpec is everything that varies between one shell and the next.
type PodSpec struct {
	Namespace string
	Image     string
	UserID    uint
	Username  string
	// MaxLifetime becomes the pod's own activeDeadlineSeconds. It is written into
	// the cluster rather than only enforced by the reaper because a bastion that
	// is down, wedged or mid-upgrade must not be what stands between a forgotten
	// shell and the end of it: past the deadline the kubelet stops the pod
	// whether or not KubeMG is there to ask.
	MaxLifetime time.Duration
	// Now stamps the activity clock at creation, so a shell nobody ever types
	// into is still reaped an idle window after it was made.
	Now time.Time
}

// PodManifest renders the pod as it is posted to the Jobs' collection's
// neighbour — the Pods collection — through the ordinary impersonated tunnel.
//
// Every hardening decision below is deliberate and none of them is decoration:
//
//   - **No service account token.** `automountServiceAccountToken: false` on a
//     service account with no bindings. This is the one that makes the pod
//     powerless by construction rather than by policy.
//   - **No privilege escalation.** `allowPrivilegeEscalation: false` and every
//     capability dropped, so a setuid binary in the image — there are none, but
//     the rule must not depend on that — cannot gain anything by being run.
//   - **Non-root, read-only root filesystem.** The only writable paths are two
//     size-limited emptyDirs, which is also what makes the shell ephemeral:
//     nothing written in it survives the pod, home directory included.
//   - **No host anything.** No host network, PID or IPC namespace, no host path
//     mount, and `enableServiceLinks: false` so the pod is not handed the
//     addresses of every Service in the namespace as environment variables.
//   - **restartPolicy: Never.** A shell that ended is ended; a crash loop that
//     silently produced a fresh terminal would be a session nobody opened.
func PodManifest(spec PodSpec) ([]byte, error) {
	name := PodName(spec.UserID)
	deadline := int64(spec.MaxLifetime / time.Second)
	if deadline <= 0 {
		deadline = int64(DefaultMaxLifetime / time.Second)
	}
	now := spec.Now.UTC()

	pod := map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      name,
			"namespace": spec.Namespace,
			"labels": map[string]any{
				LabelName:      "kubemg-shell",
				LabelPartOf:    "kubemg",
				LabelManagedBy: "kubemg",
				LabelComponent: ComponentValue,
				LabelUserID:    strconv.FormatUint(uint64(spec.UserID), 10),
			},
			"annotations": map[string]any{
				AnnotationUsername:     spec.Username,
				AnnotationLastActivity: now.Format(time.RFC3339),
				AnnotationExpiresAt:    now.Add(time.Duration(deadline) * time.Second).Format(time.RFC3339),
			},
		},
		"spec": map[string]any{
			"serviceAccountName":            ServiceAccount,
			"automountServiceAccountToken":  false,
			"enableServiceLinks":            false,
			"restartPolicy":                 "Never",
			"activeDeadlineSeconds":         deadline,
			"terminationGracePeriodSeconds": 5,
			"securityContext": map[string]any{
				"runAsNonRoot":   true,
				"runAsUser":      runAsUser,
				"runAsGroup":     runAsUser,
				"seccompProfile": map[string]any{"type": "RuntimeDefault"},
			},
			"containers": []any{
				map[string]any{
					"name":  "shell",
					"image": spec.Image,
					// The container does nothing on its own. It waits, and every
					// terminal is an exec into it — which is what puts each session
					// on the audited path rather than in a process nobody attached
					// to. `sleep infinity` is deliberately not used: it is a GNU
					// coreutils spelling and busybox's sleep does not take it.
					"command": []any{"/bin/sh", "-c", "while :; do sleep 3600; done"},
					"env": []any{
						map[string]any{"name": "HOME", "value": HomeDir},
						map[string]any{"name": "KUBECONFIG", "value": KubeconfigPath},
						map[string]any{"name": "TERM", "value": "xterm-256color"},
						map[string]any{"name": "KUBEMG_USER", "value": spec.Username},
					},
					"resources": map[string]any{
						"requests": map[string]any{"cpu": cpuRequest, "memory": memoryRequest},
						"limits":   map[string]any{"cpu": cpuLimit, "memory": memoryLimit},
					},
					"securityContext": map[string]any{
						"allowPrivilegeEscalation": false,
						"privileged":               false,
						"readOnlyRootFilesystem":   true,
						"runAsNonRoot":             true,
						"runAsUser":                runAsUser,
						"capabilities":             map[string]any{"drop": []any{"ALL"}},
					},
					"volumeMounts": []any{
						map[string]any{"name": "home", "mountPath": HomeDir},
						map[string]any{"name": "tmp", "mountPath": "/tmp"},
					},
				},
			},
			"volumes": []any{
				map[string]any{
					"name":     "home",
					"emptyDir": map[string]any{"sizeLimit": scratchLimit},
				},
				map[string]any{
					"name":     "tmp",
					"emptyDir": map[string]any{"sizeLimit": scratchLimit},
				},
			},
		},
	}
	return json.Marshal(pod)
}

// SeedCommand is the argv that writes the caller's kubeconfig into the pod.
//
// The credential travels on the exec's **stdin**, never in this command line and
// never in the pod's spec or a Secret. That is the whole reason the seeding is a
// separate exec rather than an environment variable: an env var is readable by
// anyone who can get the pod, a Secret is a credential at rest in somebody
// else's cluster, and both outlive the moment they were needed. Stdin reaches
// the process and nothing else — it is not in the audit path either, which
// carries the request's query string.
//
// `head -c` rather than `cat` is what makes this work without a stdin EOF: the
// v4 channel protocol has no way to half-close, so `cat` would sit waiting for
// an end-of-file that only arrives when the session is killed, and a killed
// session is not a written file. Reading an exact byte count ends the process on
// its own, and its exit status is then a real answer about whether the write
// happened.
func SeedCommand(size int) []string {
	return []string{
		"/bin/sh", "-c",
		fmt.Sprintf(
			"mkdir -p %s && head -c %d > %s && chmod 600 %s",
			HomeDir+"/.kube", size, KubeconfigPath, KubeconfigPath,
		),
	}
}

// Status is a shell pod as the console needs to see it.
type Status struct {
	// Exists is false when there is no pod, which is the ordinary state — a shell
	// is created when somebody asks for one.
	Exists bool `json:"exists"`
	Name   string `json:"name,omitempty"`
	// Phase is the pod's own phase, verbatim.
	Phase string `json:"phase,omitempty"`
	// Ready is whether the container is up and can be attached to. A Running pod
	// whose container is still pulling is not a terminal yet.
	Ready        bool      `json:"ready"`
	Image        string    `json:"image,omitempty"`
	CreatedAt    time.Time `json:"created_at,omitempty"`
	LastActivity time.Time `json:"last_activity,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	// CredentialExpiresAt is when the kubeconfig inside the pod stops working.
	// Zero means the pod has not been seeded yet.
	CredentialExpiresAt time.Time `json:"credential_expires_at,omitempty"`
	// CredentialID is the register row that credential was written as, so ending
	// the shell can withdraw it. Zero means there is nothing to withdraw. It is
	// deliberately not serialised to the console: which row it is is the
	// register's business, and the console reads that register by its own route.
	CredentialID uint `json:"-"`
	// Message explains a pod that is not going to become a terminal — an image
	// that will not pull, a deadline that ran out — so the console can say why
	// rather than spinning.
	Message string `json:"message,omitempty"`
}

// ReadStatus reads a pod object into the shape above. Anything it cannot find is
// left zero rather than guessed: a missing activity annotation on a pod that is
// otherwise fine reads as "no activity recorded", and the reaper treats that as
// the pod's creation time rather than as an infinitely old session.
func ReadStatus(pod map[string]any) Status {
	status := Status{Exists: true}

	metadata, _ := pod["metadata"].(map[string]any)
	status.Name, _ = metadata["name"].(string)
	if raw, ok := metadata["creationTimestamp"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			status.CreatedAt = parsed.UTC()
		}
	}
	annotations, _ := metadata["annotations"].(map[string]any)
	status.LastActivity = annotationTime(annotations, AnnotationLastActivity)
	status.ExpiresAt = annotationTime(annotations, AnnotationExpiresAt)
	status.CredentialExpiresAt = annotationTime(annotations, AnnotationCredentialExpires)
	if raw, ok := annotations[AnnotationCredentialID].(string); ok {
		if parsed, err := strconv.ParseUint(raw, 10, 64); err == nil {
			status.CredentialID = uint(parsed)
		}
	}
	if status.LastActivity.IsZero() {
		status.LastActivity = status.CreatedAt
	}

	spec, _ := pod["spec"].(map[string]any)
	if containers, ok := spec["containers"].([]any); ok && len(containers) > 0 {
		if first, ok := containers[0].(map[string]any); ok {
			status.Image, _ = first["image"].(string)
		}
	}

	podStatus, _ := pod["status"].(map[string]any)
	status.Phase, _ = podStatus["phase"].(string)
	if reason, ok := podStatus["reason"].(string); ok && reason != "" {
		status.Message = reason
	}
	if message, ok := podStatus["message"].(string); ok && message != "" {
		status.Message = message
	}
	status.Ready, status.Message = containerReadiness(podStatus, status.Message)
	return status
}

// containerReadiness reads the container's own account of itself. A pod can sit
// in Running for minutes with a container that is not up, and the waiting
// reason — ImagePullBackOff is the one that actually happens here, on an
// air-gapped site with no mirror for the shell image — is the only thing that
// tells an operator to stop waiting.
func containerReadiness(podStatus map[string]any, message string) (bool, string) {
	statuses, ok := podStatus["containerStatuses"].([]any)
	if !ok || len(statuses) == 0 {
		return false, message
	}
	first, ok := statuses[0].(map[string]any)
	if !ok {
		return false, message
	}
	ready, _ := first["ready"].(bool)
	if state, ok := first["state"].(map[string]any); ok {
		if waiting, ok := state["waiting"].(map[string]any); ok {
			if reason, ok := waiting["reason"].(string); ok && reason != "" {
				message = reason
				if detail, ok := waiting["message"].(string); ok && detail != "" {
					message = reason + ": " + detail
				}
			}
		}
	}
	return ready, message
}

func annotationTime(annotations map[string]any, key string) time.Time {
	raw, _ := annotations[key].(string)
	if raw == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

// Idle reports whether a shell has gone quiet for longer than the timeout.
//
// A pod that has finished — Succeeded or Failed, which is what the kubelet
// leaves behind when the absolute deadline lands — is always collectable, no
// matter how recently somebody typed into it: there is no terminal there any
// more, only a record of one.
func (s Status) Idle(now time.Time, timeout time.Duration) bool {
	switch s.Phase {
	case "Succeeded", "Failed":
		return true
	}
	last := s.LastActivity
	if last.IsZero() {
		last = s.CreatedAt
	}
	if last.IsZero() {
		// A pod with no clock at all is not evidence of an idle session; leaving
		// it alone is the safer half of an ambiguity that only arises if somebody
		// edited the annotations by hand.
		return false
	}
	return now.UTC().Sub(last) >= timeout
}

// ActivityPatch is the merge patch that moves the activity clock forward.
func ActivityPatch(at time.Time) []byte {
	patch := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]any{
				AnnotationLastActivity: at.UTC().Format(time.RFC3339),
			},
		},
	}
	encoded, err := json.Marshal(patch)
	if err != nil {
		// The document above has no shape json cannot render; the branch exists so
		// callers are not handed an error they would have to invent handling for.
		return []byte(`{}`)
	}
	return encoded
}

// Selector is the label selector that finds every shell pod in a namespace. It
// is what the reaper lists by, and it is deliberately the component label rather
// than a per-user one: the reaper's question is "which shells are stale", not
// "where is this person's shell".
func Selector() string {
	return strings.Join([]string{
		LabelManagedBy + "=kubemg",
		LabelComponent + "=" + ComponentValue,
	}, ",")
}

// ClampIdleTimeout and ClampMaxLifetime hold an operator's setting inside what
// the build is willing to run. Out-of-bounds reads as unset rather than as the
// nearest bound: a stored 0 is how a setting is cleared everywhere else here,
// and silently rounding a mistyped value would leave an operator looking at a
// number the server is not using.
func ClampIdleTimeout(d time.Duration) time.Duration {
	if d < MinIdleTimeout || d > MaxIdleTimeout {
		return DefaultIdleTimeout
	}
	return d
}

// ClampMaxLifetime is ClampIdleTimeout's other half.
func ClampMaxLifetime(d time.Duration) time.Duration {
	if d < MinMaxLifetime || d > MaxMaxLifetime {
		return DefaultMaxLifetime
	}
	return d
}
