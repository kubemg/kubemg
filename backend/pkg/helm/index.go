package helm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"sigs.k8s.io/yaml"
)

/*
 * Reading what a repository holds.
 *
 * A public repository's index.yaml is not a small document. Bitnami's runs to
 * tens of megabytes and tens of thousands of chart versions, and it is served
 * gzipped, so the number that matters is the decompressed one — the same reason
 * the release payload decode is bounded rather than trusting a Secret's size.
 * Two bounds, against two different failures:
 *
 *   - maxIndexBytes bounds the document. Past it the fetch fails and says so,
 *     rather than the process being killed with no row to explain why.
 *   - MaxVersionsPerChart bounds what is *kept*. An operator installing
 *     ingress-nginx picks between the newest few versions; storing four hundred
 *     of them would put a repository's whole history in the bastion's database
 *     to render one dropdown.
 *
 * The reduction happens here, before anything is stored, so the bound is on the
 * write path rather than on the read: a caller cannot ask for the four hundred.
 */

const (
	// maxIndexBytes bounds a decompressed index. Bitnami's is the largest in
	// common use at roughly 60 MB; this leaves room for it and refuses a
	// document no legitimate repository publishes.
	maxIndexBytes = 96 << 20

	// MaxVersionsPerChart is how many versions of one chart survive a sync,
	// newest first. Five is what a version dropdown can show without becoming a
	// search problem, and an operator who needs an older one is pinning a
	// version they already know rather than browsing for it.
	MaxVersionsPerChart = 5

	// maxCharts bounds how many distinct charts one repository contributes.
	// This is not a real repository's shape — it is the bound that keeps a
	// hostile or broken index from being an unbounded write.
	maxCharts = 5000

	// fetchTimeout bounds one repository read. The sync runs on a schedule and
	// a repository that has gone away must not hold the pass open.
	fetchTimeout = 90 * time.Second
)

// ChartVersion is one published version of a chart, reduced to what a catalogue
// and an install need. The index carries a great deal more — maintainers,
// sources, keywords, the full dependency list — and none of it is stored,
// because storing a repository's metadata verbatim is how a catalogue table
// becomes a mirror of the repository.
type ChartVersion struct {
	Version    string    `json:"version"`
	AppVersion string    `json:"app_version,omitempty"`
	Created    time.Time `json:"created,omitempty"`
	Digest     string    `json:"digest,omitempty"`
	Deprecated bool      `json:"deprecated,omitempty"`
	// URLs is where the archive lives, as the index named it. It is kept rather
	// than resolved at sync time because a repository may be re-pointed at a
	// mirror, and a stored absolute URL would outlive the change.
	URLs []string `json:"urls,omitempty"`
}

// Chart is one chart as the catalogue lists it: its identity, and the newest few
// versions of it.
type Chart struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Icon        string         `json:"icon,omitempty"`
	Home        string         `json:"home,omitempty"`
	Deprecated  bool           `json:"deprecated,omitempty"`
	Versions    []ChartVersion `json:"versions"`
}

// LatestVersion is the version an install form offers first: the newest one that
// is not a prerelease, falling back to the newest of anything if a chart only
// ever published prereleases.
func (c Chart) LatestVersion() string {
	for _, version := range c.Versions {
		if parsed, err := semver.NewVersion(version.Version); err == nil && parsed.Prerelease() == "" {
			return version.Version
		}
	}
	if len(c.Versions) > 0 {
		return c.Versions[0].Version
	}
	return ""
}

// Version finds one published version by its exact string. An install names a
// version rather than picking one, so this is a lookup and not a constraint
// solve: "the newest matching ^1.2" is a question the CLI answers for a user
// typing it, and a form that already listed the versions has no reason to ask it.
func (c Chart) Version(wanted string) (ChartVersion, bool) {
	wanted = strings.TrimSpace(wanted)
	for _, version := range c.Versions {
		if version.Version == wanted {
			return version, true
		}
	}
	return ChartVersion{}, false
}

// rawIndex is index.yaml, narrowed to the fields that are read. Unmarshalling
// into a narrow struct rather than into a map is the bound that matters most
// here: the parser allocates what this names and discards the rest, so a chart
// carrying a megabyte of keywords costs nothing.
type rawIndex struct {
	APIVersion string                `json:"apiVersion"`
	Entries    map[string][]rawEntry `json:"entries"`
}

type rawEntry struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	AppVersion  string   `json:"appVersion"`
	Description string   `json:"description"`
	Icon        string   `json:"icon"`
	Home        string   `json:"home"`
	Deprecated  bool     `json:"deprecated"`
	Digest      string   `json:"digest"`
	Created     string   `json:"created"`
	URLs        []string `json:"urls"`
	// Type distinguishes a library chart from an application one. A library
	// chart renders nothing and cannot be installed, so it is dropped rather
	// than offered and then refused.
	Type string `json:"type"`
}

// FetchIndex reads a repository's index and reduces it to a catalogue.
//
// A failure here is reported with its own reason and is deliberately *not*
// destructive: the caller keeps serving what it last held. A chart list that
// empties on a DNS blip is worse than a stale one — an operator looking at an
// empty catalogue concludes the feature is broken, where one looking at a stale
// catalogue with a reported sync error knows exactly what to fix.
func FetchIndex(ctx context.Context, client *http.Client, repo Repository) ([]Chart, error) {
	if err := repo.Validate(); err != nil {
		return nil, err
	}

	document, err := fetch(ctx, client, repo, repo.indexURL(), maxIndexBytes, "index")
	if err != nil {
		return nil, err
	}

	var index rawIndex
	if err := yaml.Unmarshal(document, &index); err != nil {
		return nil, fmt.Errorf("this URL does not serve a Helm index.yaml")
	}
	if len(index.Entries) == 0 {
		// A valid but empty index is a real state — a private repository nobody
		// has pushed to yet — and is not an error. An unparseable document that
		// happened to yaml-decode into nothing lands here too, which is why the
		// caller reports the chart count rather than only a status.
		return nil, nil
	}

	return catalogueOf(index), nil
}

// catalogueOf reduces a parsed index to what is stored. It is separate from the
// fetch so the reduction — which is where every bound and every ordering rule
// lives — is testable against a document rather than against a server.
func catalogueOf(index rawIndex) []Chart {
	names := make([]string, 0, len(index.Entries))
	for name := range index.Entries {
		names = append(names, name)
	}
	// Sorted so a sync of an unchanged index writes the same rows in the same
	// order, and a diff of two catalogues is a diff rather than a reshuffle.
	slices.Sort(names)

	charts := make([]Chart, 0, min(len(names), maxCharts))
	for _, name := range names {
		if len(charts) >= maxCharts {
			break
		}
		chart, ok := chartOf(name, index.Entries[name])
		if !ok {
			continue
		}
		charts = append(charts, chart)
	}
	return charts
}

// chartOf collapses every published version of one chart into a catalogue row.
//
// The identity fields — description, icon, whether the chart is deprecated —
// are taken from the **newest** version rather than merged across versions,
// because they are facts about the chart as it is published now: a chart
// deprecated last month is deprecated, whatever its 2019 release said.
func chartOf(name string, entries []rawEntry) (Chart, bool) {
	versions := make([]ChartVersion, 0, len(entries))
	for _, entry := range entries {
		if strings.EqualFold(strings.TrimSpace(entry.Type), "library") {
			continue
		}
		if strings.TrimSpace(entry.Version) == "" || len(entry.URLs) == 0 {
			// A version with no version or nowhere to fetch it from is a row
			// that could only ever fail at install time.
			continue
		}
		versions = append(versions, ChartVersion{
			Version:    strings.TrimSpace(entry.Version),
			AppVersion: strings.TrimSpace(entry.AppVersion),
			Created:    parseCreated(entry.Created),
			Digest:     strings.TrimSpace(entry.Digest),
			Deprecated: entry.Deprecated,
			URLs:       slices.Clone(entry.URLs),
		})
	}
	if len(versions) == 0 {
		return Chart{}, false
	}

	sortVersions(versions)
	newest := indexEntryFor(entries, versions[0].Version)

	if len(versions) > MaxVersionsPerChart {
		versions = versions[:MaxVersionsPerChart]
	}
	return Chart{
		Name:        name,
		Description: strings.TrimSpace(newest.Description),
		Icon:        strings.TrimSpace(newest.Icon),
		Home:        strings.TrimSpace(newest.Home),
		Deprecated:  newest.Deprecated,
		Versions:    versions,
	}, true
}

// indexEntryFor finds the raw entry a reduced version came from, so the chart's
// identity is read off one published version rather than assembled from several.
func indexEntryFor(entries []rawEntry, version string) rawEntry {
	for _, entry := range entries {
		if strings.TrimSpace(entry.Version) == version {
			return entry
		}
	}
	return rawEntry{}
}

// sortVersions puts the newest version first.
//
// Semver is the ordering, not the publication date: a repository that
// backported a fix publishes 1.2.4 after 2.0.0, and a date sort would offer the
// backport as the newest chart. A version string that is not semver at all
// sorts below every version that is — it cannot be compared, and guessing at an
// order for it would be worse than putting it at the bottom — and two of those
// fall back to the date, which is the only thing left that distinguishes them.
func sortVersions(versions []ChartVersion) {
	parsed := make(map[string]*semver.Version, len(versions))
	for _, version := range versions {
		if value, err := semver.NewVersion(version.Version); err == nil {
			parsed[version.Version] = value
		}
	}

	slices.SortStableFunc(versions, func(a, b ChartVersion) int {
		left, leftOK := parsed[a.Version]
		right, rightOK := parsed[b.Version]
		switch {
		case leftOK && rightOK:
			return right.Compare(left)
		case leftOK:
			return -1
		case rightOK:
			return 1
		default:
			return b.Created.Compare(a.Created)
		}
	})
}

// parseCreated reads an index timestamp. An unparseable or absent one is a zero
// time rather than an error: it is used for ordering non-semver versions and for
// display, and neither is worth failing a whole repository over.
func parseCreated(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if at, err := time.Parse(layout, raw); err == nil {
			return at.UTC()
		}
	}
	return time.Time{}
}

// fetch reads one bounded document from a repository.
//
// The bound is applied to what comes *out* of the reader rather than to
// Content-Length, because a gzipped index declares its compressed size and the
// number that can exhaust this process is the other one. Reading one byte past
// the bound is what distinguishes "exactly at the limit" from "truncated at the
// limit", which is the difference between a valid document and a corrupt one.
func fetch(ctx context.Context, client *http.Client, repo Repository,
	target string, limit int64, what string,
) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("the %s URL could not be requested", what)
	}
	if repo.Username != "" || repo.Credential != "" {
		request.SetBasicAuth(repo.Username, repo.Credential)
	}
	request.Header.Set("User-Agent", "kubemg")

	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%s could not be reached: %w", repo.Name, err)
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		// Worth its own sentence: this is the one failure an operator fixes by
		// editing the repository rather than by looking at the network.
		return nil, fmt.Errorf("%s refused the credential (%s)", repo.Name, response.Status)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%s answered %s for the %s", repo.Name, response.Status, what)
	}

	document, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("the %s could not be read: %w", what, err)
	}
	if int64(len(document)) > limit {
		return nil, fmt.Errorf("the %s is larger than KubeMG will read (%d MB)", what, limit>>20)
	}
	return document, nil
}
