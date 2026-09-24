package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * The agent's credential, kept apart from the URL that installs it.
 *
 * An install URL used to carry the cluster's tunnel credential in its path, and
 * the endpoint behind it is unauthenticated because kubectl cannot carry a
 * session. So a URL that landed in shell history, a CI log or a chat message
 * was a working agent credential for the life of the install — enough to stand
 * up a fake agent, displace the real one and receive that cluster's traffic.
 *
 * Three things close that, and this file is two of them:
 *
 *   - The URL carries a download ticket, not the credential. It is minted each
 *     time an administrator opens the install package, redeemed by the first
 *     fetch (either form — the manifest or the Kustomize archive), and dead
 *     after installTicketTTL unused. The package it returns still carries the
 *     tunnel credential; that is unavoidable, since the agent has to present
 *     something. A URL from before this change is refused by its shape.
 *   - The credential can be rotated: a new token, the attached tunnel closed,
 *     every outstanding ticket withdrawn. No grace window — the point is that
 *     the old token stops working, and the console says so before the click.
 *   - A displacement is recorded (pkg/bastion), so a takeover is not silent.
 */

// installTicketTTL is how long a minted install URL may wait to be fetched.
//
// A WebSocket ticket lives twenty seconds because the browser that mints it
// redeems it on the next line of code. This one is read by a person, who then
// reviews the manifest, switches to a terminal, finds the right kube context and
// pastes — minutes, not seconds. Fifteen leaves room for that without leaving a
// URL alive for the rest of the working day; and it only bounds a URL nobody
// used, since the first fetch spends it regardless. Opening the install package
// again mints another.
const installTicketTTL = 15 * time.Minute

// legacyInstallURLMessage answers an install URL that carries the tunnel
// credential itself — the form every URL had before download tickets.
const legacyInstallURLMessage = "install URLs no longer carry the agent's registration token and this one " +
	"has stopped working; open Agent install on the cluster's dashboard in the KubeMG console for a fresh, " +
	"single-use URL"

// spentInstallURLMessage answers a ticket that was used, expired, withdrawn by
// a rotation, or never existed — deliberately one answer for all four.
const spentInstallURLMessage = "this install URL has already been used or has expired; open Agent install " +
	"on the cluster's dashboard in the KubeMG console for a fresh one"

// mintInstallTicket files a download ticket for a cluster and returns it with
// its expiry.
func (s *server) mintInstallTicket(ctx context.Context, clusterID uint) (string, time.Time, error) {
	ticket, hash, err := bastion.NewInstallTicket()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().UTC().Add(installTicketTTL)
	if err := s.store.PutAgentInstallTicket(ctx, hash, clusterID, expiresAt); err != nil {
		return "", time.Time{}, err
	}
	return ticket, expiresAt, nil
}

// agentTokenRotation is the answer to a rotation: the fresh install package the
// agent now needs, and whether a tunnel was cut off here.
type agentTokenRotation struct {
	Install agentInstallResponse `json:"install"`
	// Disconnected reports whether this replica held the agent's tunnel and
	// closed it. False does not mean the agent is still up on the old token: a
	// tunnel held by another replica is closed by that replica's credential
	// sweep within half a minute, and every handshake refuses the old token.
	Disconnected bool `json:"disconnected"`
}

// rotateAgentToken replaces a cluster's tunnel credential (admin only).
//
// The new token is written and every outstanding install ticket withdrawn in
// one transaction; the attached tunnel is then closed, because a tunnel that is
// already up never handshakes again and would otherwise keep the old
// credential working until it happened to reconnect. The agent stays down until
// the returned package is applied.
func (s *server) rotateAgentToken(c *gin.Context) {
	cluster, ok := s.agentCluster(c)
	if !ok {
		return
	}
	actor, ok := s.currentUser(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	token, err := bastion.NewAgentToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not mint a registration token"})
		return
	}
	if err := s.store.RotateClusterAgentToken(ctx, cluster.ID, token); err != nil {
		s.log().Error("could not rotate an agent registration token",
			slog.Uint64("cluster_id", uint64(cluster.ID)),
			slog.String("error", err.Error()))
		s.recordAgentTokenRotate(c, actor, cluster, http.StatusInternalServerError,
			"the new registration token could not be stored; the old one is still valid")
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "could not store the new registration token; the old one is still valid",
		})
		return
	}
	cluster.AgentToken = token

	disconnected := false
	if s.agents != nil {
		disconnected = s.agents.Retire(cluster.ID)
	}
	s.recordAgentTokenRotate(c, actor, cluster, http.StatusOK, "")

	install, err := s.agentInstallEnvelope(ctx, cluster)
	if err != nil {
		// The rotation happened; only the package failed to render. Say both,
		// rather than a bare error that reads as "nothing changed".
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "the registration token was rotated but the install package could not be rendered: " +
				err.Error() + " — open Agent install to try again",
		})
		return
	}
	c.JSON(http.StatusOK, agentTokenRotation{Install: install, Disconnected: disconnected})
}

// recordAgentTokenRotate writes the audit record for a rotation. A rotation
// that could not be stored is recorded too, with why — the trail never reports
// a rotation that did not happen, and never omits one that was attempted.
func (s *server) recordAgentTokenRotate(c *gin.Context, actor *db.User, cluster *db.Cluster, status int, reason string) {
	if s.auditor == nil {
		return
	}
	s.auditor.Record(c.Request.Context(), bastion.Event{
		At:        time.Now().UTC(),
		UserID:    actor.ID,
		Username:  actor.Username,
		ClusterID: cluster.ID,
		Cluster:   cluster.Name,
		Verb:      bastion.VerbAgentTokenRotate,
		Method:    c.Request.Method,
		Path:      c.Request.URL.Path,
		Resource:  "agent",
		Status:    status,
		Error:     reason,
	})
}
