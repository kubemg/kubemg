package api

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
)

/*
 * Which pods a workload owns.
 *
 * Reading one pod's log answers a question about that pod. Almost nothing an
 * operator actually asks is about one pod: a Deployment with ten replicas fails
 * on one of them, and finding out which means opening ten drawers and reading
 * ten tails. So a workload's logs are the workload's pods' logs, interleaved,
 * and this is the read that turns the first into the second.
 *
 * It is deliberately a *resolver*, not a log route. The log itself is still the
 * existing per-pod read — bounded through `podLogs`, followed through the proxy's
 * stream path — once per pod, which means there is no new streaming shape, no new
 * multiplexing and no new audit verb: ten followed pods are ten `log` records in
 * the trail, exactly as if someone had opened ten of them by hand, which is what
 * they asked for.
 *
 * The selector is read off the workload rather than taken from the caller. A
 * caller-supplied `labelSelector` would make this a general pod query — a
 * namespace-scoped grant could still not reach past its namespaces, but it could
 * enumerate that namespace by label, which is not what any part of KubeMG offers.
 * Naming a workload is naming an object; the label matcher is derived from it.
 */

// maxWorkloadLogPods bounds what one resolve answers with. A DaemonSet on a
// three-hundred-node cluster has three hundred pods, and a browser asked to hold
// three hundred followed streams open is a browser that stops responding. The
// response says it was cut short so the UI can say so too.
const maxWorkloadLogPods = 50

// workloadPodKinds are the kinds that own pods through a selector, keyed by the
// same sidebar key every other resource call uses. It is its own table rather
// than a flag on objectKinds because most kinds in that table own nothing: a
// ConfigMap has no pods, and a Service's selector points at pods it does not own.
//
// ReplicaSets and Jobs are here even though neither is a first-class sidebar
// entry: a Job's pods are the only place its failure is written down, and both
// carry a real `spec.selector`.
var workloadPodKinds = map[string]resourceListPath{
	"deployments":  {"/apis/apps/v1", "deployments"},
	"statefulsets": {"/apis/apps/v1", "statefulsets"},
	"daemonsets":   {"/apis/apps/v1", "daemonsets"},
	"replicasets":  {"/apis/apps/v1", "replicasets"},
	"jobs":         {"/apis/batch/v1", "jobs"},
}

// labelSelector is the `metav1.LabelSelector` shape, decoded by hand for the same
// reason every other read here is: pulling in the full typed object to read two
// fields would mean decoding a whole PodSpec.
type labelSelector struct {
	MatchLabels      map[string]string `json:"matchLabels"`
	MatchExpressions []struct {
		Key      string   `json:"key"`
		Operator string   `json:"operator"`
		Values   []string `json:"values"`
	} `json:"matchExpressions"`
}

// workloadPodsResult is what the resolve answers with: the pods, plus the union
// of their container names so the UI can offer a container picker without
// walking the list itself, and the selector it used so the read is explainable.
type workloadPodsResult struct {
	Pods      []podView `json:"pods"`
	Namespace string    `json:"namespace"`
	Kind      string    `json:"kind"`
	Selector  string    `json:"selector"`
	// Containers is every container name that appears in any of the pods. For a
	// workload that is the pod template's containers; during a rollout across a
	// template change it is briefly the union of two templates, which is the
	// honest answer.
	Containers []string `json:"containers"`
	// Truncated reports that the workload has more pods than this answers with.
	Truncated bool `json:"truncated"`
}

// listWorkloadPods resolves a workload to the pods it owns, so their logs can be
// read together.
func (s *server) listWorkloadPods(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}

	key := strings.TrimSpace(c.Query("kind"))
	path, known := workloadPodKinds[key]
	if !known {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kubemg does not resolve pods for " + key})
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

	// The workload is read as an object, through the same walk the manifest
	// editor and the describe use — so a caller who may not read the workload is
	// refused here by the cluster's own RBAC before any pod is listed.
	kind := objectKind{versions: []resourceListPath{path}, namespaced: true}
	body, ok := s.readObject(c, user, cluster, grant, kind, namespace, name)
	if !ok {
		return
	}

	var object struct {
		Kind string `json:"kind"`
		Spec struct {
			Selector *labelSelector `json:"selector"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(body, &object); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "the cluster returned an unreadable response"})
		return
	}
	if object.Spec.Selector == nil {
		c.JSON(http.StatusConflict, gin.H{
			"error": "this workload declares no pod selector, so kubemg cannot tell which pods are its own",
		})
		return
	}

	selector, err := encodeLabelSelector(*object.Spec.Selector)
	if err != nil {
		// An empty or unrepresentable selector must never become a list of every
		// pod in the namespace: those are not this workload's logs.
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}

	query := url.Values{}
	query.Set("labelSelector", selector)
	listPath := fmt.Sprintf("%s?%s",
		resourceListPath{"/api/v1", "pods"}.namespaced(namespace), query.Encode())

	var list struct {
		Items []podObject `json:"items"`
	}
	if !s.fetch(c, user, cluster, grant, listPath, &list) {
		return
	}

	result := workloadPodsResult{
		Pods:       []podView{},
		Namespace:  namespace,
		Kind:       object.Kind,
		Selector:   selector,
		Containers: []string{},
	}
	for _, item := range list.Items {
		result.Pods = append(result.Pods, item.view())
	}
	sortResources(result.Pods)
	if len(result.Pods) > maxWorkloadLogPods {
		result.Pods = result.Pods[:maxWorkloadLogPods]
		result.Truncated = true
	}

	for _, pod := range result.Pods {
		for _, container := range pod.Containers {
			if !slices.Contains(result.Containers, container.Name) {
				result.Containers = append(result.Containers, container.Name)
			}
		}
	}
	slices.Sort(result.Containers)

	c.JSON(http.StatusOK, result)
}

// encodeLabelSelector renders a LabelSelector as the string form the API server's
// `labelSelector` parameter takes. It refuses rather than degrades: a selector
// this cannot represent has to come back as an error, because the alternative —
// dropping the term it could not encode — silently widens the match, and a wider
// match here means logs from pods that are not this workload's.
func encodeLabelSelector(selector labelSelector) (string, error) {
	terms := make([]string, 0, len(selector.MatchLabels)+len(selector.MatchExpressions))

	// Sorted so the same selector renders the same way every time: it travels in
	// the audit trail, where two spellings of one query read as two queries.
	for _, key := range slices.Sorted(maps.Keys(selector.MatchLabels)) {
		if err := checkLabelToken(key); err != nil {
			return "", err
		}
		if err := checkLabelToken(selector.MatchLabels[key]); err != nil {
			return "", err
		}
		terms = append(terms, key+"="+selector.MatchLabels[key])
	}

	for _, expression := range selector.MatchExpressions {
		if err := checkLabelToken(expression.Key); err != nil {
			return "", err
		}
		for _, value := range expression.Values {
			if err := checkLabelToken(value); err != nil {
				return "", err
			}
		}

		switch expression.Operator {
		case "In", "NotIn":
			if len(expression.Values) == 0 {
				return "", fmt.Errorf("this workload's pod selector is not one kubemg can read")
			}
			operator := "in"
			if expression.Operator == "NotIn" {
				operator = "notin"
			}
			terms = append(terms, fmt.Sprintf("%s %s (%s)",
				expression.Key, operator, strings.Join(expression.Values, ",")))
		case "Exists":
			terms = append(terms, expression.Key)
		case "DoesNotExist":
			terms = append(terms, "!"+expression.Key)
		default:
			return "", fmt.Errorf("this workload's pod selector uses %q, which kubemg cannot read",
				expression.Operator)
		}
	}

	if len(terms) == 0 {
		return "", fmt.Errorf("this workload's pod selector matches everything in the namespace, so kubemg will not read it as the workload's own pods")
	}
	return strings.Join(terms, ","), nil
}

// labelSelectorSyntax are the characters that mean something in a selector. A
// label key or value containing one is not valid Kubernetes to begin with — the
// API server would have refused the object — so this is a check against a
// hand-written or migrated object carrying something the parser would read as
// syntax rather than as a name.
const labelSelectorSyntax = ",()=!<> \t\n"

func checkLabelToken(token string) error {
	if strings.ContainsAny(token, labelSelectorSyntax) {
		return fmt.Errorf("this workload's pod selector contains a label kubemg cannot read")
	}
	return nil
}
