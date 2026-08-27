package db

import (
	"encoding/json"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/apptemplate"
)

/*
 * A named bundle of manifests, plus the small declared hole set that makes it
 * a template rather than a saved YAML file.
 *
 * Server-wide, like a chart repository — a template is a fact about what this
 * installation offers to create, not about any one cluster, and the same
 * argument applies: duplicating one per cluster would mean an administrator
 * writing it once per cluster and a fleet where half the clusters can install
 * it. Writing one is admin-only for the reason a chart repository's write is:
 * a template renders into objects the console then offers to create with a
 * single click, and deciding what belongs on that list is an administrative
 * act. Reading the catalogue is open to anyone the console is open to, for the
 * same reason a chart catalogue is — a form offering a template must not
 * discover the list by being refused.
 *
 * Rendering stops at YAML. This table stores no cluster, no namespace and no
 * record of anything it was ever used to create; that is `resources_create.go`'s
 * job, one object at a time, exactly as it is for a hand-written manifest.
 */

// AppTemplate is one named bundle.
type AppTemplate struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Name is the identity — a slug, because it addresses the row the way a
	// cluster console names a chart release, not a free-text title.
	Name        string `gorm:"size:63;uniqueIndex;not null" json:"name"`
	Title       string `gorm:"size:255" json:"title,omitempty"`
	Description string `gorm:"type:text" json:"description,omitempty"`

	// Manifests and Parameters are `json:"-"`: the API layer decodes them into
	// apptemplate types and reports those, rather than the raw storage
	// columns, so a handler that forgets to hide anything cannot leak the
	// stored JSON's shape by accident.
	Manifests string `gorm:"type:text;not null" json:"-"`
	// Parameters is a JSON array of apptemplate.Parameter.
	Parameters string `gorm:"type:text" json:"-"`

	// Seeded marks a row this build put there on first boot rather than one an
	// administrator wrote. Exactly like a seeded chart repository, it changes
	// nothing about how the row behaves — a seeded template is editable and
	// deletable like any other.
	Seeded bool `gorm:"not null;default:false" json:"seeded"`

	CreatedBy string `gorm:"size:255" json:"created_by,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (AppTemplate) TableName() string { return "app_templates" }

// Params decodes the stored parameter declaration. A row whose JSON does not
// decode reads as a template with no parameters rather than as an error — like
// HelmChart.ChartVersions, it can only have been written by a different build,
// and the fix is to save the row again, not to fail every read of it.
func (t AppTemplate) Params() []apptemplate.Parameter {
	if t.Parameters == "" {
		return nil
	}
	var params []apptemplate.Parameter
	if err := json.Unmarshal([]byte(t.Parameters), &params); err != nil {
		return nil
	}
	return params
}
