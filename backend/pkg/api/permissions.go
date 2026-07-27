package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// Permission subjects. A grant either targets one account or every member of a
// group.
const (
	subjectUser  = "user"
	subjectGroup = "group"
)

// permissionResponse is one cell of the access matrix, carrying the display
// names so the UI does not have to join three lists itself.
type permissionResponse struct {
	SubjectType string   `json:"subject_type"`
	SubjectID   uint     `json:"subject_id"`
	SubjectName string   `json:"subject_name"`
	ClusterID   uint     `json:"cluster_id"`
	ClusterName string   `json:"cluster_name"`
	K8sRole     string   `json:"k8s_role"`
	Namespaces  []string `json:"namespaces"`
}

type assignPermissionRequest struct {
	SubjectType string   `json:"subject_type" binding:"required,oneof=user group"`
	SubjectID   uint     `json:"subject_id" binding:"required"`
	ClusterID   uint     `json:"cluster_id" binding:"required"`
	K8sRole     string   `json:"k8s_role" binding:"required,oneof=cluster-admin edit view"`
	Namespaces  []string `json:"namespaces"`
}

type revokePermissionRequest struct {
	SubjectType string `json:"subject_type" binding:"required,oneof=user group"`
	SubjectID   uint   `json:"subject_id" binding:"required"`
	ClusterID   uint   `json:"cluster_id" binding:"required"`
}

// listPermissions returns the full access matrix: direct user grants and group
// grants, alongside the subjects and clusters they can be drawn against.
func (s *server) listPermissions(c *gin.Context) {
	ctx := c.Request.Context()

	users, err := s.store.ListUsers(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load users"})
		return
	}
	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load groups"})
		return
	}
	clusters, err := s.store.Clusters(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load clusters"})
		return
	}
	userGrants, err := s.store.ListUserAccess(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load user permissions"})
		return
	}
	groupGrants, err := s.store.ListGroupAccess(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load group permissions"})
		return
	}

	userNames := make(map[uint]string, len(users))
	for _, u := range users {
		userNames[u.ID] = u.Username
	}
	groupNames := make(map[uint]string, len(groups))
	for _, g := range groups {
		groupNames[g.ID] = g.Name
	}
	clusterNames := make(map[uint]string, len(clusters))
	for _, cl := range clusters {
		clusterNames[cl.ID] = cl.Name
	}

	userPermissions := make([]permissionResponse, 0, len(userGrants))
	for _, g := range userGrants {
		userPermissions = append(userPermissions, permissionResponse{
			SubjectType: subjectUser,
			SubjectID:   g.UserID,
			SubjectName: userNames[g.UserID],
			ClusterID:   g.ClusterID,
			ClusterName: clusterNames[g.ClusterID],
			K8sRole:     g.K8sRole,
			Namespaces:  g.NamespaceList(),
		})
	}

	groupPermissions := make([]permissionResponse, 0, len(groupGrants))
	for _, g := range groupGrants {
		groupPermissions = append(groupPermissions, permissionResponse{
			SubjectType: subjectGroup,
			SubjectID:   g.GroupID,
			SubjectName: groupNames[g.GroupID],
			ClusterID:   g.ClusterID,
			ClusterName: clusterNames[g.ClusterID],
			K8sRole:     g.K8sRole,
			Namespaces:  g.NamespaceList(),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"user_permissions":  userPermissions,
		"group_permissions": groupPermissions,
	})
}

// assignPermission grants a user or a group access to a cluster, replacing any
// existing grant for that pair (admin only).
func (s *server) assignPermission(c *gin.Context) {
	var req assignPermissionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	cluster, err := s.store.ClusterByID(ctx, req.ClusterID)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "cluster not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the cluster"})
		return
	}

	namespaces := db.JoinNamespaces(req.Namespaces)
	out := permissionResponse{
		SubjectType: req.SubjectType,
		SubjectID:   req.SubjectID,
		ClusterID:   cluster.ID,
		ClusterName: cluster.Name,
		K8sRole:     req.K8sRole,
		Namespaces:  db.UserClusterAccess{Namespaces: namespaces}.NamespaceList(),
	}

	if req.SubjectType == subjectUser {
		user, err := s.store.UserByID(ctx, req.SubjectID)
		if errors.Is(err, db.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the user"})
			return
		}

		grant := db.UserClusterAccess{
			UserID:     user.ID,
			ClusterID:  cluster.ID,
			K8sRole:    req.K8sRole,
			Namespaces: namespaces,
		}
		if err := s.store.AssignUserAccess(ctx, &grant); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not assign the permission"})
			return
		}
		out.SubjectName = user.Username
		c.JSON(http.StatusOK, out)
		return
	}

	group, err := s.store.GroupByID(ctx, req.SubjectID)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "group not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the group"})
		return
	}

	grant := db.GroupClusterAccess{
		GroupID:    group.ID,
		ClusterID:  cluster.ID,
		K8sRole:    req.K8sRole,
		Namespaces: namespaces,
	}
	if err := s.store.AssignGroupAccess(ctx, &grant); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not assign the permission"})
		return
	}
	out.SubjectName = group.Name
	c.JSON(http.StatusOK, out)
}

// revokePermission drops a user's or a group's grant on a cluster (admin only).
func (s *server) revokePermission(c *gin.Context) {
	var req revokePermissionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	var err error
	if req.SubjectType == subjectUser {
		err = s.store.RevokeUserAccess(ctx, req.SubjectID, req.ClusterID)
	} else {
		err = s.store.RevokeGroupAccess(ctx, req.SubjectID, req.ClusterID)
	}

	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "that permission does not exist"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not revoke the permission"})
		return
	}

	c.Status(http.StatusNoContent)
}
