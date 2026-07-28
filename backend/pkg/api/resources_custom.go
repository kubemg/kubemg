package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
)

/*
 * Custom resources, addressed by the API they are actually served under.
 *
 * The rest of the inventory is a fixed table: one route per kind KubeMG knows
 * how to normalise into columns. That cannot work for CRDs — which CRDs a
 * cluster has is a property of the cluster, discovered at runtime from its own
 * CRD list, so the Explore sidebar builds those entries per cluster and names
 * the API here rather than picking from a list compiled into the binary.
 *
 * Naming an API is not the same as reaching one. Three things keep this from
 * being an unrestricted API client:
 *
 *   - The path is *built* from three validated components, never taken from the
 *     caller. A group must look like a Kubernetes API group, a version like a
 *     Kubernetes version and a plural like a resource name; nothing else gets
 *     through, so no caller can steer the read onto a subresource, a proxy path
 *     or anything outside `/apis/{group}/{version}/{plural}`.
 *   - The group must be a *non-core* group, i.e. contain a dot. The core group
 *     is where Secrets live, and its lists are served by handlers that redact
 *     before answering — this route must never become the way around them.
 *   - The read still goes through bastion.Proxy.Call, so it carries the caller's
 *     impersonated identity, the same namespace enforcement and the same audit
 *     trail. The cluster's own RBAC decides what comes back, exactly as it does
 *     for a kubectl call.
 *
 * What comes back is a generic object: a CRD has no schema KubeMG knows, so the
 * only honest columns are the ones every Kubernetes object has.
 */

// customKindPrefix marks a resource key naming a CRD-served API instead of one
// of the fixed kinds. The key is `crd:{group}/{version}/{plural}` — the same
// string the sidebar uses, so the browser addresses an object by the identifier
// it already holds rather than by a path.
const customKindPrefix = "crd:"

// A Kubernetes API group is a DNS subdomain, a version is `v1`/`v2beta1`, and a
// resource plural is lowercase alphanumeric. Anchored, so a component carrying a
// slash, a dot-dot or a query cannot survive.
var (
	apiGroupPattern    = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)+$`)
	apiVersionPattern  = regexp.MustCompile(`^v[0-9]+((alpha|beta)[0-9]+)?$`)
	apiResourcePattern = regexp.MustCompile(`^[a-z0-9]+$`)
)

// customResourceView is one custom object reduced to what every Kubernetes
// object carries. A CRD's spec is whatever its author decided, so there is
// nothing else that can be shown for all of them without guessing.
type customResourceView struct {
	listMeta
	Kind       string `json:"kind,omitempty"`
	APIVersion string `json:"api_version,omitempty"`
}

// customResourcePath validates the three components of a custom API and renders
// the list path for them. The error it returns is the one shown to the caller.
func customResourcePath(group, version, plural string) (resourceListPath, error) {
	group = strings.TrimSpace(group)
	version = strings.TrimSpace(version)
	plural = strings.TrimSpace(plural)

	switch {
	case !apiGroupPattern.MatchString(group):
		// A group without a dot is the core group, which this route does not
		// serve: its lists have handlers of their own that redact first.
		return resourceListPath{}, fmt.Errorf("%q is not a custom API group", group)
	case !apiVersionPattern.MatchString(version):
		return resourceListPath{}, fmt.Errorf("%q is not a Kubernetes API version", version)
	case !apiResourcePattern.MatchString(plural):
		return resourceListPath{}, fmt.Errorf("%q is not a resource name", plural)
	}
	return resourceListPath{group: "/apis/" + group + "/" + version, resource: plural}, nil
}

// parseCustomKind resolves a `crd:{group}/{version}/{plural}` resource key.
func parseCustomKind(key string) (resourceListPath, bool) {
	rest, found := strings.CutPrefix(key, customKindPrefix)
	if !found {
		return resourceListPath{}, false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		return resourceListPath{}, false
	}
	path, err := customResourcePath(parts[0], parts[1], parts[2])
	if err != nil {
		return resourceListPath{}, false
	}
	return path, true
}

// customObjectKind resolves a `crd:` resource key into an addressable kind for
// the manifest editor. Whether the object is namespaced is decided by whether a
// namespace was named: a namespaced custom resource is always addressed with
// one, and a cluster-scoped one has none to give — which is also what puts a
// namespace-scoped grant in front of requireClusterScope for it.
//
// It is writable: a custom resource round-trips exactly like a built-in one, and
// whether this caller may actually write it is settled by the cluster's RBAC on
// the impersonated PUT rather than by a judgement here.
func customObjectKind(key, namespace string) (objectKind, bool) {
	path, ok := parseCustomKind(key)
	if !ok {
		return objectKind{}, false
	}
	return objectKind{
		versions:   []resourceListPath{path},
		namespaced: strings.TrimSpace(namespace) != "",
		writable:   true,
	}, true
}

// listCustomResources reads one CRD-served list. `scope=cluster` reads it
// cluster-wide and is refused to a namespace-scoped grant like every other
// cluster-wide list; anything else reads it per namespace through the usual
// scope resolution.
//
// A cluster that does not serve the API answers 404, which is reported as an
// empty list marked unavailable — the same contract the Gateway API and Istio
// lists already follow. A CRD can be uninstalled while a sidebar built from an
// earlier read is still on screen, so this is a normal outcome, not a failure.
func (s *server) listCustomResources(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}

	path, err := customResourcePath(c.Query("group"), c.Query("version"), c.Query("plural"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var (
		scope     readScope
		clustered = c.Query("scope") == "cluster"
	)
	if clustered {
		if !s.requireClusterScope(c, grant, path.resource) {
			return
		}
	} else {
		if scope, ok = s.resourceScope(c, grant); !ok {
			return
		}
	}

	out := []customResourceView{}
	for _, candidate := range scope.paths(path) {
		var list struct {
			Items []struct {
				Kind       string     `json:"kind"`
				APIVersion string     `json:"apiVersion"`
				Metadata   objectMeta `json:"metadata"`
			} `json:"items"`
		}

		found, callOK := s.fetchOptional(c, user, cluster, grant, []string{candidate}, &list)
		if !callOK {
			return
		}
		// The API is either served by the cluster or it is not; one namespace
		// answering 404 settles it for all of them.
		if !found {
			c.JSON(http.StatusOK, gin.H{
				"items":          []customResourceView{},
				"namespace":      scope.Namespace,
				"all_namespaces": scope.All,
				"available":      false,
				"reason":         "this cluster does not serve " + path.resource,
			})
			return
		}

		for _, item := range list.Items {
			out = append(out, customResourceView{
				listMeta:   item.Metadata.meta(),
				Kind:       item.Kind,
				APIVersion: item.APIVersion,
			})
		}
	}

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{
		"items":          out,
		"namespace":      scope.Namespace,
		"all_namespaces": scope.All,
		"available":      true,
	})
}
