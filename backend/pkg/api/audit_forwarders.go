package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/auditforward"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Configuring where the trail is shipped.
 *
 * Administrative throughout, and for a stronger reason than "it is fleet-wide
 * configuration": a forwarder sends every audit record — every username, every
 * cluster, every namespace touched — to an address somebody types into a box.
 * That is a data-egress control, which is the same bar the alarm channels are
 * held to.
 *
 * Unlike a channel there is no credential to hide. Syslog authenticates by
 * network position or by TLS, so the row reads back whole: the CA bundle is a
 * public certificate, and pretending otherwise would only mean an operator
 * cannot check which CA they pinned.
 */

// Field bounds. A name and a host both end up in a delivered frame, so they are
// bounded here rather than by the column width.
const (
	maxForwarderNameLength = 120
	maxForwarderHostLength = 253
	maxForwarderAppLength  = 48
	maxForwarderCALength   = 64 * 1024
)

type auditForwarderRequest struct {
	Name                  string `json:"name"`
	Kind                  string `json:"kind"`
	Host                  string `json:"host"`
	Port                  int    `json:"port"`
	Protocol              string `json:"protocol"`
	Facility              *int   `json:"facility"`
	AppName               string `json:"app_name"`
	OctetCounting         bool   `json:"octet_counting"`
	TLSCABundle           string `json:"tls_ca_bundle"`
	TLSInsecureSkipVerify bool   `json:"tls_insecure_skip_verify"`
	Enabled               *bool  `json:"enabled"`
}

// normalize validates a request and folds it onto a row, filling the defaults
// that have one. It returns the message a refusal is reported with.
func (r auditForwarderRequest) normalize(into *db.AuditForwarder) string {
	name := strings.TrimSpace(r.Name)
	if name == "" || len(name) > maxForwarderNameLength {
		return "name is required and must be at most 120 characters"
	}

	kind := strings.TrimSpace(r.Kind)
	if kind == "" {
		kind = db.ForwarderSyslog
	}
	if !slices.Contains(db.AuditForwarderKinds, kind) {
		return "kind must be one of: " + strings.Join(db.AuditForwarderKinds, ", ")
	}

	protocol := strings.TrimSpace(strings.ToLower(r.Protocol))
	if protocol == "" {
		protocol = db.ForwarderProtoTCP
	}
	if !slices.Contains(db.AuditForwarderProtocols, protocol) {
		return "protocol must be one of: " + strings.Join(db.AuditForwarderProtocols, ", ")
	}

	host := strings.TrimSpace(r.Host)
	if host == "" || len(host) > maxForwarderHostLength {
		return "host is required and must be at most 253 characters"
	}
	// A host with a scheme or a port in it is the mistake an operator makes
	// coming from the alarm channels, where the field is a URL. Refused by name
	// rather than dialled: "logsign.example.com:515" resolves to nothing and
	// would sit there reporting a connection error nobody can explain.
	if strings.Contains(host, "/") || strings.Contains(host, ":") || strings.Contains(host, " ") {
		return "host is a hostname or IP address on its own — the port is a separate field, and no scheme is used"
	}

	port := r.Port
	if port == 0 {
		port = db.DefaultForwarderTCPPort
		if protocol == db.ForwarderProtoUDP {
			port = db.DefaultForwarderUDPPort
		}
	}
	if port < 1 || port > 65535 {
		return "port must be between 1 and 65535"
	}

	facility := db.DefaultForwarderFacility
	if r.Facility != nil {
		facility = *r.Facility
	}
	if facility < 0 || facility > 23 {
		return "facility must be a syslog facility between 0 and 23"
	}

	appName := strings.TrimSpace(r.AppName)
	if appName == "" {
		appName = db.DefaultForwarderAppName
	}
	if len(appName) > maxForwarderAppLength {
		return "app_name must be at most 48 characters"
	}
	// RFC 5424 header fields are printable US-ASCII with no spaces. A space here
	// shifts every field after it, so the receiver reads the message as the
	// structured data and the record as garbage — refused rather than silently
	// stripped, so the operator sees the name they will actually filter on.
	if strings.ContainsFunc(appName, func(r rune) bool { return r < 33 || r > 126 }) {
		return "app_name must be printable ASCII with no spaces — it is a syslog header field"
	}

	bundle := strings.TrimSpace(r.TLSCABundle)
	if len(bundle) > maxForwarderCALength {
		return "tls_ca_bundle is too large"
	}
	if bundle != "" && protocol != db.ForwarderProtoTLS {
		return "tls_ca_bundle only applies to the tls protocol"
	}

	enabled := true
	if r.Enabled != nil {
		enabled = *r.Enabled
	}

	into.Name = name
	into.Kind = kind
	into.Host = host
	into.Port = port
	into.Protocol = protocol
	into.Facility = facility
	into.AppName = appName
	into.OctetCounting = r.OctetCounting
	into.TLSCABundle = bundle
	into.TLSInsecureSkipVerify = r.TLSInsecureSkipVerify && protocol == db.ForwarderProtoTLS
	into.Enabled = enabled
	return ""
}

func (s *server) listAuditForwarders(c *gin.Context) {
	forwarders, err := s.store.ListAuditForwarders(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not list audit forwarders"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"forwarders": forwarders,
		"kinds":      db.AuditForwarderKinds,
		"protocols":  db.AuditForwarderProtocols,
	})
}

func (s *server) createAuditForwarder(c *gin.Context) {
	var req auditForwarderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	var forwarder db.AuditForwarder
	if msg := req.normalize(&forwarder); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}
	if err := s.store.CreateAuditForwarder(c.Request.Context(), &forwarder); err != nil {
		if errors.Is(err, db.ErrConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "a forwarder with that name already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create audit forwarder"})
		return
	}
	s.forwarder.Reload()
	c.JSON(http.StatusCreated, forwarder)
}

func (s *server) updateAuditForwarder(c *gin.Context) {
	id, ok := parseIDParam(c, "id", "audit forwarder")
	if !ok {
		return
	}
	existing, err := s.store.AuditForwarderByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "audit forwarder not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load audit forwarder"})
		return
	}

	var req auditForwarderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if msg := req.normalize(existing); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}
	if err := s.store.UpdateAuditForwarder(c.Request.Context(), existing); err != nil {
		switch {
		case errors.Is(err, db.ErrConflict):
			c.JSON(http.StatusConflict, gin.H{"error": "a forwarder with that name already exists"})
		case errors.Is(err, db.ErrNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "audit forwarder not found"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not update audit forwarder"})
		}
		return
	}
	s.forwarder.Reload()
	c.JSON(http.StatusOK, existing)
}

func (s *server) deleteAuditForwarder(c *gin.Context) {
	id, ok := parseIDParam(c, "id", "audit forwarder")
	if !ok {
		return
	}
	if err := s.store.DeleteAuditForwarder(c.Request.Context(), id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "audit forwarder not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not delete audit forwarder"})
		return
	}
	s.forwarder.Reload()
	c.Status(http.StatusNoContent)
}

// testAuditForwarder delivers one synthetic record, so an operator finds out
// here rather than from the audit nobody could produce records for.
//
// It dials the *stored* configuration on its own connection rather than
// borrowing the running shipper's: the question being asked is whether this
// destination is reachable now, and a held socket opened before the last edit
// would answer a different one.
func (s *server) testAuditForwarder(c *gin.Context) {
	id, ok := parseIDParam(c, "id", "audit forwarder")
	if !ok {
		return
	}
	forwarder, err := s.store.AuditForwarderByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "audit forwarder not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not load audit forwarder"})
		return
	}

	status, message := db.ForwarderStatusOK, ""
	if err := auditforward.Probe(c.Request.Context(), *forwarder); err != nil {
		status, message = db.ForwarderStatusError, err.Error()
	}
	// The probe is a delivery, so it updates delivery health like one — a
	// successful test that left last_status saying "error" would be a console
	// contradicting itself.
	_ = s.store.RecordAuditForwarderAttempt(c.Request.Context(), forwarder.ID, status, message)

	if status == db.ForwarderStatusError {
		c.JSON(http.StatusBadGateway, gin.H{
			"status": status,
			"error":  message,
			// A UDP destination cannot fail this test for the reason that
			// matters, so say so rather than letting a green tick be read as
			// proof of delivery.
			"note": udpNote(forwarder.Protocol),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": status, "note": udpNote(forwarder.Protocol)})
}

// udpNote states the one thing a UDP test cannot tell you. A datagram is sent
// and nothing comes back, so "it worked" here means only that the address
// resolved — not that anything is listening, and not that the record arrived.
func udpNote(protocol string) string {
	if protocol != db.ForwarderProtoUDP {
		return ""
	}
	return "udp is fire-and-forget: this proves the address resolved, not that a collector received the record. Use tcp or tls if delivery has to be verifiable."
}
