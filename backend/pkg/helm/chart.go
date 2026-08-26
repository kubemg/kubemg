package helm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
)

// maxChartBytes bounds a chart archive. A chart is a handful of text templates
// and a values file; the largest in common use are a few hundred kilobytes, and
// the ones that are larger are so because somebody vendored a binary into
// `files/`. Twenty megabytes is generous for the first and refuses the second
// before it is decompressed rather than after.
const maxChartBytes = 20 << 20

// FetchChart downloads and loads one published version of a chart.
//
// Two things are checked before the archive is a chart. The **digest**, when the
// index published one: an index entry is a claim about a file, and a repository
// that serves a different file than it indexed is either broken or hostile, and
// either way the rendered objects are not the ones the operator chose. And the
// **size**, on the way in rather than after — `loader.LoadArchive` decompresses,
// and an archive nobody bounded is the same gzip bomb the release decode is
// bounded against, only reached from the network instead of from a cluster.
func FetchChart(ctx context.Context, client *http.Client,
	repo Repository, version ChartVersion,
) (*chart.Chart, error) {
	if len(version.URLs) == 0 {
		return nil, fmt.Errorf("this chart version names no archive")
	}

	// An index may publish several URLs for one version — a repository with a
	// mirror. The first that answers wins, and the last failure is what is
	// reported, since reporting the first would name a host that may be the one
	// deliberately taken out of service.
	var lastErr error
	for _, raw := range version.URLs {
		target, err := repo.resolve(raw)
		if err != nil {
			lastErr = err
			continue
		}

		archive, err := fetch(ctx, client, repo, target, maxChartBytes, "chart archive")
		if err != nil {
			lastErr = err
			continue
		}
		if err := verifyDigest(archive, version.Digest); err != nil {
			lastErr = err
			continue
		}

		loaded, err := loader.LoadArchive(bytes.NewReader(archive))
		if err != nil {
			lastErr = fmt.Errorf("this archive is not a readable Helm chart: %w", err)
			continue
		}
		if loaded.Metadata == nil {
			lastErr = fmt.Errorf("this chart has no Chart.yaml")
			continue
		}
		return loaded, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("this chart version could not be fetched")
	}
	return nil, lastErr
}

// verifyDigest holds a downloaded archive to what the index said it would be.
// An index that published no digest is not an error — plenty of internal
// repositories are generated without one — but a digest that disagrees is,
// and it is the one network failure here that is worth naming as such.
func verifyDigest(archive []byte, digest string) error {
	if digest == "" {
		return nil
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != digest {
		return fmt.Errorf("this chart archive does not match the digest the repository published " +
			"for it — the repository is serving something other than what it indexed")
	}
	return nil
}

// Installable refuses the charts that cannot produce a release, before an
// operator has filled in a values editor for one.
//
// A **library** chart has no templates of its own and exists to be imported by
// another chart; `helm install` refuses it too. A chart declaring a `kubeVersion`
// the target cluster does not satisfy is refused by the render itself, which is
// where that check belongs, since it is a fact about the pair rather than about
// the chart.
func Installable(loaded *chart.Chart) error {
	if loaded == nil || loaded.Metadata == nil {
		return fmt.Errorf("this is not a readable chart")
	}
	if loaded.Metadata.Type == "library" {
		return fmt.Errorf("%s is a library chart — it has no templates of its own and is imported "+
			"by other charts rather than installed", loaded.Metadata.Name)
	}
	if len(loaded.Templates) == 0 && len(loaded.CRDObjects()) == 0 {
		return fmt.Errorf("%s renders no objects", loaded.Metadata.Name)
	}
	return nil
}
