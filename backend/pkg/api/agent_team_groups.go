package api

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Team groups and the agent's allow-list.
 *
 * A KubeMG group with a grant on an agent cluster is asserted to it as
 * `kubemg:g:<name>`, so an operator can bind a Role to the team once and manage
 * who is in it here. The agent may only impersonate groups its ClusterRole names
 * — resourceNames has no prefix match — so the package rendered for a cluster
 * lists its team groups by name, and a group's *first* grant on a cluster means
 * the package has to be re-applied there.
 *
 * Until it is, asserting the new group would not narrow anything: the API server
 * refuses the whole impersonation, and every member of the team would lose the
 * cluster, base role included. So a team group is asserted only once an
 * administrator confirms the re-apply, here. Until then it waits — on the
 * install sheet, on the identity page and on every audit row it was left off.
 *
 * It is a confirmation and not a check on purpose. The agent holds no right to
 * read its own ClusterRole, and giving it one to answer this question would be
 * widening the one component whose narrowness the whole model leans on.
 */

type confirmAgentTeamGroupsRequest struct {
	// TeamGroups are the names the applied package carries: the list the
	// install sheet showed when it was rendered.
	TeamGroups []string `json:"team_groups"`
}

type agentTeamGroupsResponse struct {
	TeamGroups          []string `json:"team_groups"`
	ConfirmedTeamGroups []string `json:"confirmed_team_groups"`
	WaitingTeamGroups   []string `json:"waiting_team_groups"`
}

// confirmAgentTeamGroups records that the agent package naming these team
// groups is applied on the cluster (admin only).
//
// What is stored is the confirmed list intersected with the groups granted the
// cluster *now*. A name the request carries that holds no grant here any more is
// dropped rather than refused — the package allowing it does no harm, and
// nothing will assert it. A granted group the request does not carry stays
// waiting: the package that was applied did not name it, so the agent cannot
// assert it yet.
func (s *server) confirmAgentTeamGroups(c *gin.Context) {
	cluster, ok := s.agentCluster(c)
	if !ok {
		return
	}
	actor, ok := s.currentUser(c)
	if !ok {
		return
	}

	var req confirmAgentTeamGroupsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "team_groups is required"})
		return
	}
	for _, name := range req.TeamGroups {
		if !db.ValidTeamGroupName(strings.TrimSpace(name)) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "the team group " + name + " is not a valid group name"})
			return
		}
	}

	ctx := c.Request.Context()
	granted, err := s.store.TeamGroupsForCluster(ctx, cluster.ID)
	if err != nil {
		s.log().Error("could not read a cluster's team groups",
			slog.Uint64("cluster_id", uint64(cluster.ID)),
			slog.String("error", err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the groups granted this cluster"})
		return
	}

	confirmed := []string{}
	for _, name := range granted {
		if slices.ContainsFunc(req.TeamGroups, func(sent string) bool { return strings.TrimSpace(sent) == name }) {
			confirmed = append(confirmed, name)
		}
	}

	if err := s.store.ConfirmAgentTeamGroups(ctx, cluster.ID, confirmed); err != nil {
		s.log().Error("could not store a cluster's confirmed team groups",
			slog.Uint64("cluster_id", uint64(cluster.ID)),
			slog.String("error", err.Error()))
		s.recordAgentTeamGroups(c, actor, cluster, confirmed, http.StatusInternalServerError,
			"the confirmation could not be stored; nothing changed")
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "could not store the confirmation; the team groups asserted on this cluster are unchanged",
		})
		return
	}
	s.recordAgentTeamGroups(c, actor, cluster, confirmed, http.StatusOK, "")

	cluster.AgentTeamGroups = db.JoinNamespaces(confirmed)
	confirmedNow, waiting := splitTeamGroups(cluster, granted)
	c.JSON(http.StatusOK, agentTeamGroupsResponse{
		TeamGroups:          nonNil(granted),
		ConfirmedTeamGroups: confirmedNow,
		WaitingTeamGroups:   waiting,
	})
}

// recordAgentTeamGroups writes the confirmation to the audit trail. The groups
// confirmed are the object of the action, so they ride on Name, shown as the
// Kubernetes groups the cluster will now be told about.
func (s *server) recordAgentTeamGroups(c *gin.Context, actor *db.User, cluster *db.Cluster, confirmed []string, status int, reason string) {
	if s.auditor == nil {
		return
	}
	groups := make([]string, 0, len(confirmed))
	for _, name := range confirmed {
		groups = append(groups, bastion.TeamGroupPrefix+name)
	}
	s.auditor.Record(c.Request.Context(), bastion.Event{
		At:        time.Now().UTC(),
		UserID:    actor.ID,
		Username:  actor.Username,
		ClusterID: cluster.ID,
		Cluster:   cluster.Name,
		Verb:      bastion.VerbAgentTeamGroups,
		Method:    c.Request.Method,
		Path:      c.Request.URL.Path,
		Resource:  "agent",
		Name:      truncate(strings.Join(groups, ","), 250),
		Status:    status,
		Error:     reason,
	})
}
