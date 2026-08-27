// Package apptemplate renders a stored manifest bundle with a declared set of
// holes in it, and stops there.
//
// A template is not a chart. It has no `.Values` object, no `tpl` function, no
// `.Capabilities`, no `lookup`, no subcharts and no hooks — none of the things
// that make `pkg/helm` need the real Helm engine. What it substitutes is a
// closed, declared list of named parameters, each a scalar, and what it
// substitutes them into is literal text. That narrowness is deliberate: a
// template is written by an administrator and *rendered* by anyone the console
// is open to, and the one property that has to hold across that boundary is
// that rendering can never become an evaluation seam — a renderer's caller
// cannot make it execute anything, reach the filesystem, or produce a
// substitution that was not one of the parameters the administrator declared.
//
// Rendering stops at YAML text. Creating the objects it describes is the
// existing per-object `POST /clusters/:id/resources/object` path, one object at
// a time, each its own audit record and its own grant check — a template does
// not get a bulk-apply route the rest of the console does not have.
package apptemplate

import "regexp"

// Parameter is one declared hole in a bundle.
type Parameter struct {
	// Name is what appears inside `${...}` in the manifests. The pattern is
	// deliberately narrow — lowercase, digits, underscore, starting with a
	// letter — because it is substituted into YAML text unescaped, and a name
	// containing YAML's own punctuation would make the *declaration* the
	// injection seam this package exists to close.
	Name        string `json:"name"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	// Type is "string" or "number". A number type is enforced at render time —
	// see Render — so a bundle that positions a parameter as, say, a replica
	// count cannot be handed a value that would render invalid YAML.
	Type string `json:"type"`
	// Default is used when a render omits this parameter. An empty Default on
	// a required parameter with no value supplied is a render-time refusal, not
	// a silent empty string.
	Default  string `json:"default,omitempty"`
	Required bool   `json:"required"`
}

// Object is one Kubernetes object recovered from a rendered bundle, in the same
// shape `pkg/helm`'s Object reports one in — a template and a chart both end at
// "here is what would be created," and a console showing one alongside the
// other benefits from them looking alike.
type Object struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace,omitempty"`
	YAML       string `json:"yaml"`
}

// parameterNamePattern is deliberately the same shape a Kubernetes label key's
// simple form takes, minus the dashes: a name that can only ever be a bare
// identifier, never something that needs escaping wherever it is quoted.
var parameterNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// placeholderPattern is `${name}`. It is intentionally not configurable — a
// second syntax would be a second place to audit for the same injection
// property the first one was built to avoid.
var placeholderPattern = regexp.MustCompile(`\$\{([a-zA-Z0-9_]+)\}`)

// Placeholders lists every `${name}` referenced in a bundle, deduplicated, in
// the order each first appears. It is what ValidateBundle checks declarations
// against and what a console can use to prompt for values without decoding the
// parameter list at all.
func Placeholders(manifests string) []string {
	matches := placeholderPattern.FindAllStringSubmatch(manifests, -1)
	seen := make(map[string]bool, len(matches))
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		name := match[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}
