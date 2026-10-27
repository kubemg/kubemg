package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Who changed it — as far as KubeMG can know.
 *
 * An object drifts because something wrote to it after the controller last
 * synced it. KubeMG records every write that goes through it, so it can name the
 * last one; it records nothing about a write that did not, and a kubectl with
 * another credential, a controller or an admission webhook all write without
 * passing here. So the answer is "the last write through KubeMG", and an empty
 * answer is said to be exactly that — never "nobody changed it".
 *
 * The read is the audit trail's own rule: an administrator reads anyone's
 * writes, anyone else only their own. A caller who may not read the trail must
 * not learn from this route who did.
 */

// gitopsWriteVerbs are the verbs that change an object.
var gitopsWriteVerbs = []string{"create", "update", "patch", "delete"}

// gitopsChange is the last write to an object that went through KubeMG.
type gitopsChange struct {
	Username string    `json:"username"`
	Verb     string    `json:"verb"`
	At       time.Time `json:"at"`
}

// gitopsAttribution answers "who changed it" for the diff sheet.
type gitopsAttribution struct {
	// Change is nil when no write the caller may read went through KubeMG in
	// the window.
	Change *gitopsChange `json:"change"`
	// Since is the window's start: Argo CD's last finished sync. Nil when the
	// Application records none, and the window is the retained trail.
	Since *time.Time `json:"since,omitempty"`
	// ScopedToSelf says the read was narrowed to the caller's own writes.
	ScopedToSelf bool `json:"scoped_to_self"`
	// Note says what an empty or failed answer means. Empty when Change is set.
	Note string `json:"note,omitempty"`
}

const (
	attributionOutside = "A change made outside KubeMG — kubectl with another credential, a " +
		"controller, Argo CD itself — leaves no record here."
	attributionUnreadable = "The audit trail could not be read, so the last change through KubeMG is not known."
)

// auditTrailReader is the slice of the store attribution needs.
type auditTrailReader interface {
	ListAuditEvents(ctx context.Context, filter db.AuditFilter) ([]db.AuditEvent, int64, error)
}

// lastKubemgWrite reads the newest accepted write to one object since a sync.
// A non-zero userID narrows it to that account's writes.
func lastKubemgWrite(ctx context.Context, trail auditTrailReader, clusterID uint,
	namespace, resource, name string, since *time.Time, userID uint,
) (*gitopsChange, error) {
	if resource == "" || name == "" {
		return nil, nil
	}
	events, _, err := trail.ListAuditEvents(ctx, db.AuditFilter{
		ClusterID:     clusterID,
		UserID:        userID,
		Verbs:         gitopsWriteVerbs,
		Namespace:     namespace,
		Resource:      resource,
		Name:          name,
		SucceededOnly: true,
		Since:         since,
		Limit:         1,
	})
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}
	// A namespace filter of "" matches every namespace; a cluster-scoped object
	// must not be attributed a namespaced object's write of the same name.
	if namespace == "" && events[0].Namespace != "" {
		return nil, nil
	}
	return &gitopsChange{Username: events[0].Username, Verb: events[0].Verb, At: events[0].At}, nil
}

// attributeFor builds the sheet's answer for one caller.
func attributeFor(ctx context.Context, trail auditTrailReader, user *db.User, clusterID uint,
	namespace, resource, name string, since *time.Time,
) *gitopsAttribution {
	out := &gitopsAttribution{Since: since, ScopedToSelf: !user.IsAdmin()}
	userID := uint(0)
	if out.ScopedToSelf {
		userID = user.ID
	}
	change, err := lastKubemgWrite(ctx, trail, clusterID, namespace, resource, name, since, userID)
	switch {
	case err != nil:
		out.Note = attributionUnreadable
	case change != nil:
		out.Change = change
	case out.ScopedToSelf:
		out.Note = "You made no write to this object through KubeMG" + sinceClause(since) +
			". Writes by others are shown to an administrator only. " + attributionOutside
	default:
		out.Note = "No write to this object went through KubeMG" + sinceClause(since) +
			". " + attributionOutside
	}
	return out
}

func sinceClause(since *time.Time) string {
	if since == nil {
		return " in the retained audit trail"
	}
	return " since Argo CD last synced it"
}

/* ------------------------------------------------ plural, for the poller --- */

// pluralResolver turns an Argo CD entry's group, version and Kind into the
// resource name the audit trail records — asked of the cluster's discovery,
// never pluralised by hand. One discovery document per group-version, read at
// most once per pass and only when a transition needs a name.
type pluralResolver struct {
	read  trafficReader
	cache map[string]map[string]string
}

func newPluralResolver(read trafficReader) *pluralResolver {
	return &pluralResolver{read: read, cache: map[string]map[string]string{}}
}

func (r *pluralResolver) plural(group, version, kind string) string {
	if version == "" || kind == "" {
		return ""
	}
	groupVersion := version
	path := "/api/" + url.PathEscape(version)
	if group != "" {
		groupVersion = group + "/" + version
		path = "/apis/" + url.PathEscape(group) + "/" + url.PathEscape(version)
	}
	kinds, seen := r.cache[groupVersion]
	if !seen {
		kinds = map[string]string{}
		r.cache[groupVersion] = kinds
		code, body, err := r.read.get(path)
		if err != nil || code != http.StatusOK {
			return ""
		}
		var list struct {
			Resources []struct {
				Name string `json:"name"`
				Kind string `json:"kind"`
			} `json:"resources"`
		}
		if json.Unmarshal(body, &list) != nil {
			return ""
		}
		for _, resource := range list.Resources {
			// Subresources (`deployments/scale`) carry their parent's Kind too.
			if strings.Contains(resource.Name, "/") {
				continue
			}
			kinds[resource.Kind] = resource.Name
		}
	}
	return kinds[kind]
}
