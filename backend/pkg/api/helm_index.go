package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/helm"
)

/*
 * Keeping the catalogue current, once per installation rather than once per
 * replica.
 *
 * This is the second piece of work in the product that reaches a network on its
 * own, and the first that reaches one *outside* the fleet. The alarm watcher's
 * argument applies unchanged and then some: three replicas behind a load
 * balancer, each pulling a sixty-megabyte index.yaml from a public repository
 * every interval, is a bandwidth bill an operator did not ask for by running
 * three replicas for availability — and it is the sort of traffic that gets an
 * installation rate-limited by the repository, which fails the *install* path
 * and not just the sync.
 *
 * So the pass takes `db.LeaseHelmIndex` first, and the three rules are the alarm
 * watcher's, for the same reasons:
 *
 *   - a store error on the lease means **do not poll**. An unreachable database
 *     is the case where every replica would otherwise decide it is the one.
 *   - the TTL is a multiple of the interval, so a slow pass renews rather than
 *     expiring under itself.
 *   - the pass is a loop over repositories with a per-repository failure, not a
 *     transaction: one repository being down must not stop the other five from
 *     refreshing.
 *
 * And the rule that matters most to whoever is looking at a chart list: **a
 * failed sync is not destructive.** The catalogue is replaced only by a fetch
 * that succeeded. A repository that cannot be reached keeps every chart it last
 * held and records why, because a list that empties on a DNS blip reads as "this
 * feature is broken" while a stale list with a reported error reads as exactly
 * what it is.
 */

const (
	// helmSyncInterval is how often the catalogue is refreshed. Chart
	// repositories publish on the order of days; an hour is far more often than
	// the data changes and far less often than an operator would notice it
	// being stale.
	helmSyncInterval = time.Hour
	// helmSyncStartupDelay lets a starting replica serve requests before it
	// pulls tens of megabytes. It also staggers replicas that were restarted
	// together, so the lease is contended rather than three simultaneous fetches
	// racing to lose.
	helmSyncStartupDelay = 2 * time.Minute
	// helmLeaseTTL outlives a pass. The alarm watcher's multiple, for the
	// alarm watcher's reason.
	helmLeaseTTL = 3 * helmSyncInterval

	// helmFetchTimeout bounds one repository's HTTP client. The package's own
	// per-request timeout is tighter; this is the outer bound including
	// redirects and a slow body.
	helmFetchTimeout = 3 * time.Minute
)

// helmClient is the HTTP client every repository read uses. It is built per call
// rather than held on the server because it carries no state worth sharing and
// the transport's own connection pool is process-wide regardless.
func (s *server) helmClient() *http.Client {
	return &http.Client{Timeout: helmFetchTimeout}
}

// startHelmIndexSync refreshes every repository on a schedule, on one replica.
func (s *server) startHelmIndexSync(ctx context.Context) {
	// A first pass shortly after boot, so a freshly seeded installation has a
	// catalogue before an operator opens the install form rather than an hour
	// later. It is still behind the lease, so N replicas starting together
	// still produce one fetch.
	select {
	case <-ctx.Done():
		return
	case <-time.After(helmSyncStartupDelay):
		s.helmSyncTick(ctx)
	}

	ticker := time.NewTicker(helmSyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.helmSyncTick(ctx)
		}
	}
}

// helmSyncTick is one pass: take the lease, then refresh what is declared.
func (s *server) helmSyncTick(ctx context.Context) {
	held, err := s.store.AcquireLease(ctx, db.LeaseHelmIndex, s.instanceID, helmLeaseTTL)
	if err != nil {
		// Not "assume we hold it". A database that cannot answer is exactly the
		// state in which every replica would decide it is the one.
		slog.Error("helm index lease", "error", err)
		return
	}
	if !held {
		return
	}

	repositories, err := s.store.HelmRepositories(ctx)
	if err != nil {
		slog.Error("helm repositories", "error", err)
		return
	}
	for index := range repositories {
		if ctx.Err() != nil {
			return
		}
		if err := s.syncRepository(ctx, &repositories[index]); err != nil {
			// Already recorded on the row, where an operator will see it. The
			// log line is for the case where nobody is looking at the console.
			slog.Warn("helm repository sync",
				"repository", repositories[index].Name, "error", err)
		}
	}
}

// syncRepository refreshes one repository's catalogue.
//
// It returns the fetch error so a caller pressing "sync now" can report it, and
// records it on the row either way — the scheduled pass has nobody to return to.
func (s *server) syncRepository(ctx context.Context, repository *db.HelmRepository) error {
	charts, err := helm.FetchIndex(ctx, s.helmClient(), repository.Repository())
	if err != nil {
		// Health only. The charts are untouched, which is the whole rule: what
		// the repository last served is a better answer than nothing.
		if writeErr := s.store.UpdateHelmRepositoryHealth(ctx, repository.ID,
			db.HelmRepoError, err.Error(), repository.ChartCount, repository.SyncedAt); writeErr != nil {
			slog.Error("helm repository health", "repository", repository.Name, "error", writeErr)
		}
		repository.Status = db.HelmRepoError
		repository.StatusMessage = err.Error()
		return err
	}

	repository.Status = db.HelmRepoOK
	repository.StatusMessage = ""
	return s.storeCatalogue(ctx, repository, charts)
}
