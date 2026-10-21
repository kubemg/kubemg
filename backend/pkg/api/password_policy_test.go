package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/mailer"
)

// The fake's half of the password age tables.

func (f *fakeStore) PasswordAccounts(_ context.Context) ([]db.PasswordAccount, error) {
	var out []db.PasswordAccount
	for _, u := range f.users {
		if !u.IsActive || u.IsMachine() || u.IsFederated() {
			continue
		}
		out = append(out, db.PasswordAccount{
			ID: u.ID, Username: u.Username, Email: u.Email, CreatedAt: u.CreatedAt,
			PasswordChangedAt: u.PasswordChangedAt, PasswordExpiredAt: u.PasswordExpiredAt,
			PasswordNoticeDue: u.PasswordNoticeDue, PasswordNoticeDays: u.PasswordNoticeDays,
		})
	}
	return out, nil
}

func (f *fakeStore) ExpirePassword(_ context.Context, userID uint, at time.Time) (bool, error) {
	u, ok := f.users[userID]
	if !ok || u.PasswordExpiredAt != nil {
		return false, nil
	}
	u.PasswordExpiredAt = &at
	return true, nil
}

func (f *fakeStore) RecordPasswordNotice(_ context.Context, userID uint, due time.Time, days int) error {
	if u, ok := f.users[userID]; ok {
		u.PasswordNoticeDue = &due
		u.PasswordNoticeDays = days
	}
	return nil
}

func (f *fakeStore) SMTPConfig(_ context.Context) (*db.SMTPConfig, error) {
	if f.smtp == nil {
		return nil, db.ErrNotFound
	}
	copied := *f.smtp
	return &copied, nil
}

func (f *fakeStore) PutSMTPConfig(_ context.Context, config *db.SMTPConfig) error {
	copied := *config
	f.smtp = &copied
	return nil
}

func (f *fakeStore) DeleteSMTPConfig(_ context.Context) error {
	f.smtp = nil
	return nil
}

// mailRecorder stands in for the relay.
type mailRecorder struct {
	mu   sync.Mutex
	sent []mailer.Message
	err  error
}

func (m *mailRecorder) send(_ context.Context, _ mailer.Config, msg mailer.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, msg)
	return nil
}

func (m *mailRecorder) all() []mailer.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]mailer.Message(nil), m.sent...)
}

func daysAgo(days int) *time.Time {
	at := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	return &at
}

func setPasswordPolicy(t *testing.T, env *testEnv, admin *db.User, days int) {
	t.Helper()
	rec := env.do(t, http.MethodPut, "/api/v1/settings", env.tokenFor(t, admin), map[string]any{
		"password_max_age_days": days,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("setting the policy: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPasswordDue(t *testing.T) {
	changed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	account := db.PasswordAccount{CreatedAt: changed.Add(-time.Hour), PasswordChangedAt: &changed}

	if _, ok := passwordDue(account, 0); ok {
		t.Fatal("an account with the policy off and no forced expiry fell due")
	}
	due, ok := passwordDue(account, 90)
	if !ok || !due.Equal(changed.Add(90*24*time.Hour)) {
		t.Fatalf("policy due = %v %v", due, ok)
	}

	forced := changed.Add(24 * time.Hour)
	account.PasswordExpiredAt = &forced
	if due, _ := passwordDue(account, 90); !due.Equal(forced) {
		t.Fatalf("an earlier forced expiry did not win: %v", due)
	}
	if due, _ := passwordDue(account, 0); !due.Equal(forced) {
		t.Fatalf("a forced expiry needs no policy: %v", due)
	}
	late := changed.Add(200 * 24 * time.Hour)
	account.PasswordExpiredAt = &late
	if due, _ := passwordDue(account, 90); !due.Equal(changed.Add(90 * 24 * time.Hour)) {
		t.Fatalf("a later forced expiry pushed the policy back: %v", due)
	}

	// No recorded change reads from the account's creation.
	account = db.PasswordAccount{CreatedAt: changed}
	if due, _ := passwordDue(account, 1); !due.Equal(changed.Add(24 * time.Hour)) {
		t.Fatalf("creation fallback = %v", due)
	}
}

func TestPasswordNoticeStage(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	cases := []struct {
		left  time.Duration
		stage int
		owed  bool
	}{
		{20 * day, 0, false},
		{14*day + time.Hour, 0, false},
		{14 * day, 14, true},
		{13*day + time.Hour, 14, true},
		{5 * day, 7, true},
		{2 * day, 3, true},
		{23 * time.Hour, 1, true},
		{0, 0, true},
		{-3 * day, 0, true},
	}
	for _, tc := range cases {
		stage, owed := passwordNoticeStage(now.Add(tc.left), now)
		if stage != tc.stage || owed != tc.owed {
			t.Errorf("%v left: got stage %d owed %v, want %d %v", tc.left, stage, owed, tc.stage, tc.owed)
		}
	}
}

func TestAnExpiredPasswordHoldsTheSession(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("root", "admin-password", db.RoleAdmin)
	user := env.store.addUser("devops", "old-password", db.RoleUser)
	user.PasswordChangedAt = daysAgo(40)
	token := env.tokenFor(t, user)

	if rec := env.do(t, http.MethodGet, "/api/v1/clusters", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("before the policy: %d", rec.Code)
	}
	setPasswordPolicy(t, env, admin, 30)

	rec := env.do(t, http.MethodGet, "/api/v1/clusters", token, nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), passwordChangeRequiredCode) {
		t.Fatalf("an open session past its password's due date was served: %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.do(t, http.MethodPost, "/api/v1/auth/ws-ticket", token, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("a held session minted a terminal ticket: %d", rec.Code)
	}

	me := env.do(t, http.MethodGet, "/api/v1/auth/me", token, nil)
	if me.Code != http.StatusOK {
		t.Fatalf("a held session could not read who it is: %d", me.Code)
	}
	if body := decode[userResponse](t, me); !body.PasswordChangeRequired || body.PasswordExpiresAt == nil {
		t.Fatalf("/auth/me did not say the password is due: %+v", body)
	}

	// The admin, whose password is recent, is not held.
	if rec := env.do(t, http.MethodGet, "/api/v1/clusters", env.tokenFor(t, admin), nil); rec.Code != http.StatusOK {
		t.Fatalf("an account inside the window was held: %d", rec.Code)
	}

	change := env.do(t, http.MethodPost, passwordPath, token, map[string]any{
		"current_password": "old-password",
		"new_password":     "new-password",
	})
	if change.Code != http.StatusOK {
		t.Fatalf("a held session could not change its password: %d %s", change.Code, change.Body.String())
	}
	if rec := env.do(t, http.MethodGet, "/api/v1/clusters", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("still held after changing the password: %d %s", rec.Code, rec.Body.String())
	}
}

// A due password still signs in — it was the right one — and says so.
func TestSigningInWithAnExpiredPassword(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("root", "admin-password", db.RoleAdmin)
	user := env.store.addUser("devops", "old-password", db.RoleUser)
	user.PasswordChangedAt = daysAgo(40)
	setPasswordPolicy(t, env, admin, 30)

	rec := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": "devops", "password": "old-password",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	body := decode[loginResponse](t, rec)
	if body.Token == "" || !body.User.PasswordChangeRequired {
		t.Fatalf("login did not report the change owed: %+v", body.User)
	}
	if code := signIn(t, env, "devops", "wrong-password"); code != http.StatusUnauthorized {
		t.Fatalf("a wrong password was not refused: %d", code)
	}
}

func TestAdministratorExpiresAPassword(t *testing.T) {
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(o *Options) { o.Auditor = auditor })
	admin := env.store.addUser("root", "admin-password", db.RoleAdmin)
	user := env.store.addUser("devops", "password-1", db.RoleUser)
	token := env.tokenFor(t, user)
	path := "/api/v1/users/" + itoa(user.ID) + "/password/expire"

	rec := env.do(t, http.MethodPost, path, env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expire: %d %s", rec.Code, rec.Body.String())
	}
	if body := decode[userResponse](t, rec); !body.PasswordChangeRequired {
		t.Fatalf("the response did not say the change is owed: %+v", body)
	}
	if rec := env.do(t, http.MethodGet, "/api/v1/clusters", token, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("the open session was not held: %d", rec.Code)
	}

	// Again: answered, not recorded twice.
	if rec := env.do(t, http.MethodPost, path, env.tokenFor(t, admin), nil); rec.Code != http.StatusOK {
		t.Fatalf("repeat expire: %d", rec.Code)
	}
	var expiries []string
	for _, event := range auditor.all() {
		if event.Verb == verbPasswordExpire {
			expiries = append(expiries, event.Username+"→"+event.ImpersonatedUser)
		}
	}
	if len(expiries) != 1 || expiries[0] != "root→devops" {
		t.Fatalf("expected one record with the identities crossed, got %v", expiries)
	}

	// An administrator setting a new password does not lift it: somebody else
	// knows that password.
	env.do(t, http.MethodPut, "/api/v1/users/"+itoa(user.ID), env.tokenFor(t, admin), map[string]any{
		"password": "typed-by-admin",
	})
	if rec := env.do(t, http.MethodGet, "/api/v1/clusters", token, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("an administrator's password write lifted the forced expiry: %d", rec.Code)
	}
	env.do(t, http.MethodPost, passwordPath, token, map[string]any{
		"current_password": "typed-by-admin", "new_password": "chosen-by-owner",
	})
	if rec := env.do(t, http.MethodGet, "/api/v1/clusters", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("the owner's own change did not lift it: %d", rec.Code)
	}
	if env.store.users[user.ID].PasswordExpiredAt != nil {
		t.Fatal("the flag survived the owner's change")
	}
}

func TestExpiringAPasswordIsRefusedByName(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("root", "admin-password", db.RoleAdmin)
	super := env.store.addSuperAdmin("boss", "super-password")
	federated := env.store.addUser("sso-user", "unused-password", db.RoleUser)
	federated.AuthSource = db.ProtocolOIDC
	machine := env.store.addMachineAccount("ci")
	adminToken := env.tokenFor(t, admin)

	cases := []struct {
		name string
		id   uint
		code int
	}{
		{"own", admin.ID, http.StatusForbidden},
		{"super admin by an admin", super.ID, http.StatusForbidden},
		{"federated", federated.ID, http.StatusConflict},
		{"machine (not a person's route)", machine.ID, http.StatusNotFound},
		{"missing", 9999, http.StatusNotFound},
	}
	for _, tc := range cases {
		rec := env.do(t, http.MethodPost, "/api/v1/users/"+itoa(tc.id)+"/password/expire", adminToken, nil)
		if rec.Code != tc.code {
			t.Errorf("%s: expected %d, got %d (%s)", tc.name, tc.code, rec.Code, rec.Body.String())
		}
	}
	user := env.store.addUser("devops", "password-1", db.RoleUser)
	rec := env.do(t, http.MethodPost, "/api/v1/users/"+itoa(user.ID)+"/password/expire", env.tokenFor(t, user), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a non-admin expired somebody's password: %d", rec.Code)
	}
}

func TestPasswordPolicySetting(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("root", "admin-password", db.RoleAdmin)
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]any{"password_max_age_days": 4000})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an out-of-bounds policy was accepted: %d", rec.Code)
	}
	rec = env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]any{"password_max_age_days": 90})
	body := decode[settingsResponse](t, rec)
	if body.Effective.PasswordMaxAgeDays != 90 || body.Overrides.PasswordMaxAgeDays != 90 {
		t.Fatalf("policy not stored: %+v", body.Effective)
	}
	if !strings.Contains(strings.Join(body.Warnings, " "), "no mail relay") {
		t.Fatalf("a policy with no relay was not disclosed: %v", body.Warnings)
	}
	env.store.smtp = &db.SMTPConfig{Host: "smtp.example.com", Port: 587, Security: "starttls", From: "k@example.com"}
	body = decode[settingsResponse](t, env.do(t, http.MethodGet, "/api/v1/settings", token, nil))
	if strings.Contains(strings.Join(body.Warnings, " "), "no mail relay") {
		t.Fatalf("warned with a relay configured: %v", body.Warnings)
	}
	body = decode[settingsResponse](t, env.do(t, http.MethodPut, "/api/v1/settings", token,
		map[string]any{"password_max_age_days": 0}))
	if body.Effective.PasswordMaxAgeDays != 0 {
		t.Fatalf("0 did not turn the policy off: %d", body.Effective.PasswordMaxAgeDays)
	}
}

func TestSMTPRelaySettings(t *testing.T) {
	relay := &mailRecorder{}
	env := newTestEnvWith(t, func(o *Options) { o.MailSender = relay.send })
	admin := env.store.addUser("root", "admin-password", db.RoleAdmin)
	admin.Email = "root@example.com"
	user := env.store.addUser("devops", "password-1", db.RoleUser)
	token := env.tokenFor(t, admin)

	if rec := env.do(t, http.MethodGet, "/api/v1/settings/smtp", env.tokenFor(t, user), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("a non-admin read the relay: %d", rec.Code)
	}
	if body := decode[smtpResponse](t, env.do(t, http.MethodGet, "/api/v1/settings/smtp", token, nil)); body.Configured {
		t.Fatal("an empty relay read as configured")
	}
	if rec := env.do(t, http.MethodPost, "/api/v1/settings/smtp/test", token, nil); rec.Code != http.StatusConflict {
		t.Fatalf("a test with no relay: %d", rec.Code)
	}

	bad := env.do(t, http.MethodPut, "/api/v1/settings/smtp", token, map[string]any{
		"host": "smtp.example.com", "port": 25, "security": "none", "username": "u", "password": "p",
		"from": "kubemg@example.com",
	})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("a password over plain text was accepted: %d", bad.Code)
	}

	rec := env.do(t, http.MethodPut, "/api/v1/settings/smtp", token, map[string]any{
		"host": "smtp.example.com", "port": 587, "security": "starttls", "username": "relay-user",
		"password": "relay-password", "from": "KubeMG <kubemg@example.com>",
	})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "relay-password") {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	// Omitting the password keeps it.
	env.do(t, http.MethodPut, "/api/v1/settings/smtp", token, map[string]any{
		"host": "smtp2.example.com", "port": 587, "security": "starttls", "username": "relay-user",
		"from": "kubemg@example.com",
	})
	if env.store.smtp.Password != "relay-password" || env.store.smtp.Host != "smtp2.example.com" {
		t.Fatalf("stored relay = %+v", env.store.smtp)
	}
	got := decode[smtpResponse](t, env.do(t, http.MethodGet, "/api/v1/settings/smtp", token, nil))
	if !got.Configured || !got.HasPassword {
		t.Fatalf("read back = %+v", got)
	}

	if rec := env.do(t, http.MethodPost, "/api/v1/settings/smtp/test", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("test: %d %s", rec.Code, rec.Body.String())
	}
	if sent := relay.all(); len(sent) != 1 || sent[0].To != "root@example.com" {
		t.Fatalf("test mail = %+v", sent)
	}

	if rec := env.do(t, http.MethodDelete, "/api/v1/settings/smtp", token, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if env.store.smtp != nil {
		t.Fatal("the relay survived its delete")
	}
}

func noticeServer(store *fakeStore, relay *mailRecorder) *server {
	return &server{
		store: store, instanceID: "replica-under-test", mail: relay.send,
		passwords: &passwordGate{}, publicURL: "https://kubemg.example.com",
	}
}

func TestPasswordRemindersAreSentOncePerStage(t *testing.T) {
	store := newFakeStore()
	store.settings[db.SettingPasswordMaxAgeDays] = "30"
	store.smtp = &db.SMTPConfig{Host: "smtp.example.com", Port: 587, Security: "starttls", From: "k@example.com"}
	soon := store.addUser("soon", "password-1", db.RoleUser)
	soon.Email = "soon@example.com"
	soon.PasswordChangedAt = daysAgo(25) // 5 days left → the 7-day reminder
	fresh := store.addUser("fresh", "password-1", db.RoleUser)
	fresh.Email = "fresh@example.com"
	fresh.PasswordChangedAt = daysAgo(1)
	nomail := store.addUser("nomail", "password-1", db.RoleUser)
	nomail.PasswordChangedAt = daysAgo(29)
	gone := store.addUser("gone", "password-1", db.RoleUser)
	gone.Email = "gone@example.com"
	gone.PasswordChangedAt = daysAgo(31)

	relay := &mailRecorder{}
	s := noticeServer(store, relay)
	s.sendPasswordNotices(context.Background())
	s.sendPasswordNotices(context.Background())

	sent := relay.all()
	if len(sent) != 2 {
		t.Fatalf("expected two reminders across two passes, got %+v", sent)
	}
	byTo := map[string]mailer.Message{}
	for _, msg := range sent {
		byTo[msg.To] = msg
	}
	if !strings.Contains(byTo["soon@example.com"].Subject, "7 days") {
		t.Fatalf("soon got %q", byTo["soon@example.com"].Subject)
	}
	if !strings.Contains(byTo["gone@example.com"].Subject, "has expired") {
		t.Fatalf("gone got %q", byTo["gone@example.com"].Subject)
	}
	if !strings.Contains(byTo["soon@example.com"].Body, "https://kubemg.example.com/me/profile") {
		t.Fatalf("the reminder does not say where to change it:\n%s", byTo["soon@example.com"].Body)
	}

	// The next stage is owed once the clock reaches it.
	soon.PasswordChangedAt = daysAgo(28)
	s.sendPasswordNotices(context.Background())
	if sent := relay.all(); len(sent) != 3 || !strings.Contains(sent[2].Subject, "3 days") {
		t.Fatalf("the 3-day reminder was not sent: %+v", sent)
	}

	// A new password is a new due date: the stages start again.
	soon.PasswordChangedAt = daysAgo(17)
	s.sendPasswordNotices(context.Background())
	if sent := relay.all(); len(sent) != 4 || !strings.Contains(sent[3].Subject, "14 days") {
		t.Fatalf("a new due date did not restart the reminders: %+v", sent)
	}
}

func TestPasswordRemindersNeedARelayAndTheLease(t *testing.T) {
	store := newFakeStore()
	store.settings[db.SettingPasswordMaxAgeDays] = "30"
	user := store.addUser("soon", "password-1", db.RoleUser)
	user.Email = "soon@example.com"
	user.PasswordChangedAt = daysAgo(29)
	relay := &mailRecorder{}
	s := noticeServer(store, relay)

	s.sendPasswordNotices(context.Background())
	if len(relay.all()) != 0 {
		t.Fatal("mail went out with no relay configured")
	}

	store.smtp = &db.SMTPConfig{Host: "smtp.example.com", Port: 587, Security: "starttls", From: "k@example.com"}
	store.leases[db.LeasePasswordNotices] = "another-replica"
	s.sendPasswordNotices(context.Background())
	if len(relay.all()) != 0 {
		t.Fatal("mail went out from a replica that does not hold the lease")
	}

	delete(store.leases, db.LeasePasswordNotices)
	relay.err = context.DeadlineExceeded
	s.sendPasswordNotices(context.Background())
	if store.users[user.ID].PasswordNoticeDue != nil {
		t.Fatal("a failed send was recorded as sent")
	}
	relay.err = nil
	s.sendPasswordNotices(context.Background())
	if len(relay.all()) != 1 {
		t.Fatal("a failed reminder was not retried")
	}
}

func TestAForcedExpiryIsMailedAsSuch(t *testing.T) {
	store := newFakeStore()
	store.smtp = &db.SMTPConfig{Host: "smtp.example.com", Port: 587, Security: "starttls", From: "k@example.com"}
	user := store.addUser("devops", "password-1", db.RoleUser)
	user.Email = "devops@example.com"
	now := time.Now().UTC()
	user.PasswordExpiredAt = &now
	relay := &mailRecorder{}
	noticeServer(store, relay).sendPasswordNotices(context.Background())
	sent := relay.all()
	if len(sent) != 1 || !strings.Contains(sent[0].Subject, "administrator") {
		t.Fatalf("forced expiry mail = %+v", sent)
	}
}
