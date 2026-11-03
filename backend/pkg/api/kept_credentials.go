package api

import (
	"net/url"
	"strings"
)

// A form that edits a stored credential never receives it back, so leaving the
// field out means "keep it". That is safe only while the credential goes where
// it was given. Otherwise an administrator who cannot read a token could point
// the row at a host of their own, leave the field out, and have kubemg send the
// stored token there (on the save's probe, a Test, or the next delivery)
// without anyone having typed it. The mail relay held this rule first
// (smtp.go); every kept credential holds it now.

// errRetypeCredential is the refusal when a kept credential would follow a
// changed destination.
const errRetypeCredential = "enter the credential again — a stored credential is kept only for the host it was given for"

// sameOrigin reports whether two URLs name the same scheme, host and port, the
// port defaulted from the scheme. Either one unparseable is not the same.
func sameOrigin(a, b string) bool {
	ua, errA := url.Parse(strings.TrimSpace(a))
	ub, errB := url.Parse(strings.TrimSpace(b))
	if errA != nil || errB != nil || ua.Host == "" || ub.Host == "" {
		return false
	}
	return strings.EqualFold(ua.Scheme, ub.Scheme) &&
		strings.EqualFold(ua.Hostname(), ub.Hostname()) &&
		effectivePort(ua) == effectivePort(ub)
}

func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	return ""
}
