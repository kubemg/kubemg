package api

import (
	"context"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// The alarm half of fakeStore. It lives in its own file rather than in
// router_test.go because it is a self-contained table pair and router_test.go is
// already the longest fake in the package.
//
// Maps keyed by id, like the tables they stand in for. Ordering is reconstructed
// from the id on read, so a list is stable — a test asserting on the second rule
// should not depend on Go's map iteration.

type alarmTables struct {
	channels map[uint]*db.AlarmChannel
	rules    map[uint]*db.AlarmRule
}

func (f *fakeStore) alarmTables() *alarmTables {
	if f.alarms == nil {
		f.alarms = &alarmTables{
			channels: map[uint]*db.AlarmChannel{},
			rules:    map[uint]*db.AlarmRule{},
		}
	}
	return f.alarms
}

// addAlarmChannel seeds a destination.
func (f *fakeStore) addAlarmChannel(channel db.AlarmChannel) *db.AlarmChannel {
	tables := f.alarmTables()
	channel.ID = f.nextID
	f.nextID++
	if channel.CreatedAt.IsZero() {
		channel.CreatedAt = time.Now()
	}
	tables.channels[channel.ID] = &channel
	return &channel
}

// addAlarmRule seeds a rule.
func (f *fakeStore) addAlarmRule(rule db.AlarmRule) *db.AlarmRule {
	tables := f.alarmTables()
	rule.ID = f.nextID
	f.nextID++
	tables.rules[rule.ID] = &rule
	return &rule
}

func (f *fakeStore) ListAlarmChannels(_ context.Context) ([]db.AlarmChannel, error) {
	tables := f.alarmTables()
	out := []db.AlarmChannel{}
	for id := uint(1); id <= f.nextID; id++ {
		if channel, ok := tables.channels[id]; ok {
			out = append(out, *channel)
		}
	}
	return out, nil
}

func (f *fakeStore) AlarmChannelByID(_ context.Context, id uint) (*db.AlarmChannel, error) {
	channel, ok := f.alarmTables().channels[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	copied := *channel
	return &copied, nil
}

func (f *fakeStore) CreateAlarmChannel(_ context.Context, channel *db.AlarmChannel) error {
	tables := f.alarmTables()
	for _, existing := range tables.channels {
		if existing.Name == channel.Name {
			return db.ErrConflict
		}
	}
	channel.ID = f.nextID
	f.nextID++
	channel.CreatedAt = time.Now()
	channel.UpdatedAt = channel.CreatedAt
	copied := *channel
	tables.channels[channel.ID] = &copied
	return nil
}

func (f *fakeStore) UpdateAlarmChannel(_ context.Context, channel *db.AlarmChannel) error {
	tables := f.alarmTables()
	stored, ok := tables.channels[channel.ID]
	if !ok {
		return db.ErrNotFound
	}
	for id, existing := range tables.channels {
		if id != channel.ID && existing.Name == channel.Name {
			return db.ErrConflict
		}
	}

	stored.Name = channel.Name
	stored.Kind = channel.Kind
	stored.URL = channel.URL
	stored.AuthMode = channel.AuthMode
	stored.Username = channel.Username
	stored.Headers = channel.Headers
	stored.Enabled = channel.Enabled
	stored.UpdatedAt = time.Now()
	// The store keeps the stored secret when none was supplied, which is the
	// behaviour the handlers depend on — so the fake has to do it too or the tests
	// would pass against a rule the real store does not follow.
	if channel.Secret != "" {
		stored.Secret = channel.Secret
	}
	if channel.AuthMode == db.AuthNone {
		stored.Secret = ""
		stored.Username = ""
	}
	return nil
}

func (f *fakeStore) DeleteAlarmChannel(_ context.Context, id uint) error {
	tables := f.alarmTables()
	if _, ok := tables.channels[id]; !ok {
		return db.ErrNotFound
	}
	delete(tables.channels, id)
	for ruleID, rule := range tables.rules {
		if rule.ChannelID == id {
			delete(tables.rules, ruleID)
		}
	}
	return nil
}

func (f *fakeStore) RecordAlarmDelivery(_ context.Context, id uint, status, message string) error {
	if channel, ok := f.alarmTables().channels[id]; ok {
		now := time.Now()
		channel.LastStatus = status
		channel.LastMessage = message
		channel.LastAttemptAt = &now
	}
	return nil
}

func (f *fakeStore) ListAlarmRules(_ context.Context) ([]db.AlarmRule, error) {
	tables := f.alarmTables()
	out := []db.AlarmRule{}
	for id := uint(1); id <= f.nextID; id++ {
		if rule, ok := tables.rules[id]; ok {
			out = append(out, *rule)
		}
	}
	return out, nil
}

func (f *fakeStore) AlarmRuleByID(_ context.Context, id uint) (*db.AlarmRule, error) {
	rule, ok := f.alarmTables().rules[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	copied := *rule
	return &copied, nil
}

func (f *fakeStore) CreateAlarmRule(_ context.Context, rule *db.AlarmRule) error {
	tables := f.alarmTables()
	rule.ID = f.nextID
	f.nextID++
	copied := *rule
	tables.rules[rule.ID] = &copied
	return nil
}

func (f *fakeStore) UpdateAlarmRule(_ context.Context, rule *db.AlarmRule) error {
	tables := f.alarmTables()
	if _, ok := tables.rules[rule.ID]; !ok {
		return db.ErrNotFound
	}
	copied := *rule
	tables.rules[rule.ID] = &copied
	return nil
}

func (f *fakeStore) DeleteAlarmRule(_ context.Context, id uint) error {
	tables := f.alarmTables()
	if _, ok := tables.rules[id]; !ok {
		return db.ErrNotFound
	}
	delete(tables.rules, id)
	return nil
}

func (f *fakeStore) RecordAlarmFired(_ context.Context, id uint, at time.Time) error {
	if rule, ok := f.alarmTables().rules[id]; ok {
		stamped := at
		rule.LastFiredAt = &stamped
		rule.FireCount++
	}
	return nil
}
