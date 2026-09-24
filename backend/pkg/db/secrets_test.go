package db

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/kubemg/kubemg/backend/pkg/secretbox"
)

func secretTestBox(t *testing.T, fill byte) *secretbox.Box {
	t.Helper()
	box, err := secretbox.New(bytes.Repeat([]byte{fill}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

// withBox sets the process-wide key for one test and restores it after.
func withBox(t *testing.T, box *secretbox.Box) {
	t.Helper()
	prev := currentBox()
	UseSecretBox(box)
	t.Cleanup(func() { UseSecretBox(prev) })
}

// dryRunDB is a GORM handle that builds statements without a server, so the
// real write paths — struct create, map update, Update(col, v) — can be
// inspected for what they would send.
func dryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=127.0.0.1 port=1 user=x dbname=x sslmode=disable"}),
		&gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := registerSecretCallbacks(gdb); err != nil {
		t.Fatal(err)
	}
	return gdb
}

func varsString(vars []any) string {
	parts := make([]string, len(vars))
	for i, v := range vars {
		if valuer, ok := v.(driver.Valuer); ok {
			v, _ = valuer.Value()
		}
		parts[i] = fmt.Sprint(v)
	}
	return strings.Join(parts, "|")
}

func TestSecretColumnsCoverEveryCredential(t *testing.T) {
	cols, err := SecretColumns()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range cols {
		got[c.Table+"."+c.Column] = true
	}
	for _, want := range []string{
		"server_secrets.value",
		"clusters.agent_token",
		"clusters.service_account_token",
		"observability_sources.credential",
		"helm_repositories.credential",
		"alarm_channels.secret",
		"sso_providers.client_secret",
		"sso_providers.ldap_bind_password",
	} {
		if !got[want] {
			t.Errorf("%s is not tagged serializer:secret", want)
		}
	}
}

func TestStructWritesAreSealed(t *testing.T) {
	box := secretTestBox(t, 1)
	withBox(t, box)
	gdb := dryRunDB(t)

	stmt := gdb.Create(&AlarmChannel{Name: "c", Secret: "routing-key-123"}).Statement
	vars := varsString(stmt.Vars)
	if strings.Contains(vars, "routing-key-123") || !strings.Contains(vars, secretbox.Prefix) {
		t.Fatalf("create sent %s", vars)
	}

	stmt = gdb.Model(&SSOProviderConfig{}).Where("id = ?", 1).
		Select("client_secret").Updates(&SSOProviderConfig{ClientSecret: "oidc-secret"}).Statement
	vars = varsString(stmt.Vars)
	if strings.Contains(vars, "oidc-secret") || !strings.Contains(vars, secretbox.Prefix) {
		t.Fatalf("struct update sent %s", vars)
	}
}

func TestMapWritesAreSealed(t *testing.T) {
	box := secretTestBox(t, 1)
	withBox(t, box)
	gdb := dryRunDB(t)

	stmt := gdb.Model(&AlarmChannel{}).Where("id = ?", 1).
		Updates(map[string]any{"secret": "routing-key-123", "name": "n"}).Statement
	vars := varsString(stmt.Vars)
	if strings.Contains(vars, "routing-key-123") || !strings.Contains(vars, secretbox.Prefix) {
		t.Fatalf("map update sent %s", vars)
	}

	token := "kmg_tunnel_credential"
	stmt = gdb.Model(&Cluster{}).Where("id = ?", 1).Update("agent_token", token).Statement
	vars = varsString(stmt.Vars)
	if strings.Contains(vars, token) || !strings.Contains(vars, secretbox.Prefix) {
		t.Fatalf("Update(col) sent %s", vars)
	}
	if !strings.Contains(vars, HashAgentToken(token)) {
		t.Fatalf("agent token write did not carry its lookup hash: %s", vars)
	}
	if !strings.Contains(stmt.SQL.String(), "agent_token_hash") {
		t.Fatalf("agent token write did not set agent_token_hash: %s", stmt.SQL.String())
	}
}

func TestEmptyCredentialStaysEmpty(t *testing.T) {
	withBox(t, secretTestBox(t, 1))
	updates := map[string]any{"secret": ""}
	s, err := schemaOf(&AlarmChannel{})
	if err != nil {
		t.Fatal(err)
	}
	if err := sealSecretAssignments(s, updates, currentBox()); err != nil {
		t.Fatal(err)
	}
	if updates["secret"] != "" {
		t.Fatalf("clearing a credential wrote %q", updates["secret"])
	}
}

func TestSerializerOpensAndRefuses(t *testing.T) {
	box := secretTestBox(t, 1)
	withBox(t, box)
	s, err := schemaOf(&HelmRepository{})
	if err != nil {
		t.Fatal(err)
	}
	field := s.LookUpField("credential")
	sealed, _ := box.Seal("repo-password")

	for stored, want := range map[string]string{sealed: "repo-password", "legacy-plain": "legacy-plain", "": ""} {
		var repo HelmRepository
		if err := (secretSerializer{}).Scan(context.Background(), field, reflectOf(&repo), stored); err != nil {
			t.Fatalf("Scan(%q): %v", stored, err)
		}
		if repo.Credential != want {
			t.Fatalf("Scan(%q) = %q, want %q", stored, repo.Credential, want)
		}
	}

	UseSecretBox(secretTestBox(t, 2))
	var repo HelmRepository
	if err := (secretSerializer{}).Scan(context.Background(), field, reflectOf(&repo), sealed); err == nil || repo.Credential != "" {
		t.Fatalf("wrong key read %q, %v", repo.Credential, err)
	}
	UseSecretBox(nil)
	if err := (secretSerializer{}).Scan(context.Background(), field, reflectOf(&repo), sealed); !errors.Is(err, secretbox.ErrKeyRequired) {
		t.Fatalf("no key: err = %v", err)
	}
}

// fakeSecretRows is the migration's database, in memory.
type fakeSecretRows struct {
	values map[string]map[string]string // "table.column" -> id -> value
	hashes map[string]string
	writes int
}

func (f *fakeSecretRows) List(_ context.Context, col SecretColumn) ([]SecretRow, error) {
	var out []SecretRow
	for id, v := range f.values[col.Table+"."+col.Column] {
		if v != "" {
			out = append(out, SecretRow{ID: id, Value: v})
		}
	}
	return out, nil
}

func (f *fakeSecretRows) Write(_ context.Context, col SecretColumn, id, value string) error {
	f.values[col.Table+"."+col.Column][id] = value
	f.writes++
	return nil
}

func (f *fakeSecretRows) BackfillAgentTokenHash(_ context.Context, id, hash string) error {
	f.hashes[id] = hash
	return nil
}

func newFakeRows() *fakeSecretRows {
	return &fakeSecretRows{
		values: map[string]map[string]string{
			"server_secrets.value":  {"jwt_signing_key": "signing-key"},
			"clusters.agent_token":  {"1": "kmg_token_one", "2": ""},
			"alarm_channels.secret": {"4": "hook"},
		},
		hashes: map[string]string{},
	}
}

func TestMigrateSecretsEncryptsInPlaceAndIsIdempotent(t *testing.T) {
	box := secretTestBox(t, 1)
	rows := newFakeRows()

	report, err := MigrateSecrets(context.Background(), rows, box)
	if err != nil {
		t.Fatal(err)
	}
	if report.Sealed != 3 || report.Encrypted != 0 {
		t.Fatalf("first pass = %+v", report)
	}
	for key, byID := range rows.values {
		for id, v := range byID {
			if v != "" && !secretbox.IsSealed(v) {
				t.Fatalf("%s[%s] still plaintext: %q", key, id, v)
			}
		}
	}
	if rows.hashes["1"] != HashAgentToken("kmg_token_one") {
		t.Fatalf("agent token hash = %q", rows.hashes["1"])
	}
	if opened, _ := box.Open(rows.values["server_secrets.value"]["jwt_signing_key"]); opened != "signing-key" {
		t.Fatalf("signing key round-trip = %q", opened)
	}

	writes := rows.writes
	report, err = MigrateSecrets(context.Background(), rows, box)
	if err != nil {
		t.Fatal(err)
	}
	if rows.writes != writes || report.Sealed != 0 || report.Encrypted != 3 {
		t.Fatalf("second pass rewrote values: %+v, writes %d -> %d", report, writes, rows.writes)
	}
}

func TestMigrateSecretsWithoutKeyLeavesPlaintextAndBackfillsHash(t *testing.T) {
	rows := newFakeRows()
	report, err := MigrateSecrets(context.Background(), rows, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Plaintext != 3 || rows.writes != 0 {
		t.Fatalf("no-key pass = %+v, writes %d", report, rows.writes)
	}
	if rows.hashes["1"] != HashAgentToken("kmg_token_one") {
		t.Fatal("hash not backfilled without a key")
	}
}

func TestMigrateSecretsRefusesForeignOrMissingKey(t *testing.T) {
	rows := newFakeRows()
	if _, err := MigrateSecrets(context.Background(), rows, secretTestBox(t, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateSecrets(context.Background(), rows, secretTestBox(t, 2)); !errors.Is(err, ErrSecretKeyMismatch) {
		t.Fatalf("different key: err = %v", err)
	}
	if _, err := MigrateSecrets(context.Background(), rows, nil); !errors.Is(err, ErrSecretKeyMismatch) {
		t.Fatalf("key removed: err = %v", err)
	}
}

func TestHashAgentToken(t *testing.T) {
	if HashAgentToken("") != "" {
		t.Fatal("empty token must hash to empty, so a direct-mode row is never found by it")
	}
	if h := HashAgentToken("kmg_x"); len(h) != 64 || h == HashAgentToken("kmg_y") {
		t.Fatalf("hash = %q", h)
	}
}

func schemaOf(model any) (*schema.Schema, error) {
	return schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
}

func reflectOf(ptr any) reflect.Value { return reflect.ValueOf(ptr).Elem() }

// No credential column may ever be serialized into a response: every one of
// them, and the agent token's hash, is `json:"-"`.
func TestSecretColumnsNeverSerialize(t *testing.T) {
	for _, model := range secretModels {
		s, err := schemaOf(model)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range s.Fields {
			if (isSecretField(field) || field.DBName == "agent_token_hash") && field.Tag.Get("json") != "-" {
				t.Errorf("%s.%s is a credential but serializes as %q", s.Table, field.DBName, field.Tag.Get("json"))
			}
		}
	}
}
