package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/guardrails"
	"github.com/kubemg/kubemg/backend/pkg/k8s"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}

// fakeStore is an in-memory Store used to exercise the HTTP layer without a
// live PostgreSQL instance.
type fakeStore struct {
	users       map[uint]*db.User
	clusters    map[uint]*db.Cluster
	access      map[uint]map[uint]db.UserClusterAccess // userID -> clusterID -> grant
	groups      map[uint]*db.Group
	members     map[uint]map[uint]bool                  // groupID -> userID -> member
	groupAccess map[uint]map[uint]db.GroupClusterAccess // groupID -> clusterID -> grant
	audit       []db.AuditEvent
	// recordings stands in for the terminal_sessions table.
	recordings  []db.TerminalSession
	settings    map[string]string
	// sources holds the observability datasources, keyed the way the table is:
	// one per cluster per kind.
	sources map[uint]map[string]db.ObservabilitySource
	// consoles holds the external console links, keyed the same way.
	consoles map[uint]map[string]db.ClusterConsole
	// Federation: the providers and the rules that say what an external group is
	// worth. Keyed by id like the tables they stand in for.
	providers   map[uint]*db.SSOProviderConfig
	mappings    map[uint]*db.SSOGroupMapping
	syncResults map[string]*db.SSOSyncResult
	syncErr     error
	// syncedIdentities records what the handlers passed down, so a test can
	// assert on the identity a protocol engine resolved rather than only on the
	// session that came back.
	syncedIdentities []db.SSOIdentity
	nextID           uint
	createErr   error
	// pruned records the cutoff of every retention pass, so a test can assert
	// on the window the pruner chose rather than only on what survived.
	pruned   []time.Time
	pruneErr error
	// alarms holds the channel and rule tables; see alarms_fake_test.go.
	alarms *alarmTables
	// guardrails stands in for the guardrail_policies table; see
	// guardrails_fake_test.go.
	guardrails map[uint]*db.GuardrailPolicy
	// jit stands in for the jit_requests table, and jitGrants for the temporary
	// rows of user_cluster_access an approval writes — a second dimension because
	// `access` above cannot hold two provenances for one (user, cluster), which is
	// exactly the shape the real table gained. See jit_fake_test.go.
	jit       map[string]*db.JitRequest
	jitGrants map[uint]map[uint]db.UserClusterAccess
	// now is the clock the fake resolves expiring grants against. A test that
	// moves an elevation past its window sets it, so the fake and the workflow
	// engine agree about what time it is — two clocks would make the expiry
	// assertions depend on how long the test took to run.
	now func() time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users:       map[uint]*db.User{},
		clusters:    map[uint]*db.Cluster{},
		access:      map[uint]map[uint]db.UserClusterAccess{},
		groups:      map[uint]*db.Group{},
		members:     map[uint]map[uint]bool{},
		groupAccess: map[uint]map[uint]db.GroupClusterAccess{},
		settings:    map[string]string{},
		sources:     map[uint]map[string]db.ObservabilitySource{},
		consoles:    map[uint]map[string]db.ClusterConsole{},
		providers:   map[uint]*db.SSOProviderConfig{},
		mappings:    map[uint]*db.SSOGroupMapping{},
		syncResults: map[string]*db.SSOSyncResult{},
		nextID:      1,
	}
}

func (f *fakeStore) addUser(username, password, role string) *db.User {
	hash, err := auth.HashPassword(password)
	if err != nil {
		panic(err)
	}
	user := &db.User{
		ID:           f.nextID,
		Username:     username,
		PasswordHash: hash,
		Role:         role,
		SystemRole:   role,
		IsActive:     true,
		CreatedAt:    time.Now(),
	}
	f.nextID++
	f.users[user.ID] = user
	return user
}

// addSuperAdmin is the protected top-level account. The legacy role column is
// what the JWT and the admin middleware read, so it stays "admin"; the system
// role is what the IAM rules reason about.
func (f *fakeStore) addSuperAdmin(username, password string) *db.User {
	user := f.addUser(username, password, db.RoleAdmin)
	user.SystemRole = db.SystemRoleSuperAdmin
	return user
}

// addRecordingViewer is an administrator who also holds the recording-viewer
// capability — the account that may replay somebody else's session. The two are
// separate on purpose, so a test that only needs an admin does not accidentally
// assert that every admin can watch recordings.
func (f *fakeStore) addRecordingViewer(username, password string) *db.User {
	user := f.addUser(username, password, db.RoleAdmin)
	user.CanViewRecordings = true
	return user
}

func (f *fakeStore) addGroup(name string) *db.Group {
	group := &db.Group{ID: f.nextID, Name: name, CreatedAt: time.Now()}
	f.nextID++
	f.groups[group.ID] = group
	return group
}

func (f *fakeStore) addCluster(name, environment string) *db.Cluster {
	cluster := &db.Cluster{
		ID:                  f.nextID,
		Name:                name,
		Environment:         environment,
		APIURL:              "https://" + name + ".example.com:6443",
		ServiceAccountToken: "secret-token",
		CACertData:          base64.StdEncoding.EncodeToString([]byte(testPEM)),
		Status:              db.StatusPending,
		ConnectionMode:      db.ModeDirect,
		CreatedAt:           time.Now(),
	}
	f.nextID++
	f.clusters[cluster.ID] = cluster
	return cluster
}

// addAgentCluster registers a cluster that is reached through a tunnel rather
// than by dialling its API server.
func (f *fakeStore) addAgentCluster(name, environment, token string) *db.Cluster {
	cluster := &db.Cluster{
		ID:             f.nextID,
		Name:           name,
		Environment:    environment,
		Status:         db.StatusPending,
		ConnectionMode: db.ModeAgent,
		AgentToken:     token,
		CreatedAt:      time.Now(),
	}
	f.nextID++
	f.clusters[cluster.ID] = cluster
	return cluster
}

func (f *fakeStore) grant(userID, clusterID uint, role string, namespaces []string) {
	if f.access[userID] == nil {
		f.access[userID] = map[uint]db.UserClusterAccess{}
	}
	f.access[userID][clusterID] = db.UserClusterAccess{
		ID:         f.nextID,
		UserID:     userID,
		ClusterID:  clusterID,
		K8sRole:    role,
		Namespaces: db.JoinNamespaces(namespaces),
	}
	f.nextID++
}

func (f *fakeStore) UserByUsername(_ context.Context, username string) (*db.User, error) {
	for _, u := range f.users {
		if u.Username == username {
			return u, nil
		}
	}
	return nil, db.ErrNotFound
}

func (f *fakeStore) UserByID(_ context.Context, id uint) (*db.User, error) {
	if u, ok := f.users[id]; ok {
		return u, nil
	}
	return nil, db.ErrNotFound
}

func (f *fakeStore) ClustersForUser(_ context.Context, user *db.User) ([]db.Cluster, error) {
	effective, _ := f.AccessForUser(context.Background(), user.ID)
	out := []db.Cluster{}
	for id, cluster := range f.clusters {
		if !user.IsAdmin() {
			if _, ok := effective[id]; !ok {
				continue
			}
		}
		out = append(out, *cluster)
	}
	return out, nil
}

func (f *fakeStore) AccessForUser(_ context.Context, userID uint) (map[uint]db.UserClusterAccess, error) {
	out := map[uint]db.UserClusterAccess{}
	for clusterID, grant := range f.access[userID] {
		out[clusterID] = grant
	}
	// Live temporary elevations, merged on top exactly as the store does it: an
	// expired one is dropped here rather than waiting for a sweeper, because that
	// is the behaviour the proxy depends on.
	now := f.clock()
	for clusterID, grant := range f.jitGrants[userID] {
		if grant.ExpiresAt != nil && !grant.ExpiresAt.After(now) {
			continue
		}
		if existing, ok := out[clusterID]; ok {
			out[clusterID] = db.MergeAccess(existing, grant)
			continue
		}
		out[clusterID] = grant
	}
	for groupID, users := range f.members {
		if !users[userID] {
			continue
		}
		for clusterID, grant := range f.groupAccess[groupID] {
			candidate := db.UserClusterAccess{
				UserID:     userID,
				ClusterID:  clusterID,
				K8sRole:    grant.K8sRole,
				Namespaces: grant.Namespaces,
			}
			if existing, ok := out[clusterID]; ok {
				out[clusterID] = db.MergeAccess(existing, candidate)
				continue
			}
			out[clusterID] = candidate
		}
	}
	return out, nil
}

func (f *fakeStore) Clusters(_ context.Context) ([]db.Cluster, error) {
	out := []db.Cluster{}
	for _, cluster := range f.clusters {
		out = append(out, *cluster)
	}
	return out, nil
}

func (f *fakeStore) ClusterByID(_ context.Context, id uint) (*db.Cluster, error) {
	if cluster, ok := f.clusters[id]; ok {
		return cluster, nil
	}
	return nil, db.ErrNotFound
}

func (f *fakeStore) ClusterByAgentToken(_ context.Context, token string) (*db.Cluster, error) {
	if token == "" {
		return nil, db.ErrNotFound
	}
	for _, cluster := range f.clusters {
		if cluster.AgentToken == token {
			return cluster, nil
		}
	}
	return nil, db.ErrNotFound
}

func (f *fakeStore) UpdateClusterAgent(_ context.Context, id uint, state db.AgentState) error {
	cluster, ok := f.clusters[id]
	if !ok {
		return db.ErrNotFound
	}
	at := state.At
	cluster.LastCheckedAt = &at
	if state.Connected {
		cluster.Status = db.StatusHealthy
		cluster.StatusMessage = ""
		cluster.AgentConnectedAt = &at
		if state.AgentVersion != "" {
			cluster.AgentVersion = state.AgentVersion
		}
		if state.KubernetesVersion != "" {
			cluster.KubernetesVersion = state.KubernetesVersion
		}
		return nil
	}
	cluster.Status = db.StatusUnhealthy
	cluster.StatusMessage = state.StatusMessage
	cluster.AgentConnectedAt = nil
	return nil
}

func (f *fakeStore) CreateCluster(_ context.Context, cluster *db.Cluster) error {
	if f.createErr != nil {
		return f.createErr
	}
	for _, existing := range f.clusters {
		if existing.Name == cluster.Name {
			return db.ErrConflict
		}
	}
	cluster.ID = f.nextID
	cluster.CreatedAt = time.Now()
	f.nextID++
	stored := *cluster
	f.clusters[cluster.ID] = &stored
	return nil
}

func (f *fakeStore) UpdateClusterHealth(_ context.Context, id uint, health db.ClusterHealth) error {
	cluster, ok := f.clusters[id]
	if !ok {
		return db.ErrNotFound
	}
	checkedAt := health.CheckedAt
	cluster.Status = health.Status
	cluster.StatusMessage = health.StatusMessage
	cluster.KubernetesVersion = health.KubernetesVersion
	cluster.LastCheckedAt = &checkedAt
	return nil
}

func (f *fakeStore) DeleteCluster(_ context.Context, id uint) error {
	if _, ok := f.clusters[id]; !ok {
		return db.ErrNotFound
	}
	delete(f.clusters, id)
	for _, grants := range f.access {
		delete(grants, id)
	}
	return nil
}

func (f *fakeStore) ListUsers(_ context.Context) ([]db.User, error) {
	out := []db.User{}
	for _, user := range f.users {
		out = append(out, *user)
	}
	slices.SortFunc(out, func(a, b db.User) int { return strings.Compare(a.Username, b.Username) })
	return out, nil
}

func (f *fakeStore) CreateUser(_ context.Context, user *db.User) error {
	for _, existing := range f.users {
		if existing.Username == user.Username {
			return db.ErrConflict
		}
	}
	user.Normalize()
	user.ID = f.nextID
	user.CreatedAt = time.Now()
	f.nextID++
	stored := *user
	f.users[user.ID] = &stored
	return nil
}

func (f *fakeStore) UpdateUser(_ context.Context, id uint, update db.UserUpdate) (*db.User, error) {
	user, ok := f.users[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	if update.Username != nil {
		for otherID, other := range f.users {
			if otherID != id && other.Username == *update.Username {
				return nil, db.ErrConflict
			}
		}
		user.Username = *update.Username
	}
	if update.Email != nil {
		user.Email = *update.Email
	}
	if update.PasswordHash != nil {
		user.PasswordHash = *update.PasswordHash
	}
	if update.SystemRole != nil {
		user.SystemRole = *update.SystemRole
		user.Role = db.LegacyRoleFor(*update.SystemRole)
	}
	if update.CanViewRecordings != nil {
		user.CanViewRecordings = *update.CanViewRecordings
	}
	return user, nil
}

func (f *fakeStore) SetUserActive(_ context.Context, id uint, active bool) (*db.User, error) {
	user, ok := f.users[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	user.IsActive = active
	return user, nil
}

func (f *fakeStore) DeleteUser(_ context.Context, id uint) error {
	if _, ok := f.users[id]; !ok {
		return db.ErrNotFound
	}
	delete(f.users, id)
	delete(f.access, id)
	for _, users := range f.members {
		delete(users, id)
	}
	return nil
}

func (f *fakeStore) TouchLastLogin(_ context.Context, id uint, at time.Time) error {
	user, ok := f.users[id]
	if !ok {
		return db.ErrNotFound
	}
	user.LastLoginAt = &at
	return nil
}

func (f *fakeStore) ListGroups(_ context.Context) ([]db.GroupSummary, error) {
	out := []db.GroupSummary{}
	for _, group := range f.groups {
		members := []uint{}
		for userID, in := range f.members[group.ID] {
			if in {
				members = append(members, userID)
			}
		}
		slices.Sort(members)
		out = append(out, db.GroupSummary{Group: *group, MemberIDs: members})
	}
	slices.SortFunc(out, func(a, b db.GroupSummary) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (f *fakeStore) GroupByID(_ context.Context, id uint) (*db.Group, error) {
	if group, ok := f.groups[id]; ok {
		return group, nil
	}
	return nil, db.ErrNotFound
}

func (f *fakeStore) CreateGroup(_ context.Context, group *db.Group) error {
	for _, existing := range f.groups {
		if existing.Name == group.Name {
			return db.ErrConflict
		}
	}
	group.ID = f.nextID
	group.CreatedAt = time.Now()
	f.nextID++
	stored := *group
	f.groups[group.ID] = &stored
	return nil
}

func (f *fakeStore) DeleteGroup(_ context.Context, id uint) error {
	if _, ok := f.groups[id]; !ok {
		return db.ErrNotFound
	}
	delete(f.groups, id)
	delete(f.members, id)
	delete(f.groupAccess, id)
	return nil
}

func (f *fakeStore) AddGroupMember(_ context.Context, groupID, userID uint) error {
	if f.members[groupID] == nil {
		f.members[groupID] = map[uint]bool{}
	}
	f.members[groupID][userID] = true
	return nil
}

func (f *fakeStore) RemoveGroupMember(_ context.Context, groupID, userID uint) error {
	if !f.members[groupID][userID] {
		return db.ErrNotFound
	}
	delete(f.members[groupID], userID)
	return nil
}

func (f *fakeStore) ListUserAccess(_ context.Context) ([]db.UserClusterAccess, error) {
	out := []db.UserClusterAccess{}
	for _, grants := range f.access {
		for _, grant := range grants {
			out = append(out, grant)
		}
	}
	return out, nil
}

func (f *fakeStore) ListGroupAccess(_ context.Context) ([]db.GroupClusterAccess, error) {
	out := []db.GroupClusterAccess{}
	for _, grants := range f.groupAccess {
		for _, grant := range grants {
			out = append(out, grant)
		}
	}
	return out, nil
}

func (f *fakeStore) AssignUserAccess(_ context.Context, grant *db.UserClusterAccess) error {
	if f.access[grant.UserID] == nil {
		f.access[grant.UserID] = map[uint]db.UserClusterAccess{}
	}
	grant.ID = f.nextID
	f.nextID++
	f.access[grant.UserID][grant.ClusterID] = *grant
	return nil
}

func (f *fakeStore) AssignGroupAccess(_ context.Context, grant *db.GroupClusterAccess) error {
	if f.groupAccess[grant.GroupID] == nil {
		f.groupAccess[grant.GroupID] = map[uint]db.GroupClusterAccess{}
	}
	grant.ID = f.nextID
	f.nextID++
	f.groupAccess[grant.GroupID][grant.ClusterID] = *grant
	return nil
}

func (f *fakeStore) RevokeUserAccess(_ context.Context, userID, clusterID uint) error {
	if _, ok := f.access[userID][clusterID]; !ok {
		return db.ErrNotFound
	}
	delete(f.access[userID], clusterID)
	return nil
}

func (f *fakeStore) RevokeGroupAccess(_ context.Context, groupID, clusterID uint) error {
	if _, ok := f.groupAccess[groupID][clusterID]; !ok {
		return db.ErrNotFound
	}
	delete(f.groupAccess[groupID], clusterID)
	return nil
}

func (f *fakeStore) AppendAuditEvents(_ context.Context, events []db.AuditEvent) error {
	for _, event := range events {
		event.ID = f.nextID
		f.nextID++
		f.audit = append(f.audit, event)
	}
	return nil
}

func (f *fakeStore) ListAuditEvents(_ context.Context, filter db.AuditFilter) ([]db.AuditEvent, int64, error) {
	matched := []db.AuditEvent{}
	for _, event := range f.audit {
		if filter.UserID != 0 && event.UserID != filter.UserID {
			continue
		}
		if filter.ClusterID != 0 && event.ClusterID != filter.ClusterID {
			continue
		}
		if filter.Verb != "" && event.Verb != filter.Verb {
			continue
		}
		if len(filter.Verbs) > 0 && !slices.Contains(filter.Verbs, event.Verb) {
			continue
		}
		if filter.Status != 0 && event.Status != filter.Status {
			continue
		}
		if filter.Since != nil && event.At.Before(*filter.Since) {
			continue
		}
		if filter.Until != nil && event.At.After(*filter.Until) {
			continue
		}
		if filter.Namespace != "" && event.Namespace != filter.Namespace {
			continue
		}
		if filter.Streaming && !event.Streaming {
			continue
		}
		if filter.FailedOnly && event.Error == "" && event.Status < 400 {
			continue
		}
		if filter.Search != "" && !strings.Contains(
			strings.ToLower(event.Path+event.Username+event.Resource+event.Namespace),
			strings.ToLower(filter.Search),
		) {
			continue
		}
		matched = append(matched, event)
	}

	// Newest first, matching the store.
	slices.SortFunc(matched, func(a, b db.AuditEvent) int { return b.At.Compare(a.At) })

	total := int64(len(matched))
	if filter.Offset > 0 {
		if filter.Offset >= len(matched) {
			return []db.AuditEvent{}, total, nil
		}
		matched = matched[filter.Offset:]
	}
	if filter.Limit > 0 && filter.Limit < len(matched) {
		matched = matched[:filter.Limit]
	}
	return matched, total, nil
}

func (f *fakeStore) AuditSummary(_ context.Context, since time.Time) (db.AuditStats, error) {
	var stats db.AuditStats
	for _, event := range f.audit {
		if event.At.Before(since) {
			continue
		}
		stats.Total++
		if event.Error != "" || event.Status >= 400 {
			stats.Failed++
		}
		if event.Streaming && event.Phase == "open" {
			stats.Streams++
		}
	}
	return stats, nil
}

func (f *fakeStore) PruneAuditEvents(_ context.Context, before time.Time) (int64, error) {
	if f.pruneErr != nil {
		return 0, f.pruneErr
	}

	kept := make([]db.AuditEvent, 0, len(f.audit))
	for _, event := range f.audit {
		if event.At.Before(before) {
			continue
		}
		kept = append(kept, event)
	}

	removed := int64(len(f.audit) - len(kept))
	f.audit = kept
	f.pruned = append(f.pruned, before)
	return removed, nil
}

func (f *fakeStore) addTerminalSession(session db.TerminalSession) db.TerminalSession {
	if session.StartedAt.IsZero() {
		session.StartedAt = time.Now()
	}
	if session.SessionID == "" {
		session.SessionID = "session-" + itoa(f.nextID)
	}
	session.ID = f.nextID
	f.nextID++
	f.recordings = append(f.recordings, session)
	return session
}

func (f *fakeStore) ListTerminalSessions(
	_ context.Context, filter db.TerminalSessionFilter,
) ([]db.TerminalSession, int64, error) {
	matched := []db.TerminalSession{}
	for _, session := range f.recordings {
		if filter.UserID != 0 && session.UserID != filter.UserID {
			continue
		}
		if filter.ClusterID != 0 && session.ClusterID != filter.ClusterID {
			continue
		}
		if filter.Namespace != "" && session.Namespace != filter.Namespace {
			continue
		}
		if filter.Pod != "" && session.PodName != filter.Pod {
			continue
		}
		if filter.SessionID != "" && session.SessionID != filter.SessionID {
			continue
		}
		if filter.OpenOnly && !session.IsOpen() {
			continue
		}
		if filter.Search != "" && !strings.Contains(
			strings.ToLower(session.PodName+session.ContainerName+session.Username+session.Namespace),
			strings.ToLower(filter.Search),
		) {
			continue
		}
		matched = append(matched, session)
	}

	slices.SortFunc(matched, func(a, b db.TerminalSession) int {
		return b.StartedAt.Compare(a.StartedAt)
	})
	return matched, int64(len(matched)), nil
}

func (f *fakeStore) TerminalSessionByID(_ context.Context, id uint) (*db.TerminalSession, error) {
	for i := range f.recordings {
		if f.recordings[i].ID == id {
			session := f.recordings[i]
			return &session, nil
		}
	}
	return nil, db.ErrNotFound
}

func (f *fakeStore) DeleteTerminalSession(_ context.Context, id uint) error {
	for i := range f.recordings {
		if f.recordings[i].ID == id {
			f.recordings = slices.Delete(f.recordings, i, i+1)
			return nil
		}
	}
	return db.ErrNotFound
}

func (f *fakeStore) PruneTerminalSessions(
	_ context.Context, before time.Time,
) ([]db.TerminalSession, error) {
	if f.pruneErr != nil {
		return nil, f.pruneErr
	}

	stale := []db.TerminalSession{}
	kept := make([]db.TerminalSession, 0, len(f.recordings))
	for _, session := range f.recordings {
		if session.StartedAt.Before(before) {
			stale = append(stale, session)
			continue
		}
		kept = append(kept, session)
	}
	f.recordings = kept
	return stale, nil
}

func (f *fakeStore) Settings(_ context.Context) (map[string]string, error) {
	out := make(map[string]string, len(f.settings))
	for key, value := range f.settings {
		out[key] = value
	}
	return out, nil
}

func (f *fakeStore) PutSettings(_ context.Context, values map[string]string, _ uint) error {
	for key, value := range values {
		f.settings[key] = value
	}
	return nil
}

func (f *fakeStore) ObservabilitySources(_ context.Context, clusterID uint) ([]db.ObservabilitySource, error) {
	out := []db.ObservabilitySource{}
	for _, kind := range db.SourceKinds {
		if source, ok := f.sources[clusterID][kind]; ok {
			out = append(out, source)
		}
	}
	return out, nil
}

func (f *fakeStore) ObservabilitySource(
	_ context.Context, clusterID uint, kind string,
) (*db.ObservabilitySource, error) {
	source, ok := f.sources[clusterID][kind]
	if !ok {
		return nil, db.ErrNotFound
	}
	return &source, nil
}

func (f *fakeStore) PutObservabilitySource(_ context.Context, source *db.ObservabilitySource) error {
	if f.sources[source.ClusterID] == nil {
		f.sources[source.ClusterID] = map[string]db.ObservabilitySource{}
	}
	if source.ID == 0 {
		source.ID = f.nextID
		f.nextID++
	}
	source.UpdatedAt = time.Now()
	f.sources[source.ClusterID][source.Kind] = *source
	return nil
}

func (f *fakeStore) UpdateSourceHealth(_ context.Context, id uint, health db.SourceHealth) error {
	for clusterID, byKind := range f.sources {
		for kind, source := range byKind {
			if source.ID != id {
				continue
			}
			source.LastStatus = health.Status
			source.LastMessage = health.Message
			source.DetectedVersion = health.DetectedVersion
			source.LastCheckedAt = &health.CheckedAt
			f.sources[clusterID][kind] = source
			return nil
		}
	}
	return db.ErrNotFound
}

func (f *fakeStore) DeleteObservabilitySource(_ context.Context, clusterID uint, kind string) error {
	if _, ok := f.sources[clusterID][kind]; !ok {
		return db.ErrNotFound
	}
	delete(f.sources[clusterID], kind)
	return nil
}

func (f *fakeStore) ClusterConsoles(_ context.Context, clusterID uint) ([]db.ClusterConsole, error) {
	out := []db.ClusterConsole{}
	for _, kind := range db.ConsoleKinds {
		if console, ok := f.consoles[clusterID][kind]; ok {
			out = append(out, console)
		}
	}
	return out, nil
}

func (f *fakeStore) PutClusterConsole(_ context.Context, console *db.ClusterConsole) error {
	if f.consoles[console.ClusterID] == nil {
		f.consoles[console.ClusterID] = map[string]db.ClusterConsole{}
	}
	if console.ID == 0 {
		console.ID = f.nextID
		f.nextID++
	}
	console.UpdatedAt = time.Now()
	f.consoles[console.ClusterID][console.Kind] = *console
	return nil
}

func (f *fakeStore) DeleteClusterConsole(_ context.Context, clusterID uint, kind string) error {
	if _, ok := f.consoles[clusterID][kind]; !ok {
		return db.ErrNotFound
	}
	delete(f.consoles[clusterID], kind)
	return nil
}

// fakeIssuer stands in for a target Kubernetes cluster's TokenRequest API.
type fakeIssuer struct {
	calls       int
	lastCluster *db.Cluster
	lastRequest k8s.TokenRequest
	token       string
	expiresAt   time.Time
	err         error
}

func (f *fakeIssuer) IssueToken(_ context.Context, cluster *db.Cluster, req k8s.TokenRequest) (*k8s.IssuedToken, error) {
	f.calls++
	f.lastCluster = cluster
	f.lastRequest = req
	if f.err != nil {
		return nil, f.err
	}
	return &k8s.IssuedToken{Token: f.token, ExpiresAt: f.expiresAt}, nil
}

// fakeChecker stands in for probing a target cluster.
type fakeChecker struct {
	calls  int
	report k8s.HealthReport
}

func (f *fakeChecker) CheckHealth(_ context.Context, _ *db.Cluster) k8s.HealthReport {
	f.calls++
	return f.report
}

type testEnv struct {
	router   *gin.Engine
	store    *fakeStore
	jwt      *auth.Manager
	tokens   *fakeIssuer
	health   *fakeChecker
	gateway  *bastion.Server
	registry *bastion.Registry
	// guard is the engine both the router and the gateway share, so a test can
	// assert on what is actually enforced rather than only on what was stored.
	guard *guardrails.Engine
}

// recordingAuditor captures the audit records the API writes about itself, which
// is how the tests assert that watching a recording leaves a trail.
type recordingAuditor struct {
	mu     sync.Mutex
	events []bastion.Event
}

func (a *recordingAuditor) Record(_ context.Context, event bastion.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, event)
}

func (a *recordingAuditor) all() []bastion.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]bastion.Event(nil), a.events...)
}

func authManagerForTest() *auth.Manager {
	return auth.NewManager("test-secret", time.Hour)
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnvWith(t, nil)
}

// newTestEnvWith is newTestEnv with the router options adjusted, for the cases
// that turn on a dependency the default stack leaves off.
func newTestEnvWith(t *testing.T, adjust func(*Options)) *testEnv {
	t.Helper()
	store := newFakeStore()
	manager := authManagerForTest()
	issuer := &fakeIssuer{
		token:     "issued-token",
		expiresAt: time.Now().Add(time.Hour).UTC().Truncate(time.Second),
	}
	checker := &fakeChecker{report: k8s.HealthReport{Reachable: true, Version: "v1.31.4"}}
	gateway := bastion.NewServer(bastion.ServerOptions{Store: store})
	// The guardrail engine is wired into both halves by default, exactly as the
	// server wires it: the router publishes the rules and the gateway enforces
	// them, and a test that seeds a rule needs both ends to agree.
	guard := guardrails.New()
	proxy := bastion.NewProxy(bastion.ProxyOptions{
		Store:    store,
		Registry: gateway.Registry(),
		Guard:    guard,
	})

	opts := Options{
		Store:          store,
		JWT:            manager,
		Tokens:         issuer,
		Health:         checker,
		SANamespace:    "kubemg-system",
		Bastion:        gateway,
		Proxy:          proxy,
		PublicURL:      "https://kubemg.example.com",
		AgentImage:     "ghcr.io/kubemg/kubemg-agent:test",
		AgentNamespace: "kubemg-system",
		Guardrails:     guard,
	}
	if adjust != nil {
		adjust(&opts)
	}

	return &testEnv{
		router: NewRouter(opts),
		store:    store,
		jwt:      manager,
		tokens:   issuer,
		health:   checker,
		gateway:  gateway,
		registry: gateway.Registry(),
		guard:    guard,
	}
}

func (e *testEnv) do(t *testing.T, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader = bytes.NewReader(nil)
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(payload)
	}

	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func (e *testEnv) tokenFor(t *testing.T, user *db.User) string {
	t.Helper()
	token, _, err := e.jwt.Generate(user.ID, user.Username, user.Role)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	return token
}

func itoa(id uint) string { return strconv.FormatUint(uint64(id), 10) }

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON response %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestHealthEndpoint(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, http.MethodGet, "/health", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	body := decode[map[string]string](t, rec)
	if body["status"] != "ok" {
		t.Fatalf("expected status \"ok\", got %q", body["status"])
	}
}

func TestUnknownRouteReturns404(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, http.MethodGet, "/does-not-exist", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestHealthCORSHeader(t *testing.T) {
	env := newTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got == "" {
		t.Fatal("expected Access-Control-Allow-Origin header to be set")
	}
}

func TestRouterWithoutDependenciesServesHealthOnly(t *testing.T) {
	router := NewRouter(Options{})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected health status %d, got %d", http.StatusOK, rec.Code)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d for unwired API route, got %d", http.StatusNotFound, rec.Code)
	}
}
