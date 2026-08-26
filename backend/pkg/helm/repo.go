// Package helm carries Helm's own machinery into the bastion.
//
// Everything KubeMG could say about a release until now was read out of what
// Helm left behind: a Secret holding a release object, decoded and reported.
// That is enough to list a release, show the values it was installed with and
// record new ones — and it is not enough to change anything, because changing
// what a chart produced means rendering the chart, and a release Secret does not
// contain one. So every write in `resources_helm.go` had to travel with the same
// caveat: it records what Helm will start from and re-applies nothing.
//
// This package is what removes that caveat, and the shape of it is one decision
// worth stating plainly: **Helm's template engine is used, never reimplemented,
// and Helm's cluster client is not used at all.**
//
//   - Rendering is `helm.sh/helm/v3/pkg/engine` over `helm.sh/helm/v3/pkg/chart`,
//     with `chartutil` building `.Values`/`.Release`/`.Capabilities` and
//     `releaseutil` splitting hooks out and putting the rest in Helm's own
//     install order. A chart is a Go program in all but name — `tpl`, `lookup`,
//     sprig, subchart value merging, `Capabilities.APIVersions.Has` — and a
//     second implementation of that would disagree with the `helm` CLI on the
//     first non-trivial chart.
//
//   - `helm.sh/helm/v3/pkg/kube` is deliberately absent. Helm's client dials a
//     cluster with a kubeconfig, which is precisely the access this product
//     exists to remove: the rendered objects go down the same impersonated,
//     audited tunnel every other write in the console goes down, one at a time,
//     so the target cluster's own RBAC answers for each of them. Keeping that
//     package out also keeps `k8s.io/kubectl` and the OCI stack out of the
//     server binary.
//
// The repository half is the same discipline applied to the network: an
// `index.yaml` and a chart archive are fetched here, by this package, bounded on
// the way in, so nothing a repository serves can become a bastion out of memory.
package helm

import (
	"fmt"
	"net/url"
	"strings"
)

// Repository is where charts come from: a name an operator chose, a URL, and
// optionally the credential to read it with. It is the argument to every fetch
// in this package rather than a stored row, so the package has no opinion about
// where repositories live.
type Repository struct {
	Name string
	URL  string
	// Username and Credential are HTTP basic auth. A repository with neither is
	// read anonymously, which is what every public repository wants.
	Username   string
	Credential string
}

// Validate checks a repository is one this package can read from before it is
// stored, so a broken row is refused at the form rather than discovered by a
// background sync three minutes later.
//
// The scheme list is a deny of everything else rather than an allow of two
// things: `file://` would make an operator's form a reader of the bastion's own
// filesystem, and `oci://` is not a mistake but a repository kind this package
// does not implement, so it earns its own sentence rather than "unsupported
// scheme".
func (r Repository) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("a repository needs a name")
	}
	raw := strings.TrimSpace(r.URL)
	if raw == "" {
		return fmt.Errorf("a repository needs a URL")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("that is not a URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	case "oci":
		return fmt.Errorf("OCI registries are not a chart repository KubeMG can read yet — " +
			"it reads the index.yaml of an HTTP chart repository")
	default:
		return fmt.Errorf("a chart repository URL has to be http or https")
	}
	if parsed.Host == "" {
		return fmt.Errorf("that URL names no host")
	}
	return nil
}

// indexURL is where a repository publishes what it holds. Helm's own rule, and
// the reason the trim is here rather than at the call site: an operator pastes
// a URL with a trailing slash roughly half the time, and `//index.yaml` is a
// 404 on enough servers to be worth one line.
func (r Repository) indexURL() string {
	return strings.TrimRight(strings.TrimSpace(r.URL), "/") + "/index.yaml"
}

// resolve turns one of a chart version's URLs into something fetchable. An
// index may name a chart by an absolute URL — a repository whose archives live
// on a CDN — or by a path relative to the repository itself, which is what
// `helm package` plus `helm repo index` produces. Both are ordinary, and the
// relative case is the common one.
func (r Repository) resolve(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("this chart version names no archive")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("this chart version's archive URL is not a URL")
	}
	if parsed.IsAbs() {
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https":
			return parsed.String(), nil
		default:
			// An index that points its archives at a scheme this cannot fetch is
			// a repository problem, and saying which scheme is what makes it one
			// somebody can act on.
			return "", fmt.Errorf("this chart's archive is served over %s, which KubeMG does not fetch",
				parsed.Scheme)
		}
	}

	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(r.URL), "/") + "/")
	if err != nil {
		return "", fmt.Errorf("the repository URL is not a URL")
	}
	return base.ResolveReference(parsed).String(), nil
}
