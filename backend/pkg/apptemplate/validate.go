package apptemplate

import (
	"fmt"
	"strconv"
	"strings"
)

// parameterTypes is the closed set. A third type is a feature request, not a
// bug fix — adding one means deciding its render-time validation rule too, so
// it is spelled out here rather than left open to whatever a caller passes.
var parameterTypes = map[string]bool{"string": true, "number": true}

// ValidateParameters checks a declaration on its own, independent of any
// manifest text: the name matches the pattern, no two parameters share a name,
// the type is one of the two this package understands, and a `number`
// parameter's default — if it has one — actually parses as a number. Every
// error names the parameter it is about, because a bundle with several
// parameters and one anonymous complaint is a bundle an administrator has to
// guess at.
func ValidateParameters(params []Parameter) error {
	seen := make(map[string]bool, len(params))
	for _, p := range params {
		if !parameterNamePattern.MatchString(p.Name) {
			return fmt.Errorf("apptemplate: parameter %q does not match %s", p.Name, parameterNamePattern.String())
		}
		if seen[p.Name] {
			return fmt.Errorf("apptemplate: parameter %q is declared more than once", p.Name)
		}
		seen[p.Name] = true

		if !parameterTypes[p.Type] {
			return fmt.Errorf("apptemplate: parameter %q has an unknown type %q", p.Name, p.Type)
		}
		if p.Type == "number" && p.Default != "" {
			if _, err := strconv.ParseFloat(p.Default, 64); err != nil {
				return fmt.Errorf("apptemplate: parameter %q's default %q is not a number", p.Name, p.Default)
			}
		}
	}
	return nil
}

// ValidateBundle is the save-time check: it is what stands between an
// administrator's typo and a bundle that renders successfully today and fails
// the instant someone actually installs it.
//
// Three things have to hold for a bundle to be storable at all. The manifests
// cannot be empty. Every `${placeholder}` the manifests reference has to be a
// declared parameter — an undeclared placeholder would render as itself,
// literally, and land in a cluster as `${whatever}` rather than as an error
// anyone saw coming. And the bundle has to actually produce parseable YAML once
// substituted, which is checked by rendering it here, now, with every
// parameter's default (or a type-appropriate probe value for one that has
// none) rather than waiting for the first real render to discover a bundle
// that can never produce valid output.
func ValidateBundle(manifests string, params []Parameter) error {
	if strings.TrimSpace(manifests) == "" {
		return fmt.Errorf("apptemplate: the bundle has no manifests")
	}
	if err := ValidateParameters(params); err != nil {
		return err
	}

	declared := make(map[string]bool, len(params))
	for _, p := range params {
		declared[p.Name] = true
	}
	for _, name := range Placeholders(manifests) {
		if !declared[name] {
			return fmt.Errorf("apptemplate: %s is used but not declared as a parameter", "${"+name+"}")
		}
	}

	probes := make(map[string]string, len(params))
	for _, p := range params {
		switch {
		case p.Default != "":
			probes[p.Name] = p.Default
		case p.Type == "number":
			probes[p.Name] = "1"
		default:
			probes[p.Name] = "x"
		}
	}

	rendered, err := Render(manifests, params, probes)
	if err != nil {
		// Render's own errors already name the parameter; wrapping here would
		// only restate that this happened while validating a save.
		return fmt.Errorf("apptemplate: the bundle cannot be rendered: %w", err)
	}

	for _, doc := range splitDocuments(rendered) {
		if err := parseDocument(doc); err != nil {
			return fmt.Errorf("apptemplate: rendering produces invalid YAML: %w", err)
		}
	}
	return nil
}

// splitDocuments breaks a multi-document YAML string on its `---` separators
// and drops anything that renders to nothing: a blank document or one that is
// only comments, both of which are legal YAML and neither of which is an
// object.
func splitDocuments(rendered string) []string {
	parts := strings.Split(rendered, "\n---")
	// The first document is never preceded by its own separator; a leading
	// standalone "---" document-start marker still splits cleanly because the
	// loop below trims and discards anything left empty.
	docs := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		if isCommentOnly(trimmed) {
			continue
		}
		docs = append(docs, trimmed)
	}
	return docs
}

func isCommentOnly(doc string) bool {
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "---" || strings.HasPrefix(line, "#") {
			continue
		}
		return false
	}
	return true
}
