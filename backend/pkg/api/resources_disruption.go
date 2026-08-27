package api

import (
	"encoding/json"
	"strconv"

	"github.com/gin-gonic/gin"
)

/*
 * PodDisruptionBudgets.
 *
 * A PDB is what a `kubectl drain` blocks on and what a rollout stalls against,
 * and until now the console could show neither the budget nor the number that
 * decides it. The interesting column is `disruptionsAllowed`: zero is the state
 * where a drain hangs forever and every other list looks healthy, because the
 * pods are running — they are simply not allowed to stop.
 *
 * `minAvailable` and `maxUnavailable` are IntOrString on the wire: `2` and
 * `"50%"` are both valid and mean different things, so they are read through a
 * decoder that keeps whichever was written rather than coerced into a number
 * that would silently turn a percentage into a count.
 */

// policyGroup is the API PodDisruptionBudget has been GA under since 1.21.
const policyGroup = "/apis/policy/v1"

// intOrString is Kubernetes' own union type, kept as the text it was written
// as. Rendering is the whole job here: `50%` is not 50.
type intOrString struct {
	text string
}

func (v *intOrString) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		v.text = text
		return nil
	}
	var number int64
	if err := json.Unmarshal(data, &number); err != nil {
		// Neither shape. An unreadable field reads as unset rather than
		// failing the whole list: one malformed budget must not cost the
		// operator every other one in the namespace.
		v.text = ""
		return nil
	}
	v.text = strconv.FormatInt(number, 10)
	return nil
}

func (v *intOrString) String() string {
	if v == nil {
		return ""
	}
	return v.text
}

type pdbObject struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		MinAvailable   *intOrString   `json:"minAvailable"`
		MaxUnavailable *intOrString   `json:"maxUnavailable"`
		Selector       *labelSelector `json:"selector"`
	} `json:"spec"`
	Status struct {
		CurrentHealthy     int32 `json:"currentHealthy"`
		DesiredHealthy     int32 `json:"desiredHealthy"`
		DisruptionsAllowed int32 `json:"disruptionsAllowed"`
		ExpectedPods       int32 `json:"expectedPods"`
	} `json:"status"`
}

// podDisruptionBudgetView is one budget as the list shows it.
type podDisruptionBudgetView struct {
	listMeta
	// Selector is empty for a budget that selects nothing, which is a real and
	// broken state: a PDB with no selector protects no pods at all.
	Selector       string `json:"selector"`
	MinAvailable   string `json:"min_available,omitempty"`
	MaxUnavailable string `json:"max_unavailable,omitempty"`

	CurrentHealthy     int32 `json:"current_healthy"`
	DesiredHealthy     int32 `json:"desired_healthy"`
	DisruptionsAllowed int32 `json:"disruptions_allowed"`
	ExpectedPods       int32 `json:"expected_pods"`
}

func (p pdbObject) view() podDisruptionBudgetView {
	view := podDisruptionBudgetView{
		listMeta:           p.Metadata.meta(),
		MinAvailable:       p.Spec.MinAvailable.String(),
		MaxUnavailable:     p.Spec.MaxUnavailable.String(),
		CurrentHealthy:     p.Status.CurrentHealthy,
		DesiredHealthy:     p.Status.DesiredHealthy,
		DisruptionsAllowed: p.Status.DisruptionsAllowed,
		ExpectedPods:       p.Status.ExpectedPods,
	}
	if p.Spec.Selector != nil {
		view.Selector = selectorText(*p.Spec.Selector)
	}
	return view
}

func (s *server) listPodDisruptionBudgets(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	out := []podDisruptionBudgetView{}
	for _, path := range scope.paths(resourceListPath{policyGroup, "poddisruptionbudgets"}) {
		var items []pdbObject
		if !fetchList(s, c, user, cluster, grant, path, &items) {
			return
		}
		for _, item := range items {
			out = append(out, item.view())
		}
	}

	sortResources(out)
	listResponse(c, gin.H{
		"poddisruptionbudgets": out,
		"namespace":            scope.Namespace,
		"all_namespaces":       scope.All,
	})
}
