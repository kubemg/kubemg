package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

/*
 * What a ConfigMap or a Secret holds, for its own detail.
 *
 * A ConfigMap is its data. Its drawer used to open on metadata — kind, API
 * version, age — and the only way to read the one thing anybody opens a
 * ConfigMap for was its YAML tab. So this answers with each key and its value,
 * one entry each, which is the same exposure the YAML tab has always had: a
 * ConfigMap is not redacted (`objectKinds`), because nothing in it is meant to
 * be secret.
 *
 * A Secret answers with the same shape and **no value**: its keys and how big
 * each one is, which is what `kubectl describe secret` prints. A value still
 * leaves the cluster only through the reveal route — one key, the capability,
 * an audit record written before the bytes go out — and nothing here changes
 * that. The value is decoded server-side only to count its bytes, and dropped.
 */

// configValueLimit bounds one ConfigMap value in the response. A ConfigMap can
// carry a megabyte; a drawer cannot usefully show one, and the YAML tab still
// has the whole object.
const configValueLimit = 64 << 10

type dataEntryView struct {
	Key string `json:"key"`
	// Value is a ConfigMap's text value; never set for a Secret, nor for a
	// ConfigMap's binaryData.
	Value     *string `json:"value,omitempty"`
	Bytes     int     `json:"bytes"`
	Binary    bool    `json:"binary"`
	Truncated bool    `json:"truncated,omitempty"`
}

type dataEntriesView struct {
	Kind      string            `json:"kind"`
	Type      string            `json:"type,omitempty"`
	Immutable bool              `json:"immutable"`
	Entries   []dataEntryView `json:"entries"`
	// ValuesShown says whether entries carry values — true for a ConfigMap,
	// false for a Secret, whose values come only from the reveal route.
	ValuesShown bool `json:"values_shown"`
}

// configMapEntries renders a ConfigMap's keys and values, sorted by key.
func configMapEntries(body []byte) (dataEntriesView, error) {
	var object struct {
		Immutable  *bool             `json:"immutable"`
		Data       map[string]string `json:"data"`
		BinaryData map[string]string `json:"binaryData"`
	}
	if err := json.Unmarshal(body, &object); err != nil {
		return dataEntriesView{}, err
	}
	out := dataEntriesView{Kind: "ConfigMap", Immutable: deref(object.Immutable, false),
		Entries: []dataEntryView{}, ValuesShown: true}
	for key, value := range object.Data {
		entry := dataEntryView{Key: key, Bytes: len(value)}
		if len(value) > configValueLimit {
			// Cut on a rune boundary, so a truncated value is still text.
			cut := configValueLimit
			for cut > 0 && !utf8.RuneStart(value[cut]) {
				cut--
			}
			value, entry.Truncated = value[:cut], true
		}
		entry.Value = &value
		out.Entries = append(out.Entries, entry)
	}
	for key, encoded := range object.BinaryData {
		out.Entries = append(out.Entries, dataEntryView{Key: key, Bytes: decodedLength(encoded), Binary: true})
	}
	sortEntries(out.Entries)
	return out, nil
}

// secretEntries renders a Secret's keys and their sizes — never a value.
func secretEntries(body []byte) (dataEntriesView, error) {
	var object struct {
		Type      string            `json:"type"`
		Immutable *bool             `json:"immutable"`
		Data      map[string]string `json:"data"`
	}
	if err := json.Unmarshal(body, &object); err != nil {
		return dataEntriesView{}, err
	}
	out := dataEntriesView{Kind: "Secret", Type: cmpOr(object.Type, "Opaque"),
		Immutable: deref(object.Immutable, false), Entries: []dataEntryView{}}
	for key, encoded := range object.Data {
		out.Entries = append(out.Entries, dataEntryView{Key: key, Bytes: decodedLength(encoded)})
	}
	sortEntries(out.Entries)
	return out, nil
}

func decodedLength(encoded string) int {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return base64.StdEncoding.DecodedLen(len(encoded))
	}
	return len(decoded)
}

func sortEntries(entries []dataEntryView) {
	slices.SortFunc(entries, func(a, b dataEntryView) int { return strings.Compare(a.Key, b.Key) })
}

// showConfigEntries answers GET .../resources/config/entries?kind=&namespace=&name=.
func (s *server) showConfigEntries(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	kind := strings.TrimSpace(c.Query("kind"))
	if kind != "configmaps" && kind != "secrets" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "entries are read for ConfigMaps and Secrets only"})
		return
	}
	name := strings.TrimSpace(c.Query("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a resource name is required"})
		return
	}
	namespace, ok := s.resourceNamespace(c, grant)
	if !ok {
		return
	}

	body, ok := s.readObject(c, user, cluster, grant, objectKinds[kind], namespace, name)
	if !ok {
		return
	}
	render := configMapEntries
	if kind == "secrets" {
		render = secretEntries
	}
	view, err := render(body)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "the cluster returned an unreadable response"})
		return
	}
	c.JSON(http.StatusOK, view)
}
