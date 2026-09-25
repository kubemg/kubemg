package db

// Credentials at rest.
//
// Every column that holds a credential KubeMG has to present somewhere — to a
// cluster, an identity provider, a datasource, a chart repository, an alarm
// destination, or to itself when it signs a session — is tagged
// `serializer:secret`. That tag is the single source of truth: the serializer
// seals on write and opens on read, the update callback below seals the same
// columns when they are written through a map, and the boot migration finds the
// columns to encrypt by reading the same tag off the schema. A new credential
// column is covered by adding the tag, and by nothing else.
//
// Why a serializer *and* a callback: GORM applies a field serializer when a
// struct is created, saved or updated, but `Updates(map)` and `Update(col, v)`
// put the map's values into the SET clause untouched. A credential written that
// way would land in the clear with no error anywhere, so the callback closes
// that path for every table rather than trusting each store method to remember.
//
// The key itself is process state (UseSecretBox), set once at boot before the
// first query. A GORM serializer has no other way to reach it, and a key that
// travelled with each call would be one more parameter to forget.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/kubemg/kubemg/backend/pkg/secretbox"
)

// secretSerializerName is the tag value that marks a credential column.
const secretSerializerName = "secret"

var activeBox atomic.Pointer[secretbox.Box]

func init() {
	schema.RegisterSerializer(secretSerializerName, secretSerializer{})
}

// UseSecretBox sets the key credentials are sealed under. Nil means no key:
// values are written in the clear and sealed ones refuse to read.
func UseSecretBox(box *secretbox.Box) { activeBox.Store(box) }

func currentBox() *secretbox.Box { return activeBox.Load() }

// HashAgentToken is the lookup key a tunnel credential is found by. The token
// itself is encrypted, and ciphertext under a random nonce cannot be searched
// for, so the handshake finds the row by this and then compares the decrypted
// token in constant time.
func HashAgentToken(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// secretSerializer seals a string field on write and opens it on read.
type secretSerializer struct{}

func (secretSerializer) Scan(ctx context.Context, field *schema.Field, dst reflect.Value, dbValue any) error {
	var stored string
	switch v := dbValue.(type) {
	case nil:
	case string:
		stored = v
	case []byte:
		stored = string(v)
	default:
		return fmt.Errorf("%s.%s: unexpected stored type %T", field.Schema.Table, field.DBName, dbValue)
	}
	plain, err := currentBox().Open(stored)
	if err != nil {
		return fmt.Errorf("%s.%s: %w", field.Schema.Table, field.DBName, err)
	}
	field.ReflectValueOf(ctx, dst).SetString(plain)
	return nil
}

func (secretSerializer) Value(_ context.Context, field *schema.Field, _ reflect.Value, fieldValue any) (any, error) {
	plain, ok := fieldValue.(string)
	if !ok {
		return nil, fmt.Errorf("%s.%s: secret column must be a string, got %T", field.Schema.Table, field.DBName, fieldValue)
	}
	return currentBox().Seal(plain)
}

// isSecretField reports whether a schema field is a credential column.
func isSecretField(field *schema.Field) bool {
	return field != nil && field.TagSettings["SERIALIZER"] == secretSerializerName
}

// registerSecretCallbacks closes the map-update path the serializer does not
// cover. It is registered on the *gorm.DB every Store is built from.
func registerSecretCallbacks(gdb *gorm.DB) error {
	return gdb.Callback().Update().Before("gorm:update").Register("kubemg:seal_secrets", func(tx *gorm.DB) {
		if tx.Statement.Schema == nil {
			return
		}
		updates, ok := tx.Statement.Dest.(map[string]any)
		if !ok {
			return
		}
		if err := sealSecretAssignments(tx.Statement.Schema, updates, currentBox()); err != nil {
			_ = tx.AddError(err)
		}
	})
}

// sealSecretAssignments rewrites, in place, every credential column in a map
// update to its sealed form, and keeps the agent token's lookup hash in step
// with the token itself.
func sealSecretAssignments(s *schema.Schema, updates map[string]any, box *secretbox.Box) error {
	for key, value := range updates {
		field := s.LookUpField(key)
		if !isSecretField(field) {
			continue
		}
		plain, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s.%s: secret column written with %T, not a string", s.Table, field.DBName, value)
		}
		if field.DBName == "agent_token" {
			updates["agent_token_hash"] = HashAgentToken(plain)
		}
		sealed, err := box.Seal(plain)
		if err != nil {
			return err
		}
		updates[key] = sealed
	}
	return nil
}

// secretModels is every model that carries a credential column. The migration
// reads the columns off the schema by tag; this list only says where to look.
var secretModels = []any{
	&ServerSecret{},
	&Cluster{},
	&ObservabilitySource{},
	&HelmRepository{},
	&AlarmChannel{},
	&SSOProviderConfig{},
}

// SecretColumn is one credential column, addressed by table and primary key.
type SecretColumn struct {
	Table      string
	PrimaryKey string
	Column     string
}

// SecretColumns lists every credential column, read off the models' tags.
func SecretColumns() ([]SecretColumn, error) {
	var out []SecretColumn
	cache := &sync.Map{}
	for _, model := range secretModels {
		s, err := schema.Parse(model, cache, schema.NamingStrategy{})
		if err != nil {
			return nil, err
		}
		if len(s.PrimaryFieldDBNames) != 1 {
			return nil, fmt.Errorf("%s: secret migration needs a single-column primary key", s.Table)
		}
		for _, field := range s.Fields {
			if isSecretField(field) {
				out = append(out, SecretColumn{Table: s.Table, PrimaryKey: s.PrimaryFieldDBNames[0], Column: field.DBName})
			}
		}
	}
	return out, nil
}

// SecretRow is one stored credential value.
type SecretRow struct {
	ID    string
	Value string
}

// SecretRows is the migration's view of the database: read one column's
// non-empty values, write one back. It exists so the migration can be tested
// without Postgres.
type SecretRows interface {
	List(ctx context.Context, col SecretColumn) ([]SecretRow, error)
	Write(ctx context.Context, col SecretColumn, id, value string) error
	// BackfillAgentTokenHash sets a cluster's lookup hash.
	BackfillAgentTokenHash(ctx context.Context, id, hash string) error
}

// ErrSecretKeyMismatch is the boot refusal for stored ciphertext the configured
// key does not open, or ciphertext with no key configured at all.
var ErrSecretKeyMismatch = errors.New("stored credentials cannot be decrypted")

// SecretMigration reports what the boot pass did.
type SecretMigration struct {
	// Sealed is how many plaintext values were encrypted in place.
	Sealed int
	// Plaintext is how many values remain in the clear (no key configured).
	Plaintext int
	// Encrypted is how many values were already encrypted and opened.
	Encrypted int
}

// MigrateSecrets verifies every stored credential against the configured key
// and, with a key set, encrypts in place every value still in the clear. It is
// idempotent: a value already carrying the prefix is only checked.
//
// It refuses — and the server must not boot — when a stored value is encrypted
// and no key is set, or is encrypted under a different key. Either would
// otherwise surface later as a datasource that fails, a login provider that
// rejects KubeMG, or worse, ciphertext presented to a cluster as a token. The
// agent token's lookup hash is backfilled on the same pass, with or without a
// key, because the handshake finds a cluster by it.
func MigrateSecrets(ctx context.Context, rows SecretRows, box *secretbox.Box) (SecretMigration, error) {
	var report SecretMigration
	columns, err := SecretColumns()
	if err != nil {
		return report, err
	}
	for _, col := range columns {
		values, err := rows.List(ctx, col)
		if err != nil {
			return report, fmt.Errorf("read %s.%s: %w", col.Table, col.Column, err)
		}
		for _, row := range values {
			plain, err := box.Open(row.Value)
			if err != nil {
				return report, fmt.Errorf("%w: %s.%s (%s=%s): %v",
					ErrSecretKeyMismatch, col.Table, col.Column, col.PrimaryKey, row.ID, err)
			}
			switch {
			case secretbox.IsSealed(row.Value):
				report.Encrypted++
			case box.Enabled():
				sealed, err := box.Seal(plain)
				if err != nil {
					return report, err
				}
				if err := rows.Write(ctx, col, row.ID, sealed); err != nil {
					return report, fmt.Errorf("encrypt %s.%s (%s=%s): %w", col.Table, col.Column, col.PrimaryKey, row.ID, err)
				}
				report.Sealed++
			default:
				report.Plaintext++
			}
			if col.Table == "clusters" && col.Column == "agent_token" {
				if err := rows.BackfillAgentTokenHash(ctx, row.ID, HashAgentToken(plain)); err != nil {
					return report, fmt.Errorf("backfill agent token hash (id=%s): %w", row.ID, err)
				}
			}
		}
	}
	return report, nil
}

// gormSecretRows is SecretRows over the real database. It goes around the
// serializer on purpose: the migration has to see what is stored, not what the
// serializer would make of it.
type gormSecretRows struct{ gdb *gorm.DB }

// NewSecretRows returns the database-backed SecretRows.
func NewSecretRows(gdb *gorm.DB) SecretRows { return gormSecretRows{gdb: gdb} }

func (r gormSecretRows) List(ctx context.Context, col SecretColumn) ([]SecretRow, error) {
	var out []SecretRow
	q := fmt.Sprintf(`SELECT CAST(%q AS text) AS id, %q AS value FROM %q WHERE %q IS NOT NULL AND %q <> ''`,
		col.PrimaryKey, col.Column, col.Table, col.Column, col.Column)
	err := r.gdb.WithContext(ctx).Raw(q).Scan(&out).Error
	return out, err
}

func (r gormSecretRows) Write(ctx context.Context, col SecretColumn, id, value string) error {
	q := fmt.Sprintf(`UPDATE %q SET %q = ? WHERE CAST(%q AS text) = ?`, col.Table, col.Column, col.PrimaryKey)
	return r.gdb.WithContext(ctx).Exec(q, value, id).Error
}

func (r gormSecretRows) BackfillAgentTokenHash(ctx context.Context, id, hash string) error {
	return r.gdb.WithContext(ctx).
		Exec(`UPDATE clusters SET agent_token_hash = ? WHERE CAST(id AS text) = ? AND agent_token_hash IS DISTINCT FROM ?`, hash, id, hash).Error
}
