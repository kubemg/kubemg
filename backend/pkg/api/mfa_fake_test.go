package api

import (
	"context"
	"sort"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// The second-factor tables, as the fake store holds them. The conditional
// writes keep the real store's semantics — a step that does not advance, a
// spent code, a taken challenge all answer false — because those are what the
// replay rules rest on.
type mfaTables struct {
	factors    map[uint]*db.MFAFactor
	codes      map[uint][]*db.MFARecoveryCode
	challenges map[string]db.MFAChallenge
}

func (f *fakeStore) mfaTables() *mfaTables {
	if f.mfa == nil {
		f.mfa = &mfaTables{
			factors:    map[uint]*db.MFAFactor{},
			codes:      map[uint][]*db.MFARecoveryCode{},
			challenges: map[string]db.MFAChallenge{},
		}
	}
	return f.mfa
}

func (f *fakeStore) MFAState(_ context.Context, userID uint) (db.MFAState, error) {
	if f.mfaErr != nil {
		return db.MFAState{}, f.mfaErr
	}
	t := f.mfaTables()
	state := db.MFAState{Factors: []db.MFAFactor{}}
	for _, factor := range t.factors {
		if factor.UserID == userID && factor.ConfirmedAt != nil {
			state.Factors = append(state.Factors, *factor)
		}
	}
	sort.Slice(state.Factors, func(i, j int) bool { return state.Factors[i].ID < state.Factors[j].ID })
	for _, code := range t.codes[userID] {
		if code.UsedAt == nil {
			state.RecoveryCodesRemaining++
		}
	}
	return state, nil
}

func (f *fakeStore) MFAEnrolledUserIDs(_ context.Context) ([]uint, error) {
	if f.mfaErr != nil {
		return nil, f.mfaErr
	}
	seen := map[uint]bool{}
	var ids []uint
	for _, factor := range f.mfaTables().factors {
		if factor.ConfirmedAt != nil && !seen[factor.UserID] {
			seen[factor.UserID] = true
			ids = append(ids, factor.UserID)
		}
	}
	return ids, nil
}

func (f *fakeStore) MFAFactorByID(_ context.Context, userID, id uint) (*db.MFAFactor, error) {
	if f.mfaErr != nil {
		return nil, f.mfaErr
	}
	factor, ok := f.mfaTables().factors[id]
	if !ok || factor.UserID != userID {
		return nil, db.ErrNotFound
	}
	copied := *factor
	return &copied, nil
}

func (f *fakeStore) CreatePendingMFAFactor(_ context.Context, factor *db.MFAFactor) error {
	if f.mfaErr != nil {
		return f.mfaErr
	}
	t := f.mfaTables()
	for id, existing := range t.factors {
		if existing.UserID == factor.UserID && existing.ConfirmedAt == nil {
			delete(t.factors, id)
		}
	}
	factor.ID = f.nextID
	f.nextID++
	factor.ConfirmedAt = nil
	factor.CreatedAt = time.Now().UTC()
	stored := *factor
	t.factors[factor.ID] = &stored
	return nil
}

func (f *fakeStore) ConfirmMFAFactor(
	_ context.Context, userID, id uint, step int64, secret string, at time.Time, hashes []string,
) error {
	if f.mfaErr != nil {
		return f.mfaErr
	}
	t := f.mfaTables()
	factor, ok := t.factors[id]
	if !ok || factor.UserID != userID || factor.ConfirmedAt != nil {
		return db.ErrNotFound
	}
	factor.ConfirmedAt = &at
	factor.LastUsedAt = &at
	factor.LastStep = step
	if secret != "" {
		factor.Secret = secret
	}
	if hashes != nil {
		f.replaceCodes(userID, hashes)
	}
	return nil
}

func (f *fakeStore) replaceCodes(userID uint, hashes []string) {
	t := f.mfaTables()
	t.codes[userID] = nil
	for _, hash := range hashes {
		t.codes[userID] = append(t.codes[userID], &db.MFARecoveryCode{UserID: userID, CodeHash: hash})
	}
}

func (f *fakeStore) AdvanceMFAStep(_ context.Context, factorID uint, step int64, at time.Time) (bool, error) {
	if f.mfaErr != nil {
		return false, f.mfaErr
	}
	factor, ok := f.mfaTables().factors[factorID]
	if !ok || factor.ConfirmedAt == nil || factor.LastStep >= step {
		return false, nil
	}
	factor.LastStep = step
	factor.LastUsedAt = &at
	return true, nil
}

func (f *fakeStore) AdvanceMFACounter(_ context.Context, factorID uint, counter int64, at time.Time) (bool, error) {
	if f.mfaErr != nil {
		return false, f.mfaErr
	}
	factor, ok := f.mfaTables().factors[factorID]
	if !ok || factor.ConfirmedAt == nil || factor.Kind != db.MFAKindWebAuthn {
		return false, nil
	}
	if (counter == 0 && factor.LastStep != 0) || (counter != 0 && factor.LastStep >= counter) {
		return false, nil
	}
	factor.LastStep = counter
	factor.LastUsedAt = &at
	return true, nil
}

func (f *fakeStore) DeleteMFAFactor(_ context.Context, userID, id uint) error {
	if f.mfaErr != nil {
		return f.mfaErr
	}
	t := f.mfaTables()
	factor, ok := t.factors[id]
	if !ok || factor.UserID != userID || factor.ConfirmedAt == nil {
		return db.ErrNotFound
	}
	delete(t.factors, id)
	for _, other := range t.factors {
		if other.UserID == userID && other.ConfirmedAt != nil {
			return nil
		}
	}
	delete(t.codes, userID)
	return nil
}

func (f *fakeStore) ReplaceMFARecoveryCodes(_ context.Context, userID uint, hashes []string) error {
	if f.mfaErr != nil {
		return f.mfaErr
	}
	f.replaceCodes(userID, hashes)
	return nil
}

func (f *fakeStore) UseMFARecoveryCode(_ context.Context, userID uint, hash string, at time.Time) (bool, error) {
	if f.mfaErr != nil {
		return false, f.mfaErr
	}
	for _, code := range f.mfaTables().codes[userID] {
		if code.CodeHash == hash && code.UsedAt == nil {
			code.UsedAt = &at
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) ResetMFA(_ context.Context, userID uint) (int64, error) {
	if f.mfaErr != nil {
		return 0, f.mfaErr
	}
	t := f.mfaTables()
	var removed int64
	for id, factor := range t.factors {
		if factor.UserID == userID {
			if factor.ConfirmedAt != nil {
				removed++
			}
			delete(t.factors, id)
		}
	}
	delete(t.codes, userID)
	for hash, challenge := range t.challenges {
		if challenge.UserID == userID {
			delete(t.challenges, hash)
		}
	}
	return removed, nil
}

func (f *fakeStore) PutMFAChallenge(_ context.Context, challenge *db.MFAChallenge) error {
	if f.mfaErr != nil {
		return f.mfaErr
	}
	f.mfaTables().challenges[challenge.TokenHash] = *challenge
	return nil
}

func (f *fakeStore) MFAChallengeByHash(_ context.Context, hash string) (*db.MFAChallenge, error) {
	if f.mfaErr != nil {
		return nil, f.mfaErr
	}
	challenge, ok := f.mfaTables().challenges[hash]
	if !ok || !challenge.ExpiresAt.After(time.Now()) {
		return nil, db.ErrNotFound
	}
	return &challenge, nil
}

func (f *fakeStore) TakeMFAChallenge(_ context.Context, hash string) (bool, error) {
	if f.mfaErr != nil {
		return false, f.mfaErr
	}
	t := f.mfaTables()
	challenge, ok := t.challenges[hash]
	if !ok || !challenge.ExpiresAt.After(time.Now()) {
		return false, nil
	}
	delete(t.challenges, hash)
	return true, nil
}

func (f *fakeStore) TakeMFACeremony(_ context.Context, hash string) (*db.MFAChallenge, error) {
	if f.mfaErr != nil {
		return nil, f.mfaErr
	}
	t := f.mfaTables()
	challenge, ok := t.challenges[hash]
	if !ok || challenge.Purpose != db.MFAChallengeWebAuthn || !challenge.ExpiresAt.After(time.Now()) {
		return nil, nil
	}
	delete(t.challenges, hash)
	return &challenge, nil
}
