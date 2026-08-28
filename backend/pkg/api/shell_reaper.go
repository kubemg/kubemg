package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/shell"
)

/*
 * Reclaiming shells nobody is using.
 *
 * A browser shell is a pod in somebody else's cluster, and the failure mode of
 * getting this wrong is not a bug report — it is a fleet of forgotten terminals
 * consuming a customer's quota, each one holding a credential. So the lifetime
 * is enforced in three places on purpose, and they fail in different directions:
 *
 *   1. The pod's own `activeDeadlineSeconds`, which the kubelet enforces whether
 *      or not KubeMG is running. This is the one that holds when the bastion is
 *      down, upgrading, or has lost the tunnel for a day.
 *   2. This sweep, which reclaims the far more common case: a shell that is up,
 *      healthy and has not been typed into for an hour.
 *   3. The operator, who can end one from the console at any time.
 *
 * The idle clock lives on the pod as an annotation rather than in KubeMG's
 * database. That is what makes this sweep stateless — any replica can read it,
 * a restart loses nothing, and there is no second record to disagree with the
 * pod about whether somebody is using it.
 */

// shellReapInterval is how often the sweep runs. It is a fraction of the
// shortest idle window the settings allow, so the worst case is a shell living a
// few minutes past its timeout rather than a whole window past it.
const shellReapInterval = 2 * time.Minute

// shellReapLeaseTTL is the alarm watcher's rule: a multiple of the interval, so
// the holder renewing every tick never expires in the gap between two.
const shellReapLeaseTTL = 3 * shellReapInterval

// shellReapLimit bounds one cluster's sweep. A fleet with more shells than this
// on one cluster has a problem this loop is not going to fix, and the ones left
// are picked up on the next pass.
const shellReapLimit = 200

// startShellReaper reclaims idle shells for as long as the context lives.
func (s *server) startShellReaper(ctx context.Context) {
	ticker := time.NewTicker(shellReapInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reapShells(ctx)
		}
	}
}

// reapShells is one pass, on the replica holding the lease.
func (s *server) reapShells(ctx context.Context) {
	held, err := s.store.AcquireLease(ctx, db.LeaseShellReaper, s.instanceID, shellReapLeaseTTL)
	if err != nil {
		// Failing closed, the alarm watcher's rule: if the database cannot answer,
		// every replica gets this error, and treating it as permission to sweep
		// would put all of them on every cluster at once — deleting the same pods
		// and filing the same audit records, precisely when something is already
		// wrong.
		if ctx.Err() == nil {
			s.log().Warn("shell reaper could not take its lease", slog.String("error", err.Error()))
		}
		return
	}
	if !held {
		return
	}

	config := s.shellSettings(ctx)
	clusters, err := s.store.Clusters(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log().Warn("shell reaper could not list clusters", slog.String("error", err.Error()))
		}
		return
	}

	for _, cluster := range clusters {
		if ctx.Err() != nil {
			return
		}
		// No tunnel, no sweep. A cluster whose agent is away keeps its shells
		// until it comes back, and the pods' own deadlines are what bound them in
		// the meantime.
		if connectionMode(cluster) != db.ModeAgent || s.tunnels == nil || !s.tunnels.Connected(cluster.ID) {
			continue
		}
		s.reapClusterShells(ctx, cluster, config)
	}
}

// reapClusterShells deletes the stale shells on one cluster.
//
// The sweep runs even when the feature has been switched off since these pods
// were created — turning the shell off stops new ones rather than stranding the
// ones already running, and something still has to collect them.
func (s *server) reapClusterShells(ctx context.Context, cluster db.Cluster, config shellConfig) {
	user, grant := shellRunner(cluster.ID)
	path := shellPodCollection(config.Namespace) +
		"?labelSelector=" + url.QueryEscape(shell.Selector()) +
		"&limit=" + strconv.Itoa(shellReapLimit)

	resp, err := s.proxy.Call(ctx, user, &cluster, grant, http.MethodGet, path, nil, nil)
	if err != nil {
		if ctx.Err() == nil {
			// Debug rather than warn: a cluster whose agent dropped between the
			// check above and this call is ordinary, and warning on it every two
			// minutes would make the log the problem.
			s.log().Debug("shell reaper could not list shells",
				slog.String("cluster", cluster.Name), slog.String("error", err.Error()))
		}
		return
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return
	}

	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(resp.Body, &list); err != nil {
		return
	}

	now := time.Now().UTC()
	for _, item := range list.Items {
		status := shell.ReadStatus(item)
		if !status.Idle(now, config.IdleTimeout) {
			continue
		}
		s.reapOneShell(ctx, cluster, config.Namespace, status)
	}
}

// reapOneShell deletes a single stale pod, by the name it is actually called
// rather than by a name derived from a label — a pod that has been renamed or
// hand-made carrying the shell label is still collected, and nothing here has to
// trust an annotation to address it.
func (s *server) reapOneShell(
	ctx context.Context, cluster db.Cluster, namespace string, status shell.Status,
) {
	if status.Name == "" {
		return
	}
	user, grant := shellRunner(cluster.ID)
	path := shellPodCollection(namespace) + "/" + url.PathEscape(status.Name) + "?propagationPolicy=Background"
	if _, err := s.proxy.Call(ctx, user, &cluster, grant, http.MethodDelete, path, nil, nil); err != nil {
		if ctx.Err() == nil {
			s.log().Debug("shell reaper could not delete a shell",
				slog.String("cluster", cluster.Name),
				slog.String("pod", status.Name),
				slog.String("error", err.Error()))
		}
		return
	}
	// The credential the shell was holding goes with it. A reaped shell is the
	// case this matters most in: nobody was there to end it, and its token would
	// otherwise stay live for the rest of its window with no copy anywhere.
	s.withdrawShellCredential(ctx, &db.User{Username: shell.RunnerUser}, status)

	s.log().Info("reclaimed an idle browser shell",
		slog.String("cluster", cluster.Name),
		slog.String("pod", status.Name),
		slog.String("phase", status.Phase))
}
