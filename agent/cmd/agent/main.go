// Command agent is the KubeMG Dumb Agent.
//
// It is deliberately dumb: it holds one outbound tunnel to the KubeMG bastion
// and replays whatever requests arrive down it against its own cluster's API
// server. It makes no authorization decisions, installs no CRDs, runs no
// controllers, and caches no cluster state — all of that lives in the bastion,
// which is why this binary stays around 10 MB and this package stays open
// source.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/kubemg/kubemg/agent/internal/kube"
	"github.com/kubemg/kubemg/agent/internal/tunnel"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	listen := flag.String("listen", envOr("KUBEMG_LISTEN_ADDR", ":8081"),
		"address for the health and readiness probes")
	apiURL := flag.String("kubernetes-url", envOr("KUBEMG_KUBERNETES_URL", kube.DefaultAPIURL),
		"address of the cluster's API server")
	insecure := flag.Bool("insecure-skip-verify", envBool("KUBEMG_INSECURE_SKIP_VERIFY"),
		"skip API server certificate verification (development only)")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	if err := run(logger, *listen, *apiURL, *insecure); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("agent stopped", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(logger *slog.Logger, listen, apiURL string, insecure bool) error {
	client, err := kube.New(kube.Options{APIURL: apiURL, InsecureSkipVerify: insecure})
	if err != nil {
		return err
	}

	tunnelClient, err := tunnel.New(tunnel.Options{
		BastionURL: os.Getenv("KUBEMG_BASTION_URL"),
		Token:      os.Getenv("KUBEMG_CLUSTER_TOKEN"),
		Version:    version,
		Kube:       client,
		Logger:     logger,
		// A self-signed or internal-CA bastion is pinned rather than trusted
		// blindly; the install manifest carries the PEM in the agent's Secret.
		CAPEM:              os.Getenv("KUBEMG_BASTION_CA"),
		InsecureSkipVerify: envBool("KUBEMG_BASTION_INSECURE_SKIP_VERIFY"),
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	probes := serveProbes(ctx, logger, listen, tunnelClient)
	defer probes()

	logger.Info("kubemg agent starting",
		slog.String("version", version),
		slog.String("namespace", kube.Namespace()),
		slog.String("kubernetes_url", apiURL),
	)

	return tunnelClient.Run(ctx)
}

// serveProbes exposes liveness and readiness. Readiness tracks the tunnel, so a
// cluster whose agent cannot reach the bastion shows up as not-ready in its own
// cluster as well as in KubeMG. It returns a shutdown function.
func serveProbes(ctx context.Context, logger *slog.Logger, addr string, client *tunnel.Client) func() {
	mux := http.NewServeMux()

	// Liveness is about this process, not about the bastion: restarting the pod
	// would not fix an unreachable bastion, so a down tunnel must not fail it.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !client.Connected() {
			http.Error(w, "tunnel is not connected", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("probe listener failed", slog.String("error", err.Error()))
		}
	}()

	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envBool(key string) bool {
	value, err := strconv.ParseBool(os.Getenv(key))
	return err == nil && value
}
