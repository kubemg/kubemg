package db

import (
	"testing"
	"time"
)

// What a request means at a given instant, and what happens when a temporary grant
// is merged with a standing one. Both are read on the authorization path, so both
// are worth pinning down here rather than only through the HTTP layer.

func TestJitRequestLiveness(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	future := now.Add(30 * time.Minute)
	past := now.Add(-time.Minute)

	cases := []struct {
		name      string
		request   JitRequest
		live      bool
		remaining int64
	}{
		{
			name:      "an active window still open",
			request:   JitRequest{Status: JitStatusActive, ExpiresAt: &future},
			live:      true,
			remaining: 1800,
		},
		{
			// The row still says active because the sweeper runs on a timer. The
			// resolver has already stopped honouring it, and this has to agree.
			name:      "an active window that has run out",
			request:   JitRequest{Status: JitStatusActive, ExpiresAt: &past},
			live:      false,
			remaining: 0,
		},
		{
			name:      "a pending request",
			request:   JitRequest{Status: JitStatusPending},
			live:      false,
			remaining: 0,
		},
		{
			name:      "a revoked request",
			request:   JitRequest{Status: JitStatusRevoked, ExpiresAt: &future},
			live:      false,
			remaining: 0,
		},
		{
			// `approved` is treated as live everywhere `active` is; see the status
			// block in jit_models.go for why both exist.
			name:      "an approved request",
			request:   JitRequest{Status: JitStatusApproved, ExpiresAt: &future},
			live:      true,
			remaining: 1800,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.request.Live(now); got != tc.live {
				t.Fatalf("Live: want %v, got %v", tc.live, got)
			}
			if got := tc.request.RemainingSeconds(now); got != tc.remaining {
				t.Fatalf("RemainingSeconds: want %d, got %d", tc.remaining, got)
			}
		})
	}
}

// TestMergeAccessExpiry is the rule that makes an elevation safe to expire: merged
// with a standing grant, the *access* does not end — only the elevated part of it
// does, and the merged row must not claim otherwise.
func TestMergeAccessExpiry(t *testing.T) {
	soon := time.Date(2026, 7, 31, 12, 30, 0, 0, time.UTC)
	later := soon.Add(time.Hour)

	standing := UserClusterAccess{K8sRole: K8sRoleView}
	elevated := UserClusterAccess{K8sRole: K8sRoleClusterAdmin, ExpiresAt: &soon}

	merged := MergeAccess(standing, elevated)
	if merged.K8sRole != K8sRoleClusterAdmin {
		t.Fatalf("want the elevation to win, got %q", merged.K8sRole)
	}
	if merged.ExpiresAt != nil {
		t.Fatalf("a standing grant makes the merged access permanent, got %v", merged.ExpiresAt)
	}

	// Two temporary grants: the merged access lasts as long as the longer of them.
	first := UserClusterAccess{K8sRole: K8sRoleEdit, ExpiresAt: &soon}
	second := UserClusterAccess{K8sRole: K8sRoleView, ExpiresAt: &later}
	if got := MergeAccess(first, second); got.ExpiresAt == nil || !got.ExpiresAt.Equal(later) {
		t.Fatalf("want the later expiry, got %v", got.ExpiresAt)
	}
	if got := MergeAccess(second, first); got.ExpiresAt == nil || !got.ExpiresAt.Equal(later) {
		t.Fatalf("want the later expiry whichever way round, got %v", got.ExpiresAt)
	}
}
