package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

type groupResponse struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MemberIDs   []uint `json:"member_ids"`
	CreatedAt   string `json:"created_at"`
}

func toGroupResponse(g db.GroupSummary) groupResponse {
	members := g.MemberIDs
	if members == nil {
		members = []uint{}
	}
	return groupResponse{
		ID:          g.ID,
		Name:        g.Name,
		Description: g.Description,
		MemberIDs:   members,
		CreatedAt:   g.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

type createGroupRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

type groupMemberRequest struct {
	UserID uint `json:"user_id" binding:"required"`
}

// listGroups returns every local group with its membership (admin only).
func (s *server) listGroups(c *gin.Context) {
	groups, err := s.store.ListGroups(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not list groups"})
		return
	}

	out := make([]groupResponse, 0, len(groups))
	for _, g := range groups {
		out = append(out, toGroupResponse(g))
	}
	c.JSON(http.StatusOK, gin.H{"groups": out})
}

// createGroup adds a local group (admin only).
func (s *server) createGroup(c *gin.Context) {
	var req createGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	group := db.Group{Name: name, Description: strings.TrimSpace(req.Description)}
	err := s.store.CreateGroup(c.Request.Context(), &group)
	if errors.Is(err, db.ErrConflict) {
		c.JSON(http.StatusConflict, gin.H{"error": "group name already exists"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create the group"})
		return
	}

	c.JSON(http.StatusCreated, toGroupResponse(db.GroupSummary{Group: group}))
}

// deleteGroup removes a group along with its grants (admin only).
func (s *server) deleteGroup(c *gin.Context) {
	id, ok := parseIDParam(c, "id", "group")
	if !ok {
		return
	}

	err := s.store.DeleteGroup(c.Request.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "group not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not delete the group"})
		return
	}

	c.Status(http.StatusNoContent)
}

// addGroupMember puts a user into a group (admin only).
func (s *server) addGroupMember(c *gin.Context) {
	groupID, ok := parseIDParam(c, "id", "group")
	if !ok {
		return
	}

	var req groupMemberRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_id is required"})
		return
	}

	ctx := c.Request.Context()
	if _, err := s.store.GroupByID(ctx, groupID); errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "group not found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the group"})
		return
	}

	if _, err := s.store.UserByID(ctx, req.UserID); errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the user"})
		return
	}

	if err := s.store.AddGroupMember(ctx, groupID, req.UserID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not add the member"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"group_id": groupID, "user_id": req.UserID})
}

// removeGroupMember takes a user out of a group (admin only).
func (s *server) removeGroupMember(c *gin.Context) {
	groupID, ok := parseIDParam(c, "id", "group")
	if !ok {
		return
	}

	userID, err := strconv.ParseUint(c.Param("userId"), 10, 64)
	if err != nil || userID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user id"})
		return
	}

	err = s.store.RemoveGroupMember(c.Request.Context(), groupID, uint(userID))
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "that user is not a member of this group"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not remove the member"})
		return
	}

	c.Status(http.StatusNoContent)
}

// parseIDParam reads a positive integer path parameter, writing the error
// response itself when it is malformed.
func parseIDParam(c *gin.Context, param, subject string) (uint, bool) {
	id, err := strconv.ParseUint(c.Param(param), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid " + subject + " id"})
		return 0, false
	}
	return uint(id), true
}
