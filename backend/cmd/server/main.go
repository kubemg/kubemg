package main

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/kubemg/kubemg/backend/pkg/api"
	"github.com/kubemg/kubemg/backend/pkg/auditpolicy"
	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/certs"
	"github.com/kubemg/kubemg/backend/pkg/config"
	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/guardrails"
	"github.com/kubemg/kubemg/backend/pkg/jit"
	"github.com/kubemg/kubemg/backend/pkg/k8s"
	"github.com/kubemg/kubemg/backend/pkg/observability"
	"github.com/kubemg/kubemg/backend/pkg/terminal"
	"github.com/kubemg/kubemg/backend/pkg/webui"
)

// version is stamped at build time (`-ldflags "-X main.version=..."`) and is
// "dev" in every build that was not cut by the release pipeline. It is logged
// at boot because the first question about a bastion nobody can reach is which
// build is running, and an image tag is what somebody *meant* to deploy.
var version = "dev"

func main() {
	cfg := config.Load()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	gdb, err := db.Open(cfg.DB)
	if err != nil {
		log.Fatalf("database connection failed: %v", err)
	}
	if err := db.Migrate(gdb); err != nil {
		log.Fatalf("database migration failed: %v", err)
	}

	store := db.NewStore(gdb)

	// Everything from here to the router is what makes an install possible with
	// no configuration at all: the signing key, the first administrator and the
	// question of whether this database has ever been set up. All three used to
	// be an operator's problem before the first boot; the console now asks for
	// what it genuinely needs and the server supplies the rest.
	boot := context.Background()

	signingKey, err := resolveSigningKey(boot, store, cfg, logger)
	if err != nil {
		log.Fatalf("signing key setup failed: %v", err)
	}

	seeded, err := seedAdmin(boot, store, cfg)
	if err != nil {
		log.Fatalf("admin bootstrap failed: %v", err)
	}
	// Whether this database has ever been offered the wizard — which is not the
	// same question as whether it has users. See api.ResolveSetupStamp.
	if err := api.ResolveSetupStamp(boot, store, seeded, 0); err != nil {
		log.Fatalf("setup state could not be recorded: %v", err)
	}

	clusters := k8s.NewManager()

	// The bastion and the proxy share one connection pool: the tunnel listener
	// fills it, the proxy draws from it, and the API reads it to report which
	// clusters have an agent attached.
	gateway := bastion.NewServer(bastion.ServerOptions{Store: store, Logger: logger})

	// Which verbs reach the audit table and whether shells are recorded are
	// runtime settings, and both are read on the gateway's hot path. The policy is
	// the handoff: the HTTP layer resolves it from the database and publishes it
	// here, the gateway reads it lock-free. It starts out recording everything, so
	// a server that has not read its settings yet keeps a complete trail.
	policy := auditpolicy.New()

	// The command guardrails follow the same handoff, and for the same reason:
	// the rules live in the database and the decision is taken on the gateway's
	// hot path. It starts out empty, so a server that has not read its rules yet
	// refuses nothing — a guardrail failing open is a policy that did not apply,
	// while one failing closed would be a fleet nobody can reach.
	guard := guardrails.New()

	// The trail goes two places on purpose: structured logs are what a SIEM
	// already tails, and the table is what the audit page queries. Persisting
	// is asynchronous so a slow database never becomes a slow kubectl. The verb
	// selection applies to the table only — see StoreAuditor.
	auditStore := bastion.NewStoreAuditor(store, logger, policy)
	auditCtx, stopAudit := context.WithCancel(context.Background())
	go auditStore.Run(auditCtx)
	defer func() {
		stopAudit()
		auditStore.Wait()
	}()

	// Alarms are the third consumer of the same records, and the only one that
	// pushes: a refused action or an OOMKilled pod goes to whoever has to know
	// now. It is started unconditionally because it costs nothing until a rule
	// exists — with none configured, Observe returns immediately and nothing polls.
	alarms := observability.NewDispatcher(observability.DispatcherOptions{
		Store:  store,
		Logger: logger,
		Origin: cfg.PublicURL,
	})
	go alarms.Run(auditCtx)
	defer alarms.Wait()

	// Interactive sessions are recorded for replay when a directory is
	// configured. A recorder that cannot be prepared leaves recording off rather
	// than stopping the server: an unmountable volume must not take the console
	// down with it, and the gap is loud in the log and visible in the UI, which
	// says whether this server is recording at all.
	recorder, recording := resolveRecorder(cfg, store, logger)

	// One auditor, two consumers: the proxy records what it forwarded to a
	// cluster, and the API records the one thing it does that is more sensitive
	// than most of that — serving somebody a recording of a production shell.
	auditor := bastion.NewMultiAuditor(
		bastion.NewAuditor(logger),
		auditStore,
		api.NewAlarmAuditor(alarms),
	)

	// Just-in-time elevated access. It shares the audit writer with the proxy, so
	// "who was given production and why" lands in the same trail as the calls they
	// then made; its approval notices go out through the alarm dispatcher, which is
	// where the Slack and Teams destinations already are; and the tokens in those
	// notices are signed with the server's own signing key, since a second secret
	// to configure would be a second secret to leave unset.
	access := jit.New(jit.Options{
		Store:          store,
		Auditor:        auditor,
		Notify:         alarms,
		CallbackSecret: []byte(signingKey),
		ConsoleURL:     cfg.PublicURL,
		Logger:         logger,
	})

	proxy := bastion.NewProxy(bastion.ProxyOptions{
		Store:          store,
		Registry:       gateway.Registry(),
		Auditor:        auditor,
		Recorder:       recorder,
		Policy:         policy,
		Guard:          guard,
		AllowedOrigins: cfg.AllowedOrigins,
		PublicURL:      cfg.PublicURL,
	})

	// TLS is resolved before the router is built: an agent's install package
	// has to carry whatever certificate this server will actually present, and
	// that is only known once the material exists on disk.
	tlsMaterial, err := resolveTLS(cfg, logger)
	if err != nil {
		log.Fatalf("tls setup failed: %v", err)
	}

	router := api.NewRouter(api.Options{
		Store:          store,
		JWT:            auth.NewManager(signingKey, cfg.JWTTTL),
		Tokens:         clusters,
		Health:         clusters,
		SANamespace:    cfg.SANamespace,
		AllowedOrigins: cfg.AllowedOrigins,
		Bastion:        gateway,
		Proxy:          proxy,
		PublicURL:      cfg.PublicURL,
		AgentImage:     cfg.AgentImage,
		AgentNamespace: cfg.AgentNamespace,
		BastionCA:      tlsMaterial.agentCA,
		// Housekeeping shares the audit writer's lifetime: both are background
		// work that has to stop when the process is winding down.
		AuditRetentionDays: cfg.AuditRetentionDays,
		ReadCacheTTL:       cfg.ReadCacheTTL,
		EventCacheTTL:      cfg.EventCacheTTL,
		EventScanLimit:     cfg.EventScanLimit,
		RecordingDir:       recording.dir,
		RecordingKey:       recording.key,
		RecordingInput:     recording.input,
		Auditor:            auditor,
		AuditPolicy:        policy,
		Guardrails:         guard,
		Alarms:             alarms,
		JIT:                access,
		JITCallbackSecret:  []byte(signingKey),
		// What this install was started with, for the setup wizard to report
		// rather than pretend to own. See api.Deployment.
		Deployment: api.Deployment{
			SigningKeyFromEnv: strings.TrimSpace(cfg.JWTSecret) != "",
			TLSEnabled:        cfg.TLS.Enabled,
			TLSSelfSigned:     tlsMaterial.selfSigned,
			TLSCertFile:       tlsMaterial.certFile,
			TLSSuppliedDir:    tlsMaterial.suppliedDir,
			TLSSupplied:       tlsMaterial.supplied,
			AgentCABundleSet:  strings.TrimSpace(cfg.TLS.AgentCABundle) != "",
		},
		Background:         auditCtx,
		Logger:             logger,
	})

	// The console is served from the same origin as the API it calls, which is
	// what makes a production install need no CORS configuration at all. It is
	// mounted after every API route because it answers on NoRoute: it can only
	// ever be reached by a path the API did not claim. A binary built without a
	// frontend build — the dev stack, where Vite serves the console — has none
	// embedded, and this is a no-op that says so.
	webui.Mount(router, logger)

	if cfg.TLS.Enabled {
		logger.Info("serving https",
			slog.String("version", version),
			slog.String("addr", cfg.ListenAddr),
			slog.String("certificate", tlsMaterial.certFile),
		)
		if err := router.RunTLS(cfg.ListenAddr, tlsMaterial.certFile, tlsMaterial.keyFile); err != nil {
			log.Fatalf("server exited: %v", err)
		}
		return
	}

	// Plaintext still starts, because the console works over it and a dev stack
	// should not need certificates. kubectl is the part that does not: client-go
	// refuses to send a bearer token over http, so say so once at boot rather
	// than letting every generated kubeconfig fail unexplained.
	logger.Warn("serving http without TLS; generated kubeconfigs and kubectl exec will not work",
		slog.String("version", version),
		slog.String("addr", cfg.ListenAddr),
		slog.String("fix", "set KUBEMG_TLS_ENABLED=true"),
	)
	if err := router.Run(cfg.ListenAddr); err != nil {
		log.Fatalf("server exited: %v", err)
	}
}

// recordingSetup is what the HTTP layer needs to serve recordings back: where
// they are, the key they were written with, and whether keystrokes are part of
// them. All three are zero when recording is off, which is what the replay routes
// report rather than answering with a file path nothing wrote.
type recordingSetup struct {
	dir   string
	key   []byte
	input bool
}

// resolveRecorder prepares terminal session recording, returning the recorder the
// proxy tees sessions into alongside that setup.
//
// A misconfigured key stops recording rather than silently writing recordings in
// the clear: an operator who set a key believes the files are encrypted, and a
// server that quietly disagreed with them would be worse than one that says so.
// A *missing* key is different — it is the documented default and the state every
// install before this upgrade is in — so it warns loudly and carries on.
func resolveRecorder(
	cfg config.Config, store *db.Store, logger *slog.Logger,
) (bastion.SessionRecorder, recordingSetup) {
	if !cfg.SessionRecording.Enabled {
		logger.Info("terminal session recording is off",
			slog.String("enable", "KUBEMG_SESSION_RECORDING_ENABLED=true"))
		return nil, recordingSetup{}
	}

	key, err := terminal.ParseKey(cfg.SessionRecording.Key)
	if err != nil {
		logger.Error("terminal session recording is enabled but the recording key is unusable; "+
			"sessions will be audited but not recorded",
			slog.String("error", err.Error()))
		return nil, recordingSetup{}
	}

	recorder, err := terminal.NewRecorder(terminal.Options{
		Dir:       cfg.SessionRecording.Dir,
		Sessions:  store,
		MaxBytes:  cfg.SessionRecording.MaxBytes,
		Key:       key,
		OmitInput: !cfg.SessionRecording.Input,
		Logger:    logger,
	})
	if err != nil {
		logger.Error("terminal session recording is enabled but could not be started; "+
			"sessions will be audited but not recorded",
			slog.String("directory", cfg.SessionRecording.Dir),
			slog.String("error", err.Error()))
		return nil, recordingSetup{}
	}

	if !recorder.Encrypting() {
		// Loud, once, at boot: this is the difference between a stolen volume
		// snapshot being an inconvenience and it being every password anyone
		// typed into a production shell.
		logger.Warn("session recordings are being written unencrypted; "+
			"they hold production shell output and keystrokes",
			slog.String("fix", "set KUBEMG_SESSION_RECORDING_KEY (openssl rand -base64 32)"),
			slog.String("directory", recorder.Dir()))
	}

	logger.Info("recording interactive sessions for replay",
		slog.String("directory", recorder.Dir()),
		slog.Bool("encrypted", recorder.Encrypting()),
		slog.Bool("keystrokes", recorder.RecordingInput()))
	return recorder, recordingSetup{
		dir:   recorder.Dir(),
		key:   recorder.Key(),
		input: recorder.RecordingInput(),
	}
}

// tlsMaterial is what the rest of the process needs to know about TLS: the
// certificate an agent must trust, if it is not one the public CAs cover.
type tlsMaterial struct {
	agentCA string
	// selfSigned reports that the certificate this process serves vouches for
	// itself. The setup wizard says so, because replacing it later means
	// re-issuing the install package for every cluster that pinned it.
	selfSigned bool
	// certFile and keyFile are what the listener actually serves, which is not
	// always what the configuration named: a certificate found in the supplied
	// directory wins over both the generated pair and the configured paths.
	certFile string
	keyFile  string
	// supplied reports that the pair came from that directory rather than from
	// this process, and suppliedDir names it either way — a check that tells an
	// operator where to put a certificate is worth more than one that tells them
	// they have not.
	supplied    bool
	suppliedDir string
}

// resolveTLS provisions the listener's certificate and decides what agents are
// told to trust. With TLS off it does nothing beyond an explicit CA bundle; with
// TLS on it serves a certificate an operator dropped into the supplied
// directory, or the files they configured, and mints a self-signed pair when
// there is neither — so a first boot serves HTTPS rather than refusing to start
// on a file nobody has been asked for yet.
func resolveTLS(cfg config.Config, logger *slog.Logger) (tlsMaterial, error) {
	out := tlsMaterial{
		certFile:    cfg.TLS.CertFile,
		keyFile:     cfg.TLS.KeyFile,
		suppliedDir: cfg.TLS.SuppliedDir,
	}

	// An explicit bundle wins over anything inferred, and is read whether or
	// not this process terminates TLS: behind an ingress the certificate agents
	// verify is the ingress's, which nothing here can see. An internal
	// corporate PKI is the case that matters — it is not self-signed, so no
	// amount of inspecting our own certificate would reveal that agents need it.
	bundle := strings.TrimSpace(cfg.TLS.AgentCABundle)
	if bundle != "" {
		pemBytes, err := certs.LoadBundle(bundle)
		if err != nil {
			return tlsMaterial{}, err
		}
		logger.Info("pinning an operator-supplied CA bundle into agent install packages",
			slog.String("bundle", bundle))
		out.agentCA = string(pemBytes)
	}

	if !cfg.TLS.Enabled {
		return out, nil
	}

	// The supplied directory is read first, and a pair in it wins. On a first
	// boot that is the only certificate there is; on every boot after one, the
	// generated pair also exists — and letting it win would mean an operator who
	// dropped a real certificate in place watched the self-signed one keep
	// serving, with nothing saying why.
	material, supplied, err := certs.Supplied(cfg.TLS.SuppliedDir)
	if err != nil {
		return tlsMaterial{}, err
	}
	if supplied {
		out.certFile, out.keyFile, out.supplied = material.CertFile, material.KeyFile, true
		logger.Info("serving the certificate found in the supplied directory",
			slog.String("directory", cfg.TLS.SuppliedDir),
			slog.String("certificate", material.CertFile))
	} else {
		// Refused before Ensure rather than on its answer: Ensure writes the pair
		// it reports as generated, so refusing afterwards would leave a
		// self-signed certificate on disk that the next boot finds, honours, and
		// pins into every agent package — with the setting that forbade it still
		// off.
		if !cfg.TLS.SelfSigned {
			has, err := certs.HasPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
			if err != nil {
				return tlsMaterial{}, err
			}
			if !has {
				return tlsMaterial{}, fmt.Errorf(
					"no certificate in %s or at %s, and KUBEMG_TLS_SELF_SIGNED is off",
					cfg.TLS.SuppliedDir, cfg.TLS.CertFile)
			}
		}

		hosts := certs.HostsFor(cfg.PublicURL, cfg.TLS.Hosts)
		material, err = certs.Ensure(cfg.TLS.CertFile, cfg.TLS.KeyFile, hosts)
		if err != nil {
			return tlsMaterial{}, err
		}
		if material.Generated {
			logger.Warn("generated a self-signed certificate; replace it before this is a real deployment",
				slog.String("certificate", cfg.TLS.CertFile),
				slog.String("replace_by", "putting tls.crt and tls.key in "+cfg.TLS.SuppliedDir),
				slog.Any("hosts", hosts),
			)
		}
	}

	// The CA is pinned into agent install packages only when it is self-signed —
	// including a self-signed certificate the operator supplied themselves, since
	// agents have no other way to verify one. Shipping a publicly-trusted
	// certificate to every agent would pin KubeMG to that one certificate, so
	// renewing it would strand the whole fleet.
	if isSelfSigned(material.CertPEM) {
		out.selfSigned = true
		if bundle == "" {
			out.agentCA = string(material.CertPEM)
		}
	}
	return out, nil
}

// isSelfSigned reports whether the leaf certificate vouches for itself, which
// is what decides whether agents have to be handed it explicitly.
func isSelfSigned(certPEM []byte) bool {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	return cert.CheckSignatureFrom(cert) == nil
}

// resolveSigningKey settles the key that signs sessions, generated kubeconfigs
// and the JIT approval links.
//
// `JWT_SECRET` wins wherever it is set — a deployment already rotating it out of
// a secret manager is untouched. Where it is not, the server mints one and keeps
// it in the database, which is what lets an install come up having been told
// nothing at all. The alternative KubeMG shipped with until now was a literal
// default in the source, and a signing key everybody has is not a signing key.
//
// It is generated once and read forever after: minting a fresh one on every boot
// would invalidate every session and every kubeconfig on each restart.
func resolveSigningKey(
	ctx context.Context, store *db.Store, cfg config.Config, logger *slog.Logger,
) (string, error) {
	if key := strings.TrimSpace(cfg.JWTSecret); key != "" {
		logger.Info("signing sessions with the configured key", slog.String("source", "JWT_SECRET"))
		return key, nil
	}

	key, err := store.EnsureServerSecret(ctx, db.ServerSecretJWTSigningKey, func() (string, error) {
		return randomSecret(32)
	})
	if err != nil {
		return "", err
	}
	logger.Info("signing sessions with a server-generated key",
		slog.String("source", "database"),
		slog.String("note", "set JWT_SECRET to supply your own; it takes precedence"))
	return key, nil
}

// seedAdmin creates the bootstrap admin account on a fresh database so the
// platform is reachable before any user exists. It reports whether it created
// one, which is how the caller tells a fresh install from an existing one.
//
// With no `KUBEMG_ADMIN_PASSWORD` it generates a password and prints it once,
// here, rather than seeding a fleet gateway with something guessable. That log
// line is the only place the password is ever readable — the console makes
// changing it the first step of setup, and after that it is a hash like any
// other.
func seedAdmin(ctx context.Context, store *db.Store, cfg config.Config) (bool, error) {
	count, err := store.CountUsers(ctx)
	if err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil
	}

	password := strings.TrimSpace(cfg.AdminPassword)
	generated := password == ""
	if generated {
		if password, err = randomPassword(); err != nil {
			return false, err
		}
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return false, err
	}
	admin := &db.User{
		Username:     cfg.AdminUsername,
		PasswordHash: hash,
		SystemRole:   db.SystemRoleSuperAdmin,
		IsActive:     true,
	}
	if err := store.CreateUser(ctx, admin); err != nil {
		return false, err
	}

	// Remember which account this is for as long as it still holds the password
	// seeded here. Setup refuses to finish while that is true, so a bastion
	// cannot end up configured, connected to production, and still reachable
	// with the password that scrolled past in a log.
	if err := store.PutSettings(ctx, map[string]string{
		db.SettingBootstrapAdminID: strconv.FormatUint(uint64(admin.ID), 10),
	}, admin.ID); err != nil {
		return false, err
	}

	if generated {
		// Deliberately on the plain logger and deliberately shouted: this is a
		// credential printed to stdout exactly once, and somebody has to see it
		// scroll past to be able to sign in at all.
		log.Printf("\n"+
			"  ┌───────────────────────────────────────────────────────────────┐\n"+
			"  │  KubeMG is not configured yet. Sign in to set it up:          │\n"+
			"  │                                                               │\n"+
			"  │    username: %-49s│\n"+
			"  │    password: %-49s│\n"+
			"  │                                                               │\n"+
			"  │  This password is shown once. Change it in the first step of  │\n"+
			"  │  setup; set KUBEMG_ADMIN_PASSWORD to choose your own instead. │\n"+
			"  └───────────────────────────────────────────────────────────────┘\n",
			cfg.AdminUsername, password)
	} else {
		log.Printf("bootstrap admin user %q created", cfg.AdminUsername)
	}
	return true, nil
}

// randomSecret returns n cryptographically random bytes, base64url-encoded so it
// survives an environment variable, a YAML file and a copy-paste unchanged.
func randomSecret(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// randomPassword returns a password a human has to type once. It is shorter than
// a signing key for that reason, and drawn from an alphabet with no character
// whose shape is ambiguous in a terminal font — a password nobody can transcribe
// is a bastion nobody can sign into.
func randomPassword() (string, error) {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	const length = 20

	// Rejection sampling rather than a modulo: 256 does not divide by 56, so
	// folding a byte would quietly favour the first 32 letters of the alphabet.
	// The ceiling is the largest multiple of the alphabet that fits in a byte.
	ceiling := byte(256 / len(alphabet) * len(alphabet))

	out := make([]byte, 0, length)
	buf := make([]byte, length)
	for len(out) < length {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if b >= ceiling {
				continue
			}
			if out = append(out, alphabet[int(b)%len(alphabet)]); len(out) == length {
				break
			}
		}
	}
	return string(out), nil
}
