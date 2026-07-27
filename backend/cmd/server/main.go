package main

import (
	"context"
	"log"
	"log/slog"
	"os"

	"github.com/kubemg/kubemg/backend/pkg/api"
	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/config"
	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/k8s"
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

	proxy := bastion.NewProxy(bastion.ProxyOptions{
		Store:    store,
		Registry: gateway.Registry(),
		Auditor:  bastion.NewMultiAuditor(bastion.NewAuditor(logger), auditStore),
	})

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
		// Housekeeping shares the audit writer's lifetime: both are background
		// work that has to stop when the process is winding down.
		AuditRetentionDays: cfg.AuditRetentionDays,
		Background:         auditCtx,
		Logger:             logger,
	})

	if err := router.Run(cfg.ListenAddr); err != nil {
		log.Fatalf("server exited: %v", err)
	}
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
