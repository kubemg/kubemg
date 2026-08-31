package db

import (
	"net"
	"strconv"
	"time"
)

/*
 * Shipping the audit trail off the platform.
 *
 * The trail already goes two places: the structured log, which is complete and
 * which a collector has to come and tail, and the database table, which the
 * audit page queries and which the verb selection may narrow. A forwarder is the
 * third, and it is the only one that *pushes* the complete trail: a SIEM that
 * cannot reach into this container's stdout gets the records sent to it instead.
 *
 * It is deliberately not an alarm channel, even though both end in "post this
 * somewhere". An alarm is an alerting path — it dedups by fingerprint, holds a
 * five-minute cool-off per rule, and drops signals when its queue backs up,
 * because a page nobody can read is worse than a page not sent. Every one of
 * those behaviours loses records, which is exactly what an audit trail may not
 * do. Sending the trail down the alarm path would produce a SIEM that looks
 * complete and silently is not.
 */

// Forwarder kinds. Only syslog exists today; the column is stored rather than
// assumed so a second transport can arrive without a migration that has to
// guess what the existing rows meant.
const (
	// ForwarderSyslog frames each record as RFC 5424 with a JSON message. It is
	// what Logsign and most SIEMs document as their ingest, and the JSON body
	// means the vendor's own JSON parser reads it without a bespoke grammar.
	ForwarderSyslog = "syslog"
)

// AuditForwarderKinds enumerates the supported transports.
var AuditForwarderKinds = []string{ForwarderSyslog}

// Syslog transports.
const (
	// ForwarderProtoTCP is a plain TCP stream. This is the one to prefer on a
	// trusted network: UDP silently truncates and silently loses, and an audit
	// record that arrived as half a line is worse than one that did not arrive.
	ForwarderProtoTCP = "tcp"
	// ForwarderProtoUDP is the classic syslog datagram. Supported because a great
	// many collectors only listen on it, but a record over the datagram limit is
	// truncated by the network rather than by us.
	ForwarderProtoUDP = "udp"
	// ForwarderProtoTLS is TCP inside TLS, which is what shipping a trail across
	// anything but a datacentre link has to be: the records name people,
	// clusters and the namespaces they touched.
	ForwarderProtoTLS = "tls"
)

// AuditForwarderProtocols enumerates the supported syslog transports.
var AuditForwarderProtocols = []string{ForwarderProtoTCP, ForwarderProtoUDP, ForwarderProtoTLS}

// Defaults. The port is deliberately not one number for every protocol: Logsign
// listens for syslog on UDP 514 and TCP 515, and defaulting a TCP forwarder to
// 514 would produce a destination that resolves, connects to nothing, and
// reports itself healthy until somebody goes looking for records.
const (
	DefaultForwarderTCPPort = 515
	DefaultForwarderUDPPort = 514
	// DefaultForwarderFacility is local0. Syslog's named facilities are all
	// spoken for by daemons that predate this by decades; local0-7 are the range
	// reserved for exactly this.
	DefaultForwarderFacility = 16
	// DefaultForwarderAppName is the APP-NAME field, which is what a SIEM rule
	// filters this stream on.
	DefaultForwarderAppName = "kubemg"
)

// AuditForwarder is one destination the complete audit trail is pushed to.
//
// There is no credential field, which is the one way this row differs from an
// alarm channel: syslog authenticates by network position, or by TLS, and a
// bearer token has nowhere to go in an RFC 5424 frame. That also means the row
// is safe to read back whole — TLSCABundle is a public certificate, not a
// secret — so unlike a channel there is no has_secret dance.
type AuditForwarder struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:120;uniqueIndex;not null" json:"name"`
	// Kind decides the framing. See the forwarder constants.
	Kind string `gorm:"size:32;not null;default:'syslog'" json:"kind"`

	Host string `gorm:"size:253;not null" json:"host"`
	Port int    `gorm:"not null" json:"port"`
	// Protocol is tcp, udp or tls.
	Protocol string `gorm:"size:8;not null;default:'tcp'" json:"protocol"`

	// Facility and AppName are the two RFC 5424 fields an operator actually
	// routes on at the far end.
	Facility int    `gorm:"not null;default:16" json:"facility"`
	AppName  string `gorm:"size:48;not null;default:'kubemg'" json:"app_name"`

	// OctetCounting selects RFC 6587 framing over a stream — a length prefix
	// rather than a trailing newline. Collectors disagree about which they want
	// and the failure mode of guessing wrong is a receiver that concatenates
	// every record into one enormous line, so it is a setting rather than a
	// default nobody can override.
	OctetCounting bool `gorm:"not null;default:false" json:"octet_counting"`

	// TLSCABundle pins the collector's certificate authority, PEM encoded. Empty
	// means the system roots, which is right for a publicly-issued certificate
	// and wrong for the private CA most SIEM appliances use.
	TLSCABundle string `gorm:"type:text" json:"tls_ca_bundle,omitempty"`
	// TLSInsecureSkipVerify turns verification off. It exists because an
	// appliance with a self-signed certificate and no exportable CA is a real
	// situation, and the honest options there are this or no forwarding at all.
	TLSInsecureSkipVerify bool `gorm:"not null;default:false" json:"tls_insecure_skip_verify"`

	Enabled bool `gorm:"not null;default:true" json:"enabled"`

	// Delivery health, recorded on every flush. This is the field that matters
	// most on this table: a forwarder that stopped working is invisible by
	// construction — nothing is missing from anywhere an operator looks, records
	// simply stop arriving somewhere nobody watches.
	LastStatus    string     `gorm:"size:16" json:"last_status,omitempty"`
	LastMessage   string     `gorm:"type:text" json:"last_message,omitempty"`
	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the forwarder table name.
func (AuditForwarder) TableName() string { return "audit_forwarders" }

// Address is the dial target.
func (f AuditForwarder) Address() string {
	return net.JoinHostPort(f.Host, strconv.Itoa(f.Port))
}

// Delivery statuses recorded on a forwarder.
const (
	ForwarderStatusOK    = "ok"
	ForwarderStatusError = "error"
)
