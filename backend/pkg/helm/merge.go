package helm

import (
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/kubernetes/scheme"
)

/*
 * Upgrading an object that is already there.
 *
 * `helm upgrade` does not overwrite a live object with what the chart rendered,
 * and the difference matters on the first upgrade of anything real. A live
 * Deployment carries fields nobody wrote: defaults the API server filled in, a
 * `clusterIP` allocated for a Service, a replica count a HorizontalPodAutoscaler
 * owns. A plain replace would send all of those back as their template values
 * and undo them.
 *
 * So the write is a **three-way merge**, over three documents:
 *
 *   - **original** — what the *previous revision* rendered. This is the only
 *     reason a release records its manifest, and it is what makes deletion of a
 *     removed field possible: a field present in the old render and absent from
 *     the new one was removed *by the chart*, and must be cleared; a field
 *     absent from both was never Helm's and must be left alone.
 *   - **modified** — what this revision rendered.
 *   - **live** — what the cluster currently holds.
 *
 * Which merge is used depends on whether the kind has a Go type: a built-in
 * kind gets a **strategic** merge, which knows that `containers` is keyed by
 * name rather than being a list to replace wholesale — replacing it is how a
 * sidecar added by a mutating webhook disappears on every upgrade. A CRD's kind
 * has no such schema anywhere, so it gets a JSON merge patch, which is exactly
 * what Helm falls back to for the same reason.
 *
 * The patch is computed here and **applied here**, producing a whole object to
 * PUT. That is not a shortcut around a PATCH — the tunnel carries one content
 * type, so a strategic-merge PATCH cannot be expressed on it — and it is the
 * read-modify-write the rest of this package's writes already use, which means
 * the live object's `resourceVersion` travels back with it and a concurrent
 * change becomes a 409 instead of a silent clobber.
 */

// ThreeWayMerge produces the object to write: the live object with this
// revision's render merged into it, and with what the previous revision rendered
// and this one did not, removed.
//
// The returned document keeps the live object's `resourceVersion`, so the write
// it feeds is conditional on nothing having changed since the read.
func ThreeWayMerge(original, modified, live []byte) ([]byte, error) {
	if len(live) == 0 {
		return modified, nil
	}
	gvk, err := groupVersionKindOf(modified)
	if err != nil {
		return nil, err
	}

	// No previous render to compare against — a release adopting an object
	// somebody else created, or an upgrade from a revision whose manifest was
	// not recorded. The patch is then the render itself: every field the chart
	// declares is applied, and **nothing is removed**, because nothing here can
	// tell which of the live object's fields Helm used to own.
	//
	// Neither three-way form would do. With `{}` as the original, every field
	// the live object carries reads as a change somebody else made and
	// `overwrite=false` reports the whole object as a conflict; with the live
	// object as the original, every field the chart does not render reads as one
	// the chart removed. Both turn the most ordinary case — adopting an object
	// that is already there — into a destructive or failed upgrade.
	patch := modified
	if len(original) > 0 {
		if patch, err = patchFor(gvk, original, modified, live); err != nil {
			return nil, err
		}
	}
	merged, err := applyPatch(gvk, live, patch)
	if err != nil {
		return nil, err
	}
	return preserveIdentity(merged, live)
}

// patchFor computes the three-way patch, strategic where a schema exists.
func patchFor(gvk schema.GroupVersionKind, original, modified, live []byte) ([]byte, error) {
	object, err := scheme.Scheme.New(gvk)
	if err != nil {
		// Not a registered kind: a custom resource, or a built-in from an API
		// version this build of client-go predates. Both take the JSON merge
		// patch, which is Helm's own fallback.
		patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch(original, modified, live)
		if err != nil {
			return nil, fmt.Errorf("this object's change could not be computed: %w", err)
		}
		return patch, nil
	}

	lookup, err := strategicpatch.NewPatchMetaFromStruct(object)
	if err != nil {
		return nil, fmt.Errorf("this object's change could not be computed: %w", err)
	}
	// `overwrite` is **true**, which is what Helm's own `updateResource` passes
	// (`pkg/kube/client.go`), and it is not the obvious choice — the tempting
	// reading is that `false` is the careful one, reporting a conflict rather
	// than resolving it in the chart's favour. That reading is wrong, and it
	// fails in the ordinary case rather than an exotic one: `helm upgrade` run
	// once from a terminal against a release installed here changes the live
	// object without changing the recorded manifest, and every field it touched
	// is then a conflict for ever. The object becomes permanently
	// un-upgradeable and un-rollback-able, which is far worse than the chart
	// winning a field somebody else also set. Deciding between the two is what
	// `--force` and a re-render are for, not a patch that refuses to compute.
	patch, err := strategicpatch.CreateThreeWayMergePatch(original, modified, live, lookup, true)
	if err != nil {
		return nil, fmt.Errorf("this object's change could not be computed: %w", err)
	}
	return patch, nil
}

// applyPatch turns the patch into the whole object the write will send.
func applyPatch(gvk schema.GroupVersionKind, live, patch []byte) ([]byte, error) {
	object, err := scheme.Scheme.New(gvk)
	if err != nil {
		merged, err := jsonMergePatch(live, patch)
		if err != nil {
			return nil, fmt.Errorf("this object's change could not be applied: %w", err)
		}
		return merged, nil
	}

	lookup, err := strategicpatch.NewPatchMetaFromStruct(object)
	if err != nil {
		return nil, fmt.Errorf("this object's change could not be applied: %w", err)
	}
	merged, err := strategicpatch.StrategicMergePatchUsingLookupPatchMeta(live, patch, lookup)
	if err != nil {
		return nil, fmt.Errorf("this object's change could not be applied: %w", err)
	}
	return merged, nil
}

// preserveIdentity puts back the three fields a merge must never move.
//
// A patch that clears `resourceVersion` turns a conditional write into an
// unconditional one, which is the whole guard against a concurrent change. `uid`
// and `creationTimestamp` are the API server's and are refused if they change.
// None of these are things a chart can legitimately render, so restoring them
// from the live object is not overriding an operator's intent.
func preserveIdentity(merged, live []byte) ([]byte, error) {
	var into, from map[string]any
	if err := json.Unmarshal(merged, &into); err != nil {
		return nil, fmt.Errorf("the merged object could not be read")
	}
	if err := json.Unmarshal(live, &from); err != nil {
		return nil, fmt.Errorf("the live object could not be read")
	}

	liveMeta, _ := from["metadata"].(map[string]any)
	intoMeta, _ := into["metadata"].(map[string]any)
	if liveMeta == nil || intoMeta == nil {
		return merged, nil
	}
	for _, field := range []string{"resourceVersion", "uid", "creationTimestamp"} {
		if value, ok := liveMeta[field]; ok {
			intoMeta[field] = value
		}
	}

	document, err := json.Marshal(into)
	if err != nil {
		return nil, fmt.Errorf("the merged object could not be encoded")
	}
	return document, nil
}

// jsonMergePatch applies RFC 7386 semantics: a null clears a key, a mapping
// merges, everything else replaces.
func jsonMergePatch(live, patch []byte) ([]byte, error) {
	var target, changes map[string]any
	if err := json.Unmarshal(live, &target); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(patch, &changes); err != nil {
		return nil, err
	}
	return json.Marshal(mergeMaps(target, changes))
}

func mergeMaps(target, changes map[string]any) map[string]any {
	if target == nil {
		target = map[string]any{}
	}
	for key, value := range changes {
		if value == nil {
			delete(target, key)
			continue
		}
		if nested, ok := value.(map[string]any); ok {
			if existing, ok := target[key].(map[string]any); ok {
				target[key] = mergeMaps(existing, nested)
				continue
			}
		}
		target[key] = value
	}
	return target
}

// groupVersionKindOf reads an object's kind for the schema lookup.
func groupVersionKindOf(document []byte) (schema.GroupVersionKind, error) {
	var header struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
	}
	if err := json.Unmarshal(document, &header); err != nil {
		return schema.GroupVersionKind{}, fmt.Errorf("this object could not be read")
	}
	group, err := schema.ParseGroupVersion(header.APIVersion)
	if err != nil {
		return schema.GroupVersionKind{}, fmt.Errorf("%q is not an apiVersion", header.APIVersion)
	}
	if header.Kind == "" {
		return schema.GroupVersionKind{}, fmt.Errorf("this object names no kind")
	}
	return group.WithKind(header.Kind), nil
}
