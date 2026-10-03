package observability

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

/*
 * An alarm on one object, written as a PrometheusRule.
 *
 * The cluster's Prometheus evaluates it and the cluster's Alertmanager routes
 * it, so the alarm keeps working while KubeMG is down and lands in the silences,
 * inhibitions and on-call routing that fleet already has. KubeMG's part is
 * writing the rule — and the rule is the one place in KubeMG where somebody's
 * choice becomes PromQL, so the query path's rule applies exactly: **the
 * browser never sends an expression**. A caller names an object and a condition
 * from this catalogue; the expression is written here around names validated
 * against the Kubernetes grammar, never escaped, so nothing a caller sends can
 * become matcher syntax.
 *
 * The object is the record. There is no table of alarms in KubeMG: a rule is
 * found again by its `app.kubernetes.io/managed-by: kubemg` label, read and
 * written through the tunnel as the person asking, so the cluster's own RBAC
 * decides who may create one and a GitOps diff shows it like anything else.
 *
 * Every expression reads kube-state-metrics (or the kubelet's volume stats),
 * which is what a kube-prometheus-stack scrapes. One object and one condition
 * is one rule object with a deterministic name, so "the same alarm twice" is a
 * conflict the cluster reports rather than a duplicate page.
 */

// ManagedByLabel and ManagedByValue mark a PrometheusRule KubeMG wrote.
const (
	ManagedByLabel = "app.kubernetes.io/managed-by"
	ManagedByValue = "kubemg"

	alarmKindLabel      = "kubemg.io/target-kind"
	alarmConditionLabel = "kubemg.io/condition"

	alarmTargetAnnotation    = "kubemg.io/target-name"
	alarmThresholdAnnotation = "kubemg.io/threshold"
	alarmCreatedByAnnotation = "kubemg.io/created-by"
	alarmUpdatedByAnnotation = "kubemg.io/updated-by"
	alarmNoteAnnotation      = "kubemg.io/note"

	// maxNote bounds the free text kept on the object.
	maxNote = 500
)

// AlarmDurations is how long a condition must hold before it fires — the
// rule's `for`. A ladder rather than a number box, as JIT's windows are.
var AlarmDurations = []string{"0m", "1m", "5m", "10m", "15m", "30m", "1h"}

// AlarmSeverityLevels are the severities a rule may carry, in the vocabulary
// Alertmanager routing conventionally matches on.
var AlarmSeverityLevels = []string{"info", "warning", "critical"}

// Threshold is a condition's one number.
type Threshold struct {
	Label   string  `json:"label"`
	Unit    string  `json:"unit,omitempty"`
	Default float64 `json:"default"`
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
}

// AlarmCondition is one thing worth alarming on for a kind of object.
type AlarmCondition struct {
	Key         string     `json:"key"`
	Label       string     `json:"label"`
	Description string     `json:"description"`
	Threshold   *Threshold `json:"threshold,omitempty"`
	DefaultFor  string     `json:"default_for"`
	// DefaultSeverity is what the form opens on.
	DefaultSeverity string `json:"default_severity"`

	// expr writes the PromQL. ns and name are validated; podRe and jobRe are
	// their regex-safe forms.
	expr func(t alarmTerms) string
	// summary is the alert's one line.
	summary func(t alarmTerms) string
}

// alarmTerms is what an expression is written from.
type alarmTerms struct {
	Namespace string
	Name      string
	// NameRe is Name with its dots escaped for a PromQL regex matcher.
	NameRe    string
	Threshold string
}

func (t alarmTerms) sel(label string, extra ...string) string {
	parts := append([]string{fmt.Sprintf(`namespace="%s"`, t.Namespace), fmt.Sprintf(`%s="%s"`, label, t.Name)}, extra...)
	return "{" + strings.Join(parts, ",") + "}"
}

func (t alarmTerms) podsLike(suffix string) string {
	return fmt.Sprintf(`{namespace="%s",pod=~"%s%s"}`, t.Namespace, t.NameRe, suffix)
}

func restartsCondition(suffix, what string) AlarmCondition {
	return AlarmCondition{
		Key:             "restarts",
		Label:           "Pods restarting",
		Description:     "Containers restarted more than the threshold in the last 15 minutes.",
		Threshold:       &Threshold{Label: "Restarts in 15 minutes", Default: 3, Min: 1, Max: 1000},
		DefaultFor:      "0m",
		DefaultSeverity: "warning",
		expr: func(t alarmTerms) string {
			sel := t.podsLike(suffix)
			if suffix == "" {
				sel = t.sel("pod")
			}
			return fmt.Sprintf(`sum(increase(kube_pod_container_status_restarts_total%s[15m])) > %s`, sel, t.Threshold)
		},
		summary: func(t alarmTerms) string {
			return fmt.Sprintf("%s %s/%s restarted more than %s times in 15 minutes", what, t.Namespace, t.Name, t.Threshold)
		},
	}
}

func gaugeCondition(key, label, description, defaultFor, severity, expr, summary string) AlarmCondition {
	return AlarmCondition{
		Key: key, Label: label, Description: description,
		DefaultFor: defaultFor, DefaultSeverity: severity,
		expr: func(t alarmTerms) string {
			return strings.NewReplacer("$NS", t.Namespace, "$NAME_RE", t.NameRe, "$NAME", t.Name).Replace(expr)
		},
		summary: func(t alarmTerms) string {
			return strings.NewReplacer("$NS", t.Namespace, "$NAME", t.Name).Replace(summary)
		},
	}
}

// alarmCatalogue is every condition, by the resource key the console addresses
// a kind by. Adding one is adding an entry, written by someone who knows what
// its series are.
var alarmCatalogue = map[string][]AlarmCondition{
	"deployments": {
		gaugeCondition("unavailable", "Replicas unavailable",
			"Some of the Deployment's replicas are not available.", "5m", "warning",
			`kube_deployment_status_replicas_unavailable{namespace="$NS",deployment="$NAME"} > 0`,
			"Deployment $NS/$NAME has unavailable replicas"),
		gaugeCondition("rollout-stuck", "Rollout stuck",
			"The rollout passed its progress deadline without completing.", "1m", "warning",
			`kube_deployment_status_condition{namespace="$NS",deployment="$NAME",condition="Progressing",status="false"} == 1`,
			"Deployment $NS/$NAME rollout is stuck"),
		restartsCondition(podPatterns["deployments"], "Deployment"),
	},
	"statefulsets": {
		gaugeCondition("unavailable", "Replicas not ready",
			"Fewer replicas are ready than the StatefulSet declares.", "5m", "warning",
			`max(kube_statefulset_replicas{namespace="$NS",statefulset="$NAME"}) - max(kube_statefulset_status_replicas_ready{namespace="$NS",statefulset="$NAME"}) > 0`,
			"StatefulSet $NS/$NAME has replicas that are not ready"),
		restartsCondition(podPatterns["statefulsets"], "StatefulSet"),
	},
	"daemonsets": {
		gaugeCondition("unavailable", "Pods unavailable",
			"Some nodes that should run this DaemonSet's pod do not have an available one.", "5m", "warning",
			`max(kube_daemonset_status_number_unavailable{namespace="$NS",daemonset="$NAME"}) > 0`,
			"DaemonSet $NS/$NAME has unavailable pods"),
		gaugeCondition("misscheduled", "Pods misscheduled",
			"Pods are running on nodes they should not be.", "15m", "warning",
			`max(kube_daemonset_status_number_misscheduled{namespace="$NS",daemonset="$NAME"}) > 0`,
			"DaemonSet $NS/$NAME has misscheduled pods"),
		restartsCondition(podPatterns["daemonsets"], "DaemonSet"),
	},
	"pods": {
		gaugeCondition("not-ready", "Not ready",
			"The pod is not ready.", "5m", "warning",
			`max(kube_pod_status_ready{namespace="$NS",pod="$NAME",condition="false"}) == 1`,
			"Pod $NS/$NAME is not ready"),
		gaugeCondition("crashloop", "Crash looping",
			"A container is waiting in CrashLoopBackOff.", "1m", "critical",
			`max(kube_pod_container_status_waiting_reason{namespace="$NS",pod="$NAME",reason="CrashLoopBackOff"}) == 1`,
			"Pod $NS/$NAME is crash looping"),
		restartsCondition("", "Pod"),
	},
	"jobs": {
		gaugeCondition("failed", "Job failed",
			"The Job has failed pods.", "0m", "warning",
			`max(kube_job_status_failed{namespace="$NS",job_name="$NAME"}) > 0`,
			"Job $NS/$NAME failed"),
	},
	"cronjobs": {
		gaugeCondition("failed", "A run failed",
			"One of the Jobs this CronJob created has failed. It keeps firing until that Job is cleaned up by the history limit.", "0m", "warning",
			`max(kube_job_status_failed{namespace="$NS",job_name=~"$NAME_RE-[0-9]+"}) > 0`,
			"CronJob $NS/$NAME has a failed run"),
	},
	"persistentvolumeclaims": {
		{
			Key:             "filling",
			Label:           "Volume filling up",
			Description:     "The volume's used space passed the threshold.",
			Threshold:       &Threshold{Label: "Used", Unit: "%", Default: 85, Min: 1, Max: 100},
			DefaultFor:      "5m",
			DefaultSeverity: "warning",
			expr: func(t alarmTerms) string {
				return fmt.Sprintf(`max(kubelet_volume_stats_used_bytes%s / kubelet_volume_stats_capacity_bytes%s) * 100 > %s`,
					t.sel("persistentvolumeclaim"), t.sel("persistentvolumeclaim"), t.Threshold)
			},
			summary: func(t alarmTerms) string {
				return fmt.Sprintf("Volume claim %s/%s is more than %s%% full", t.Namespace, t.Name, t.Threshold)
			},
		},
		gaugeCondition("pending", "Claim pending",
			"The claim is not bound to a volume.", "5m", "warning",
			`max(kube_persistentvolumeclaim_status_phase{namespace="$NS",persistentvolumeclaim="$NAME",phase="Pending"}) == 1`,
			"Volume claim $NS/$NAME is pending"),
	},
}

// AlarmCatalogue returns the whole catalogue, by resource key.
func AlarmCatalogue() map[string][]AlarmCondition { return alarmCatalogue }

// AlarmConditionFor finds one condition.
func AlarmConditionFor(kind, key string) (AlarmCondition, bool) {
	for _, condition := range alarmCatalogue[kind] {
		if condition.Key == key {
			return condition, true
		}
	}
	return AlarmCondition{}, false
}

// AlarmRequest is one alarm as a caller asks for it.
type AlarmRequest struct {
	Namespace string
	Kind      string
	Name      string
	Condition string
	Threshold *float64
	For       string
	Severity  string
	Note      string
	// Actor is the KubeMG username writing it.
	Actor string
	// RuleLabels are what this cluster's Prometheus selects rules by.
	RuleLabels map[string]string
}

// AlarmSpec is what a request renders to: the object's name and the parts of
// it KubeMG owns. The caller assembles the object, so an update can keep
// everything else the cluster holds on it.
type AlarmSpec struct {
	Name        string
	Labels      map[string]string
	Annotations map[string]string
	Spec        map[string]any
	Expr        string
}

// BuildAlarm validates a request and renders it.
func BuildAlarm(req AlarmRequest) (AlarmSpec, error) {
	if err := validateName("namespace", req.Namespace); err != nil || req.Namespace == "" {
		return AlarmSpec{}, fmt.Errorf("an alarm needs a valid namespace")
	}
	if err := validateName("object", req.Name); err != nil || req.Name == "" {
		return AlarmSpec{}, fmt.Errorf("an alarm needs a valid object name")
	}
	condition, ok := AlarmConditionFor(req.Kind, req.Condition)
	if !ok {
		return AlarmSpec{}, fmt.Errorf("%q is not a condition KubeMG can alarm on for %s", req.Condition, req.Kind)
	}

	forDuration := req.For
	if forDuration == "" {
		forDuration = condition.DefaultFor
	}
	if !slices.Contains(AlarmDurations, forDuration) {
		return AlarmSpec{}, fmt.Errorf("%q is not one of the offered durations", forDuration)
	}
	severity := req.Severity
	if severity == "" {
		severity = condition.DefaultSeverity
	}
	if !slices.Contains(AlarmSeverityLevels, severity) {
		return AlarmSpec{}, fmt.Errorf("%q is not a severity", severity)
	}

	threshold := ""
	if condition.Threshold != nil {
		value := condition.Threshold.Default
		if req.Threshold != nil {
			value = *req.Threshold
		}
		if value < condition.Threshold.Min || value > condition.Threshold.Max {
			return AlarmSpec{}, fmt.Errorf("%s has to be between %s and %s",
				strings.ToLower(condition.Threshold.Label), formatNumber(condition.Threshold.Min), formatNumber(condition.Threshold.Max))
		}
		threshold = formatNumber(value)
	}

	note := strings.TrimSpace(req.Note)
	if len(note) > maxNote {
		return AlarmSpec{}, fmt.Errorf("the note is limited to %d characters", maxNote)
	}

	terms := alarmTerms{
		Namespace: req.Namespace,
		Name:      req.Name,
		NameRe:    strings.ReplaceAll(req.Name, ".", `\\.`),
		Threshold: threshold,
	}
	name := AlarmObjectName(req.Kind, req.Name, req.Condition)
	kindLabel := KindLabel(req.Kind)
	expr := condition.expr(terms)

	ruleLabels := map[string]string{
		"severity":         severity,
		"namespace":        req.Namespace,
		"kubemg_kind":      kindLabel,
		"kubemg_name":      req.Name,
		"kubemg_condition": req.Condition,
		KubeMGRuleLabel:    name,
	}
	if key, ok := objectLabel[req.Kind]; ok {
		ruleLabels[key] = req.Name
	}
	summary := condition.summary(terms)
	annotations := map[string]string{
		"summary":     summary,
		"description": condition.Description,
	}
	if note != "" {
		annotations["description"] = condition.Description + " " + note
	}

	rule := map[string]any{
		"alert":       "KubeMG" + kindLabel + camel(req.Condition),
		"expr":        expr,
		"labels":      ruleLabels,
		"annotations": annotations,
	}
	if forDuration != "0m" {
		rule["for"] = forDuration
	}

	labels := map[string]string{}
	for key, value := range req.RuleLabels {
		labels[key] = value
	}
	labels[ManagedByLabel] = ManagedByValue
	labels[alarmKindLabel] = req.Kind
	labels[alarmConditionLabel] = req.Condition

	objectAnnotations := map[string]string{
		alarmTargetAnnotation:    req.Name,
		alarmCreatedByAnnotation: req.Actor,
	}
	if threshold != "" {
		objectAnnotations[alarmThresholdAnnotation] = threshold
	}
	if note != "" {
		objectAnnotations[alarmNoteAnnotation] = note
	}

	return AlarmSpec{
		Name:        name,
		Labels:      labels,
		Annotations: objectAnnotations,
		Spec: map[string]any{
			"groups": []any{map[string]any{
				"name":  "kubemg." + name,
				"rules": []any{rule},
			}},
		},
		Expr: expr,
	}, nil
}

// AlarmObjectName is the deterministic name of the rule for one object and one
// condition. A name too long for the API is cut and given a hash of the whole,
// so two long names never collide.
func AlarmObjectName(kind, name, condition string) string {
	singular := strings.ToLower(KindLabel(kind))
	full := "kubemg-" + singular + "-" + name + "-" + condition
	if len(full) <= 253 {
		return full
	}
	sum := sha256.Sum256([]byte(full))
	return strings.TrimRight(full[:240], "-.") + "-" + hex.EncodeToString(sum[:])[:10]
}

// Alarm is a KubeMG-written rule read back from the cluster.
type Alarm struct {
	Namespace       string    `json:"namespace"`
	Name            string    `json:"name"`
	Kind            string    `json:"kind"`
	Target          string    `json:"target"`
	Condition       string    `json:"condition"`
	ConditionLabel  string    `json:"condition_label"`
	Threshold       *float64  `json:"threshold,omitempty"`
	ThresholdUnit   string    `json:"threshold_unit,omitempty"`
	For             string    `json:"for"`
	Severity        string    `json:"severity"`
	Expr            string    `json:"expr"`
	Note            string    `json:"note,omitempty"`
	CreatedBy       string    `json:"created_by,omitempty"`
	UpdatedBy       string    `json:"updated_by,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	ResourceVersion string    `json:"resource_version,omitempty"`
}

// PrometheusRuleObject is the slice of a PrometheusRule this package reads.
type PrometheusRuleObject struct {
	Metadata struct {
		Name              string            `json:"name"`
		Namespace         string            `json:"namespace"`
		Labels            map[string]string `json:"labels"`
		Annotations       map[string]string `json:"annotations"`
		ResourceVersion   string            `json:"resourceVersion"`
		CreationTimestamp time.Time         `json:"creationTimestamp"`
	} `json:"metadata"`
	Spec struct {
		Groups []struct {
			Rules []struct {
				Expr   string            `json:"expr"`
				For    string            `json:"for"`
				Labels map[string]string `json:"labels"`
			} `json:"rules"`
		} `json:"groups"`
	} `json:"spec"`
}

// ParseAlarm reads a rule back, reporting false for one KubeMG did not write.
func ParseAlarm(object PrometheusRuleObject) (Alarm, bool) {
	meta := object.Metadata
	if meta.Labels[ManagedByLabel] != ManagedByValue {
		return Alarm{}, false
	}
	kind := meta.Labels[alarmKindLabel]
	conditionKey := meta.Labels[alarmConditionLabel]
	alarm := Alarm{
		Namespace:       meta.Namespace,
		Name:            meta.Name,
		Kind:            kind,
		Target:          meta.Annotations[alarmTargetAnnotation],
		Condition:       conditionKey,
		ConditionLabel:  conditionKey,
		Note:            meta.Annotations[alarmNoteAnnotation],
		CreatedBy:       meta.Annotations[alarmCreatedByAnnotation],
		UpdatedBy:       meta.Annotations[alarmUpdatedByAnnotation],
		CreatedAt:       meta.CreationTimestamp,
		ResourceVersion: meta.ResourceVersion,
		For:             "0m",
	}
	if condition, ok := AlarmConditionFor(kind, conditionKey); ok {
		alarm.ConditionLabel = condition.Label
		if condition.Threshold != nil {
			alarm.ThresholdUnit = condition.Threshold.Unit
		}
	}
	if raw := meta.Annotations[alarmThresholdAnnotation]; raw != "" {
		if value, err := strconv.ParseFloat(raw, 64); err == nil {
			alarm.Threshold = &value
		}
	}
	if len(object.Spec.Groups) > 0 && len(object.Spec.Groups[0].Rules) > 0 {
		rule := object.Spec.Groups[0].Rules[0]
		alarm.Expr = rule.Expr
		alarm.Severity = rule.Labels["severity"]
		if rule.For != "" {
			alarm.For = rule.For
		}
	}
	return alarm, true
}

// UpdatedByAnnotation names who last changed a rule.
func UpdatedByAnnotation() string { return alarmUpdatedByAnnotation }

// CreatedByAnnotation names who created a rule.
func CreatedByAnnotation() string { return alarmCreatedByAnnotation }

func formatNumber(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }

func camel(key string) string {
	parts := strings.Split(key, "-")
	for i, part := range parts {
		if part != "" {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, "")
}
