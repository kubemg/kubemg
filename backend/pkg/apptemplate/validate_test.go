package apptemplate

import "testing"

func TestValidateBundleRefusesEmptyManifests(t *testing.T) {
	if err := ValidateBundle("   ", nil); err == nil {
		t.Fatal("expected a refusal")
	}
}

func TestValidateBundleRefusesAnUndeclaredPlaceholder(t *testing.T) {
	err := ValidateBundle("metadata:\n  name: ${name}\n", nil)
	if err == nil {
		t.Fatal("expected a refusal")
	}
}

func TestValidateBundleAcceptsADeclaredPlaceholder(t *testing.T) {
	manifests := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: ${name}\n"
	params := []Parameter{{Name: "name", Type: "string", Required: true, Default: "cfg"}}
	if err := ValidateBundle(manifests, params); err != nil {
		t.Fatalf("expected the bundle to validate: %v", err)
	}
}

func TestValidateBundleRefusesABundleThatCanNeverRenderValidYAML(t *testing.T) {
	// The probe value substituted for a required, default-less parameter is a
	// bare "x" here, which leaves this document invalid however it is filled
	// in: an unquoted colon-bearing scalar breaks the map on that line.
	manifests := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: bad: ${name}\n"
	params := []Parameter{{Name: "name", Type: "string", Required: true}}
	if err := ValidateBundle(manifests, params); err == nil {
		t.Fatal("expected a refusal")
	}
}

func TestSplitDocumentsSkipsBlankAndCommentOnlyDocuments(t *testing.T) {
	docs := splitDocuments("a: 1\n---\n\n---\n# just a comment\n---\nb: 2\n")
	if len(docs) != 2 {
		t.Fatalf("expected 2 documents, got %d: %v", len(docs), docs)
	}
}
