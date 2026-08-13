package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// Deployment describes the parts of an install that were settled before this
// router existed and that no request can change: TLS material read off a volume,
// the origin of the signing key, whether recordings are encrypted at rest.
//
// It exists for the setup wizard, and specifically so the wizard can be honest.
// A first-run experience that collected a database password or a recording key
// into a form would be collecting values that vanish at the next restart —
// the process reads them once, at boot, from an environment it cannot rewrite.
// So the wizard writes what is genuinely runtime-settable and *reports* this,
// with the variable to set and where to set it.
type Deployment struct {
	// SigningKeyFromEnv distinguishes a key supplied as JWT_SECRET from one the
	// server minted for itself and keeps in the database.
	SigningKeyFromEnv bool
	// TLSEnabled reports whether this process terminates HTTPS itself. It is not
	// cosmetic: client-go refuses to send a bearer token over plain http, so a
	// plaintext bastion cannot serve a generated kubeconfig or an exec session
	// at all.
	TLSEnabled bool
	// TLSSelfSigned reports that the certificate this process serves vouches for
	// itself — the one minted on first boot, which browsers warn about and which
	// is pinned into every agent install package.
	TLSSelfSigned bool
	// TLSCertFile is where the certificate being served lives, named so the fix
	// can name it.
	TLSCertFile string
	// TLSSuppliedDir is the directory a deployment mounts a real certificate
	// into, checked ahead of everything else at boot. The remedy for a
	// self-signed certificate is stated in terms of it, because "copy two files
	// here and restart" is a smaller instruction than any arrangement of
	// environment variables.
	TLSSuppliedDir string
	// TLSSupplied reports that the certificate came from that directory rather
	// than being minted here.
	TLSSupplied bool
	// AgentCABundleSet reports that an operator supplied the chain agents must
	// trust explicitly, which is the case an ingress in front of KubeMG creates.
	AgentCABundleSet bool
}

// SetupStore is the slice of persistence the boot-time decision below needs.
// It is an interface rather than *db.Store so the decision can be tested
// without a database, which matters more here than usual: it is taken once per
// boot, before anything can observe it going wrong.
type SetupStore interface {
	Settings(ctx context.Context) (map[string]string, error)
	PutSettings(ctx context.Context, values map[string]string, updatedBy uint) error
}

// ResolveSetupStamp settles, at boot, whether this database has ever been
// offered first-run setup. It is called once, after the bootstrap administrator
// has been seeded or found already present.
//
// The distinction it draws is the whole point. Three databases arrive here:
//
//   - One just seeded. It is waiting for somebody to walk the wizard, so it is
//     marked as started and left alone.
//   - One seeded by an earlier boot of this same version, restarted before
//     anybody finished. It carries that mark, and is left alone again — this is
//     the case that matters, because it is indistinguishable from the next one
//     by user count alone, and getting it wrong means an operator sees the
//     wizard once and never again.
//   - One that predates the wizard: users, no marks at all. Stamping it complete
//     is what keeps an upgrade from walking an administrator back through
//     decisions their fleet already depends on.
func ResolveSetupStamp(ctx context.Context, store SetupStore, seeded bool, by uint) error {
	stored, err := store.Settings(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(stored[db.SettingSetupCompletedAt]) != "" {
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if seeded {
		return store.PutSettings(ctx, map[string]string{db.SettingSetupStartedAt: now}, by)
	}
	if strings.TrimSpace(stored[db.SettingSetupStartedAt]) != "" {
		// Seeded by an earlier boot and still unfinished. Leave it be.
		return nil
	}
	return store.PutSettings(ctx, map[string]string{db.SettingSetupCompletedAt: now}, by)
}

// setupCheck is one thing the wizard looked at. Severity is what the console
// paints it as; Fix is the literal line to add somewhere, because a warning
// that does not say what to type is a warning somebody closes.
type setupCheck struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Severity string `json:"severity"`
	Detail   string `json:"detail"`
	// Fix is empty for a check that passed, and for one whose remedy is a field
	// on the wizard itself rather than something outside it.
	Fix string `json:"fix,omitempty"`
}

// Check severities. "blocked" is reserved for a state in which a documented
// feature cannot work at all, as opposed to one that works less safely.
const (
	checkOK      = "ok"
	checkWarn    = "warn"
	checkBlocked = "blocked"
)

type setupStateResponse struct {
	// Required is the only field, and deliberately so: this route is
	// unauthenticated because the sign-in page has to render before anybody has
	// a session, and an unauthenticated route describing a server's
	// configuration would be a reconnaissance endpoint.
	Required bool `json:"required"`
}

type setupPreflightResponse struct {
	// AdminPasswordPristine reports that the seeded administrator still holds the
	// password it was created with — generated and printed to the boot log, or
	// taken from KUBEMG_ADMIN_PASSWORD. Setup will not finish while it is true.
	AdminPasswordPristine bool `json:"admin_password_pristine"`
	// Checks are the deployment facts the wizard cannot write, resolved.
	Checks []setupCheck `json:"checks"`
	// Warnings are the settings-level warnings, reused verbatim from the
	// settings API so the two surfaces cannot drift into saying different
	// things about the same address.
	Warnings []string `json:"warnings"`
}

// setupState reports whether this server still needs first-run setup.
//
// Unauthenticated, and narrow in exactly the way listSSOProvidersPublic is: the
// login page needs the answer before a session exists, so the response is one
// boolean and carries nothing a stranger could learn from.
//
// A database failure reads as "already set up". That is the safe direction — the
// wizard is a redirect that overrides the whole console, and a transient error
// must not be able to drop an established install back into it.
func (s *server) setupState(c *gin.Context) {
	c.JSON(http.StatusOK, setupStateResponse{Required: s.setupRequired(c.Request.Context())})
}

// setupRequired resolves the stamp. Setup is needed until it is written, and
// never again afterwards.
func (s *server) setupRequired(ctx context.Context) bool {
	stored, err := s.store.Settings(ctx)
	if err != nil {
		return false
	}
	return strings.TrimSpace(stored[db.SettingSetupCompletedAt]) == ""
}

// setupPreflight reports what the wizard is about to hand back to the operator:
// everything about this install that a form cannot fix (admin only).
func (s *server) setupPreflight(c *gin.Context) {
	ctx := c.Request.Context()
	resolved := s.settings(ctx)

	c.JSON(http.StatusOK, setupPreflightResponse{
		AdminPasswordPristine: s.bootstrapAdminPending(ctx) != 0,
		Checks:                s.deploymentChecks(),
		Warnings:              settingsWarnings(resolved),
	})
}

// deploymentPostureResponse is the same reading of this install the wizard's
// last step shows, served to a console that has no wizard left.
type deploymentPostureResponse struct {
	Checks []setupCheck `json:"checks"`
	// Attention counts the checks that are not ok, so a tab can carry a badge
	// without the caller re-deriving what "not ok" means.
	Attention int `json:"attention"`
}

// deploymentPosture reports what this install was started with (admin only).
//
// It answers off the same deploymentChecks the setup wizard reads, deliberately:
// a self-signed certificate and unencrypted recordings do not stop being true
// once setup is stamped finished, and the wizard was the only place that said so.
// An operator who inherits an install, or who took the fast path through setup,
// has to be able to find these later — and a second implementation of the same
// facts would be free to drift into disagreeing with the first.
func (s *server) deploymentPosture(c *gin.Context) {
	checks := s.deploymentChecks()
	attention := 0
	for _, check := range checks {
		if check.Severity != checkOK {
			attention++
		}
	}
	c.JSON(http.StatusOK, deploymentPostureResponse{Checks: checks, Attention: attention})
}

// deploymentChecks resolves the boot-time facts into something a console can
// render. The order is the order they matter in: a bastion without TLS cannot
// serve kubectl at all, an unencrypted recording is production shell output
// sitting in the clear, and a self-signed certificate is a decision rather than
// a fault.
func (s *server) deploymentChecks() []setupCheck {
	checks := []setupCheck{}

	switch {
	case !s.deployment.TLSEnabled:
		checks = append(checks, setupCheck{
			Key:      "tls",
			Title:    "HTTPS is off",
			Severity: checkBlocked,
			Detail: "kubectl cannot use this bastion over plain http — client-go refuses to send a " +
				"bearer token unencrypted, so generated kubeconfigs and exec sessions will not work. " +
				"The console still does.",
			Fix: "KUBEMG_TLS_ENABLED=true",
		})
	case s.deployment.TLSSelfSigned && s.deployment.TLSSupplied:
		checks = append(checks, setupCheck{
			Key:      "tls",
			Title:    "The certificate you supplied is self-signed",
			Severity: checkWarn,
			Detail: "It is being served and it is pinned into every agent install package, so agents " +
				"connect — but browsers warn, and replacing it later means re-issuing the install " +
				"package for every cluster that pinned it. A certificate issued by a CA the agents " +
				"already trust avoids both.",
			Fix: "replace " + s.deployment.TLSCertFile + " with a CA-issued certificate, then restart KubeMG",
		})
	case s.deployment.TLSSelfSigned:
		checks = append(checks, setupCheck{
			Key:      "tls",
			Title:    "Serving a self-signed certificate",
			Severity: checkWarn,
			Detail: "Minted on first boot and pinned into every agent install package, so agents " +
				"connect and browsers warn once. Replacing it later means re-issuing the install " +
				"package for every cluster, which is why it is worth deciding now. Drop a real " +
				"certificate into the directory below — in the compose install that is ./ssl next to " +
				"docker-compose.yml — and it is served instead, with no variable to set. Certbot's " +
				"fullchain.pem and privkey.pem are recognised there too, so a Let's Encrypt live " +
				"directory can be mounted as it stands.",
			Fix: "put tls.crt and tls.key in " + s.suppliedCertDir() + ", then restart KubeMG",
		})
	case s.deployment.TLSSupplied:
		checks = append(checks, setupCheck{
			Key:      "tls",
			Title:    "Serving the certificate you supplied",
			Severity: checkOK,
			Detail: "Read from " + s.deployment.TLSCertFile + ". Agents verify it against the public " +
				"CAs, so nothing is pinned and renewing it strands nobody — a renewed file in the same " +
				"place is picked up on the next restart.",
		})
	default:
		checks = append(checks, setupCheck{
			Key:      "tls",
			Title:    "Serving an operator-supplied certificate",
			Severity: checkOK,
			Detail:   "Agents verify it against the public CAs, so nothing is pinned and renewing it strands nobody.",
		})
	}

	if s.deployment.AgentCABundleSet {
		checks = append(checks, setupCheck{
			Key:      "agent-ca",
			Title:    "Agents are given an explicit CA bundle",
			Severity: checkOK,
			Detail: "Every install package carries the chain you supplied. This is the setting an " +
				"internal PKI or a TLS-terminating ingress needs — nothing here could have inferred it.",
		})
	}

	switch {
	case s.recordings == "":
		checks = append(checks, setupCheck{
			Key:      "recording",
			Title:    "Interactive sessions are not being recorded",
			Severity: checkWarn,
			Detail: "Shells opened through KubeMG are still audited — who, where and when — but there " +
				"is nothing to replay. Recording needs a directory that outlives the container.",
			Fix: "KUBEMG_SESSION_RECORDING_ENABLED=true, and mount a volume at " +
				"/var/lib/kubemg/recordings",
		})
	case len(s.recordingKey) == 0:
		checks = append(checks, setupCheck{
			Key:      "recording-key",
			Title:    "Recordings are written unencrypted",
			Severity: checkWarn,
			Detail: "A recording holds production shell output and keystrokes. Without a key they are " +
				"plain gzip on disk, so a stolen volume snapshot is every password anyone typed. The " +
				"key is read from the environment and never stored here — one kept beside the " +
				"ciphertext would protect against nothing, and losing it means losing the recordings.",
			Fix: "KUBEMG_SESSION_RECORDING_KEY=$(openssl rand -base64 32)",
		})
	default:
		checks = append(checks, setupCheck{
			Key:      "recording-key",
			Title:    "Recordings are encrypted at rest",
			Severity: checkOK,
			Detail:   "Chunked AES-256-GCM, so a truncated file fails to authenticate rather than replaying short.",
		})
	}

	if s.deployment.SigningKeyFromEnv {
		checks = append(checks, setupCheck{
			Key:      "signing-key",
			Title:    "Signing sessions with the key you supplied",
			Severity: checkOK,
			Detail:   "JWT_SECRET is in force. Changing it revokes every session and every generated kubeconfig at once.",
		})
	} else {
		checks = append(checks, setupCheck{
			Key:      "signing-key",
			Title:    "Signing sessions with a generated key",
			Severity: checkOK,
			Detail: "Minted on first boot and kept in the database, so sessions survive a restart. " +
				"Set JWT_SECRET to supply your own instead — it takes precedence, and it is what " +
				"several replicas behind one address need in order to agree.",
		})
	}

	return checks
}

// suppliedCertDir names where to put a real certificate. It is the directory the
// deployment mounts for exactly that, and falls back to the directory the served
// certificate sits in for an install configured before that directory existed —
// wording a suggestion is all it is for, so it degrades rather than guessing.
func (s *server) suppliedCertDir() string {
	if dir := strings.TrimSpace(s.deployment.TLSSuppliedDir); dir != "" {
		return dir
	}
	return certDir(s.deployment.TLSCertFile)
}

// certDir names the directory a certificate sits in, from its path. It is
// only ever used to word a suggestion, so an unexpected path degrades to the
// path itself rather than to anything wrong.
func certDir(certFile string) string {
	if idx := strings.LastIndex(certFile, "/"); idx > 0 {
		return certFile[:idx]
	}
	return "/etc/kubemg/tls"
}

// completeSetup stamps first-run setup as finished (admin only).
//
// It refuses while the seeded administrator still holds its original password.
// That is the whole point of the refusal: everything else the wizard collects is
// a preference, and this one is the difference between a bastion that has been
// set up and one that merely looks like it has.
func (s *server) completeSetup(c *gin.Context) {
	caller, ok := s.currentUser(c)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	if !s.setupRequired(ctx) {
		// Already done. Answering 200 keeps a double-submit from reading as a
		// failure, and there is nothing to write.
		c.JSON(http.StatusOK, setupStateResponse{Required: false})
		return
	}

	if s.bootstrapAdminPending(ctx) != 0 {
		c.JSON(http.StatusConflict, gin.H{
			"error": "the administrator created on first boot still has its original password; " +
				"change it before finishing setup",
		})
		return
	}

	if err := s.store.PutSettings(ctx, map[string]string{
		db.SettingSetupCompletedAt: time.Now().UTC().Format(time.RFC3339),
	}, caller.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not record that setup finished"})
		return
	}

	c.JSON(http.StatusOK, setupStateResponse{Required: false})
}

// bootstrapAdminPending returns the id of the seeded administrator while it
// still holds its original password, and zero otherwise.
//
// The marker is cleared where the password changes, and checked against reality
// here: a row naming an account that no longer exists is not a password anybody
// can still use, so a deleted administrator resolves the same way a changed one
// does. A stale marker must not be able to wedge setup shut.
func (s *server) bootstrapAdminPending(ctx context.Context) uint {
	stored, err := s.store.Settings(ctx)
	if err != nil {
		return 0
	}
	raw := strings.TrimSpace(stored[db.SettingBootstrapAdminID])
	if raw == "" {
		return 0
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0
	}
	if _, err := s.store.UserByID(ctx, uint(id)); err != nil {
		return 0
	}
	return uint(id)
}

// clearBootstrapAdmin forgets the seeded administrator, which is how "the
// original password is still in force" stops being true. It is called from the
// one place that can make it stop being true deliberately: a password write.
//
// A failure here is logged rather than surfaced, because the caller succeeded at
// what it was asked to do; the consequence is a setup step that asks again,
// which is the harmless direction.
func (s *server) clearBootstrapAdmin(ctx context.Context, userID, by uint) {
	if s.bootstrapAdminPending(ctx) != userID {
		return
	}
	if err := s.store.PutSettings(ctx, map[string]string{db.SettingBootstrapAdminID: ""}, by); err != nil &&
		s.logger != nil {
		s.logger.Warn("could not clear the bootstrap administrator marker", "error", err.Error())
	}
}
