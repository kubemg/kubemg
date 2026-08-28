package db

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm/clause"
)

// Setting keys. Each one shadows the environment variable of the same meaning:
// the environment supplies the boot-time default, a stored value overrides it
// at runtime so an operator can fix the address without a redeploy.
const (
	SettingPublicURL      = "public_url"
	SettingAgentImage     = "agent_image"
	SettingAgentNamespace = "agent_namespace"
	// SettingAuditRetentionDays is how long a proxied call stays in the audit
	// table. It is stored as a decimal string like every other setting so the
	// key/value table needs no second column.
	SettingAuditRetentionDays = "audit_retention_days"
	// SettingSessionRecordingRetentionDays is how long a terminal recording is
	// kept. It defaults to the audit window and may only be *shorter*: a replay
	// of a shell outliving the trail that says the shell was opened would be the
	// wrong way round, and it is the heavier artefact of the two.
	SettingSessionRecordingRetentionDays = "session_recording_retention_days"
	// SettingAuditVerbs is the comma-separated list of verbs that reach the audit
	// table. Empty means every verb, which is the default and the only setting
	// value that cannot lose information.
	SettingAuditVerbs = "audit_verbs"
	// SettingRecordExecSessions turns interactive session recording on or off at
	// runtime, within what the process was started able to do — a server with no
	// recording directory cannot be talked into recording by a database row.
	SettingRecordExecSessions = "record_exec_sessions"
	// SettingKubeconfigMaxTTLHours is the longest a generated kubeconfig may be
	// asked to live, in hours. Unset takes k8s.DefaultMaxTTL, and no value can
	// raise it past k8s.MaxTTL — this setting moves the ceiling within a bound
	// the build fixes, it does not remove one.
	//
	// Hours rather than days because the setting has to be able to move in both
	// directions: an install handing out three-month credentials and one that
	// refuses anything over an eight-hour shift are the same decision, and days
	// cannot express the second.
	SettingKubeconfigMaxTTLHours = "kubeconfig_max_ttl_hours"
	// SettingRecordManifestDiffs turns on storing the field-level diff of a
	// manifest write on its audit row. It defaults OFF, unlike the trail's
	// other switches: a manifest body can carry values as sensitive as a
	// Secret's without being a Secret — an inlined token in a ConfigMap or a
	// Deployment's env — so this is a new class of retained data that an
	// operator opts into rather than one that quietly starts happening.
	SettingRecordManifestDiffs = "record_manifest_diffs"
	// SettingShellEnabled is the operator's switch on the browser shell. It
	// defaults on, and turning it off refuses new shells rather than reaping the
	// ones already open — a session somebody is mid-command in is not a setting.
	SettingShellEnabled = "shell_enabled"
	// SettingShellImage is the image a shell pod runs. It shadows
	// KUBEMG_SHELL_IMAGE, which is what an air-gapped site points at its mirror.
	SettingShellImage = "shell_image"
	// SettingShellIdleTimeoutMinutes is how long a shell may go without a
	// keystroke before it is reclaimed. Minutes rather than hours because the
	// interesting range is a coffee break to a working day.
	SettingShellIdleTimeoutMinutes = "shell_idle_timeout_minutes"
	// SettingShellMaxLifetimeHours is the absolute deadline written into the pod
	// itself. It is capped by the kubeconfig ceiling at the point of use: a shell
	// must not outlive the credential inside it.
	SettingShellMaxLifetimeHours = "shell_max_lifetime_hours"
	// SettingSetupCompletedAt stamps the moment first-run setup finished, as an
	// RFC 3339 timestamp. Its presence is the whole signal — the console shows
	// the install wizard until it is set, and never again afterwards.
	//
	// It is deliberately absent from SettingKeys and from the settings API: this
	// is a fact about the install's lifecycle rather than a knob an operator
	// turns, and re-running setup on a configured bastion would mean walking an
	// administrator back through decisions their fleet is already relying on.
	SettingSetupCompletedAt = "setup_completed_at"
	// SettingSetupStartedAt stamps the boot that seeded this database, which is
	// what marks it as an install that has been offered the wizard.
	//
	// It exists to tell two states apart that otherwise look identical from a
	// later boot: a database created by this version and still waiting for
	// somebody to finish setup, and one that predates the wizard entirely. Both
	// have users and no completion stamp. Deriving the difference from the user
	// count would be wrong on the second boot of every fresh install — which is
	// the boot that would then silently stamp setup as finished and hide the
	// wizard from an operator who never saw it.
	SettingSetupStartedAt = "setup_started_at"
	// SettingBootstrapAdminID names the administrator seeded on first boot, for
	// exactly as long as that account still holds the password it was seeded
	// with. Changing that password clears it, and deleting the account clears it
	// too — so its presence means "somebody could still sign in with the password
	// printed in the boot log", which is the one thing setup must not be allowed
	// to finish while it is true.
	//
	// Like SettingSetupCompletedAt this is lifecycle state rather than a setting,
	// and is kept out of SettingKeys and the settings API for the same reason.
	SettingBootstrapAdminID = "bootstrap_admin_id"
)

// SettingKeys enumerates the runtime-configurable settings.
var SettingKeys = []string{
	SettingPublicURL,
	SettingAgentImage,
	SettingAgentNamespace,
	SettingAuditRetentionDays,
	SettingSessionRecordingRetentionDays,
	SettingAuditVerbs,
	SettingRecordExecSessions,
	SettingRecordManifestDiffs,
	SettingKubeconfigMaxTTLHours,
	SettingShellEnabled,
	SettingShellImage,
	SettingShellIdleTimeoutMinutes,
	SettingShellMaxLifetimeHours,
}

// Setting is one operator-configurable value. An empty Value means "unset" and
// reads as the configured default rather than as an empty string — that is how
// a setting is cleared without deleting the row.
type Setting struct {
	Key       string    `gorm:"primaryKey;size:64" json:"key"`
	Value     string    `gorm:"type:text" json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy uint      `json:"updated_by"`
}

// Settings returns every stored override keyed by setting name.
func (s *Store) Settings(ctx context.Context) (map[string]string, error) {
	var rows []Setting
	if err := s.gdb.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.Key] = row.Value
	}
	return out, nil
}

// PutSettings upserts the supplied overrides. Keys absent from the map are left
// alone, so a caller can write one setting without reading the others first.
func (s *Store) PutSettings(ctx context.Context, values map[string]string, updatedBy uint) error {
	if len(values) == 0 {
		return nil
	}
	now := time.Now().UTC()
	rows := make([]Setting, 0, len(values))
	for key, value := range values {
		rows = append(rows, Setting{Key: key, Value: value, UpdatedAt: now, UpdatedBy: updatedBy})
	}

	err := s.gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at", "updated_by"}),
	}).Create(&rows).Error
	if err != nil {
		return fmt.Errorf("put settings: %w", err)
	}
	return nil
}
