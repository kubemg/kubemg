package api

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * HorizontalPodAutoscalers, and the workload that one of them owns.
 *
 * This is the inventory kind whose *absence* made an existing surface lie. The
 * scale control writes a replica count through the `scale` subresource and says
 * it did; where an HPA owns that workload the autoscaler puts the count back
 * within a minute and nothing on the page ever said an autoscaler was involved.
 * So the list is only half of the item: the other half is `workloadAutoscaler`,
 * which is what lets a workload's Overview and its scale control name the HPA
 * that is going to overrule them.
 *
 * The list itself is the fixed-inventory pattern with nothing new in it — same
 * normalised shape, same tunnel, same scope, same cache.
 *
 * One version, deliberately. `autoscaling/v2` is the only API that carries the
 * metric shapes this view reads (`spec.metrics` and `status.currentMetrics`);
 * `autoscaling/v1` carries a single CPU percentage in a different field
 * entirely, and parsing both would be two readers for one list where the second
 * one answers for no cluster this product supports. A cluster that does not
 * serve v2 is told so — `available: false`, the optional-resource answer — which
 * is the same thing the Gateway API and Istio lists do and is a fact rather than
 * a failure.
 */

// autoscalingGroup is the API this build reads HorizontalPodAutoscalers from.
const autoscalingGroup = "/apis/autoscaling/v2"

// unknownMetric is what a target with no reading yet shows. An HPA that has not
// scraped its metric once reports nothing for it, and a zero would read as "the
// workload is idle" — the opposite of "nobody knows".
const unknownMetric = ""

/* ------------------------------------------------------------------ wire --- */

// hpaMetricTarget is `spec.metrics[].{resource,pods,object,external}.target`:
// exactly one of the three ways of stating a target is set, and which one it is
// decides how it renders.
type hpaMetricTarget struct {
	Type               string `json:"type"`
	Value              string `json:"value"`
	AverageValue       string `json:"averageValue"`
	AverageUtilization *int32 `json:"averageUtilization"`
}

// hpaMetricCurrent is the status side of the same three shapes.
type hpaMetricCurrent struct {
	Value              string `json:"value"`
	AverageValue       string `json:"averageValue"`
	AverageUtilization *int32 `json:"averageUtilization"`
}

type hpaMetricIdentifier struct {
	Name string `json:"name"`
}

type hpaResourceMetric struct {
	Name   string          `json:"name"`
	Target hpaMetricTarget `json:"target"`
}

type hpaNamedMetric struct {
	Metric hpaMetricIdentifier `json:"metric"`
	Target hpaMetricTarget     `json:"target"`
}

type hpaMetricSpec struct {
	Type              string             `json:"type"`
	Resource          *hpaResourceMetric `json:"resource"`
	ContainerResource *hpaResourceMetric `json:"containerResource"`
	Pods              *hpaNamedMetric    `json:"pods"`
	Object            *hpaNamedMetric    `json:"object"`
	External          *hpaNamedMetric    `json:"external"`
}

type hpaResourceStatus struct {
	Name    string           `json:"name"`
	Current hpaMetricCurrent `json:"current"`
}

type hpaNamedStatus struct {
	Metric  hpaMetricIdentifier `json:"metric"`
	Current hpaMetricCurrent    `json:"current"`
}

type hpaMetricStatus struct {
	Type              string             `json:"type"`
	Resource          *hpaResourceStatus `json:"resource"`
	ContainerResource *hpaResourceStatus `json:"containerResource"`
	Pods              *hpaNamedStatus    `json:"pods"`
	Object            *hpaNamedStatus    `json:"object"`
	External          *hpaNamedStatus    `json:"external"`
}

type hpaObject struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		ScaleTargetRef struct {
			APIVersion string `json:"apiVersion"`
			Kind       string `json:"kind"`
			Name       string `json:"name"`
		} `json:"scaleTargetRef"`
		MinReplicas *int32          `json:"minReplicas"`
		MaxReplicas int32           `json:"maxReplicas"`
		Metrics     []hpaMetricSpec `json:"metrics"`
	} `json:"spec"`
	Status struct {
		CurrentReplicas int32             `json:"currentReplicas"`
		DesiredReplicas int32             `json:"desiredReplicas"`
		CurrentMetrics  []hpaMetricStatus `json:"currentMetrics"`
		Conditions      []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"conditions"`
	} `json:"status"`
}

// autoscalerMetricView is one metric an HPA scales on: what it is aiming for
// and what it is reading. The two are strings rather than numbers because a
// target is a percentage, a quantity or a raw value depending on its type, and
// rendering all three as one number would need a unit the wire does not carry.
type autoscalerMetricView struct {
	Name    string `json:"name"`
	Target  string `json:"target"`
	Current string `json:"current,omitempty"`
}

// autoscalerView is one HorizontalPodAutoscaler as the list shows it.
type autoscalerView struct {
	listMeta
	TargetKind      string                 `json:"target_kind"`
	TargetName      string                 `json:"target_name"`
	MinReplicas     int32                  `json:"min_replicas"`
	MaxReplicas     int32                  `json:"max_replicas"`
	CurrentReplicas int32                  `json:"current_replicas"`
	DesiredReplicas int32                  `json:"desired_replicas"`
	Metrics         []autoscalerMetricView `json:"metrics"`
	// Reason is the autoscaler's own account of itself when it is not working:
	// `ScalingActive=False` is how an HPA says it cannot read its metric, which
	// is the state that looks identical to "nothing to do" on a list that only
	// showed replica counts.
	Reason string `json:"reason,omitempty"`
}

func (h hpaObject) view() autoscalerView {
	view := autoscalerView{
		listMeta:        h.Metadata.meta(),
		TargetKind:      h.Spec.ScaleTargetRef.Kind,
		TargetName:      h.Spec.ScaleTargetRef.Name,
		MaxReplicas:     h.Spec.MaxReplicas,
		CurrentReplicas: h.Status.CurrentReplicas,
		DesiredReplicas: h.Status.DesiredReplicas,
		Metrics:         autoscalerMetrics(h),
	}
	// An absent minReplicas is 1, which is the API's documented default and not
	// "no minimum" — reporting 0 would say a workload may be scaled away.
	view.MinReplicas = 1
	if h.Spec.MinReplicas != nil {
		view.MinReplicas = *h.Spec.MinReplicas
	}
	for _, condition := range h.Status.Conditions {
		if condition.Type == "ScalingActive" && condition.Status == "False" {
			view.Reason = condition.Message
			if view.Reason == "" {
				view.Reason = condition.Reason
			}
		}
	}
	return view
}

/* ---------------------------------------------------------------- metrics --- */

// autoscalerMetrics pairs each declared metric with the reading the status
// carries for it, matched by type and name rather than by position: the two
// lists are usually parallel and are not promised to be, and a reading shown
// against the wrong target is worse than no reading.
func autoscalerMetrics(h hpaObject) []autoscalerMetricView {
	out := make([]autoscalerMetricView, 0, len(h.Spec.Metrics))
	for _, metric := range h.Spec.Metrics {
		name, target, ok := metricSpecView(metric)
		if !ok {
			continue
		}
		out = append(out, autoscalerMetricView{
			Name:    name,
			Target:  target,
			Current: currentMetric(h.Status.CurrentMetrics, metric.Type, name),
		})
	}
	return out
}

// metricSpecView renders one declared metric: what it is called, and what it is
// aiming for. A metric type this build does not know is skipped rather than
// rendered as a blank row — the same rule the rest of this package follows for
// a shape it cannot read.
func metricSpecView(metric hpaMetricSpec) (string, string, bool) {
	switch metric.Type {
	case "Resource":
		if metric.Resource == nil {
			return "", "", false
		}
		return metric.Resource.Name, targetText(metric.Resource.Target), true
	case "ContainerResource":
		if metric.ContainerResource == nil {
			return "", "", false
		}
		return metric.ContainerResource.Name, targetText(metric.ContainerResource.Target), true
	case "Pods":
		if metric.Pods == nil {
			return "", "", false
		}
		return metric.Pods.Metric.Name, targetText(metric.Pods.Target), true
	case "Object":
		if metric.Object == nil {
			return "", "", false
		}
		return metric.Object.Metric.Name, targetText(metric.Object.Target), true
	case "External":
		if metric.External == nil {
			return "", "", false
		}
		return metric.External.Metric.Name, targetText(metric.External.Target), true
	}
	return "", "", false
}

// currentMetric finds the reading recorded for one declared metric.
func currentMetric(statuses []hpaMetricStatus, kind, name string) string {
	for _, status := range statuses {
		if status.Type != kind {
			continue
		}
		switch kind {
		case "Resource":
			if status.Resource != nil && status.Resource.Name == name {
				return currentText(status.Resource.Current)
			}
		case "ContainerResource":
			if status.ContainerResource != nil && status.ContainerResource.Name == name {
				return currentText(status.ContainerResource.Current)
			}
		case "Pods":
			if status.Pods != nil && status.Pods.Metric.Name == name {
				return currentText(status.Pods.Current)
			}
		case "Object":
			if status.Object != nil && status.Object.Metric.Name == name {
				return currentText(status.Object.Current)
			}
		case "External":
			if status.External != nil && status.External.Metric.Name == name {
				return currentText(status.External.Current)
			}
		}
	}
	return unknownMetric
}

// targetText renders a target in the units it was declared in.
func targetText(target hpaMetricTarget) string {
	switch {
	case target.AverageUtilization != nil:
		return strconv.FormatInt(int64(*target.AverageUtilization), 10) + "%"
	case target.AverageValue != "":
		return target.AverageValue
	case target.Value != "":
		return target.Value
	}
	return unknownMetric
}

// currentText renders a reading the same way its target was rendered, so the
// two sit either side of a slash and mean the same thing.
func currentText(current hpaMetricCurrent) string {
	switch {
	case current.AverageUtilization != nil:
		return strconv.FormatInt(int64(*current.AverageUtilization), 10) + "%"
	case current.AverageValue != "":
		return current.AverageValue
	case current.Value != "":
		return current.Value
	}
	return unknownMetric
}

/* ------------------------------------------------------------------ list --- */

// listAutoscalers serves the inventory.
func (s *server) listAutoscalers(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	out := []autoscalerView{}
	served := false
	for _, candidates := range scope.candidates(resourceListPath{autoscalingGroup, "horizontalpodautoscalers"}) {
		var items []hpaObject
		found, callOK := fetchOptionalList(s, c, user, cluster, grant, candidates, &items)
		if !callOK {
			return
		}
		served = served || found
		for _, item := range items {
			out = append(out, item.view())
		}
	}

	sortResources(out)
	listResponse(c, gin.H{
		"horizontalpodautoscalers": out,
		"available":                served,
		"reason":                   autoscalingReason(served),
		"namespace":                scope.Namespace,
		"all_namespaces":           scope.All,
	})
}

func autoscalingReason(served bool) string {
	if served {
		return ""
	}
	return "This cluster does not serve autoscaling/v2, which is the API this view reads."
}

/* ------------------------------------------------- the workload's own HPA --- */

// autoscaledKinds maps a sidebar kind key onto the Kind an HPA names in its
// `scaleTargetRef`. It is its own table rather than a reuse of workloadActions
// because the question is different: a DaemonSet is restartable and can never
// be autoscaled, having no `scale` subresource for an HPA to write.
var autoscaledKinds = map[string]string{
	"deployments":  "Deployment",
	"statefulsets": "StatefulSet",
	"replicasets":  "ReplicaSet",
}

// workloadAutoscaler answers "is something else deciding this replica count".
//
// It exists because the scale control is otherwise a lie by omission: a write
// through the `scale` subresource succeeds, reports the number it set, and is
// reverted by the autoscaler on its next pass with nothing anywhere saying why.
// Answering `null` is a real answer and the common one — most workloads have no
// HPA — so it is a 200 with an empty field rather than a 404.
func (s *server) workloadAutoscaler(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}

	key := strings.TrimSpace(c.Query("kind"))
	kind, known := autoscaledKinds[key]
	if !known {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "kubemg does not look for an autoscaler on " + key,
		})
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

	found, view, ok := s.autoscalerFor(c, user, cluster, grant, namespace, kind, name)
	if !ok {
		return
	}
	body := gin.H{"namespace": namespace, "kind": kind, "name": name, "autoscaler": nil}
	if found {
		body["autoscaler"] = view
		body["notice"] = autoscalerNotice(view)
	}
	c.JSON(http.StatusOK, body)
}

// autoscalerFor reads the namespace's HPAs and returns the one that scales this
// workload. A namespace with several is normal; the first by name wins, which
// is stable rather than arbitrary — two HPAs on one workload is a
// misconfiguration the cluster itself resolves unpredictably, and this view
// says which one it is naming rather than pretending there is only ever one.
func (s *server) autoscalerFor(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, namespace, kind, name string,
) (bool, autoscalerView, bool) {
	path := resourceListPath{autoscalingGroup, "horizontalpodautoscalers"}.namespaced(namespace)

	var items []hpaObject
	found, callOK := fetchOptionalList(s, c, user, cluster, grant, []string{path}, &items)
	if !callOK {
		return false, autoscalerView{}, false
	}
	if !found {
		return false, autoscalerView{}, true
	}

	matches := make([]autoscalerView, 0, 1)
	for _, item := range items {
		if item.Spec.ScaleTargetRef.Kind == kind && item.Spec.ScaleTargetRef.Name == name {
			matches = append(matches, item.view())
		}
	}
	if len(matches) == 0 {
		return false, autoscalerView{}, true
	}
	slices.SortFunc(matches, func(a, b autoscalerView) int {
		return strings.Compare(a.Name, b.Name)
	})
	return true, matches[0], true
}

// autoscalerNotice is the sentence the scale control carries when an HPA owns
// the number being written. It is deliberately not a refusal: setting the count
// by hand is a legitimate thing to do — it is how somebody forces a floor while
// they debug — and refusing it would take away a capability the manifest editor
// has anyway. What was missing was being told.
func autoscalerNotice(view autoscalerView) string {
	return fmt.Sprintf("%s scales this workload between %d and %d replicas, "+
		"so a count set here is reverted on its next pass.",
		view.Name, view.MinReplicas, view.MaxReplicas)
}
