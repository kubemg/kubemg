package api

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * A cluster's unit prices.
 *
 * The rule is the consoles': anyone granted the cluster reads them, because a
 * figure on the waste page is only as good as the rate behind it and the page
 * shows that rate beside it; only an admin sets them. Nothing here reaches the
 * cluster, and nothing here is a credential.
 */

// maxCostRate bounds a rate. No unit KubeMG prices costs a million of anything
// an hour; a value past it is a typo, and a typo here multiplies into every
// figure on the page.
const maxCostRate = 1_000_000

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// costRatesRequest is the rates form. CPU and memory are pointers so that a
// missing one is told apart from a zero.
type costRatesRequest struct {
	Currency         string   `json:"currency"`
	CPUCoreHour      *float64 `json:"cpu_core_hour"`
	MemoryGiBHour    *float64 `json:"memory_gib_hour"`
	StorageGiBMonth  *float64 `json:"storage_gib_month"`
	LoadBalancerHour *float64 `json:"load_balancer_hour"`
}

// normalizeCostRates checks a rates form and returns the row it describes.
func normalizeCostRates(req costRatesRequest) (db.ClusterCostRates, error) {
	currency := strings.ToUpper(strings.TrimSpace(req.Currency))
	if !currencyPattern.MatchString(currency) {
		return db.ClusterCostRates{}, errors.New(
			"currency has to be a three-letter ISO 4217 code, such as USD or EUR")
	}
	if req.CPUCoreHour == nil || req.MemoryGiBHour == nil {
		return db.ClusterCostRates{}, errors.New(
			"a CPU and a memory rate are both needed: a compute price is both, and pricing " +
				"one alone would read as the whole")
	}
	fields := []struct {
		name  string
		value *float64
	}{
		{"cpu_core_hour", req.CPUCoreHour},
		{"memory_gib_hour", req.MemoryGiBHour},
		{"storage_gib_month", req.StorageGiBMonth},
		{"load_balancer_hour", req.LoadBalancerHour},
	}
	for _, field := range fields {
		if field.value == nil {
			continue
		}
		value := *field.value
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > maxCostRate {
			return db.ClusterCostRates{}, fmt.Errorf(
				"%s has to be a number between 0 and %d", field.name, maxCostRate)
		}
	}
	return db.ClusterCostRates{
		Currency:         currency,
		CPUCoreHour:      *req.CPUCoreHour,
		MemoryGiBHour:    *req.MemoryGiBHour,
		StorageGiBMonth:  req.StorageGiBMonth,
		LoadBalancerHour: req.LoadBalancerHour,
	}, nil
}

// showClusterCostRates returns a cluster's rates, null when none are set.
func (s *server) showClusterCostRates(c *gin.Context) {
	user, cluster, _, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}
	rates, err := s.store.ClusterCostRates(c.Request.Context(), cluster.ID)
	switch {
	case errors.Is(err, db.ErrNotFound):
		rates = nil
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load the rates"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"rates": rates, "editable": user.IsAdmin()})
}

// putClusterCostRates sets a cluster's rates (admin only).
func (s *server) putClusterCostRates(c *gin.Context) {
	_, cluster, _, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}
	var req costRatesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	rates, err := normalizeCostRates(req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	rates.ClusterID = cluster.ID
	if err := s.store.PutClusterCostRates(c.Request.Context(), &rates); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the rates"})
		return
	}
	// A cached waste report carries the old figures.
	s.invalidateClusterReads(cluster.ID)
	c.JSON(http.StatusOK, gin.H{"rates": rates})
}

// deleteClusterCostRates removes a cluster's rates (admin only).
func (s *server) deleteClusterCostRates(c *gin.Context) {
	_, cluster, _, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}
	err := s.store.DeleteClusterCostRates(c.Request.Context(), cluster.ID)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "this cluster has no rates set"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not remove the rates"})
		return
	}
	s.invalidateClusterReads(cluster.ID)
	c.Status(http.StatusNoContent)
}
