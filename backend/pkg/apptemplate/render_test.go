package apptemplate

import "testing"

/*
 * What is pinned here is the injection property, not the happy path: a value
 * is a value, substitution never re-scans its own output, and the declared
 * parameter list is the only vocabulary a render is allowed to use.
 */

func TestLiteralSubstitution(t *testing.T) {
	params := []Parameter{{Name: "name", Type: "string", Required: true}}
	out, err := Render("metadata:\n  name: ${name}\n", params, map[string]string{"name": "api"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if out != "metadata:\n  name: api\n" {
		t.Fatalf("got %q", out)
	}
}

func TestAnUndeclaredValueKeyIsRefused(t *testing.T) {
	params := []Parameter{{Name: "name", Type: "string"}}
	_, err := Render("name: ${name}", params, map[string]string{"other": "x"})
	if err == nil {
		t.Fatal("expected a refusal")
	}
}

func TestAMissingRequiredParameterIsRefused(t *testing.T) {
	params := []Parameter{{Name: "name", Type: "string", Required: true}}
	_, err := Render("name: ${name}", params, map[string]string{})
	if err == nil {
		t.Fatal("expected a refusal")
	}
}

func TestARequiredParameterWithADefaultNeedsNoValue(t *testing.T) {
	params := []Parameter{{Name: "name", Type: "string", Required: true, Default: "api"}}
	out, err := Render("name: ${name}", params, map[string]string{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if out != "name: api" {
		t.Fatalf("got %q", out)
	}
}

func TestAnUnsetOptionalParameterSubstitutesEmptyString(t *testing.T) {
	params := []Parameter{{Name: "note", Type: "string"}}
	out, err := Render("note: [${note}]", params, map[string]string{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if out != "note: []" {
		t.Fatalf("got %q", out)
	}
}

func TestANonNumberValueForANumberParameterIsRefused(t *testing.T) {
	params := []Parameter{{Name: "replicas", Type: "number"}}
	_, err := Render("replicas: ${replicas}", params, map[string]string{"replicas": "many"})
	if err == nil {
		t.Fatal("expected a refusal")
	}
}

func TestANewlineInAValueIsRefused(t *testing.T) {
	params := []Parameter{{Name: "name", Type: "string"}}
	_, err := Render("name: ${name}", params, map[string]string{"name": "a\nb"})
	if err == nil {
		t.Fatal("expected a refusal")
	}
}

func TestAnOversizedValueIsRefused(t *testing.T) {
	params := []Parameter{{Name: "name", Type: "string"}}
	long := make([]byte, maxParameterValueLength+1)
	for i := range long {
		long[i] = 'a'
	}
	_, err := Render("name: ${name}", params, map[string]string{"name": string(long)})
	if err == nil {
		t.Fatal("expected a refusal")
	}
}

func TestASubstitutedValueIsNeverReScanned(t *testing.T) {
	params := []Parameter{
		{Name: "a", Type: "string"},
		{Name: "other", Type: "string", Default: "should-not-appear"},
	}
	out, err := Render("value: ${a}", params, map[string]string{"a": "${other}"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if out != "value: ${other}" {
		t.Fatalf("a substituted value was expanded: %q", out)
	}
}

func TestValidateParametersNamesDuplicatesAndTypes(t *testing.T) {
	cases := []struct {
		name   string
		params []Parameter
	}{
		{"bad name", []Parameter{{Name: "Name", Type: "string"}}},
		{"duplicate", []Parameter{{Name: "x", Type: "string"}, {Name: "x", Type: "number"}}},
		{"unknown type", []Parameter{{Name: "x", Type: "bool"}}},
		{"bad number default", []Parameter{{Name: "x", Type: "number", Default: "nope"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateParameters(tc.params); err == nil {
				t.Fatal("expected a refusal")
			}
		})
	}

	if err := ValidateParameters([]Parameter{
		{Name: "replicas", Type: "number", Default: "3"},
		{Name: "name", Type: "string"},
	}); err != nil {
		t.Fatalf("expected a valid declaration to pass, got %v", err)
	}
}

func TestPlaceholders(t *testing.T) {
	got := Placeholders("a: ${one}\nb: ${two}\nc: ${one}")
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("got %v", got)
	}
}
