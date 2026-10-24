package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * One object's desired state, from Argo CD.
 *
 * The cluster holds the live object and the Application's record of whether it
 * matches; only Argo CD holds what it should be. Its managed-resources call
 * answers that per object, already normalized the way its own diff view shows it
 * — ignoreDifferences applied, defaults filled — which is why KubeMG asks Argo
 * CD instead of rendering the source itself.
 */

const (
	// maxArgoResourceBody bounds one managed-resources answer. Each item
	// carries the object up to four times (target, live, normalized,
	// predicted), so a large ConfigMap fills it fast; the tunnel's own ceiling
	// is the same 8 MiB.
	maxArgoResourceBody = 8 << 20
	argoTimeout         = 20 * time.Second
)

// ArgoObject names one object inside one Application.
type ArgoObject struct {
	App          string
	AppNamespace string
	Group        string
	Kind         string
	Namespace    string
	Name         string
}

// ArgoState is the pair Argo CD's own diff view compares: the live object as it
// normalizes it, and the live object as it would be after a sync. Both are JSON
// documents; either is empty when Argo CD has nothing for that side.
type ArgoState struct {
	NormalizedLive string
	PredictedLive  string
	Modified       bool
}

// ArgoError is a refusal from Argo CD itself, kept apart from a transport
// failure so the caller can say which of the two happened.
type ArgoError struct {
	Status  int
	Message string
}

func (e *ArgoError) Error() string { return e.Message }

// ErrArgoNoResource is returned when Argo CD answered but does not list the
// object under that Application.
var ErrArgoNoResource = fmt.Errorf("Argo CD does not list this object under its application")

// ArgoManagedResource reads one object's state from Argo CD.
func ArgoManagedResource(ctx context.Context, target Target, tunnel TunnelCall, object ArgoObject) (ArgoState, error) {
	if target.Provider != db.ProviderArgoCD {
		return ArgoState{}, fmt.Errorf("%s does not hold desired state", providerLabel(target.Provider))
	}

	query := url.Values{}
	query.Set("appNamespace", object.AppNamespace)
	query.Set("group", object.Group)
	query.Set("kind", object.Kind)
	query.Set("namespace", object.Namespace)
	query.Set("name", object.Name)
	path := "/api/v1/applications/" + url.PathEscape(object.App) + "/managed-resources?" + query.Encode()

	status, body, err := callLimited(ctx, target, path, tunnel, maxArgoResourceBody, argoTimeout)
	if err != nil {
		return ArgoState{}, err
	}
	switch {
	case status == http.StatusUnauthorized:
		return ArgoState{}, &ArgoError{Status: status,
			Message: "Argo CD no longer accepts the stored token; an administrator needs to replace it"}
	case status == http.StatusForbidden || status == http.StatusNotFound:
		// Argo CD answers both for an application the account may not see, so
		// neither is read as "it does not exist".
		return ArgoState{}, &ArgoError{Status: status,
			Message: "Argo CD did not show this application to KubeMG's account (HTTP " +
				fmt.Sprint(status) + "); check the account's RBAC policy"}
	case status < 200 || status >= 300:
		return ArgoState{}, &ArgoError{Status: status,
			Message: fmt.Sprintf("Argo CD answered HTTP %d", status)}
	}

	var answer struct {
		Items []struct {
			Group               string `json:"group"`
			Kind                string `json:"kind"`
			Namespace           string `json:"namespace"`
			Name                string `json:"name"`
			NormalizedLiveState string `json:"normalizedLiveState"`
			PredictedLiveState  string `json:"predictedLiveState"`
			Modified            bool   `json:"modified"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return ArgoState{}, fmt.Errorf("Argo CD answered with something unreadable")
	}
	// Argo CD filters on the query already; matching again keeps a server that
	// ignores a parameter from handing back another object's state.
	for _, item := range answer.Items {
		if item.Group == object.Group && item.Kind == object.Kind &&
			item.Namespace == object.Namespace && item.Name == object.Name {
			return ArgoState{
				NormalizedLive: item.NormalizedLiveState,
				PredictedLive:  item.PredictedLiveState,
				Modified:       item.Modified,
			}, nil
		}
	}
	return ArgoState{}, ErrArgoNoResource
}
