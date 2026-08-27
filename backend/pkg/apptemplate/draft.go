package apptemplate

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"
)

// Sentinels stand in for a parameter's value in the object tree, before that
// tree is marshalled back to YAML. They are unique enough that a textual
// search-and-replace over the marshalled document cannot mistake anything else
// for one — which is what makes the "marshal, then replace the sentinel"
// approach exact rather than a best-effort guess at which line held which
// field. A string sentinel is a plain identifier, so YAML never has a reason to
// quote it; a numeric sentinel is a Go int, so YAML never has a reason to write
// it any way but as bare digits — in both cases the placeholder ends up in the
// output exactly where the original scalar was, and never as a quoted string
// standing in a numeric field.
const (
	nameSentinel     = "kubemgdraftplaceholdername"
	imageSentinel    = "kubemgdraftplaceholderimage"
	replicasSentinel = -918273645
	portSentinel     = -192837465
)

// Draft turns one live object's manifest into a starter template bundle: the
// cluster's own fingerprints on it stripped out, and the handful of fields an
// operator obviously wants to vary next time offered up as parameters.
//
// What is stripped is everything the *cluster* wrote rather than the object's
// author: identity and bookkeeping the API server assigns (`uid`,
// `resourceVersion`, `generation`, `managedFields`, `selfLink`,
// `creationTimestamp`, wherever in the document it appears),
// `ownerReferences` and `finalizers` (both describe this specific object's
// place in this specific cluster, not the shape a new one should take),
// `status` in full (every word of it is the cluster's report, never the
// author's declaration), the two annotations kubectl and the Deployment
// controller stamp on for their own bookkeeping, and `spec.clusterIP`/
// `spec.clusterIPs` (assigned, never declared). `namespace` is stripped too,
// and deliberately not offered back as a parameter: exactly as it is for
// create, the namespace is the *address* an object is created at, not a
// property of the bundle — a template says what to create, not where.
//
// What is offered as a parameter is the small set of fields a second instance
// of this object would almost certainly want to change: `name` always,
// because two objects cannot share one; the first container's `image`, if
// there is one; `spec.replicas`, if the kind has one; and the first exposed
// port, whether that is a container's `containerPort` or, for a Service with
// no container to ask, its own first `port`.
func Draft(objectYAML string) (string, []Parameter, error) {
	var obj map[string]any
	if err := yaml.Unmarshal([]byte(objectYAML), &obj); err != nil {
		return "", nil, fmt.Errorf("apptemplate: the object did not parse as YAML: %w", err)
	}
	kind, _ := obj["kind"].(string)
	if kind == "" {
		return "", nil, fmt.Errorf("apptemplate: the object has no kind")
	}

	delete(obj, "status")
	stripMetadataDeep(obj)

	metadata, _ := obj["metadata"].(map[string]any)
	if metadata == nil {
		return "", nil, fmt.Errorf("apptemplate: the object has no metadata")
	}
	for _, key := range []string{
		"uid", "resourceVersion", "generation", "managedFields",
		"selfLink", "ownerReferences", "finalizers", "namespace",
	} {
		delete(metadata, key)
	}

	if spec, ok := obj["spec"].(map[string]any); ok {
		delete(spec, "clusterIP")
		delete(spec, "clusterIPs")
	}

	originalName, _ := metadata["name"].(string)
	if originalName == "" {
		return "", nil, fmt.Errorf("apptemplate: the object has no metadata.name")
	}
	metadata["name"] = nameSentinel

	params := []Parameter{
		{Name: "name", Label: "Name", Type: "string", Default: originalName, Required: true},
	}

	if container := findFirstContainer(obj); container != nil {
		if image, ok := container["image"].(string); ok && image != "" {
			container["image"] = imageSentinel
			params = append(params, Parameter{
				Name: "image", Label: "Image", Type: "string", Default: image,
			})
		}
		if portEntry, port, ok := firstContainerPort(container); ok {
			portEntry["containerPort"] = portSentinel
			params = append(params, Parameter{
				Name: "port", Label: "Port", Type: "number", Default: numberToString(port),
			})
		}
	} else if kind == "Service" {
		if portEntry, port, ok := firstServicePort(obj); ok {
			portEntry["port"] = portSentinel
			params = append(params, Parameter{
				Name: "port", Label: "Port", Type: "number", Default: numberToString(port),
			})
		}
	}

	if spec, ok := obj["spec"].(map[string]any); ok {
		if replicas, ok := spec["replicas"].(float64); ok {
			spec["replicas"] = replicasSentinel
			params = append(params, Parameter{
				Name: "replicas", Label: "Replicas", Type: "number", Default: numberToString(replicas),
			})
		}
	}

	rendered, err := yaml.Marshal(obj)
	if err != nil {
		return "", nil, fmt.Errorf("apptemplate: the stripped object could not be re-encoded: %w", err)
	}
	manifest := string(rendered)
	manifest = strings.ReplaceAll(manifest, nameSentinel, "${name}")
	manifest = strings.ReplaceAll(manifest, imageSentinel, "${image}")
	manifest = strings.ReplaceAll(manifest, strconv.Itoa(replicasSentinel), "${replicas}")
	manifest = strings.ReplaceAll(manifest, strconv.Itoa(portSentinel), "${port}")

	return manifest, params, nil
}

// stripMetadataDeep removes the fields that belong to every `metadata` block
// wherever one appears in the document — not only the top-level object's, but
// a pod template's nested one too, which is where `creationTimestamp` shows up
// a second time on anything that owns a pod template.
func stripMetadataDeep(node any) {
	switch v := node.(type) {
	case map[string]any:
		if meta, ok := v["metadata"].(map[string]any); ok {
			delete(meta, "creationTimestamp")
			if annotations, ok := meta["annotations"].(map[string]any); ok {
				delete(annotations, "kubectl.kubernetes.io/last-applied-configuration")
				delete(annotations, "deployment.kubernetes.io/revision")
				if len(annotations) == 0 {
					delete(meta, "annotations")
				}
			}
			if labels, ok := meta["labels"].(map[string]any); ok && len(labels) == 0 {
				delete(meta, "labels")
			}
		}
		for _, child := range v {
			stripMetadataDeep(child)
		}
	case []any:
		for _, child := range v {
			stripMetadataDeep(child)
		}
	}
}

// findFirstContainer walks the whole document for the first `containers` list
// it finds — under `spec.template.spec` for a Deployment/StatefulSet/DaemonSet/
// Job, under `spec.jobTemplate.spec.template.spec` for a CronJob, or directly
// under `spec` for a bare Pod — and returns its first entry. Map key order in
// Go is random, so the walk sorts keys at each level; the document has at most
// one `containers` list in practice, so the order only matters for producing
// the same answer on repeated calls against the same input.
func findFirstContainer(node any) map[string]any {
	switch v := node.(type) {
	case map[string]any:
		if list, ok := v["containers"].([]any); ok && len(list) > 0 {
			if c, ok := list[0].(map[string]any); ok {
				return c
			}
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if found := findFirstContainer(v[k]); found != nil {
				return found
			}
		}
	case []any:
		for _, item := range v {
			if found := findFirstContainer(item); found != nil {
				return found
			}
		}
	}
	return nil
}

// firstContainerPort returns the first port entry of a container along with
// the numeric value it currently holds, so the caller can both read the
// original for the parameter's default and overwrite it with the sentinel in
// the same map the marshaller will see.
func firstContainerPort(container map[string]any) (map[string]any, float64, bool) {
	ports, ok := container["ports"].([]any)
	if !ok || len(ports) == 0 {
		return nil, 0, false
	}
	first, ok := ports[0].(map[string]any)
	if !ok {
		return nil, 0, false
	}
	port, ok := first["containerPort"].(float64)
	if !ok {
		return nil, 0, false
	}
	return first, port, true
}

func firstServicePort(obj map[string]any) (map[string]any, float64, bool) {
	spec, ok := obj["spec"].(map[string]any)
	if !ok {
		return nil, 0, false
	}
	ports, ok := spec["ports"].([]any)
	if !ok || len(ports) == 0 {
		return nil, 0, false
	}
	first, ok := ports[0].(map[string]any)
	if !ok {
		return nil, 0, false
	}
	port, ok := first["port"].(float64)
	if !ok {
		return nil, 0, false
	}
	return first, port, true
}

// numberToString formats a float64 recovered from decoded YAML/JSON the way an
// operator wrote it — as "3", never "3.0" — for whole numbers, which is what
// every field this package offers as a `number` parameter actually holds.
func numberToString(v float64) string {
	if v == math.Trunc(v) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}
