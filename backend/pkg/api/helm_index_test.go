package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * The sync pass is the second piece of unattended work in the product that
 * reaches a network, and the first that reaches one outside the fleet. Three
 * rules decide whether it is safe to run three replicas: it takes the lease, it
 * refuses to poll when the lease cannot be read, and a failed fetch is not
 * destructive.
 */

// syncServer builds a bare server for the tick, the way the alarm watcher's
// tests do: no router, no middleware, just the loop under test. It is
// `watchServer` under a name that says which pass it is driving — the two jobs
// share a shape and will not share one for ever.
func syncServer(store *fakeStore) *server { return watchServer(store) }

func TestOnlyOneReplicaSyncsTheCatalogue(t *testing.T) {
	store := newFakeStore()
	var fetches int
	index := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches++
		w.Write([]byte(oneChartIndex))
	}))
	defer index.Close()
	store.helmRepos["charts"] = &db.HelmRepository{ID: 1, Name: "charts", URL: index.URL}

	// A second replica holds the lease.
	store.leases[db.LeaseHelmIndex] = "somebody-else"

	syncServer(store).helmSyncTick(context.Background())
	if fetches != 0 {
		t.Fatalf("a replica without the lease fetched %d times", fetches)
	}
}

func TestALeaseThatCannotBeReadMeansDoNotPoll(t *testing.T) {
	// An unreachable database is exactly the state in which every replica would
	// otherwise decide it is the one.
	store := newFakeStore()
	store.leaseErr = errors.New("database is down")
	var fetches int
	index := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches++
		w.Write([]byte(oneChartIndex))
	}))
	defer index.Close()
	store.helmRepos["charts"] = &db.HelmRepository{ID: 1, Name: "charts", URL: index.URL}

	syncServer(store).helmSyncTick(context.Background())
	if fetches != 0 {
		t.Fatalf("the pass ran with an unreadable lease (%d fetches)", fetches)
	}
}

func TestTheHolderRefreshesEveryDeclaredRepository(t *testing.T) {
	store := newFakeStore()
	index := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(oneChartIndex))
	}))
	defer index.Close()
	store.helmRepos["a"] = &db.HelmRepository{ID: 1, Name: "a", URL: index.URL}
	store.helmRepos["b"] = &db.HelmRepository{ID: 2, Name: "b", URL: index.URL}

	syncServer(store).helmSyncTick(context.Background())

	for _, name := range []string{"a", "b"} {
		if got := store.helmRepos[name].Status; got != db.HelmRepoOK {
			t.Fatalf("%s status = %q", name, got)
		}
		if store.helmRepos[name].SyncedAt == nil {
			t.Fatalf("%s was not marked synced", name)
		}
	}
}

func TestOneRepositoryBeingDownDoesNotStopTheOthers(t *testing.T) {
	store := newFakeStore()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(oneChartIndex))
	}))
	defer good.Close()

	store.helmRepos["a-broken"] = &db.HelmRepository{
		ID: 1, Name: "a-broken", URL: "http://127.0.0.1:1/nothing-here",
	}
	store.helmRepos["b-good"] = &db.HelmRepository{ID: 2, Name: "b-good", URL: good.URL}

	syncServer(store).helmSyncTick(context.Background())

	if got := store.helmRepos["a-broken"].Status; got != db.HelmRepoError {
		t.Fatalf("the broken repository reads as %q", got)
	}
	if got := store.helmRepos["b-good"].Status; got != db.HelmRepoOK {
		t.Fatalf("the healthy repository was not refreshed: %q", got)
	}
}

func TestAFailedSyncKeepsTheChartsItLastHeld(t *testing.T) {
	// A chart list that empties on a DNS blip reads as "this feature is broken".
	// A stale one with a reported error reads as exactly what it is.
	store := newFakeStore()
	store.helmRepos["charts"] = &db.HelmRepository{
		ID: 1, Name: "charts", URL: "http://127.0.0.1:1/gone",
		Status: db.HelmRepoOK, ChartCount: 1,
	}
	store.helmCharts[1] = []db.HelmChart{
		{RepositoryID: 1, Name: "web", Versions: `[{"version":"1.0.0"}]`},
	}

	syncServer(store).helmSyncTick(context.Background())

	if len(store.helmCharts[1]) != 1 {
		t.Fatal("a failed sync emptied the catalogue")
	}
	repository := store.helmRepos["charts"]
	if repository.Status != db.HelmRepoError || repository.StatusMessage == "" {
		t.Fatalf("the failure was not recorded: %+v", repository)
	}
	if repository.ChartCount != 1 {
		t.Fatalf("chart count = %d, want the count it still holds", repository.ChartCount)
	}
}

func TestASuccessfulSyncReplacesRatherThanMergesTheCatalogue(t *testing.T) {
	// The catalogue *is* the index: a chart the repository stopped publishing
	// has to disappear, and a merge would leave it there for ever.
	store := newFakeStore()
	index := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(oneChartIndex))
	}))
	defer index.Close()
	store.helmRepos["charts"] = &db.HelmRepository{ID: 1, Name: "charts", URL: index.URL}
	store.helmCharts[1] = []db.HelmChart{
		{RepositoryID: 1, Name: "withdrawn", Versions: `[{"version":"1.0.0"}]`},
	}

	syncServer(store).helmSyncTick(context.Background())

	for _, chart := range store.helmCharts[1] {
		if chart.Name == "withdrawn" {
			t.Fatal("a chart the repository no longer publishes survived the sync")
		}
	}
}
