package apptemplate

import (
	"fmt"

	"sigs.k8s.io/yaml"
)

// documentHeader is the handful of fields Objects needs out of a rendered
// document. It is deliberately not the whole object — everything past
// identity is left in the YAML string, unparsed, which is what lets a document
// this package cannot fully understand still be listed.
type documentHeader struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
}

func parseDocument(doc string) error {
	var probe map[string]any
	return yaml.Unmarshal([]byte(doc), &probe)
}

// Objects splits a rendered bundle into the individual objects it describes,
// reading just enough of each — apiVersion, kind, and the name and namespace
// an object's own creation call would use — to list what will be created
// without deciding how to create it. A document with no kind is an error
// rather than a skipped entry: it is not a comment and not blank, which are
// the only two things Render's output is allowed to contain besides objects.
func Objects(rendered string) ([]Object, error) {
	docs := splitDocuments(rendered)
	objects := make([]Object, 0, len(docs))
	for _, doc := range docs {
		var header documentHeader
		if err := yaml.Unmarshal([]byte(doc), &header); err != nil {
			return nil, fmt.Errorf("apptemplate: a document did not parse as YAML: %w", err)
		}
		if header.Kind == "" {
			return nil, fmt.Errorf("apptemplate: a document has no kind")
		}
		objects = append(objects, Object{
			APIVersion: header.APIVersion,
			Kind:       header.Kind,
			Name:       header.Metadata.Name,
			Namespace:  header.Metadata.Namespace,
			YAML:       doc,
		})
	}
	return objects, nil
}
