package bastion

import "context"

/*
 * Where a call came from.
 *
 * "From where" is the second question in any SOC 2 or ISO 27001 walkthrough
 * after "who", and `audit_events` could not answer it: the table carried
 * twenty-four columns and neither a source address nor a user agent. It is also
 * the half of the record that **cannot be added retroactively** — a call already
 * made has no address to go and find — which is why it lands before the record
 * view that reads it.
 *
 * It travels on the context rather than on Event, and that is the load-bearing
 * decision here. Events are constructed in a dozen places (the proxy, Call, the
 * shell, JIT, kubeconfig issuance, a password change, a recording being watched)
 * and threading two more fields through all of them would mean every future
 * event site has to remember to fill them — the failure mode being a record that
 * looks complete and is not. Every one of those sites already passes a context,
 * and for anything a person did it is the request's own. So it is captured once,
 * in one middleware, and read once, where the row is written.
 *
 * A context with no source is not an error and is not filled in with a guess: a
 * record written by the JIT expirer or the alarm poller has no caller, and an
 * empty address says exactly that.
 */

type sourceKey struct{}

// RequestSource is the caller's own network identity, as the server saw it.
type RequestSource struct {
	// Addr is the client address, resolved through the proxy headers the server
	// has been told to trust. Never a port: a source port identifies a socket
	// that closed seconds later, and printing one in an audit column invites
	// somebody to read it as meaningful.
	Addr string
	// UserAgent is the client's own claim about what it is — kubectl, a browser,
	// somebody's CI runner. Untrusted by definition, and worth recording for
	// exactly that reason: it is how "this token is being used by something that
	// is not the thing we issued it to" first shows up.
	UserAgent string
}

// WithSource carries the caller's network identity down to whatever records the
// call. Empty sources are not stored, so a lookup cannot come back with a
// present-but-blank value that reads as "we looked and there was nothing".
func WithSource(ctx context.Context, source RequestSource) context.Context {
	if source.Addr == "" && source.UserAgent == "" {
		return ctx
	}
	return context.WithValue(ctx, sourceKey{}, source)
}

// SourceFrom reads the caller's network identity, or the zero value when the
// record has no caller — a background sweep, a test, a call made before this
// existed.
func SourceFrom(ctx context.Context) RequestSource {
	if ctx == nil {
		return RequestSource{}
	}
	source, _ := ctx.Value(sourceKey{}).(RequestSource)
	return source
}

// maxUserAgent bounds what is stored. A user agent is a header a client writes,
// so it is as long as the client decided; the column is text and the trail is
// read as a table, and neither wants a kilobyte of it.
const maxUserAgent = 256

// Truncate is where the bound is applied, on the way in rather than on the way
// out — a stored value that has to be shortened every time it is read is a
// value nobody trimmed.
func (s RequestSource) Truncate() RequestSource {
	if len(s.UserAgent) > maxUserAgent {
		s.UserAgent = s.UserAgent[:maxUserAgent]
	}
	if len(s.Addr) > 64 {
		s.Addr = s.Addr[:64]
	}
	return s
}
