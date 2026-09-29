package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Secret names. Each one is a value the server needs before it can serve a
// request and that nobody should have to invent by hand.
const (
	// ServerSecretJWTSigningKey signs every session token and every generated
	// kubeconfig. It is minted on first boot when the environment supplies none,
	// which is what lets an install come up with no configuration at all.
	//
	// `JWT_SECRET` still wins where it is set: a deployment that already rotates
	// it out of a secret manager keeps doing so, and nothing here overwrites it.
	ServerSecretJWTSigningKey = "jwt_signing_key"

	// ServerSecretTLSPair is the self-signed certificate and key the listener
	// serves when nobody supplied one — the certificate every agent package
	// pins. It is kept here as well as on disk so that losing the volume is not
	// a fleet-wide re-pin; see certs.EnsureKept.
	ServerSecretTLSPair = "tls_self_signed_pair"
)

// ServerSecret is a value the server generated for itself. It lives on its own
// table rather than in the Setting key/value store on purpose: Store.Settings
// returns every row it holds and that map feeds the settings API, so a signing
// key put there would be one careless field away from being served to a browser.
//
// Nothing here is operator-editable, which is the other half of the separation —
// a setting is a decision somebody makes, and this is a fact about the install.
type ServerSecret struct {
	Name      string    `gorm:"primaryKey;size:64" json:"name"`
	Value     string    `gorm:"type:text;serializer:secret" json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName pins the table so it reads as what it is in a DBA's schema dump.
func (ServerSecret) TableName() string { return "server_secrets" }

// EnsureServerSecret returns the stored secret, minting one with generate if
// there is none yet.
//
// The insert is conditional and the read that follows is unconditional, because
// first boot is exactly when two replicas start at once: both generate, one
// insert wins, and both then read the winner. Doing it the other way round — read,
// generate, write — would hand each replica its own signing key and every session
// would be valid on one of them and rejected by the other.
func (s *Store) EnsureServerSecret(
	ctx context.Context, name string, generate func() (string, error),
) (string, error) {
	if existing, err := s.serverSecret(ctx, name); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return "", err
	}

	value, err := generate()
	if err != nil {
		return "", fmt.Errorf("generate %s: %w", name, err)
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("generate %s: produced an empty secret", name)
	}

	row := ServerSecret{Name: name, Value: value, CreatedAt: time.Now().UTC()}
	if err := s.gdb.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "name"}}, DoNothing: true}).
		Create(&row).Error; err != nil {
		return "", fmt.Errorf("store %s: %w", name, err)
	}

	// Read back rather than trusting the value just generated: with DoNothing a
	// losing racer's insert is silently discarded, and the whole point is that
	// every replica ends up on the same key.
	return s.serverSecret(ctx, name)
}

func (s *Store) serverSecret(ctx context.Context, name string) (string, error) {
	var row ServerSecret
	err := s.gdb.WithContext(ctx).Where("name = ?", name).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("server secret %s: %w", name, err)
	}
	if strings.TrimSpace(row.Value) == "" {
		return "", ErrNotFound
	}
	return row.Value, nil
}

// StoredTLSPair returns the kept self-signed certificate pair, if there is one.
func (s *Store) StoredTLSPair(ctx context.Context) (string, bool, error) {
	pair, err := s.serverSecret(ctx, ServerSecretTLSPair)
	if errors.Is(err, ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return pair, true, nil
}

// EnsureTLSPair stores the pair generate mints unless one is already stored,
// and returns whichever is stored — EnsureServerSecret's race, won once.
func (s *Store) EnsureTLSPair(ctx context.Context, generate func() (string, error)) (string, error) {
	return s.EnsureServerSecret(ctx, ServerSecretTLSPair, generate)
}

// KeepTLSPair replaces the kept pair with the one being served. It goes through
// Create with an upsert rather than Updates so the value passes the column's
// secret serializer: an Updates map would reach the kubemg:seal_secrets callback
// instead, and one path for one column is easier to trust than two.
func (s *Store) KeepTLSPair(ctx context.Context, pair string) error {
	if strings.TrimSpace(pair) == "" {
		return errors.New("keep an empty certificate pair")
	}
	row := ServerSecret{Name: ServerSecretTLSPair, Value: pair, CreatedAt: time.Now().UTC()}
	if err := s.gdb.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "name"}},
			DoUpdates: clause.AssignmentColumns([]string{"value", "created_at"}),
		}).
		Create(&row).Error; err != nil {
		return fmt.Errorf("store %s: %w", ServerSecretTLSPair, err)
	}
	return nil
}
