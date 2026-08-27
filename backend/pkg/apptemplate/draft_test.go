package apptemplate

import (
	"strings"
	"testing"
)

const liveDeployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: prod
  uid: 1234-5678
  resourceVersion: "42"
  generation: 3
  creationTimestamp: "2024-01-01T00:00:00Z"
  selfLink: /apis/apps/v1/namespaces/prod/deployments/api
  finalizers:
    - foregroundDeletion
  ownerReferences:
    - apiVersion: apps/v1
      kind: ReplicaSet
      name: api-abc123
  managedFields:
    - manager: kubectl
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: '{"stale":"json"}'
    deployment.kubernetes.io/revision: "7"
    team: platform
  labels:
    app: api
spec:
  replicas: 3
  template:
    metadata:
      creationTimestamp: null
      labels:
        app: api
    spec:
      containers:
        - name: api
          image: example.com/api:1.2.3
          ports:
            - containerPort: 8080
status:
  availableReplicas: 3
  conditions:
    - type: Available
      status: "True"
`

func TestDraftStripsClusterFingerprints(t *testing.T) {
	manifests, params, err := Draft(liveDeployment)
	if err != nil {
		t.Fatalf("draft: %v", err)
	}

	for _, absent := range []string{
		"uid:", "resourceVersion:", "generation:", "creationTimestamp:",
		"selfLink:", "finalizers:", "ownerReferences:", "managedFields:",
		"status:", "availableReplicas:", "namespace:",
		"last-applied-configuration", "deployment.kubernetes.io/revision",
	} {
		if strings.Contains(manifests, absent) {
			t.Errorf("expected %q to be stripped, manifest:\n%s", absent, manifests)
		}
	}
	// A label that was not touched survives.
	if !strings.Contains(manifests, "app: api") {
		t.Errorf("expected the untouched label to survive:\n%s", manifests)
	}
	// The annotation map is not dropped outright: "team" was not one of the
	// two cluster-owned keys and must still be there.
	if !strings.Contains(manifests, "team: platform") {
		t.Errorf("expected the surviving annotation to remain:\n%s", manifests)
	}

	names := map[string]Parameter{}
	for _, p := range params {
		names[p.Name] = p
	}
	for _, want := range []string{"name", "image", "replicas", "port"} {
		if _, ok := names[want]; !ok {
			t.Errorf("expected a %q parameter, got %+v", want, params)
		}
	}
	if names["name"].Default != "api" || !names["name"].Required {
		t.Errorf("name parameter: %+v", names["name"])
	}
	if names["image"].Default != "example.com/api:1.2.3" {
		t.Errorf("image parameter: %+v", names["image"])
	}
	if names["replicas"].Default != "3" || names["replicas"].Type != "number" {
		t.Errorf("replicas parameter: %+v", names["replicas"])
	}
	if names["port"].Default != "8080" || names["port"].Type != "number" {
		t.Errorf("port parameter: %+v", names["port"])
	}
	// A numeric field's placeholder must not end up quoted.
	if strings.Contains(manifests, `"${replicas}"`) || strings.Contains(manifests, `'${replicas}'`) {
		t.Errorf("replicas placeholder was quoted:\n%s", manifests)
	}
	if strings.Contains(manifests, `"${port}"`) || strings.Contains(manifests, `'${port}'`) {
		t.Errorf("port placeholder was quoted:\n%s", manifests)
	}
}

func TestDraftOutputRoundTrips(t *testing.T) {
	manifests, params, err := Draft(liveDeployment)
	if err != nil {
		t.Fatalf("draft: %v", err)
	}
	if err := ValidateBundle(manifests, params); err != nil {
		t.Fatalf("a draft must satisfy ValidateBundle: %v\n%s", err, manifests)
	}

	rendered, err := Render(manifests, params, map[string]string{
		"name": "api-2", "image": "example.com/api:1.3.0", "replicas": "5", "port": "9090",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if _, err := Objects(rendered); err != nil {
		t.Fatalf("a rendered draft must produce parseable objects: %v\n%s", err, rendered)
	}
	if !strings.Contains(rendered, "name: api-2") {
		t.Errorf("rendered manifest missing substituted name:\n%s", rendered)
	}
	if !strings.Contains(rendered, "replicas: 5") {
		t.Errorf("rendered manifest missing substituted replicas:\n%s", rendered)
	}
}

func TestDraftRefusesAnObjectWithNoKind(t *testing.T) {
	if _, _, err := Draft("metadata:\n  name: x\n"); err == nil {
		t.Fatal("expected a refusal")
	}
}

func TestDraftOnAServiceUsesItsOwnPort(t *testing.T) {
	service := `apiVersion: v1
kind: Service
metadata:
  name: api
  namespace: prod
spec:
  clusterIP: 10.0.0.5
  clusterIPs: ["10.0.0.5"]
  ports:
    - port: 443
      targetPort: 8443
`
	manifests, params, err := Draft(service)
	if err != nil {
		t.Fatalf("draft: %v", err)
	}
	if strings.Contains(manifests, "clusterIP") {
		t.Errorf("clusterIP was not stripped:\n%s", manifests)
	}
	found := false
	for _, p := range params {
		if p.Name == "port" {
			found = true
			if p.Default != "443" {
				t.Errorf("port default = %q, want 443", p.Default)
			}
		}
	}
	if !found {
		t.Fatalf("expected a port parameter, got %+v", params)
	}
}
