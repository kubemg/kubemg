package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/k8s"
	"github.com/kubemg/kubemg/backend/pkg/shell"
)

/*
 * The browser shell.
 *
 * Explore already opens a terminal *inside a pod*, which answers what is wrong
 * with that pod and nothing else. There has been nowhere in this console to run
 * `kubectl get` across a namespace, diff a manifest or run `helm upgrade` — so
 * the moment a question outgrew a form, the operator left for a terminal, and
 * everything they did there became invisible to this product. That is the gap
 * this closes, and closing it is only worth doing if the shell stays *inside*
 * the access model rather than beside it.
 *
 * Three rules carry the whole design.
 *
 * **It is not a way around the tunnel.** The pod holds no cluster credential:
 * no service account token is mounted, and the account it runs as is granted
 * nothing. Its kubeconfig is a KubeMG proxy credential for the caller, written
 * in over an exec once the pod is up — so every command it runs is impersonated
 * as that person, answered by the target cluster's own RBAC, held to their
 * namespace scope, and in the audit trail exactly like a command typed anywhere
 * else. A shell holding a credential of its own would undo the access model in
 * one feature.
 *
 * **The pod is KubeMG's, the session is the operator's.** Creating, seeding and
 * deleting the pod are done as shell.RunnerUser — a Role bound in the agent's
 * own namespace and nowhere else — because a read-only grant cannot create a pod
 * and should not have to. Attaching a terminal to it is recorded under the
 * *person*, with the impersonated identity beside it, and is recorded and
 * guarded exactly like the pod terminal: same recorder, same keystroke
 * guardrails, same two audit rows.
 *
 * **It is ephemeral, twice over.** An idle window reclaims a shell nobody is
 * typing into, and the pod's own activeDeadlineSeconds ends one that has been
 * open too long whether or not KubeMG is there to ask — a bastion that is down
 * or mid-upgrade must not be what stands between a forgotten shell and the end
 * of it. Nothing written in it survives: the only writable paths are two
 * size-limited emptyDirs.
 */

// shellContainer is the container's name inside the pod. It is fixed rather than
// discovered: KubeMG wrote the pod, so there is exactly one and this is it.
const shellContainer = "shell"

// shellReadyTimeout bounds how long one create request waits for the container
// to come up. A first pull on a cluster with no warm image takes longer than
// this, which is why the route is idempotent and reports "not ready yet" rather
// than failing: the console asks again, and the second call finds the pod it
// made and keeps waiting.
const shellReadyTimeout = 45 * time.Second

// shellReadyPoll is how often that wait re-reads the pod.
const shellReadyPoll = 2 * time.Second

// shellTerminatingTimeout bounds the wait for a finished shell to actually go
// away before a new one is created in its place. A pod's name is its identity,
// so a replacement cannot be created while the previous one is still
// terminating.
const shellTerminatingTimeout = 20 * time.Second

// shellActivityInterval throttles the write that moves a shell's idle clock
// forward. Every keystroke arriving as a PATCH on the API server would make a
// terminal the most expensive thing in this product; once every few minutes is
// enough resolution for an hour-long window.
const shellActivityInterval = 3 * time.Minute

// shellConfig is the resolved lifecycle policy for this install.
type shellConfig struct {
	Enabled     bool
	Image       string
	Namespace   string
	IdleTimeout time.Duration
	// MaxLifetime is the pod's absolute deadline. It is never longer than the
	// credential inside it: a shell whose kubectl stopped working an hour ago is
	// a terminal that looks alive and answers nothing, so the pod's deadline is
	// clamped to the kubeconfig ceiling this install enforces.
	MaxLifetime time.Duration
}

// shellSettings resolves the policy from the operator's settings.
func (s *server) shellSettings(ctx context.Context) shellConfig {
	runtime := s.settings(ctx)

	lifetime := shell.ClampMaxLifetime(time.Duration(runtime.ShellMaxLifetimeHours) * time.Hour)
	if ceiling := s.kubeconfigMaxTTL(ctx); lifetime > ceiling {
		lifetime = ceiling
	}
	return shellConfig{
		Enabled:     runtime.ShellEnabled,
		Image:       runtime.ShellImage,
		Namespace:   runtime.AgentNamespace,
		IdleTimeout: shell.ClampIdleTimeout(time.Duration(runtime.ShellIdleTimeoutMinutes) * time.Minute),
		MaxLifetime: lifetime,
	}
}

// shellResponse is what the console draws the surface from — the pod's state and
// everything that has to be said before somebody types into it.
type shellResponse struct {
	// Enabled is the operator's switch. A disabled install still answers this
	// route, saying so, rather than 404ing: a missing feature and a switched-off
	// one look identical from a browser otherwise.
	Enabled bool `json:"enabled"`
	// Available reports whether a shell could be started on *this* cluster at
	// all, with Reason carrying the explanation when it could not.
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`

	Image string `json:"image,omitempty"`
	// Namespace is where the pod runs — KubeMG's own namespace on the cluster,
	// not the caller's working namespace.
	Namespace string `json:"namespace,omitempty"`
	// KubeNamespace is the namespace the shell's kubectl defaults to, which is
	// the caller's scope rather than the pod's home.
	KubeNamespace string `json:"kube_namespace,omitempty"`

	IdleTimeoutSeconds int64 `json:"idle_timeout_seconds"`
	MaxLifetimeSeconds int64 `json:"max_lifetime_seconds"`

	// K8sRole and Namespaces disclose what the credential inside the shell can
	// actually do, which is the caller's own grant and nothing more.
	K8sRole    string   `json:"k8s_role,omitempty"`
	Namespaces []string `json:"namespaces,omitempty"`

	// Recorded says whether this session will be captured for replay. It is the
	// pod terminal's disclosure, and it is here for the same reason: telling
	// somebody afterwards is not disclosure.
	Recorded bool `json:"recorded"`

	Status shell.Status `json:"status"`
}

// shellRunner is the identity KubeMG manages shell pods as. The grant is
// unscoped and read-only: the *pod* verbs come from a Role bound to this
// username in the agent's namespace, so this carries no reach of its own.
func shellRunner(clusterID uint) (*db.User, db.UserClusterAccess) {
	return &db.User{Username: shell.RunnerUser},
		db.UserClusterAccess{ClusterID: clusterID, K8sRole: db.K8sRoleView}
}

// shellRunnerGroups is what the API server sees beside the runner's username.
func shellRunnerGroups() []string {
	return bastion.ImpersonationGroups(db.K8sRoleView)
}

// shellCluster resolves the caller, their cluster and the policy, refusing the
// combinations that cannot carry a shell. It writes the response itself.
func (s *server) shellCluster(c *gin.Context) (*db.User, *db.Cluster, db.UserClusterAccess, string, shellConfig, bool) {
	var none shellConfig

	user, cluster, grant, k8sRole, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return nil, nil, grant, "", none, false
	}
	if s.proxy == nil {
		c.JSON(http.StatusFailedDependency, gin.H{
			"error": "the agent proxy is not enabled on this server",
		})
		return nil, nil, grant, "", none, false
	}
	return user, cluster, grant, k8sRole, s.shellSettings(c.Request.Context()), true
}

// shellUnavailable names the reason a cluster cannot carry a shell, or empty
// when it can.
func shellUnavailable(cluster *db.Cluster, config shellConfig) string {
	switch {
	case !config.Enabled:
		return "the browser shell is switched off on this server"
	case !cluster.UsesAgent():
		return "the browser shell needs a cluster registered in agent mode: it runs as a pod " +
			"that reaches back through this server's proxy, which a directly-connected cluster has no path to"
	case config.Image == "":
		return "no shell image is configured on this server"
	}
	return ""
}

// showShell reports the state of the caller's shell on this cluster.
func (s *server) showShell(c *gin.Context) {
	user, cluster, grant, k8sRole, config, ok := s.shellCluster(c)
	if !ok {
		return
	}

	response := shellResponse{
		Enabled:            config.Enabled,
		Image:              config.Image,
		Namespace:          config.Namespace,
		KubeNamespace:      shellKubeNamespace(grant),
		IdleTimeoutSeconds: int64(config.IdleTimeout.Seconds()),
		MaxLifetimeSeconds: int64(config.MaxLifetime.Seconds()),
		K8sRole:            k8sRole,
		Namespaces:         grant.NamespaceList(),
		Recorded:           s.settings(c.Request.Context()).RecordExecSessions,
	}
	if reason := shellUnavailable(cluster, config); reason != "" {
		response.Reason = reason
		c.JSON(http.StatusOK, response)
		return
	}

	// A read that cannot reach the cluster is not an error on this route. The
	// question the page is asking is "can I have a shell here, and what would it
	// be" — an agent that is away answers the first half with "not right now",
	// and the disclosure above is still true and still worth drawing. Failing
	// the whole read would leave the page with nothing to say but a status code.
	status, err := s.readShellPod(c.Request.Context(), cluster, config.Namespace, user.ID)
	if err != nil {
		response.Reason = err.Error()
		c.JSON(http.StatusOK, response)
		return
	}
	response.Available = true
	response.Status = status
	c.JSON(http.StatusOK, response)
}

// startShell creates the caller's shell if it is not there, waits for it to come
// up, and writes their kubeconfig into it.
//
// It is idempotent by design rather than by accident: the pod's name is derived
// from the user id, so a second call finds the first call's pod. That is what
// makes "the image is still pulling" a state the console can poll through
// instead of an error, and what keeps two browser tabs from producing two
// shells.
func (s *server) startShell(c *gin.Context) {
	user, cluster, grant, k8sRole, config, ok := s.shellCluster(c)
	if !ok {
		return
	}
	if reason := shellUnavailable(cluster, config); reason != "" {
		c.JSON(http.StatusConflict, gin.H{"error": reason})
		return
	}

	ctx := c.Request.Context()
	status, err := s.readShellPod(ctx, cluster, config.Namespace, user.ID)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	// A shell that has finished is a record of a terminal, not a terminal. It is
	// cleared out of the way rather than reported, because the person asking for
	// one has no use for the remains of the last.
	if status.Exists && (status.Phase == "Succeeded" || status.Phase == "Failed") {
		if err := s.deleteShellPod(ctx, cluster, config.Namespace, user.ID); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		status, err = s.awaitShellGone(ctx, cluster, config.Namespace, user.ID)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		if status.Exists {
			c.JSON(http.StatusConflict, gin.H{
				"error": "the previous shell is still shutting down; try again in a moment",
			})
			return
		}
	}

	if !status.Exists {
		if err := s.createShellPod(ctx, user, cluster, config); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
	}

	status, err = s.awaitShellReady(ctx, cluster, config.Namespace, user.ID)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	response := shellResponse{
		Enabled:            true,
		Available:          true,
		Image:              config.Image,
		Namespace:          config.Namespace,
		KubeNamespace:      shellKubeNamespace(grant),
		IdleTimeoutSeconds: int64(config.IdleTimeout.Seconds()),
		MaxLifetimeSeconds: int64(config.MaxLifetime.Seconds()),
		K8sRole:            k8sRole,
		Namespaces:         grant.NamespaceList(),
		Recorded:           s.settings(ctx).RecordExecSessions,
		Status:             status,
	}

	if status.Ready {
		if err := s.seedShellCredential(c, user, cluster, grant, k8sRole, config, status); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, response)
}

// stopShell ends the caller's shell now, and withdraws the credential that was
// in it.
func (s *server) stopShell(c *gin.Context) {
	user, cluster, _, _, config, ok := s.shellCluster(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	// Read before deleting, so the register row this shell was holding is known:
	// a pod that is already gone cannot say what was inside it.
	status, _ := s.readShellPod(ctx, cluster, config.Namespace, user.ID)
	if err := s.deleteShellPod(ctx, cluster, config.Namespace, user.ID); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	s.withdrawShellCredential(ctx, user, status)
	c.JSON(http.StatusOK, gin.H{
		"message": "the shell is marked for deletion; nothing written inside it is kept",
	})
}

// attachShell bridges the browser's terminal to the pod.
func (s *server) attachShell(c *gin.Context) {
	user, cluster, _, _, config, ok := s.shellCluster(c)
	if !ok {
		return
	}
	if reason := shellUnavailable(cluster, config); reason != "" {
		c.JSON(http.StatusConflict, gin.H{"error": reason})
		return
	}

	ctx := c.Request.Context()
	status, err := s.readShellPod(ctx, cluster, config.Namespace, user.ID)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if !status.Exists || !status.Ready {
		c.JSON(http.StatusConflict, gin.H{"error": "no shell is running; start one first"})
		return
	}

	// The clock moves at the start of the session and then at most once every few
	// minutes while somebody is typing. Both writes are asynchronous: an idle
	// timer must never be able to stall a keystroke.
	activity := s.shellActivity(cluster, config.Namespace, user.ID)
	activity()

	s.proxy.ServeShell(c, bastion.ShellSpec{
		User:        user,
		Cluster:     cluster,
		Impersonate: shell.RunnerUser,
		Groups:      shellRunnerGroups(),
		Namespace:   config.Namespace,
		Pod:         shell.PodName(user.ID),
		Container:   shellContainer,
		// The shell is `sh` rather than a login shell: the image is minimal by
		// design and busybox's ash is what it has.
		Command:  []string{"/bin/sh"},
		Activity: activity,
	})
}

// shellActivity returns a throttled writer of the idle clock.
func (s *server) shellActivity(cluster *db.Cluster, namespace string, userID uint) func() {
	var (
		mu   sync.Mutex
		last time.Time
	)
	// Detached from the request: the last write is the one that happens as the
	// session ends, and a cancelled context cannot make it.
	background := s.backgroundContext()

	return func() {
		now := time.Now().UTC()
		mu.Lock()
		if !last.IsZero() && now.Sub(last) < shellActivityInterval {
			mu.Unlock()
			return
		}
		last = now
		mu.Unlock()

		go func() {
			ctx, cancel := context.WithTimeout(background, 10*time.Second)
			defer cancel()
			s.touchShellPod(ctx, cluster, namespace, userID, now)
		}()
	}
}

// backgroundContext is the lifetime work that outlives a request hangs off.
func (s *server) backgroundContext() context.Context {
	if s.background != nil {
		return s.background
	}
	return context.Background()
}

// readShellPod reads the caller's shell pod, answering a missing one as "does
// not exist" rather than as an error — not having a shell is the ordinary state.
func (s *server) readShellPod(
	ctx context.Context, cluster *db.Cluster, namespace string, userID uint,
) (shell.Status, error) {
	user, grant := shellRunner(cluster.ID)
	resp, err := s.proxy.Call(ctx, user, cluster, grant,
		http.MethodGet, shellPodPath(namespace, userID), nil, nil)
	if err != nil {
		return shell.Status{}, shellCallError(err, "could not read the shell pod")
	}
	if resp.Status == http.StatusNotFound {
		return shell.Status{}, nil
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return shell.Status{}, errors.New(kubeErrorMessage(resp.Body, resp.Status))
	}
	var pod map[string]any
	if err := json.Unmarshal(resp.Body, &pod); err != nil {
		return shell.Status{}, errors.New("the cluster returned an unreadable pod")
	}
	return shell.ReadStatus(pod), nil
}

// createShellPod posts the pod.
func (s *server) createShellPod(
	ctx context.Context, holder *db.User, cluster *db.Cluster, config shellConfig,
) error {
	manifest, err := shell.PodManifest(shell.PodSpec{
		Namespace:   config.Namespace,
		Image:       config.Image,
		UserID:      holder.ID,
		Username:    holder.Username,
		MaxLifetime: config.MaxLifetime,
		Now:         time.Now().UTC(),
	})
	if err != nil {
		return errors.New("the shell pod could not be rendered")
	}

	user, grant := shellRunner(cluster.ID)
	resp, err := s.proxy.Call(ctx, user, cluster, grant,
		http.MethodPost, shellPodCollection(config.Namespace), manifest, nil)
	if err != nil {
		return shellCallError(err, "could not create the shell pod")
	}
	// Somebody else's request created it a moment ago. Two tabs asking at once is
	// the ordinary way this happens, and both of them should get a shell.
	if resp.Status == http.StatusConflict {
		return nil
	}
	if resp.Status == http.StatusForbidden {
		return errors.New(staleManifestExplanation(kubeErrorMessage(resp.Body, resp.Status)))
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return errors.New(kubeErrorMessage(resp.Body, resp.Status))
	}
	return nil
}

// staleManifestExplanation names the one thing a refusal here almost always
// means.
//
// The cluster's answer — "pods is forbidden: User kubemg:shell-runner cannot
// create resource pods" — is correct and completely opaque to somebody who has
// never heard of that name. It is not a permission an operator granted or can
// grant from the console: it comes from a Role that ships in the agent's own
// manifests, so a cluster attached before this feature existed refuses every
// shell until those manifests are re-applied. Saying so is the difference
// between a five-minute fix and an afternoon.
func staleManifestExplanation(message string) string {
	return message +
		" — this is the agent's own RBAC, and a cluster attached before the browser shell existed does " +
		"not have it yet. Re-apply this cluster's agent manifests to install the kubemg-shell service " +
		"account and the kubemg-shell-runner Role, then try again."
}

// deleteShellPod removes it. A pod that is already gone is a success: the caller
// asked for there to be no shell, and there is none.
func (s *server) deleteShellPod(
	ctx context.Context, cluster *db.Cluster, namespace string, userID uint,
) error {
	user, grant := shellRunner(cluster.ID)
	resp, err := s.proxy.Call(ctx, user, cluster, grant,
		http.MethodDelete, shellPodPath(namespace, userID)+"?propagationPolicy=Background", nil, nil)
	if err != nil {
		return shellCallError(err, "could not delete the shell pod")
	}
	if resp.Status == http.StatusNotFound {
		return nil
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return errors.New(kubeErrorMessage(resp.Body, resp.Status))
	}
	return nil
}

// touchShellPod moves the idle clock forward. A failure is logged and dropped:
// the worst case is a shell reclaimed while somebody was typing into it, and the
// alternative — refusing keystrokes because an annotation would not write — is
// worse.
func (s *server) touchShellPod(
	ctx context.Context, cluster *db.Cluster, namespace string, userID uint, at time.Time,
) {
	user, grant := shellRunner(cluster.ID)
	_, err := s.proxy.Call(ctx, user, cluster, grant,
		http.MethodPatch, shellPodPath(namespace, userID), shell.ActivityPatch(at), nil)
	if err != nil && ctx.Err() == nil {
		s.log().Debug("could not stamp shell activity",
			slog.String("cluster", cluster.Name),
			slog.String("error", err.Error()))
	}
}

// awaitShellReady waits for the container to be up, and gives back whatever the
// pod's state is when it stops waiting.
func (s *server) awaitShellReady(
	ctx context.Context, cluster *db.Cluster, namespace string, userID uint,
) (shell.Status, error) {
	deadline := time.Now().Add(shellReadyTimeout)
	for {
		status, err := s.readShellPod(ctx, cluster, namespace, userID)
		if err != nil {
			return shell.Status{}, err
		}
		if status.Ready || !status.Exists || time.Now().After(deadline) {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return status, nil
		case <-time.After(shellReadyPoll):
		}
	}
}

// awaitShellGone is its opposite, for the replacement case.
func (s *server) awaitShellGone(
	ctx context.Context, cluster *db.Cluster, namespace string, userID uint,
) (shell.Status, error) {
	deadline := time.Now().Add(shellTerminatingTimeout)
	for {
		status, err := s.readShellPod(ctx, cluster, namespace, userID)
		if err != nil {
			return shell.Status{}, err
		}
		if !status.Exists || time.Now().After(deadline) {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return status, nil
		case <-time.After(shellReadyPoll):
		}
	}
}

// seedShellCredential writes the caller's kubeconfig into their pod, once.
//
// The credential is a proxy-scoped KubeMG token exactly like the one the
// kubeconfig drawer hands out — same shape, same register row, same revocation.
// What is different is where it goes: down the exec's standard input, so it is
// in no pod spec, no Secret, no audit record and no session recording. The only
// copy is a 0600 file on a tmpfs that dies with the pod.
func (s *server) seedShellCredential(
	c *gin.Context,
	holder *db.User,
	cluster *db.Cluster,
	grant db.UserClusterAccess,
	k8sRole string,
	config shellConfig,
	status shell.Status,
) error {
	// Already carrying a working credential. The pod cannot outlive it — the
	// lifetime is clamped to the same ceiling — so there is nothing to refresh.
	if !status.CredentialExpiresAt.IsZero() && status.CredentialExpiresAt.After(time.Now().UTC()) {
		return nil
	}

	ctx := c.Request.Context()
	publicURL := s.settings(ctx).PublicURL
	if publicURL == "" {
		return errors.New("no public URL is configured for this server, so the shell has nothing to point kubectl at")
	}

	token, tokenID, expiresAt, err := s.jwt.GenerateProxyToken(
		holder.ID, holder.Username, holder.Role, cluster.ID, config.MaxLifetime,
	)
	if err != nil {
		return errors.New("could not issue the shell's access token")
	}

	server := fmt.Sprintf("%s/api/v1/clusters/%d/proxy", strings.TrimRight(publicURL, "/"), cluster.ID)
	kubeconfig, err := k8s.BuildKubeconfig(k8s.KubeconfigInput{
		ClusterName: cluster.Name,
		Server:      server,
		Username:    holder.Username,
		Token:       token,
		Namespace:   shellKubeNamespace(grant),
		// The shell dials KubeMG, so the CA it has to trust is KubeMG's own —
		// and unlike a laptop, a pod in a cluster has no operator to talk into
		// an --insecure-skip-tls-verify when this is missing.
		CAData: []byte(s.bastionCA),
	})
	if err != nil {
		return errors.New("could not render the shell's kubeconfig")
	}

	runner, _ := shellRunner(cluster.ID)
	result, err := s.proxy.ExecOnce(ctx, bastion.ExecSpec{
		User:        runner,
		Cluster:     cluster,
		Impersonate: shell.RunnerUser,
		Groups:      shellRunnerGroups(),
		Namespace:   config.Namespace,
		Pod:         shell.PodName(holder.ID),
		Container:   shellContainer,
		Command:     shell.SeedCommand(len(kubeconfig)),
		Stdin:       kubeconfig,
	})
	if err != nil {
		return fmt.Errorf("could not write the shell's kubeconfig: %w", err)
	}
	if result.Failed {
		return fmt.Errorf("could not write the shell's kubeconfig: %s", result.Status)
	}

	// The register row, so the credential inside a shell is as visible and as
	// revocable as one somebody downloaded. Revoking it stops the shell's kubectl
	// on its next call, which is the point: the terminal is still there, and it
	// can no longer reach the cluster.
	issuance := newIssuance(holder, holder, cluster, tokenID, db.ModeAgent,
		shellKubeNamespace(grant), k8sRole, "", expiresAt)
	issuance.Purpose = db.KubeconfigPurposeShell
	s.recordKubeconfigIssuance(c, issuance, holder, holder, cluster)

	// Stamped last, so a failure anywhere above leaves the pod asking to be
	// seeded again rather than believing it has a credential it never got.
	s.stampShellCredential(ctx, cluster, config.Namespace, holder.ID, issuance.ID, expiresAt)
	return nil
}

// stampShellCredential records the credential's expiry and register row on the
// pod. The row id is what lets ending the shell withdraw what was inside it.
func (s *server) stampShellCredential(
	ctx context.Context, cluster *db.Cluster, namespace string, userID, issuanceID uint, expiresAt time.Time,
) {
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]any{
				shell.AnnotationCredentialExpires: expiresAt.UTC().Format(time.RFC3339),
				shell.AnnotationCredentialID:      strconv.FormatUint(uint64(issuanceID), 10),
			},
		},
	})
	if err != nil {
		return
	}
	user, grant := shellRunner(cluster.ID)
	if _, err := s.proxy.Call(ctx, user, cluster, grant,
		http.MethodPatch, shellPodPath(namespace, userID), patch, nil); err != nil && ctx.Err() == nil {
		s.log().Warn("could not stamp the shell's credential expiry",
			slog.String("cluster", cluster.Name), slog.String("error", err.Error()))
	}
}

/*
 * withdrawShellCredential closes out the credential a shell was holding.
 *
 * A shell's kubeconfig has no copy anywhere but inside the pod, so deleting the
 * pod already puts it beyond use in practice. This is the other half: the token
 * itself stays signed and valid for hours, and a credential that exists in the
 * register as live while nothing holds it is one nobody can account for — the
 * exact state the register was built to stop. Ending a shell therefore ends what
 * was in it, and the gateway refuses it from the next call.
 *
 * It is best-effort by design. The pod is already gone by the time this runs, so
 * a failure here must not turn "the shell ended" into an error; it is logged,
 * and the credential's own expiry remains the backstop it always was.
 */
func (s *server) withdrawShellCredential(ctx context.Context, caller *db.User, status shell.Status) {
	if status.CredentialID == 0 {
		return
	}
	if _, err := s.store.RevokeKubeconfigIssuance(
		ctx, status.CredentialID, time.Now().UTC(), caller.ID, caller.Username,
	); err != nil {
		if !errors.Is(err, db.ErrNotFound) {
			s.log().Warn("could not withdraw a shell's credential",
				slog.Uint64("issuance_id", uint64(status.CredentialID)),
				slog.String("error", err.Error()))
		}
		return
	}
	// Republished immediately, the revoke route's rule: a credential reported
	// withdrawn must not answer a call afterwards on this replica.
	s.publishRevokedCredentials(ctx)
}

// shellKubeNamespace is the namespace the shell's kubectl defaults to: the first
// namespace of a scoped grant, and `default` for a grant that covers the
// cluster. It is the kubeconfig drawer's rule, for the same reason — a terminal
// that opens on a namespace the caller cannot read is a terminal that greets
// them with a refusal.
func shellKubeNamespace(grant db.UserClusterAccess) string {
	if allowed := grant.NamespaceList(); len(allowed) > 0 {
		return allowed[0]
	}
	return "default"
}

// shellPodPath and shellPodCollection address the pod and its collection.
func shellPodPath(namespace string, userID uint) string {
	return shellPodCollection(namespace) + "/" + url.PathEscape(shell.PodName(userID))
}

func shellPodCollection(namespace string) string {
	return "/api/v1/namespaces/" + url.PathEscape(namespace) + "/pods"
}

// shellCallError turns a proxy refusal into something an operator can act on.
// The refusals that actually happen here are "no agent attached" and a
// guardrail, and both of them explain themselves better than a generic message.
func shellCallError(err error, fallback string) error {
	var callErr *bastion.CallError
	if errors.As(err, &callErr) && callErr.Message != "" {
		return errors.New(callErr.Message)
	}
	return errors.New(fallback)
}
