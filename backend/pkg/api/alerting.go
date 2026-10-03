package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/observability"
)

/*
 * Alarms on an object, and the cluster's Alertmanager.
 *
 * Two halves with two different authorities, kept apart on purpose.
 *
 * An alarm is a PrometheusRule, read and written down the tunnel as the person
 * asking — `create` on prometheusrules in that namespace is the cluster's own
 * RBAC to grant or refuse, exactly like any other object. KubeMG adds nothing
 * to that decision; it writes the expression (from a fixed catalogue, never
 * from the browser) and the labels this cluster's Prometheus loads rules by.
 *
 * Alertmanager has never heard of the caller, so reading it is the query
 * path's shape: reached as the datasource, narrowed here to the namespaces the
 * grant covers. Muting an alert is a write no cluster RBAC can see, so KubeMG
 * decides it — an edit grant over the alert's namespace — records it in its own
 * trail, and builds the silence's matchers from the alert itself.
 */

const prometheusRuleGroup = "/apis/monitoring.coreos.com/v1"

var alarmObjectName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)

func prometheusRulesPath(namespace string) string {
	return prometheusRuleGroup + "/namespaces/" + url.PathEscape(namespace) + "/prometheusrules"
}

// listAlarmConditions is the catalogue the alarm form offers.
func (s *server) listAlarmConditions(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"conditions": observability.AlarmCatalogue(),
		"durations":  observability.AlarmDurations,
		"severities": observability.AlarmSeverityLevels,
	})
}

// listAlarms reads the rules KubeMG wrote, optionally for one object. A cluster
// that does not serve PrometheusRule, or a role that may not read them, is an
// answer — `available: false` and why — rather than a failed drawer.
func (s *server) listAlarms(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	selector := "?labelSelector=" + url.QueryEscape(observability.ManagedByLabel+"="+observability.ManagedByValue)
	path := resourceListPath{group: prometheusRuleGroup, resource: "prometheusrules"}
	unavailable := func(reason string) {
		listResponse(c, gin.H{
			"items": []observability.Alarm{}, "namespace": scope.Namespace,
			"all_namespaces": scope.All, "available": false, "reason": reason,
		})
	}

	var objects []observability.PrometheusRuleObject
	for _, candidate := range scope.paths(path) {
		resp, callOK := s.callResource(c, user, cluster, grant, pagedPath(candidate+selector, listPageSize, ""))
		if !callOK {
			return
		}
		switch resp.Status {
		case http.StatusNotFound:
			unavailable(noPrometheusOperator)
			return
		case http.StatusForbidden:
			unavailable(kubeErrorMessage(resp.Body, resp.Status))
			return
		}
		var page listPage[observability.PrometheusRuleObject]
		if !s.decodeResource(c, resp, &page) {
			return
		}
		objects = append(objects, page.Items...)
		if page.Metadata.Continue != "" {
			walk, walkOK := walkListPages(
				pageFetcher(s, c, user, cluster, grant, candidate+selector), page.Metadata.Continue, &objects)
			if !walkOK || !walk.render(c) {
				return
			}
		}
	}

	kind := strings.TrimSpace(c.Query("kind"))
	target := strings.TrimSpace(c.Query("name"))
	alarms := []observability.Alarm{}
	for _, object := range objects {
		alarm, mine := observability.ParseAlarm(object)
		if !mine {
			continue
		}
		if kind != "" && alarm.Kind != kind {
			continue
		}
		if target != "" && alarm.Target != target {
			continue
		}
		alarms = append(alarms, alarm)
	}
	slices.SortFunc(alarms, func(a, b observability.Alarm) int {
		return strings.Compare(a.Namespace+"/"+a.Name, b.Namespace+"/"+b.Name)
	})

	listResponse(c, gin.H{
		"items": alarms, "namespace": scope.Namespace,
		"all_namespaces": scope.All, "available": true,
	})
}

const noPrometheusOperator = "this cluster does not serve PrometheusRule — alarms need the Prometheus operator " +
	"(kube-prometheus-stack installs it)"

type alarmCreateRequest struct {
	Namespace string   `json:"namespace" binding:"required"`
	Kind      string   `json:"kind" binding:"required"`
	Name      string   `json:"name" binding:"required"`
	Condition string   `json:"condition" binding:"required"`
	Threshold *float64 `json:"threshold"`
	For       string   `json:"for"`
	Severity  string   `json:"severity"`
	Note      string   `json:"note"`
}

// alertingSource is the cluster's Alertmanager registration, which an alarm
// needs for one thing: the labels its Prometheus loads rules by. Without it a
// rule would be written that nothing evaluates, so the write is refused.
func (s *server) alertingSource(c *gin.Context, cluster *db.Cluster) (*db.ObservabilitySource, bool) {
	source, err := s.store.ObservabilitySource(c.Request.Context(), cluster.ID, db.SourceAlerts)
	if errors.Is(err, db.ErrNotFound) || (err == nil && !source.Enabled) {
		c.JSON(http.StatusConflict, gin.H{
			"error": "an administrator has to register this cluster's Alertmanager before alarms can be created — " +
				"it is also where KubeMG learns which labels the cluster's Prometheus loads rules by",
			"unconfigured": true,
		})
		return nil, false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the alerting datasource"})
		return nil, false
	}
	return source, true
}

// createAlarm writes one rule, as the caller.
func (s *server) createAlarm(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	var req alarmCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "an alarm needs a namespace, an object and a condition"})
		return
	}
	namespace, ok := s.scopedNamespace(c, grant, req.Namespace)
	if !ok {
		return
	}
	source, ok := s.alertingSource(c, cluster)
	if !ok {
		return
	}

	spec, err := observability.BuildAlarm(observability.AlarmRequest{
		Namespace: namespace, Kind: req.Kind, Name: strings.TrimSpace(req.Name),
		Condition: req.Condition, Threshold: req.Threshold, For: req.For,
		Severity: req.Severity, Note: req.Note, Actor: user.Username,
		RuleLabels: source.RuleLabelMap(),
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	body, err := json.Marshal(map[string]any{
		"apiVersion": "monitoring.coreos.com/v1",
		"kind":       "PrometheusRule",
		"metadata": map[string]any{
			"name": spec.Name, "namespace": namespace,
			"labels": spec.Labels, "annotations": spec.Annotations,
		},
		"spec": spec.Spec,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not render the alarm"})
		return
	}

	resp, ok := s.callResourceWith(c, user, cluster, grant, http.MethodPost,
		prometheusRulesPath(namespace), body, "could not write to the cluster")
	if !ok {
		return
	}
	switch resp.Status {
	case http.StatusNotFound:
		c.JSON(http.StatusConflict, gin.H{"error": noPrometheusOperator})
		return
	case http.StatusConflict:
		c.JSON(http.StatusConflict, gin.H{
			"error":    "this object already has an alarm for that condition — change it instead",
			"existing": spec.Name,
		})
		return
	}
	s.answerAlarm(c, resp, http.StatusCreated)
}

func (s *server) answerAlarm(c *gin.Context, resp *bastion.Response, status int) {
	var object observability.PrometheusRuleObject
	if !s.decodeResource(c, resp, &object) {
		return
	}
	alarm, _ := observability.ParseAlarm(object)
	c.JSON(status, gin.H{"alarm": alarm})
}

type alarmUpdateRequest struct {
	Namespace string   `json:"namespace" binding:"required"`
	Name      string   `json:"name" binding:"required"`
	Threshold *float64 `json:"threshold"`
	For       string   `json:"for"`
	Severity  string   `json:"severity"`
	Note      string   `json:"note"`
}

// readAlarm loads one rule KubeMG wrote, both raw (to write back everything the
// cluster holds on it) and parsed. A rule KubeMG did not write is not found
// here: this route edits alarms, and the manifest editor edits anything else.
func (s *server) readAlarm(c *gin.Context, user *db.User, cluster *db.Cluster, grant db.UserClusterAccess,
	namespace, name string,
) (map[string]any, observability.Alarm, bool) {
	if !alarmObjectName.MatchString(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "that is not an alarm name"})
		return nil, observability.Alarm{}, false
	}
	resp, ok := s.callResource(c, user, cluster, grant, prometheusRulesPath(namespace)+"/"+url.PathEscape(name))
	if !ok {
		return nil, observability.Alarm{}, false
	}
	var raw map[string]any
	if !s.decodeResource(c, resp, &raw) {
		return nil, observability.Alarm{}, false
	}
	var object observability.PrometheusRuleObject
	if err := json.Unmarshal(resp.Body, &object); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "the cluster returned an unreadable rule"})
		return nil, observability.Alarm{}, false
	}
	alarm, mine := observability.ParseAlarm(object)
	if !mine {
		c.JSON(http.StatusNotFound, gin.H{"error": "that PrometheusRule was not written by KubeMG"})
		return nil, observability.Alarm{}, false
	}
	return raw, alarm, true
}

// updateAlarm changes an alarm's threshold, duration, severity or note — a
// read-modify-write carrying the resourceVersion back, so a concurrent change
// is a 409 rather than a silent overwrite. The object and condition are fixed:
// changing them is a different alarm.
func (s *server) updateAlarm(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	var req alarmUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name the alarm to change"})
		return
	}
	namespace, ok := s.scopedNamespace(c, grant, req.Namespace)
	if !ok {
		return
	}
	source, ok := s.alertingSource(c, cluster)
	if !ok {
		return
	}
	raw, current, ok := s.readAlarm(c, user, cluster, grant, namespace, req.Name)
	if !ok {
		return
	}

	spec, err := observability.BuildAlarm(observability.AlarmRequest{
		Namespace: namespace, Kind: current.Kind, Name: current.Target,
		Condition: current.Condition, Threshold: req.Threshold, For: req.For,
		Severity: req.Severity, Note: req.Note, Actor: current.CreatedBy,
		RuleLabels: source.RuleLabelMap(),
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if spec.Name != req.Name {
		c.JSON(http.StatusConflict, gin.H{"error": "this rule's labels no longer describe the object it was written for"})
		return
	}

	metadata, _ := raw["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
	}
	delete(metadata, "managedFields")
	labels := anyMap(metadata["labels"])
	for key, value := range spec.Labels {
		labels[key] = value
	}
	annotations := anyMap(metadata["annotations"])
	for _, key := range []string{"kubemg.io/threshold", "kubemg.io/note"} {
		delete(annotations, key)
	}
	for key, value := range spec.Annotations {
		if key == observability.CreatedByAnnotation() && annotations[key] != "" {
			continue
		}
		annotations[key] = value
	}
	annotations[observability.UpdatedByAnnotation()] = user.Username
	metadata["labels"] = labels
	metadata["annotations"] = annotations
	raw["metadata"] = metadata
	raw["spec"] = spec.Spec

	body, err := json.Marshal(raw)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not render the alarm"})
		return
	}
	resp, ok := s.callResourceWith(c, user, cluster, grant, http.MethodPut,
		prometheusRulesPath(namespace)+"/"+url.PathEscape(req.Name), body, "could not write to the cluster")
	if !ok {
		return
	}
	if resp.Status == http.StatusConflict {
		c.JSON(http.StatusConflict, gin.H{"error": "the alarm changed since it was read — reload it and try again"})
		return
	}
	s.answerAlarm(c, resp, http.StatusOK)
}

// deleteAlarm removes one rule KubeMG wrote.
func (s *server) deleteAlarm(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	namespace, ok := s.scopedNamespace(c, grant, c.Query("namespace"))
	if !ok {
		return
	}
	name := strings.TrimSpace(c.Query("name"))
	if _, _, ok := s.readAlarm(c, user, cluster, grant, namespace, name); !ok {
		return
	}
	resp, ok := s.callResourceWith(c, user, cluster, grant, http.MethodDelete,
		prometheusRulesPath(namespace)+"/"+url.PathEscape(name), nil, "could not write to the cluster")
	if !ok {
		return
	}
	if resp.Status < 200 || resp.Status >= 300 {
		c.JSON(resp.Status, gin.H{"error": kubeErrorMessage(resp.Body, resp.Status)})
		return
	}
	c.Status(http.StatusNoContent)
}

func anyMap(value any) map[string]any {
	out := map[string]any{}
	if typed, ok := value.(map[string]any); ok {
		for key, item := range typed {
			out[key] = item
		}
	}
	return out
}

// canMute reports whether a caller may silence, or end a silence on, something
// in a namespace. Muting is an act on the namespace's workloads in all but
// name, so it takes what changing them takes: an edit grant covering it. An
// alert with no namespace is about the cluster, and only an unscoped edit grant
// covers that.
func canMute(user *db.User, grant db.UserClusterAccess, namespace string) bool {
	if user.IsAdmin() {
		return true
	}
	if grant.K8sRole != db.K8sRoleEdit && grant.K8sRole != db.K8sRoleClusterAdmin {
		return false
	}
	scope := grant.NamespaceList()
	if len(scope) == 0 {
		return true
	}
	return namespace != "" && slices.Contains(scope, namespace)
}

// listFiringAlerts reads what the cluster's Alertmanager holds, narrowed to the
// caller's grant and optionally to one object.
func (s *server) listFiringAlerts(c *gin.Context) {
	user, cluster, grant, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}
	source, ok := s.querySource(c, cluster, db.SourceAlerts)
	if !ok {
		return
	}
	scope := queryScope(user, grant)
	filter := observability.AlertFilter{
		Namespace: strings.TrimSpace(c.Query("namespace")),
		Kind:      strings.TrimSpace(c.Query("kind")),
		Name:      strings.TrimSpace(c.Query("name")),
	}
	if filter.Namespace != "" && !scope.Allows(filter.Namespace) {
		c.JSON(http.StatusForbidden, gin.H{"error": fmt.Sprintf("namespace %q is outside your granted scope", filter.Namespace)})
		return
	}
	alerts, err := observability.ListAlerts(c.Request.Context(),
		observability.TargetOf(*source), s.tunnelCall(user, cluster), scope, filter)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	type alertView struct {
		observability.Alert
		CanSilence bool `json:"can_silence"`
	}
	out := make([]alertView, 0, len(alerts))
	for _, alert := range alerts {
		out = append(out, alertView{Alert: alert, CanSilence: canMute(user, grant, alert.Namespace)})
	}
	c.JSON(http.StatusOK, gin.H{
		"alerts":   out,
		"endpoint": observability.TargetOf(*source).Endpoint(),
		// Whether the drawer should offer Silence for this object at all.
		"can_silence": canMute(user, grant, filter.Namespace),
	})
}

// listSilences reads the live silences the caller may see.
func (s *server) listSilences(c *gin.Context) {
	user, cluster, grant, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}
	source, ok := s.querySource(c, cluster, db.SourceAlerts)
	if !ok {
		return
	}
	silences, err := observability.ListSilences(c.Request.Context(),
		observability.TargetOf(*source), s.tunnelCall(user, cluster), queryScope(user, grant))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	type silenceView struct {
		observability.Silence
		CanExpire bool `json:"can_expire"`
	}
	out := make([]silenceView, 0, len(silences))
	for _, silence := range silences {
		out = append(out, silenceView{Silence: silence, CanExpire: canMute(user, grant, silence.Namespace)})
	}
	c.JSON(http.StatusOK, gin.H{"silences": out})
}

// SilenceDurations are the windows a silence may be opened for.
var silenceDurations = map[string]time.Duration{
	"1h": time.Hour, "4h": 4 * time.Hour, "12h": 12 * time.Hour,
	"1d": 24 * time.Hour, "3d": 72 * time.Hour, "7d": 7 * 24 * time.Hour,
}

const maxSilenceComment = 500

type silenceCreateRequest struct {
	Fingerprint string `json:"fingerprint" binding:"required"`
	Duration    string `json:"duration" binding:"required"`
	Comment     string `json:"comment"`
}

// createSilence mutes one firing alert. The matchers are the alert's own labels
// read back from Alertmanager — never sent by the browser — so a silence can be
// no wider than the alert the caller was looking at.
func (s *server) createSilence(c *gin.Context) {
	user, cluster, grant, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}
	var req silenceCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a silence needs an alert and a duration"})
		return
	}
	window, ok := silenceDurations[req.Duration]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "that is not one of the offered silence durations"})
		return
	}
	comment := strings.TrimSpace(req.Comment)
	if comment == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "say why — a silence nobody can explain is one nobody dares to end"})
		return
	}
	if len(comment) > maxSilenceComment {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("the reason is limited to %d characters", maxSilenceComment)})
		return
	}
	source, ok := s.querySource(c, cluster, db.SourceAlerts)
	if !ok {
		return
	}
	target := observability.TargetOf(*source)
	tunnel := s.tunnelCall(user, cluster)

	alert, err := observability.FindAlert(c.Request.Context(), target, tunnel, req.Fingerprint)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	// A scoped caller is told the same thing whether the alert stopped firing
	// or was never theirs to see.
	if alert == nil || !queryScope(user, grant).Allows(alert.Namespace) ||
		(alert.Namespace == "" && !queryScope(user, grant).Unscoped()) {
		c.JSON(http.StatusNotFound, gin.H{"error": "that alert is no longer firing"})
		return
	}
	if !canMute(user, grant, alert.Namespace) {
		s.recordSilence(c, user, cluster, verbSilenceCreate, alert.Namespace, "", http.StatusForbidden, "edit access required")
		c.JSON(http.StatusForbidden, gin.H{"error": "silencing an alert takes an edit grant over its namespace"})
		return
	}

	id, err := observability.CreateSilence(c.Request.Context(), target, tunnel,
		observability.SilenceMatchersFor(*alert), time.Now().Add(window),
		user.Username+" (kubemg)", comment)
	if err != nil {
		s.recordSilence(c, user, cluster, verbSilenceCreate, alert.Namespace, "", http.StatusBadGateway, err.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	s.recordSilence(c, user, cluster, verbSilenceCreate, alert.Namespace, id, http.StatusCreated, "")
	c.JSON(http.StatusCreated, gin.H{"id": id})
}

// expireSilence ends a silence early.
func (s *server) expireSilence(c *gin.Context) {
	user, cluster, grant, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}
	source, ok := s.querySource(c, cluster, db.SourceAlerts)
	if !ok {
		return
	}
	target := observability.TargetOf(*source)
	tunnel := s.tunnelCall(user, cluster)
	id := strings.TrimSpace(c.Param("sid"))

	silence, err := observability.FindSilence(c.Request.Context(), target, tunnel, id)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	scope := queryScope(user, grant)
	if silence == nil || (!scope.Unscoped() && (silence.Namespace == "" || !scope.Allows(silence.Namespace))) {
		c.JSON(http.StatusNotFound, gin.H{"error": "silence not found"})
		return
	}
	if !canMute(user, grant, silence.Namespace) {
		s.recordSilence(c, user, cluster, verbSilenceExpire, silence.Namespace, id, http.StatusForbidden, "edit access required")
		c.JSON(http.StatusForbidden, gin.H{"error": "ending a silence takes an edit grant over its namespace"})
		return
	}
	if err := observability.ExpireSilence(c.Request.Context(), target, tunnel, id); err != nil {
		s.recordSilence(c, user, cluster, verbSilenceExpire, silence.Namespace, id, http.StatusBadGateway, err.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	s.recordSilence(c, user, cluster, verbSilenceExpire, silence.Namespace, id, http.StatusOK, "")
	c.Status(http.StatusNoContent)
}

const (
	verbSilenceCreate = "silence-create"
	verbSilenceExpire = "silence-expire"
)

// recordSilence puts a silence in KubeMG's own trail. A direct-mode
// Alertmanager is dialled from here and leaves no proxied record at all, and
// even an in-cluster one records only "a POST to a Service proxy" — neither
// says who muted what.
func (s *server) recordSilence(c *gin.Context, user *db.User, cluster *db.Cluster,
	verb, namespace, id string, status int, reason string,
) {
	if s.auditor == nil {
		return
	}
	path := c.Request.URL.Path
	if id != "" && verb == verbSilenceCreate {
		path += "/" + id
	}
	s.auditor.Record(c.Request.Context(), bastion.Event{
		At:        time.Now().UTC(),
		UserID:    user.ID,
		Username:  user.Username,
		ClusterID: cluster.ID,
		Cluster:   cluster.Name,
		Verb:      verb,
		Method:    c.Request.Method,
		Path:      path,
		Namespace: namespace,
		Resource:  "silences",
		Status:    status,
		Error:     reason,
	})
}

// prometheusRuleSelectors reads the cluster's Prometheus CRs for the labels a
// rule needs to be loaded, so an administrator registering the Alertmanager
// does not have to go and read them.
func (s *server) prometheusRuleSelectors(c *gin.Context) {
	user, cluster, _, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}
	tunnel := s.tunnelCall(user, cluster)
	if tunnel == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "reading the cluster's Prometheus needs a connected agent"})
		return
	}
	status, body, err := tunnel(c.Request.Context(), http.MethodGet, prometheusRuleGroup+"/prometheuses", nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if status == http.StatusNotFound {
		c.JSON(http.StatusOK, gin.H{"available": false, "reason": noPrometheusOperator, "prometheuses": []any{}})
		return
	}
	if status != http.StatusOK {
		c.JSON(status, gin.H{"error": kubeErrorMessage(body, status)})
		return
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				RuleSelector *struct {
					MatchLabels      map[string]string `json:"matchLabels"`
					MatchExpressions []any             `json:"matchExpressions"`
				} `json:"ruleSelector"`
				RuleNamespaceSelector *struct {
					MatchLabels      map[string]string `json:"matchLabels"`
					MatchExpressions []any             `json:"matchExpressions"`
				} `json:"ruleNamespaceSelector"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "the cluster returned an unreadable Prometheus list"})
		return
	}
	type selectorView struct {
		Namespace   string            `json:"namespace"`
		Name        string            `json:"name"`
		MatchLabels map[string]string `json:"match_labels"`
		// Expressions marks a selector KubeMG cannot satisfy by adding labels.
		Expressions bool `json:"expressions"`
		// RuleNamespaces is "all", "own" (only the Prometheus's namespace) or
		// "selected" (a namespace selector KubeMG does not evaluate).
		RuleNamespaces string `json:"rule_namespaces"`
	}
	out := []selectorView{}
	for _, item := range list.Items {
		view := selectorView{Namespace: item.Metadata.Namespace, Name: item.Metadata.Name, MatchLabels: map[string]string{}}
		if sel := item.Spec.RuleSelector; sel != nil {
			if sel.MatchLabels != nil {
				view.MatchLabels = sel.MatchLabels
			}
			view.Expressions = len(sel.MatchExpressions) > 0
		}
		switch sel := item.Spec.RuleNamespaceSelector; {
		case sel == nil:
			view.RuleNamespaces = "own"
		case len(sel.MatchLabels) == 0 && len(sel.MatchExpressions) == 0:
			view.RuleNamespaces = "all"
		default:
			view.RuleNamespaces = "selected"
		}
		out = append(out, view)
	}
	c.JSON(http.StatusOK, gin.H{"available": true, "prometheuses": out})
}
