package api

import (
	"context"
	"sort"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// The machine_tokens table, as the fake store holds it. It is a slice rather
// than a map because the ordering the handlers rely on — newest first — is part
// of what is being tested.

func (f *fakeStore) CreateMachineToken(_ context.Context, token *db.MachineToken) error {
	token.ID = f.nextID
	f.nextID++
	if token.CreatedAt.IsZero() {
		token.CreatedAt = time.Now().UTC()
	}
	stored := *token
	f.machineTokens = append(f.machineTokens, &stored)
	return nil
}

func (f *fakeStore) MachineTokenByHash(_ context.Context, hash string) (*db.MachineToken, error) {
	for _, token := range f.machineTokens {
		if token.TokenHash == hash {
			out := *token
			return &out, nil
		}
	}
	return nil, db.ErrNotFound
}

func (f *fakeStore) MachineTokenByID(_ context.Context, id uint) (*db.MachineToken, error) {
	for _, token := range f.machineTokens {
		if token.ID == id {
			out := *token
			return &out, nil
		}
	}
	return nil, db.ErrNotFound
}

func (f *fakeStore) ListMachineTokens(_ context.Context, userID uint) ([]db.MachineToken, error) {
	out := []db.MachineToken{}
	for _, token := range f.machineTokens {
		if userID != 0 && token.UserID != userID {
			continue
		}
		out = append(out, *token)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (f *fakeStore) RevokeMachineToken(_ context.Context, id uint, at time.Time) (*db.MachineToken, error) {
	for _, token := range f.machineTokens {
		if token.ID != id {
			continue
		}
		if token.RevokedAt == nil {
			stamp := at
			token.RevokedAt = &stamp
		}
		out := *token
		return &out, nil
	}
	return nil, db.ErrNotFound
}

func (f *fakeStore) TouchMachineToken(_ context.Context, id uint, at time.Time) error {
	for _, token := range f.machineTokens {
		if token.ID == id {
			stamp := at
			token.LastUsedAt = &stamp
			return nil
		}
	}
	return db.ErrNotFound
}

// addMachineAccount seeds a programmatic identity the way the handler creates
// one: no password at all, never an administrator.
func (f *fakeStore) addMachineAccount(username string) *db.User {
	user := &db.User{
		ID:          f.nextID,
		Username:    username,
		AccountType: db.AccountTypeMachine,
		SystemRole:  db.SystemRoleUser,
		IsActive:    true,
		CreatedAt:   time.Now(),
	}
	user.Normalize()
	f.nextID++
	f.users[user.ID] = user
	return user
}
