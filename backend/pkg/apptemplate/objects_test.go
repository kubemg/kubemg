package apptemplate

import "testing"

func TestObjectsSplitsAndReadsIdentity(t *testing.T) {
	rendered := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\n  namespace: default\n" +
		"---\napiVersion: v1\nkind: Service\nmetadata:\n  name: api\n"

	objects, err := Objects(rendered)
	if err != nil {
		t.Fatalf("objects: %v", err)
	}
	if len(objects) != 2 {
		t.Fatalf("expected 2 objects, got %d: %+v", len(objects), objects)
	}
	if objects[0].Kind != "Deployment" || objects[0].Name != "api" || objects[0].Namespace != "default" {
		t.Fatalf("first object: %+v", objects[0])
	}
	if objects[1].Kind != "Service" || objects[1].Namespace != "" {
		t.Fatalf("second object: %+v", objects[1])
	}
}

func TestObjectsRefusesADocumentWithNoKind(t *testing.T) {
	_, err := Objects("apiVersion: v1\nmetadata:\n  name: api\n")
	if err == nil {
		t.Fatal("expected a refusal")
	}
}
