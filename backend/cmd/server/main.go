package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strings"

	"github.com/kubemg/kubemg/backend/pkg/api"
	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/certs"
	"github.com/kubemg/kubemg/backend/pkg/config"
	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/k8s"
	"github.com/kubemg/kubemg/backend/pkg/terminal"
)

func main() {
	cfg := config.Load()

	gdb, err := db.Open(cfg.DB)
	if err != nil {
		log.Fatalf("database connection failed: %v", err)
	}
	if err := db.Migrate(gdb); err != nil {
		log.Fatalf("database migration failed: %v", err)
	}

	store := db.NewStore(gdb)
	if err := seedAdmin(context.Background(), store, cfg); err != nil {
		log.Fatalf("admin bootstrap failed: %v", err)
	}

	clusters := k8s.NewManager()

	// The bastion and the proxy share one connection pool: the tunnel listener
	// fills it, the proxy draws from it, and the API reads it to report which
	// clusters have an agent attached.
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	gateway := bastion.NewServer(bastion.ServerOptions{Store: store, Logger: logger})

	// The trail goes two places on purpose: structured logs are what a SIEM
	// already tails, and the table is what the audit page queries. Persisting
	// is asynchronous so a slow database never becomes a slow kubectl.
	auditStore := bastion.NewStoreAuditor(store, logger)
	auditCtx, stopAudit := context.WithCancel(context.Background())
	go auditStore.Run(auditCtx)
	defer func() {
		stopAudit()
		auditStore.Wait()
	}()

	// Interactive sessions are recorded for replay when a directory is
	// configured. A recorder that cannot be prepared leaves recording off rather
	// than stopping the server: an unmountable volume must not take the console
	// down with it, and the gap is loud in the log and visible in the UI, which
	// says whether this server is recording at all.
	recorder, recordingDir := resolveRecorder(cfg, store, logger)

	proxy := bastion.NewProxy(bastion.ProxyOptions{
		Store:    store,
		Registry: gateway.Registry(),
		Auditor:  bastion.NewMultiAuditor(bastion.NewAuditor(logger), auditStore),
		Recorder: recorder,
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
		JWT:            auth.NewManager(cfg.JWTSecret, cfg.JWTTTL),
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
		RecordingDir:       recordingDir,
		Background:         auditCtx,
		Logger:             logger,
	})

	if cfg.TLS.Enabled {
		logger.Info("serving https",
			slog.String("addr", cfg.ListenAddr),
			slog.String("certificate", cfg.TLS.CertFile),
		)
		if err := router.RunTLS(cfg.ListenAddr, cfg.TLS.CertFile, cfg.TLS.KeyFile); err != nil {
			log.Fatalf("server exited: %v", err)
		}
		return
	}

	// Plaintext still starts, because the console works over it and a dev stack
	// should not need certificates. kubectl is the part that does not: client-go
	// refuses to send a bearer token over http, so say so once at boot rather
	// than letting every generated kubeconfig fail unexplained.
	logger.Warn("serving http without TLS; generated kubeconfigs and kubectl exec will not work",
		slog.String("addr", cfg.ListenAddr),
		slog.String("fix", "set KUBEMG_TLS_ENABLED=true"),
	)
	if err := router.Run(cfg.ListenAddr); err != nil {
		log.Fatalf("server exited: %v", err)
	}
}

// resolveRecorder prepares terminal session recording, returning the recorder
// the proxy tees sessions into and the directory the API reads recordings back
// from. Both are empty when recording is off — which is what the replay routes
// report, rather than answering with a file path nothing wrote.
func resolveRecorder(
	cfg config.Config, store *db.Store, logger *slog.Logger,
) (bastion.SessionRecorder, string) {
	if !cfg.SessionRecording.Enabled {
		logger.Info("terminal session recording is off",
			slog.String("enable", "KUBEMG_SESSION_RECORDING_ENABLED=true"))
		return nil, ""
	}

	recorder, err := terminal.NewRecorder(terminal.Options{
		Dir:      cfg.SessionRecording.Dir,
		Sessions: store,
		MaxBytes: cfg.SessionRecording.MaxBytes,
		Logger:   logger,
	})
	if err != nil {
		logger.Error("terminal session recording is enabled but could not be started; "+
			"sessions will be audited but not recorded",
			slog.String("directory", cfg.SessionRecording.Dir),
			slog.String("error", err.Error()))
		return nil, ""
	}

	logger.Info("recording interactive sessions for replay",
		slog.String("directory", recorder.Dir()))
	return recorder, recorder.Dir()
}

// tlsMaterial is what the rest of the process needs to know about TLS: the
// certificate an agent must trust, if it is not one the public CAs cover.
type tlsMaterial struct {
	agentCA string
}

// resolveTLS provisions the listener's certificate and decides what agents are
// told to trust. With TLS off it does nothing beyond an explicit CA bundle;
// with TLS on it uses the operator's files, and mints a self-signed pair when
// there are none, so a first boot serves HTTPS rather than refusing to start on
// a file nobody has been asked for yet.
func resolveTLS(cfg config.Config, logger *slog.Logger) (tlsMaterial, error) {
	// An explicit bundle wins over anything inferred, and is read whether or
	// not this process terminates TLS: behind an ingress the certificate agents
	// verify is the ingress's, which nothing here can see. An internal
	// corporate PKI is the case that matters — it is not self-signed, so no
	// amount of inspecting our own certificate would reveal that agents need it.
	if bundle := strings.TrimSpace(cfg.TLS.AgentCABundle); bundle != "" {
		pemBytes, err := certs.LoadBundle(bundle)
		if err != nil {
			return tlsMaterial{}, err
		}
		logger.Info("pinning an operator-supplied CA bundle into agent install packages",
			slog.String("bundle", bundle))
		return tlsMaterial{agentCA: string(pemBytes)}, nil
	}

	if !cfg.TLS.Enabled {
		return tlsMaterial{}, nil
	}

	hosts := certs.HostsFor(cfg.PublicURL, cfg.TLS.Hosts)
	material, err := certs.Ensure(cfg.TLS.CertFile, cfg.TLS.KeyFile, hosts)
	if err != nil {
		return tlsMaterial{}, err
	}
	if material.Generated {
		if !cfg.TLS.SelfSigned {
			return tlsMaterial{}, fmt.Errorf(
				"no certificate at %s and KUBEMG_TLS_SELF_SIGNED is off", cfg.TLS.CertFile)
		}
		logger.Warn("generated a self-signed certificate; replace it before this is a real deployment",
			slog.String("certificate", cfg.TLS.CertFile),
			slog.Any("hosts", hosts),
		)
	}

	// The CA is pinned into agent install packages only when it is self-signed.
	// Shipping a publicly-trusted certificate to every agent would pin KubeMG
	// to that one certificate, so renewing it would strand the whole fleet.
	if isSelfSigned(material.CertPEM) {
		return tlsMaterial{agentCA: string(material.CertPEM)}, nil
	}
	return tlsMaterial{}, nil
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

// seedAdmin creates the bootstrap admin account on a fresh database so the
// platform is reachable before any user exists.
func seedAdmin(ctx context.Context, store *db.Store, cfg config.Config) error {
	count, err := store.CountUsers(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	hash, err := auth.HashPassword(cfg.AdminPassword)
	if err != nil {
		return err
	}
	if err := store.CreateUser(ctx, &db.User{
		Username:     cfg.AdminUsername,
		PasswordHash: hash,
		SystemRole:   db.SystemRoleSuperAdmin,
		IsActive:     true,
	}); err != nil {
		return err
	}

	log.Printf("bootstrap admin user %q created", cfg.AdminUsername)
	return nil
}
