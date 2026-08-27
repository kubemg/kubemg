package db

import (
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/apptemplate"
)

// TestSeededAppTemplatesValidate pins the same property SeedHelmRepositories'
// starter catalogue is trusted to have: every bundle this build ships actually
// renders. A seed that fails ValidateBundle would only be caught the first
// time somebody tried to use it.
func TestSeededAppTemplatesValidate(t *testing.T) {
	for _, template := range SeededAppTemplates {
		t.Run(template.Name, func(t *testing.T) {
			var params []apptemplate.Parameter
			row := AppTemplate{Manifests: template.Manifests, Parameters: template.Parameters}
			params = row.Params()
			if len(params) == 0 {
				t.Fatalf("%s: no parameters decoded from stored JSON", template.Name)
			}
			if err := apptemplate.ValidateBundle(template.Manifests, params); err != nil {
				t.Fatalf("%s: %v", template.Name, err)
			}
		})
	}
}

func TestAppTemplateParamsOnUndecodableJSONReadsAsNone(t *testing.T) {
	row := AppTemplate{Parameters: "not json"}
	if params := row.Params(); params != nil {
		t.Fatalf("expected no parameters, got %+v", params)
	}
}
