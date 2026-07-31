package api

import (
	"context"
	"sort"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// The guardrail half of fakeStore. It lives in its own file for the same reason
// the alarm half does: a self-contained table, and router_test.go is already the
// longest fake in the package.

func (f *fakeStore) guardrailTable() map[uint]*db.GuardrailPolicy {
	if f.guardrails == nil {
		f.guardrails = map[uint]*db.GuardrailPolicy{}
	}
	return f.guardrails
}

// addGuardrail seeds a rule.
func (f *fakeStore) addGuardrail(policy db.GuardrailPolicy) *db.GuardrailPolicy {
	table := f.guardrailTable()
	policy.ID = f.nextID
	f.nextID++
	if policy.Target == "" {
		policy.Target = db.GuardrailTargetBoth
	}
	if policy.Action == "" {
		policy.Action = db.GuardrailActionBlock
	}
	if policy.CreatedAt.IsZero() {
		policy.CreatedAt = time.Now()
	}
	table[policy.ID] = &policy
	return &policy
}

func (f *fakeStore) ListGuardrailPolicies(_ context.Context) ([]db.GuardrailPolicy, error) {
	table := f.guardrailTable()
	out := make([]db.GuardrailPolicy, 0, len(table))
	for _, policy := range table {
		out = append(out, *policy)
	}
	// The real store orders global rules first and then by id, and a test
	// asserting on the second row must not depend on map iteration.
	sort.Slice(out, func(i, j int) bool {
		if out[i].ClusterID != out[j].ClusterID {
			return out[i].ClusterID < out[j].ClusterID
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (f *fakeStore) GuardrailPolicyByID(_ context.Context, id uint) (*db.GuardrailPolicy, error) {
	policy, ok := f.guardrailTable()[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	copied := *policy
	return &copied, nil
}

func (f *fakeStore) CreateGuardrailPolicy(_ context.Context, policy *db.GuardrailPolicy) error {
	table := f.guardrailTable()
	policy.ID = f.nextID
	f.nextID++
	policy.CreatedAt = time.Now()
	policy.UpdatedAt = policy.CreatedAt
	stored := *policy
	table[policy.ID] = &stored
	return nil
}

func (f *fakeStore) UpdateGuardrailPolicy(_ context.Context, policy *db.GuardrailPolicy) error {
	table := f.guardrailTable()
	if _, ok := table[policy.ID]; !ok {
		return db.ErrNotFound
	}
	policy.UpdatedAt = time.Now()
	stored := *policy
	table[policy.ID] = &stored
	return nil
}

func (f *fakeStore) DeleteGuardrailPolicy(_ context.Context, id uint) error {
	table := f.guardrailTable()
	if _, ok := table[id]; !ok {
		return db.ErrNotFound
	}
	delete(table, id)
	return nil
}
