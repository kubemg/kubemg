package api

import (
	"context"
	"sort"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// The CLI tables as the fake store holds them. The conditional writes keep the
// real store's single-winner semantics, which is what the device flow rests on.
type cliTables struct {
	codes    map[string]*db.CLIDeviceCode
	sessions map[string]*db.CLISession
}

func (f *fakeStore) cliTables() *cliTables {
	if f.cli == nil {
		f.cli = &cliTables{codes: map[string]*db.CLIDeviceCode{}, sessions: map[string]*db.CLISession{}}
	}
	return f.cli
}

func (f *fakeStore) CreateCLIDeviceCode(_ context.Context, code *db.CLIDeviceCode) error {
	t := f.cliTables()
	for _, existing := range t.codes {
		if existing.UserCode == code.UserCode && existing.ExpiresAt.After(time.Now()) {
			return db.ErrDeviceCodeTaken
		}
	}
	code.CreatedAt = time.Now().UTC()
	stored := *code
	t.codes[code.DeviceCodeHash] = &stored
	return nil
}

func (f *fakeStore) PendingCLIDeviceCodesFrom(_ context.Context, sourceIP string, now time.Time) (int64, error) {
	var n int64
	for _, code := range f.cliTables().codes {
		if code.SourceIP == sourceIP && code.Status == db.CLIDevicePending && code.ExpiresAt.After(now) {
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) CLIDeviceCodeByUserCode(_ context.Context, userCode string, now time.Time) (*db.CLIDeviceCode, error) {
	for _, code := range f.cliTables().codes {
		if code.UserCode == userCode && code.ExpiresAt.After(now) {
			copied := *code
			return &copied, nil
		}
	}
	return nil, db.ErrNotFound
}

func (f *fakeStore) CLIDeviceCodeByHash(_ context.Context, hash string) (*db.CLIDeviceCode, error) {
	code, ok := f.cliTables().codes[hash]
	if !ok {
		return nil, db.ErrNotFound
	}
	copied := *code
	return &copied, nil
}

func (f *fakeStore) DecideCLIDeviceCode(_ context.Context, userCode, status string, userID uint, now time.Time) (bool, error) {
	for _, code := range f.cliTables().codes {
		if code.UserCode == userCode && code.Status == db.CLIDevicePending && code.ExpiresAt.After(now) {
			code.Status = status
			code.UserID = userID
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) PollCLIDeviceCode(_ context.Context, hash string, now time.Time) error {
	if code, ok := f.cliTables().codes[hash]; ok {
		code.LastPolledAt = &now
	}
	return nil
}

func (f *fakeStore) TakeCLIDeviceCode(_ context.Context, hash string) (*db.CLIDeviceCode, error) {
	t := f.cliTables()
	code, ok := t.codes[hash]
	if !ok || code.Status == db.CLIDevicePending {
		return nil, db.ErrNotFound
	}
	delete(t.codes, hash)
	return code, nil
}

func (f *fakeStore) CreateCLISession(_ context.Context, session *db.CLISession) error {
	if f.cliErr != nil {
		return f.cliErr
	}
	stored := *session
	f.cliTables().sessions[session.ID] = &stored
	return nil
}

func (f *fakeStore) CLISessionByRefreshHash(_ context.Context, hash string) (*db.CLISession, error) {
	if f.cliErr != nil {
		return nil, f.cliErr
	}
	for _, session := range f.cliTables().sessions {
		if session.RefreshHash == hash {
			copied := *session
			return &copied, nil
		}
	}
	return nil, db.ErrNotFound
}

func (f *fakeStore) CLISessionByID(_ context.Context, id string) (*db.CLISession, error) {
	if f.cliErr != nil {
		return nil, f.cliErr
	}
	session, ok := f.cliTables().sessions[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	copied := *session
	return &copied, nil
}

func (f *fakeStore) ListCLISessions(_ context.Context, filter db.CLISessionFilter) ([]db.CLISession, int64, error) {
	if f.cliErr != nil {
		return nil, 0, f.cliErr
	}
	now := filter.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	out := []db.CLISession{}
	for _, session := range f.cliTables().sessions {
		if filter.UserID != 0 && session.UserID != filter.UserID {
			continue
		}
		if filter.ActiveOnly && !session.Live(now) {
			continue
		}
		out = append(out, *session)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, int64(len(out)), nil
}

func (f *fakeStore) RevokeCLISession(_ context.Context, id string, at time.Time, by uint, byName string) (*db.CLISession, error) {
	if f.cliErr != nil {
		return nil, f.cliErr
	}
	session, ok := f.cliTables().sessions[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	if session.RevokedAt == nil {
		session.RevokedAt = &at
		session.RevokedBy = by
		session.RevokedByName = byName
	}
	copied := *session
	return &copied, nil
}

func (f *fakeStore) RevokeCLISessionsForUser(_ context.Context, userID uint, at time.Time, by uint, byName string) ([]db.CLISession, error) {
	if f.cliErr != nil {
		return nil, f.cliErr
	}
	live := []db.CLISession{}
	for _, session := range f.cliTables().sessions {
		if session.UserID == userID && session.Live(at) {
			live = append(live, *session)
			session.RevokedAt = &at
			session.RevokedBy = by
			session.RevokedByName = byName
		}
	}
	return live, nil
}

func (f *fakeStore) RevokedCLISessionIDs(_ context.Context, now time.Time) ([]string, error) {
	if f.cliErr != nil {
		return nil, f.cliErr
	}
	ids := []string{}
	for _, session := range f.cliTables().sessions {
		if session.RevokedAt != nil && session.ExpiresAt.After(now) {
			ids = append(ids, session.ID)
		}
	}
	return ids, nil
}

func (f *fakeStore) TouchCLISession(_ context.Context, id string, at time.Time) error {
	if session, ok := f.cliTables().sessions[id]; ok {
		session.LastUsedAt = &at
	}
	return nil
}

func (f *fakeStore) PruneCLISessions(_ context.Context, before time.Time) (int64, error) {
	var n int64
	t := f.cliTables()
	for id, session := range t.sessions {
		if session.ExpiresAt.Before(before) {
			delete(t.sessions, id)
			n++
		}
	}
	return n, nil
}
