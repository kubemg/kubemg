package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	// maxAnonymousBody bounds what a caller with no session may send. The
	// largest honest body here is a SAML response, a few tens of KB; a
	// megabyte is far past it.
	maxAnonymousBody int64 = 1 << 20
	// anonymousReadTimeout bounds how long such a caller may take to send it.
	// The server sets only a header timeout, because a whole-request timeout
	// would cut every stream and tunnel it carries, so the deadline is set
	// here, per request, on the routes that take no session.
	anonymousReadTimeout = 30 * time.Second
)

// limitAnonymous is the middleware on every route that answers a caller who
// has no session yet: sign-in, the second-factor and CLI device steps, SSO,
// setup state and the chat callback. Each reads a body before it knows who is
// asking, so without a bound a loop on any of them held memory and a
// connection for as long as the caller cared to keep sending.
//
// It is attached by route, never decided from the request: a check on the
// Authorization header would let a caller send any header and skip it.
func limitAnonymous(c *gin.Context) {
	if c.Request.Body != nil && c.Request.Body != http.NoBody {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAnonymousBody)
	}
	// A writer that cannot set a deadline (a test recorder) keeps the
	// server's own; nothing here depends on it.
	err := http.NewResponseController(c.Writer).SetReadDeadline(time.Now().Add(anonymousReadTimeout))
	if err != nil && !errors.Is(err, http.ErrNotSupported) {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "could not read the request"})
		return
	}
	c.Next()
}
