# Task: Implement Resource & Metric Loading Performance & Skeleton UI

Refer to `implementation_plan.md` and `PROJECT_KNOWLEDGE.md` before starting. Follow `agentrule.md` strictly (run builds and tests via Docker using `make verify` / `make test`).

## Key Tasks to Implement:

1. **Backend Scoped In-Memory Cache (`backend/pkg/cache/cache.go`)**:
   - Create thread-safe in-memory cache with configurable TTL (default 5 seconds).
   - Compute cache keys using: `hash(clusterID, userID, userGroups, namespace, resourceKind, queryParams)`.
   - Wrap `GET /clusters/:id/resources/list` in `pkg/api/resources.go` and `GET /observability/metrics/query` in `pkg/observability/metrics_query.go` with cache lookups. Bypass on `Cache-Control: no-cache`.

2. **Frontend Skeleton UI & SWR Caching (`frontend/src/components/SkeletonLoader.tsx`)**:
   - Create `SkeletonLoader.tsx` matching Signal Deck styling (table skeleton, card skeleton, meter skeleton) with shimmer animations using CSS tokens (`--deck-bg-muted`).
   - Update `ExplorePage.tsx` and `ClusterDetail.tsx` to display skeleton loaders during initial loads.
   - Configure React Query / SWR with `staleTime: 5000` and `keepPreviousData: true` so tab/sidebar navigation is instant.

3. **Verification**:
   - Run `make verify` inside Docker containers.
   - Ensure all backend tests and frontend builds pass cleanly.
