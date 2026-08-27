package api

import (
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
)

/*
 * ResourceQuotas and LimitRanges.
 *
 * For a namespace-scoped developer these two objects are the answer to "why
 * will nothing schedule", and they are the *only* objects that answer it: a pod
 * refused by a quota never becomes a pod, so there is nothing in the workload
 * lists to look at and the event that explains it scrolls away. Both are the
 * fixed-inventory pattern — one list handler, one route, one objectKinds entry
 * — with the normalisation doing the work a table would otherwise make an
 * operator do in their head.
 *
 * A quota's `hard` and `used` are two maps over the same keys and are rendered
 * as one row per key, because reading them apart is how somebody concludes a
 * namespace has room when the resource that is full is three lines further
 * down. A LimitRange is flattened the same way: one row per (type, resource),
 * carrying the four bounds it can declare, rather than the nested shape the API
 * stores it in.
 *
 * Quantities are carried as the strings the cluster wrote. They are Kubernetes
 * quantities — `500m`, `2Gi`, `1500` — and normalising them into numbers here
 * would need a unit the wire cannot carry and would lose the form an operator
 * recognises from the manifest they wrote.
 */

// quotaEntry is one resource a quota bounds, with what has been taken of it.
type quotaEntry struct {
	Resource string `json:"resource"`
	Hard     string `json:"hard"`
	// Used is empty until the quota controller has run once. That is a real
	// state on a freshly-created quota and reads as "not counted yet" rather
	// than as zero, which would say the namespace is empty.
	Used string `json:"used,omitempty"`
}

type resourceQuotaView struct {
	listMeta
	// Scopes narrow what a quota counts (`BestEffort`, `NotTerminating`, a
	// PriorityClass). A quota with scopes does not bound everything in the
	// namespace, and a table that did not say so would be read as if it did.
	Scopes  []string     `json:"scopes,omitempty"`
	Entries []quotaEntry `json:"entries"`
}

type quotaObject struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		Scopes        []string `json:"scopes"`
		ScopeSelector *struct {
			MatchExpressions []struct {
				ScopeName string `json:"scopeName"`
			} `json:"matchExpressions"`
		} `json:"scopeSelector"`
	} `json:"spec"`
	Status struct {
		Hard map[string]string `json:"hard"`
		Used map[string]string `json:"used"`
	} `json:"status"`
}

func (q quotaObject) view() resourceQuotaView {
	view := resourceQuotaView{listMeta: q.Metadata.meta(), Scopes: q.Spec.Scopes}
	if q.Spec.ScopeSelector != nil {
		for _, expr := range q.Spec.ScopeSelector.MatchExpressions {
			if expr.ScopeName != "" && !slices.Contains(view.Scopes, expr.ScopeName) {
				view.Scopes = append(view.Scopes, expr.ScopeName)
			}
		}
	}

	// `status.hard` rather than `spec.hard`: what the controller accepted is
	// what is actually enforced, and the two differ while a quota is being
	// changed. The spec is the fallback for a quota the controller has not
	// reached yet, which is the one case status carries nothing.
	entries := make([]quotaEntry, 0, len(q.Status.Hard))
	for _, resource := range sortedMapKeys(q.Status.Hard) {
		entries = append(entries, quotaEntry{
			Resource: resource,
			Hard:     q.Status.Hard[resource],
			Used:     q.Status.Used[resource],
		})
	}
	view.Entries = entries
	return view
}

func (s *server) listResourceQuotas(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	out := []resourceQuotaView{}
	for _, path := range scope.paths(resourceListPath{"/api/v1", "resourcequotas"}) {
		var items []quotaObject
		if !fetchList(s, c, user, cluster, grant, path, &items) {
			return
		}
		for _, item := range items {
			out = append(out, item.view())
		}
	}

	sortResources(out)
	listResponse(c, gin.H{
		"resourcequotas": out,
		"namespace":      scope.Namespace,
		"all_namespaces": scope.All,
	})
}

/* ------------------------------------------------------------ LimitRange --- */

// limitRangeEntry is one (type, resource) pair and the four bounds a LimitRange
// can state about it. Every bound is optional and an absent one is absent
// rather than zero: `min: 0` and "no minimum" are different statements, and a
// table that rendered both as 0 would say every namespace forbids nothing.
type limitRangeEntry struct {
	Type           string `json:"type"`
	Resource       string `json:"resource"`
	Min            string `json:"min,omitempty"`
	Max            string `json:"max,omitempty"`
	Default        string `json:"default,omitempty"`
	DefaultRequest string `json:"default_request,omitempty"`
	// MaxLimitRequestRatio bounds how far a limit may exceed its request, which
	// is the constraint that refuses a burstable pod nothing else objects to.
	MaxLimitRequestRatio string `json:"max_limit_request_ratio,omitempty"`
}

type limitRangeView struct {
	listMeta
	Entries []limitRangeEntry `json:"entries"`
}

type limitRangeObject struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		Limits []struct {
			Type                 string            `json:"type"`
			Min                  map[string]string `json:"min"`
			Max                  map[string]string `json:"max"`
			Default              map[string]string `json:"default"`
			DefaultRequest       map[string]string `json:"defaultRequest"`
			MaxLimitRequestRatio map[string]string `json:"maxLimitRequestRatio"`
		} `json:"limits"`
	} `json:"spec"`
}

func (l limitRangeObject) view() limitRangeView {
	view := limitRangeView{listMeta: l.Metadata.meta()}
	entries := []limitRangeEntry{}

	for _, limit := range l.Spec.Limits {
		// One row per resource this limit mentions anywhere, so a resource with
		// only a default and no bounds still appears — that default is what
		// gets written onto a pod that declared nothing, which is exactly the
		// case somebody is trying to explain.
		resources := []string{}
		for _, set := range []map[string]string{
			limit.Min, limit.Max, limit.Default, limit.DefaultRequest, limit.MaxLimitRequestRatio,
		} {
			for _, resource := range sortedMapKeys(set) {
				if !slices.Contains(resources, resource) {
					resources = append(resources, resource)
				}
			}
		}
		slices.Sort(resources)

		for _, resource := range resources {
			entries = append(entries, limitRangeEntry{
				Type:                 limit.Type,
				Resource:             resource,
				Min:                  limit.Min[resource],
				Max:                  limit.Max[resource],
				Default:              limit.Default[resource],
				DefaultRequest:       limit.DefaultRequest[resource],
				MaxLimitRequestRatio: limit.MaxLimitRequestRatio[resource],
			})
		}
	}

	slices.SortFunc(entries, func(a, b limitRangeEntry) int {
		if order := strings.Compare(a.Type, b.Type); order != 0 {
			return order
		}
		return strings.Compare(a.Resource, b.Resource)
	})
	view.Entries = entries
	return view
}

func (s *server) listLimitRanges(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	out := []limitRangeView{}
	for _, path := range scope.paths(resourceListPath{"/api/v1", "limitranges"}) {
		var items []limitRangeObject
		if !fetchList(s, c, user, cluster, grant, path, &items) {
			return
		}
		for _, item := range items {
			out = append(out, item.view())
		}
	}

	sortResources(out)
	listResponse(c, gin.H{
		"limitranges":    out,
		"namespace":      scope.Namespace,
		"all_namespaces": scope.All,
	})
}
