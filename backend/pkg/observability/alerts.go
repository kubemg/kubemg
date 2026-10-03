package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

/*
 * Reading and silencing a cluster's Alertmanager.
 *
 * The query path's rule holds here unchanged: Alertmanager has never heard of
 * the caller and answers whatever it is asked, so the scope is applied by KubeMG
 * to what comes back. An alert belongs to the namespace its `namespace` label
 * names; one with no such label is about the cluster, and a namespace-scoped
 * caller is not shown it. A silence is visible to a scoped caller only when it
 * is pinned to one of their namespaces by an exact matcher — a regex matcher
 * could reach past the grant, so it never counts.
 *
 * Silences are the one write. What a silence matches is built here from an
 * alert's own labels — never sent by the browser — and whether this caller may
 * mute that alert is the API layer's decision, made before anything is posted.
 */

const (
	// maxAlertsBody bounds an Alertmanager answer. A noisy fleet's Alertmanager
	// can hold thousands of alerts; eight megabytes is the agent's own ceiling.
	maxAlertsBody = 8 << 20
	alertsTimeout = 20 * time.Second
	// maxAlerts caps what one read returns, worst first.
	maxAlerts = 500
)

// Alert is one alert as the console draws it.
type Alert struct {
	Fingerprint string            `json:"fingerprint"`
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace,omitempty"`
	Severity    string            `json:"severity,omitempty"`
	State       string            `json:"state"`
	StartsAt    time.Time         `json:"starts_at"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	SilencedBy  []string          `json:"silenced_by,omitempty"`
	// KubeMG marks an alert raised by a rule KubeMG wrote.
	KubeMG bool `json:"kubemg"`
}

// Alert states as the console names them. Alertmanager's own word for both a
// silenced and an inhibited alert is "suppressed", which hides the one
// difference an operator needs: somebody chose to mute the first.
const (
	AlertFiring    = "firing"
	AlertSilenced  = "silenced"
	AlertInhibited = "inhibited"
)

// AlertFilter narrows a read to one object. Empty reads everything in scope.
type AlertFilter struct {
	Namespace string
	// Kind is a resource key as the console addresses it (deployments, pods…).
	Kind string
	Name string
}

type amAlert struct {
	Fingerprint string            `json:"fingerprint"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	Status      struct {
		State       string   `json:"state"`
		SilencedBy  []string `json:"silencedBy"`
		InhibitedBy []string `json:"inhibitedBy"`
	} `json:"status"`
}

// ListAlerts reads the active alerts the caller may see.
func ListAlerts(ctx context.Context, target Target, tunnel TunnelCall, scope Scope, filter AlertFilter) ([]Alert, error) {
	for field, value := range map[string]string{"namespace": filter.Namespace, "object": filter.Name} {
		if err := validateName(field, value); err != nil {
			return nil, err
		}
	}
	if filter.Namespace != "" && !scope.Allows(filter.Namespace) {
		return nil, fmt.Errorf("namespace %q is outside your granted scope", filter.Namespace)
	}

	raw, err := readAlerts(ctx, target, tunnel)
	if err != nil {
		return nil, err
	}

	match := filter.matcher()
	out := []Alert{}
	for _, item := range raw {
		namespace := item.Labels["namespace"]
		if !scope.Unscoped() && (namespace == "" || !scope.Allows(namespace)) {
			continue
		}
		if !match(item.Labels) {
			continue
		}
		out = append(out, toAlert(item))
	}

	slices.SortStableFunc(out, func(a, b Alert) int {
		if order := alertRank(a) - alertRank(b); order != 0 {
			return order
		}
		return b.StartsAt.Compare(a.StartsAt)
	})
	if len(out) > maxAlerts {
		out = out[:maxAlerts]
	}
	return out, nil
}

// FindAlert reads one active alert by fingerprint, unfiltered. The caller
// decides whether the person asking may act on it.
func FindAlert(ctx context.Context, target Target, tunnel TunnelCall, fingerprint string) (*Alert, error) {
	raw, err := readAlerts(ctx, target, tunnel)
	if err != nil {
		return nil, err
	}
	for _, item := range raw {
		if item.Fingerprint == fingerprint {
			alert := toAlert(item)
			return &alert, nil
		}
	}
	return nil, nil
}

func readAlerts(ctx context.Context, target Target, tunnel TunnelCall) ([]amAlert, error) {
	ctx, cancel := context.WithTimeout(ctx, alertsTimeout)
	defer cancel()
	status, body, err := callLimited(ctx, target,
		"/api/v2/alerts?active=true&silenced=true&inhibited=true&unprocessed=false",
		tunnel, maxAlertsBody, alertsTimeout)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", explain(target, status, body))
	}
	var raw []amAlert
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("Alertmanager answered with something that is not an alert list")
	}
	return raw, nil
}

func toAlert(item amAlert) Alert {
	state := AlertFiring
	switch {
	case len(item.Status.SilencedBy) > 0:
		state = AlertSilenced
	case len(item.Status.InhibitedBy) > 0:
		state = AlertInhibited
	}
	labels := item.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	annotations := item.Annotations
	if annotations == nil {
		annotations = map[string]string{}
	}
	return Alert{
		Fingerprint: item.Fingerprint,
		Name:        labels["alertname"],
		Namespace:   labels["namespace"],
		Severity:    labels["severity"],
		State:       state,
		StartsAt:    item.StartsAt,
		Labels:      labels,
		Annotations: annotations,
		SilencedBy:  item.Status.SilencedBy,
		KubeMG:      labels[KubeMGRuleLabel] != "",
	}
}

// KubeMGRuleLabel is the alert label every rule KubeMG writes carries, naming
// the PrometheusRule it came from.
const KubeMGRuleLabel = "kubemg_rule"

// alertRank orders firing before muted, and critical before the rest.
func alertRank(a Alert) int {
	rank := 0
	if a.State != AlertFiring {
		rank += 10
	}
	switch a.Severity {
	case "critical":
	case "warning":
		rank += 1
	default:
		rank += 2
	}
	return rank
}

// podPatterns is how a workload's pods are named, so an alert kube-prometheus
// raises about a pod (KubePodCrashLooping) is found from its Deployment. It is
// a naming convention, not a lookup: a Deployment "api" claims "api-7f9c5-x2kq9"
// and not "api-gateway-…", because the suffix shapes are fixed.
var podPatterns = map[string]string{
	"deployments":  `-[a-z0-9]{1,10}-[a-z0-9]{5}`,
	"statefulsets": `-[0-9]+`,
	"daemonsets":   `-[a-z0-9]{5}`,
	"jobs":         `-[a-z0-9]{5}`,
	"cronjobs":     `-[0-9]+-[a-z0-9]{5}`,
}

// objectLabel is the label kube-state-metrics' own alerts name an object by.
var objectLabel = map[string]string{
	"deployments":            "deployment",
	"statefulsets":           "statefulset",
	"daemonsets":             "daemonset",
	"jobs":                   "job_name",
	"cronjobs":               "cronjob",
	"pods":                   "pod",
	"persistentvolumeclaims": "persistentvolumeclaim",
}

func (f AlertFilter) matcher() func(map[string]string) bool {
	if f.Name == "" {
		return func(labels map[string]string) bool {
			return f.Namespace == "" || labels["namespace"] == f.Namespace
		}
	}
	var podPattern *regexp.Regexp
	if suffix, ok := podPatterns[f.Kind]; ok {
		podPattern = regexp.MustCompile("^" + regexp.QuoteMeta(f.Name) + suffix + "$")
	}
	var jobPattern *regexp.Regexp
	if f.Kind == "cronjobs" {
		jobPattern = regexp.MustCompile("^" + regexp.QuoteMeta(f.Name) + "-[0-9]+$")
	}
	kindLabel := KindLabel(f.Kind)
	return func(labels map[string]string) bool {
		if labels["namespace"] != f.Namespace {
			return false
		}
		if labels["kubemg_kind"] == kindLabel && labels["kubemg_name"] == f.Name {
			return true
		}
		if key, ok := objectLabel[f.Kind]; ok && labels[key] == f.Name {
			return true
		}
		if podPattern != nil && podPattern.MatchString(labels["pod"]) {
			return true
		}
		return jobPattern != nil && jobPattern.MatchString(labels["job_name"])
	}
}

// KindLabel is the Kubernetes Kind a resource key names, as KubeMG's alert
// labels carry it.
func KindLabel(key string) string {
	switch key {
	case "deployments":
		return "Deployment"
	case "statefulsets":
		return "StatefulSet"
	case "daemonsets":
		return "DaemonSet"
	case "jobs":
		return "Job"
	case "cronjobs":
		return "CronJob"
	case "pods":
		return "Pod"
	case "persistentvolumeclaims":
		return "PersistentVolumeClaim"
	}
	return ""
}

// Silence is one Alertmanager silence as the console draws it.
type Silence struct {
	ID        string           `json:"id"`
	State     string           `json:"state"`
	Namespace string           `json:"namespace,omitempty"`
	Matchers  []SilenceMatcher `json:"matchers"`
	StartsAt  time.Time        `json:"starts_at"`
	EndsAt    time.Time        `json:"ends_at"`
	CreatedBy string           `json:"created_by"`
	Comment   string           `json:"comment"`
}

// SilenceMatcher is one label condition of a silence.
type SilenceMatcher struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	IsRegex bool   `json:"isRegex"`
	IsEqual *bool  `json:"isEqual,omitempty"`
}

// pinnedNamespace is the namespace a silence is confined to by an exact,
// positive matcher — the only shape that can be held to a grant.
func pinnedNamespace(matchers []SilenceMatcher) string {
	for _, m := range matchers {
		if m.Name == "namespace" && !m.IsRegex && (m.IsEqual == nil || *m.IsEqual) {
			return m.Value
		}
	}
	return ""
}

type amSilence struct {
	ID     string `json:"id"`
	Status struct {
		State string `json:"state"`
	} `json:"status"`
	Matchers  []SilenceMatcher `json:"matchers"`
	StartsAt  time.Time        `json:"startsAt"`
	EndsAt    time.Time        `json:"endsAt"`
	CreatedBy string           `json:"createdBy"`
	Comment   string           `json:"comment"`
}

// ListSilences reads the live (active or pending) silences the caller may see.
func ListSilences(ctx context.Context, target Target, tunnel TunnelCall, scope Scope) ([]Silence, error) {
	raw, err := readSilences(ctx, target, tunnel)
	if err != nil {
		return nil, err
	}
	out := []Silence{}
	for _, item := range raw {
		if item.Status.State == "expired" {
			continue
		}
		silence := toSilence(item)
		if !scope.Unscoped() && (silence.Namespace == "" || !scope.Allows(silence.Namespace)) {
			continue
		}
		out = append(out, silence)
	}
	slices.SortStableFunc(out, func(a, b Silence) int { return a.EndsAt.Compare(b.EndsAt) })
	return out, nil
}

// FindSilence reads one silence by id, unfiltered.
func FindSilence(ctx context.Context, target Target, tunnel TunnelCall, id string) (*Silence, error) {
	ctx, cancel := context.WithTimeout(ctx, alertsTimeout)
	defer cancel()
	status, body, err := callLimited(ctx, target, "/api/v2/silence/"+url.PathEscape(id),
		tunnel, maxProbeBody, alertsTimeout)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", explain(target, status, body))
	}
	var item amSilence
	if err := json.Unmarshal(body, &item); err != nil {
		return nil, fmt.Errorf("Alertmanager answered with something that is not a silence")
	}
	silence := toSilence(item)
	return &silence, nil
}

func readSilences(ctx context.Context, target Target, tunnel TunnelCall) ([]amSilence, error) {
	ctx, cancel := context.WithTimeout(ctx, alertsTimeout)
	defer cancel()
	status, body, err := callLimited(ctx, target, "/api/v2/silences", tunnel, maxAlertsBody, alertsTimeout)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", explain(target, status, body))
	}
	var raw []amSilence
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("Alertmanager answered with something that is not a silence list")
	}
	return raw, nil
}

func toSilence(item amSilence) Silence {
	return Silence{
		ID:        item.ID,
		State:     item.Status.State,
		Namespace: pinnedNamespace(item.Matchers),
		Matchers:  item.Matchers,
		StartsAt:  item.StartsAt,
		EndsAt:    item.EndsAt,
		CreatedBy: item.CreatedBy,
		Comment:   item.Comment,
	}
}

// SilenceMatchersFor builds the matchers that mute exactly one alert: every
// label it carries, as an exact match. A silence any wider would be a decision
// the person asking did not make.
func SilenceMatchersFor(alert Alert) []SilenceMatcher {
	keys := make([]string, 0, len(alert.Labels))
	for key := range alert.Labels {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	equal := true
	out := make([]SilenceMatcher, 0, len(keys))
	for _, key := range keys {
		out = append(out, SilenceMatcher{Name: key, Value: alert.Labels[key], IsEqual: &equal})
	}
	return out
}

// CreateSilence posts a silence and returns its id.
func CreateSilence(ctx context.Context, target Target, tunnel TunnelCall,
	matchers []SilenceMatcher, until time.Time, createdBy, comment string,
) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"matchers":  matchers,
		"startsAt":  time.Now().UTC().Format(time.RFC3339),
		"endsAt":    until.UTC().Format(time.RFC3339),
		"createdBy": createdBy,
		"comment":   comment,
	})
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, alertsTimeout)
	defer cancel()
	status, body, err := callMethod(ctx, target, http.MethodPost, "/api/v2/silences", payload,
		tunnel, maxProbeBody, alertsTimeout)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("%s", explain(target, status, body))
	}
	var answer struct {
		SilenceID string `json:"silenceID"`
	}
	if err := json.Unmarshal(body, &answer); err != nil || answer.SilenceID == "" {
		return "", fmt.Errorf("Alertmanager did not say which silence it created")
	}
	return answer.SilenceID, nil
}

// ExpireSilence ends a silence now.
func ExpireSilence(ctx context.Context, target Target, tunnel TunnelCall, id string) error {
	ctx, cancel := context.WithTimeout(ctx, alertsTimeout)
	defer cancel()
	status, body, err := callMethod(ctx, target, http.MethodDelete, "/api/v2/silence/"+url.PathEscape(id), nil,
		tunnel, maxProbeBody, alertsTimeout)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("%s", strings.TrimSpace(explain(target, status, body)))
	}
	return nil
}
