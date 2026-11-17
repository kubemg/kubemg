package db

import (
	"slices"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

// auditIndexes renders the indexes AutoMigrate builds for audit_events as
// "column [DESC], ..." by name.
func auditIndexes(t *testing.T) map[string]string {
	t.Helper()
	parsed, err := schema.Parse(&AuditEvent{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, index := range parsed.ParseIndexes() {
		columns := make([]string, 0, len(index.Fields))
		for _, field := range index.Fields {
			columns = append(columns, strings.TrimSpace(field.DBName+" "+strings.ToUpper(field.Sort)))
		}
		out[index.Name] = strings.Join(columns, ", ")
	}
	return out
}

func TestAuditIndexesServeTheTrailsReads(t *testing.T) {
	indexes := auditIndexes(t)
	for name, want := range map[string]string{
		"idx_audit_at":         "at DESC",
		"idx_audit_user_at":    "user_id, at DESC, id DESC",
		"idx_audit_cluster_at": "cluster_id, at DESC, id DESC",
		"idx_audit_name":       "name",
	} {
		if got := indexes[name]; got != want {
			t.Errorf("%s = (%s), want (%s)", name, got, want)
		}
	}
	for name, columns := range indexes {
		for _, unread := range []string{"streaming", "guardrail_policy", "source_addr"} {
			if slices.Contains(strings.Split(columns, ", "), unread) {
				t.Errorf("%s indexes %s, which no read uses", name, unread)
			}
		}
	}
}

// Dropping an index the model still declares would have every boot build it
// and drop it again.
func TestObsoleteAuditIndexesAreNotDeclared(t *testing.T) {
	indexes := auditIndexes(t)
	for _, name := range obsoleteAuditIndexes {
		if _, declared := indexes[name]; declared {
			t.Errorf("Migrate drops %s, which AuditEvent still declares", name)
		}
	}
}
