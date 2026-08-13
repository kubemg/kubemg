package api

import (
	"math"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * The rates, and where they come from.
 *
 * They come from the operator. KubeMG calls no billing API and holds no cloud
 * credential — see db/rate_cards.go for why that is a decision rather than an
 * omission — so this surface is a form, a set of starting points to fill it
 * with, and a resolution rule.
 *
 * The starting points below are the part worth being careful about, because a
 * number KubeMG puts on screen is one an operator will reasonably assume KubeMG
 * stands behind. These are **indicative on-demand list prices for one
 * general-purpose instance family in one region**, decomposed into a CPU share
 * and a memory share. Every one of them is wrong for somebody: they reflect no
 * committed-use discount, no reserved instance, no spot capacity, no enterprise
 * agreement and no egress, and cloud list prices move. So a preset is offered as
 * something to **replace**, never as something to accept — it arrives with its
 * reference family and region written into the note that travels with it, and
 * the console repeats that wherever the money is shown.
 *
 * The decomposition needs saying too. A cloud sells an instance, not a vCPU and
 * a GiB; splitting one price into two is a modelling choice, and the one used
 * here is the ratio the same providers use in the products where they *do* price
 * the two separately. It is close enough to attribute a node's cost across the
 * pods on it, which is all it is asked to do, and it is not a quote.
 */

// hoursPerMonth is the month this report costs in. It is 730 — a mean calendar
// month — rather than 720 or 744, because a figure labelled "per month" that
// silently means "per 30 days" is out by two days in July and nobody notices.
const hoursPerMonth = 730

// bytesPerGiB converts the byte counts every other read normalises to into the
// unit a price list quotes.
const bytesPerGiB = float64(1 << 30)

// millicoresPerCore converts the millicore counts every other read normalises
// to into the unit a price list quotes.
const millicoresPerCore = 1000.0

// ratePreset is one starting point for a rate card.
type ratePreset struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
	Currency string `json:"currency"`

	CPUCoreHour       float64 `json:"cpu_core_hour"`
	MemoryGiBHour     float64 `json:"memory_gib_hour"`
	StorageGiBMonth   float64 `json:"storage_gib_month"`
	LoadBalancerMonth float64 `json:"load_balancer_month"`

	// Note is what these numbers actually are. It is stored with the card when
	// a preset is applied, so the provenance survives the operator who applied
	// it — which is the whole reason a preset carries prose at all.
	Note string `json:"note"`
}

/*
 * The presets. Each is one general-purpose family's on-demand hourly price split
 * into a CPU share and a memory share, plus that cloud's ordinary block-storage
 * and load-balancer charges.
 *
 * They are deliberately a *short* list. A rate catalogue with every family in
 * every region would be a price list KubeMG has to keep current, and a price
 * list that is quietly a year old is worse than a blank form: a blank form is
 * obviously unanswered.
 */
var ratePresets = []ratePreset{
	{
		Provider: db.RateProviderAWS, Label: "AWS — m7i, us-east-1", Currency: "USD",
		CPUCoreHour: 0.0353, MemoryGiBHour: 0.0038,
		StorageGiBMonth: 0.08, LoadBalancerMonth: 16.43,
		Note: "Indicative on-demand list price for m7i in us-east-1, split into a CPU and a " +
			"memory share, with gp3 storage and a Network Load Balancer's standing charge. " +
			"It reflects no Savings Plan, Reserved Instance, Spot capacity, enterprise " +
			"agreement or data transfer. Replace it with your own rates.",
	},
	{
		Provider: db.RateProviderGCP, Label: "Google Cloud — n2-standard, us-central1", Currency: "USD",
		CPUCoreHour: 0.0316, MemoryGiBHour: 0.0042,
		StorageGiBMonth: 0.10, LoadBalancerMonth: 18.00,
		Note: "Indicative on-demand list price for the n2 machine family in us-central1, which " +
			"Google publishes per vCPU and per GiB, with balanced persistent disk and a " +
			"forwarding rule's standing charge. It reflects no committed use discount, " +
			"sustained use discount, Spot capacity or egress. Replace it with your own rates.",
	},
	{
		Provider: db.RateProviderAzure, Label: "Azure — Dasv5, East US", Currency: "USD",
		CPUCoreHour: 0.0336, MemoryGiBHour: 0.0036,
		StorageGiBMonth: 0.096, LoadBalancerMonth: 18.25,
		Note: "Indicative pay-as-you-go list price for the Dasv5 series in East US, split into " +
			"a CPU and a memory share, with Standard SSD storage and a Standard Load " +
			"Balancer's standing charge. It reflects no Reserved Instance, savings plan, " +
			"Spot capacity or bandwidth. Replace it with your own rates.",
	},
	{
		Provider: db.RateProviderCustom, Label: "Your own rates", Currency: "USD",
		Note: "Rates you enter yourself — a private cloud's internal chargeback, a colocation " +
			"bill divided by its capacity, or a cloud price you have actually negotiated. " +
			"This is the one preset that cannot be out of date.",
	},
}

// rateCardView is a stored card as the console reads it, plus where it came
// from. Inherited is the field the settings screen turns on: "this cluster is
// priced at the installation default" and "this cluster has a card that happens
// to match it" look identical without it, and only one of them changes when the
// default does.
type rateCardView struct {
	Provider string `json:"provider"`
	Currency string `json:"currency"`

	CPUCoreHour       float64 `json:"cpu_core_hour"`
	MemoryGiBHour     float64 `json:"memory_gib_hour"`
	StorageGiBMonth   float64 `json:"storage_gib_month"`
	LoadBalancerMonth float64 `json:"load_balancer_month"`

	Note string `json:"note,omitempty"`

	// Inherited is true when these rates are the installation default rather
	// than this cluster's own.
	Inherited bool `json:"inherited"`
}

func viewOfRateCard(card *db.RateCard, clusterID uint) *rateCardView {
	if card == nil {
		return nil
	}
	return &rateCardView{
		Provider:          card.Provider,
		Currency:          card.Currency,
		CPUCoreHour:       card.CPUCoreHour,
		MemoryGiBHour:     card.MemoryGiBHour,
		StorageGiBMonth:   card.StorageGiBMonth,
		LoadBalancerMonth: card.LoadBalancerMonth,
		Note:              card.Note,
		Inherited:         card.ClusterID != clusterID,
	}
}

// rateCardRequest is a submitted card. Every rate is required rather than
// optional-with-a-default: a form that silently keeps a rate the operator
// thought they had cleared is one that reports a number nobody chose.
type rateCardRequest struct {
	Provider string `json:"provider"`
	Currency string `json:"currency"`

	CPUCoreHour       float64 `json:"cpu_core_hour"`
	MemoryGiBHour     float64 `json:"memory_gib_hour"`
	StorageGiBMonth   float64 `json:"storage_gib_month"`
	LoadBalancerMonth float64 `json:"load_balancer_month"`

	Note string `json:"note"`
}

// maxUnitRate is an upper bound on any one rate, and it exists to catch the
// unit error rather than to police pricing. Nobody pays five thousand dollars
// for a core-hour; somebody does type a monthly figure into an hourly field,
// and the resulting report is absurd in a way that is much harder to diagnose
// from the far end than a refusal here.
const maxUnitRate = 5000

// toRateCard validates a submitted card into a storable one.
func (r rateCardRequest) toRateCard(clusterID uint) (*db.RateCard, error) {
	provider := strings.ToLower(strings.TrimSpace(r.Provider))
	if provider == "" {
		provider = db.RateProviderCustom
	}
	if !db.ValidRateProvider(provider) {
		return nil, errBadRequest("provider has to be one of " + strings.Join(db.RateProviders, ", "))
	}

	currency := strings.ToUpper(strings.TrimSpace(r.Currency))
	if len(currency) != 3 || !isAlpha(currency) {
		return nil, errBadRequest("currency has to be a three-letter ISO 4217 code, such as USD or EUR")
	}

	rates := map[string]float64{
		"CPU":           r.CPUCoreHour,
		"memory":        r.MemoryGiBHour,
		"storage":       r.StorageGiBMonth,
		"load balancer": r.LoadBalancerMonth,
	}
	for label, value := range rates {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return nil, errBadRequest("the " + label + " rate has to be a number and cannot be negative")
		}
		if value > maxUnitRate {
			return nil, errBadRequest("the " + label + " rate looks like a unit error — " +
				"CPU and memory are priced per hour, storage and load balancers per month")
		}
	}

	note := strings.TrimSpace(r.Note)
	if len(note) > 500 {
		note = note[:500]
	}

	return &db.RateCard{
		ClusterID:         clusterID,
		Provider:          provider,
		Currency:          currency,
		CPUCoreHour:       r.CPUCoreHour,
		MemoryGiBHour:     r.MemoryGiBHour,
		StorageGiBMonth:   r.StorageGiBMonth,
		LoadBalancerMonth: r.LoadBalancerMonth,
		Note:              note,
	}, nil
}

func isAlpha(value string) bool {
	for _, r := range value {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// badRequest is a validation failure carrying the sentence the caller reads.
type badRequest struct{ message string }

func (e badRequest) Error() string { return e.message }

func errBadRequest(message string) error { return badRequest{message} }

/* ------------------------------------------------------------- the routes -- */

// getDefaultRateCard reports the installation-wide rates, and the presets an
// operator can start from.
func (s *server) getDefaultRateCard(c *gin.Context) {
	card, err := s.store.RateCard(c.Request.Context(), db.RateCardScopeDefault)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the rate card"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"rate_card": viewOfRateCard(card, db.RateCardScopeDefault),
		"presets":   ratePresets,
	})
}

// putDefaultRateCard stores the installation-wide rates.
func (s *server) putDefaultRateCard(c *gin.Context) {
	s.putRateCardAt(c, db.RateCardScopeDefault)
}

// deleteDefaultRateCard clears the installation-wide rates, which leaves every
// cluster that was inheriting them unpriced. That is a legitimate state and the
// reports say so rather than falling back to a number nobody entered.
func (s *server) deleteDefaultRateCard(c *gin.Context) {
	s.deleteRateCardAt(c, db.RateCardScopeDefault)
}

// getClusterRateCard reports the rates one cluster is priced at, whether its
// own or inherited.
func (s *server) getClusterRateCard(c *gin.Context) {
	_, cluster, _, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}
	card, err := s.store.RateCardFor(c.Request.Context(), cluster.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the rate card"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"rate_card": viewOfRateCard(card, cluster.ID),
		"presets":   ratePresets,
	})
}

// putClusterRateCard overrides the installation default for one cluster —
// the case a fleet that is not all one cloud needs, and the one where pricing
// an on-prem cluster at a cloud list price would be worse than pricing it at
// nothing.
func (s *server) putClusterRateCard(c *gin.Context) {
	_, cluster, _, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}
	s.putRateCardAt(c, cluster.ID)
}

// deleteClusterRateCard drops one cluster's override, returning it to the
// installation default.
func (s *server) deleteClusterRateCard(c *gin.Context) {
	_, cluster, _, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return
	}
	s.deleteRateCardAt(c, cluster.ID)
}

func (s *server) putRateCardAt(c *gin.Context, clusterID uint) {
	var req rateCardRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	card, err := req.toRateCard(clusterID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.store.PutRateCard(c.Request.Context(), card); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save the rate card"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"rate_card": viewOfRateCard(card, clusterID)})
}

func (s *server) deleteRateCardAt(c *gin.Context, clusterID uint) {
	err := s.store.DeleteRateCard(c.Request.Context(), clusterID)
	if err != nil && err != db.ErrNotFound {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not clear the rate card"})
		return
	}
	// A delete of something already absent is the state the caller asked for.
	c.Status(http.StatusNoContent)
}
