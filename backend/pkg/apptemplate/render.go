package apptemplate

import (
	"fmt"
	"strconv"
	"unicode"
)

// maxParameterValueLength bounds one substituted value. A value here is a
// scalar in somebody else's YAML document — a name, an image tag, a replica
// count — and nothing that belongs in one of those positions is anywhere near
// this long. The bound exists for the same reason the newline check does: it
// keeps a value a value, never a document.
const maxParameterValueLength = 1024

// Render substitutes a bundle's `${name}` placeholders with the given values
// and stops — it does not parse, does not create anything, and does not run a
// second pass over its own output.
//
// Every check here is about keeping a value a *value*: a scalar that lands on
// one line, in one place, and cannot reach past the position the author of the
// bundle put it in. That is why a value cannot contain a newline, a carriage
// return, or any other control character — a value with a newline in it is how
// literal text substitution turns into YAML injection, adding fields or
// documents the bundle's author never wrote. It is why values map is checked
// against the declaration in both directions: a value for an undeclared
// parameter is refused by name rather than silently ignored, and a required
// parameter with neither a supplied value nor a default is refused rather than
// rendering as an empty string nobody asked for. And it is why substitution is
// a single textual pass over the original document — the placeholder regular
// expression is matched against the bundle's own text, never against text this
// function already produced, so a value of `${other}` is inert: it appears in
// the output exactly as given, because there is no second pass left to expand
// it in.
func Render(manifests string, params []Parameter, values map[string]string) (string, error) {
	if err := ValidateParameters(params); err != nil {
		return "", err
	}

	declared := make(map[string]Parameter, len(params))
	for _, p := range params {
		declared[p.Name] = p
	}
	for name := range values {
		if _, ok := declared[name]; !ok {
			return "", fmt.Errorf("apptemplate: a value was given for undeclared parameter %q", name)
		}
	}

	resolved := make(map[string]string, len(params))
	for _, p := range params {
		given, ok := values[p.Name]
		if ok && given != "" {
			if err := validateScalar(given); err != nil {
				return "", fmt.Errorf("apptemplate: parameter %q: %w", p.Name, err)
			}
			if p.Type == "number" {
				if _, err := strconv.ParseFloat(given, 64); err != nil {
					return "", fmt.Errorf("apptemplate: parameter %q must be a number", p.Name)
				}
			}
			resolved[p.Name] = given
			continue
		}

		// No usable value was supplied. A default fills in exactly as a
		// missing value would; its absence is a refusal only for a required
		// parameter, and only here — a declaration with no value anywhere is
		// not itself invalid, since the render that will finally need one has
		// not happened yet.
		if p.Default != "" {
			resolved[p.Name] = p.Default
			continue
		}
		if p.Required {
			return "", fmt.Errorf("apptemplate: parameter %q is required", p.Name)
		}
		resolved[p.Name] = ""
	}

	return placeholderPattern.ReplaceAllStringFunc(manifests, func(match string) string {
		name := placeholderPattern.FindStringSubmatch(match)[1]
		if value, ok := resolved[name]; ok {
			return value
		}
		// An undeclared placeholder cannot reach here for a bundle that passed
		// ValidateBundle, but Render is callable on its own — leaving it as
		// written, rather than blanking it, is what makes that case visible
		// instead of silent.
		return match
	}), nil
}

// validateScalar is the "a value is a value" check: no newline, no carriage
// return, no other control character, and bounded length.
func validateScalar(v string) error {
	if len(v) > maxParameterValueLength {
		return fmt.Errorf("value is longer than %d characters", maxParameterValueLength)
	}
	for _, r := range v {
		if r == '\n' || r == '\r' || (unicode.IsControl(r)) {
			return fmt.Errorf("value may not contain control characters")
		}
	}
	return nil
}
