// Command kubemg signs a command line in to a kubemg server and keeps kubectl
// supplied with short-lived credentials for its agent-mode clusters.
//
// It is aws eks update-kubeconfig's shape, on purpose: the CLI writes an exec
// entry into the kubeconfig and steps aside. kubectl, k9s, helm and every IDE
// plugin keep working as they do; each asks `kubemg credential` for a token
// when it needs one. It is not a kubectl wrapper and never will be.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kubemg/kubemg/cli/internal/client"
	"github.com/kubemg/kubemg/cli/internal/kubeconfig"
	"github.com/kubemg/kubemg/cli/internal/store"
)

// version is stamped at build time (-ldflags -X main.version=…).
var version = "dev"

// docsURL is where an exec entry's installHint points.
const docsURL = "https://kubemg.readthedocs.io/en/latest/access/cli/"

// refreshAhead is how long before its expiry a cached credential is replaced,
// so kubectl is never handed one that dies mid-request.
const refreshAhead = 60 * time.Second

const usage = `kubemg — sign in to kubemg and give kubectl short-lived access.

Usage:
  kubemg login --server URL [--ca-file FILE] [--no-browser]
  kubemg update-kubeconfig --cluster NAME|ID [--server URL] [--namespace NS]
                           [--alias NAME] [--kubeconfig FILE] [--no-set-context]
                           [--static [--ttl 8h]] [--ca-file FILE  (with KUBEMG_TOKEN)]
  kubemg clusters [--server URL]
  kubemg logout [--server URL]
  kubemg credential --server URL --cluster-id N     (run by kubectl, not by you)
  kubemg version

KUBEMG_TOKEN, set to a machine account token, stands in for a session — the
CI path. KUBEMG_SERVER names the server when --server is not given.
`

// app is the command line with everything it touches injected, so the tests
// run it whole.
type app struct {
	stdout, stderr io.Writer
	store          store.Store
	getenv         func(string) string
	now            func() time.Time
	newClient      func(server string, caPEM []byte) (*client.Client, error)
	openBrowser    func(url string) error
	executable     func() string
}

func main() {
	st, err := store.Default()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kubemg:", err)
		os.Exit(1)
	}
	a := &app{
		stdout:      os.Stdout,
		stderr:      os.Stderr,
		store:       st,
		getenv:      os.Getenv,
		now:         time.Now,
		newClient:   client.New,
		openBrowser: openBrowser,
		executable:  execCommand,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := a.run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}

func (a *app) run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(a.stderr, usage)
		return 2
	}
	command, rest := args[0], args[1:]
	var err error
	switch command {
	case "login":
		err = a.login(ctx, rest)
	case "logout":
		err = a.logout(ctx, rest)
	case "clusters":
		err = a.clusters(ctx, rest)
	case "update-kubeconfig":
		err = a.updateKubeconfig(ctx, rest)
	case "credential":
		err = a.credential(ctx, rest)
	case "version", "--version":
		fmt.Fprintf(a.stdout, "kubemg %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(a.stdout, usage)
		return 0
	default:
		fmt.Fprintf(a.stderr, "kubemg: unknown command %q\n\n%s", command, usage)
		return 2
	}
	var usageErr usageError
	if errors.As(err, &usageErr) {
		fmt.Fprintf(a.stderr, "kubemg %s: %s\n", command, usageErr)
		return 2
	}
	if err != nil {
		fmt.Fprintln(a.stderr, "kubemg:", err)
		return 1
	}
	return 0
}

type usageError string

func (e usageError) Error() string { return string(e) }

func newFlags(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

func parse(flags *flag.FlagSet, args []string) error {
	if err := flags.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if flags.NArg() > 0 {
		return usageError("unexpected argument " + strconv.Quote(flags.Arg(0)))
	}
	return nil
}

// resolveServer picks the server: the flag, then KUBEMG_SERVER, then the only
// server signed in to.
func (a *app) resolveServer(flagValue string) (string, error) {
	if server := strings.TrimRight(strings.TrimSpace(flagValue), "/"); server != "" {
		return server, nil
	}
	if server := strings.TrimRight(strings.TrimSpace(a.getenv("KUBEMG_SERVER")), "/"); server != "" {
		return server, nil
	}
	sessions, err := a.store.Sessions()
	if err != nil {
		return "", err
	}
	switch len(sessions) {
	case 1:
		return sessions[0].Server, nil
	case 0:
		return "", usageError("no server given and none signed in — run kubemg login --server URL")
	}
	return "", usageError("signed in to more than one server — say which with --server")
}

// session loads a live session for a server, or says to sign in.
func (a *app) session(server string) (store.Session, *client.Client, error) {
	session, err := a.store.LoadSession(server)
	if errors.Is(err, store.ErrNoSession) {
		return store.Session{}, nil, fmt.Errorf("not signed in to %s — run: kubemg login --server %s", server, server)
	}
	if err != nil {
		return store.Session{}, nil, err
	}
	if !a.now().Before(session.ExpiresAt) {
		return store.Session{}, nil, fmt.Errorf("the session for %s has ended — run: kubemg login --server %s", server, server)
	}
	// The client is built from the stored session's own server, so the refresh
	// token only ever goes back to where it came from.
	c, err := a.newClient(session.Server, []byte(session.CAPEM))
	if err != nil {
		return store.Session{}, nil, err
	}
	return session, c, nil
}

func (a *app) login(ctx context.Context, args []string) error {
	flags := newFlags("login")
	server := flags.String("server", "", "")
	caFile := flags.String("ca-file", "", "")
	noBrowser := flags.Bool("no-browser", false, "")
	if err := parse(flags, args); err != nil {
		return err
	}
	resolved := strings.TrimRight(strings.TrimSpace(*server), "/")
	if resolved == "" {
		resolved = strings.TrimRight(strings.TrimSpace(a.getenv("KUBEMG_SERVER")), "/")
	}
	if resolved == "" {
		return usageError("--server is required, like --server https://kubemg.example.com")
	}
	var caPEM []byte
	if *caFile != "" {
		data, err := os.ReadFile(*caFile)
		if err != nil {
			return fmt.Errorf("read the CA file: %w", err)
		}
		caPEM = data
	}
	c, err := a.newClient(resolved, caPEM)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	device, err := c.StartLogin(ctx, client.ClientInfo{
		Name: "kubemg", Host: host, OS: runtime.GOOS + "/" + runtime.GOARCH, Version: version,
	})
	if err != nil {
		return fmt.Errorf("could not start the sign-in: %w", err)
	}

	fmt.Fprintf(a.stderr, "To sign in, open this page and approve the code %s:\n\n  %s\n\n",
		device.UserCode, device.VerificationURIComplete)
	if !*noBrowser && a.openBrowser != nil && safeBrowserURL(device.VerificationURIComplete) {
		if err := a.openBrowser(device.VerificationURIComplete); err == nil {
			fmt.Fprintln(a.stderr, "Opened it in your browser. Waiting for approval…")
		} else {
			fmt.Fprintln(a.stderr, "Waiting for approval…")
		}
	} else {
		fmt.Fprintln(a.stderr, "Waiting for approval…")
	}

	approved, err := c.WaitForApproval(ctx, device)
	if err != nil {
		return err
	}
	if err := a.store.SaveSession(store.Session{
		Server:       resolved,
		RefreshToken: approved.RefreshToken,
		SessionID:    approved.SessionID,
		ExpiresAt:    approved.ExpiresAt,
		Username:     approved.User.Username,
		CAPEM:        string(caPEM),
	}); err != nil {
		return fmt.Errorf("signed in, but could not save the session: %w", err)
	}
	fmt.Fprintf(a.stdout, "Signed in to %s as %s until %s.\n", resolved, approved.User.Username,
		approved.ExpiresAt.Local().Format("Mon 15:04"))
	fmt.Fprintln(a.stdout, "Next: kubemg update-kubeconfig --cluster NAME   (kubemg clusters lists them)")
	return nil
}

func (a *app) logout(ctx context.Context, args []string) error {
	flags := newFlags("logout")
	server := flags.String("server", "", "")
	if err := parse(flags, args); err != nil {
		return err
	}
	resolved, err := a.resolveServer(*server)
	if err != nil {
		return err
	}
	session, loadErr := a.store.LoadSession(resolved)
	if loadErr == nil {
		if c, err := a.newClient(session.Server, []byte(session.CAPEM)); err == nil {
			if err := c.Logout(ctx, session.RefreshToken); err != nil && !errors.Is(err, client.ErrSessionEnded) {
				// The local half still happens: a server that cannot be reached
				// must not leave the session on disk. Said, not hidden.
				fmt.Fprintf(a.stderr, "kubemg: could not end the session on the server (%v); it ends on its own at %s.\n",
					err, session.ExpiresAt.Local().Format("Mon 15:04"))
			}
		}
	}
	if err := a.store.ForgetSession(resolved); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "Signed out of %s.\n", resolved)
	return nil
}

func (a *app) clusters(ctx context.Context, args []string) error {
	flags := newFlags("clusters")
	server := flags.String("server", "", "")
	if err := parse(flags, args); err != nil {
		return err
	}
	resolved, err := a.resolveServer(*server)
	if err != nil {
		return err
	}
	session, c, err := a.session(resolved)
	if err != nil {
		return err
	}
	list, err := c.Clusters(ctx, session.RefreshToken)
	if err != nil {
		return a.explain(resolved, err)
	}
	table := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tENVIRONMENT\tROLE\tNAMESPACES\tACCESS")
	for _, cluster := range list {
		namespaces := "all"
		if len(cluster.Namespaces) > 0 {
			namespaces = strings.Join(cluster.Namespaces, ",")
		}
		access := "kubemg update-kubeconfig --cluster " + cluster.Name
		if cluster.Server == "" {
			access = "console download (direct API access)"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", cluster.Name, cluster.Environment, cluster.Role, namespaces, access)
	}
	return table.Flush()
}

func (a *app) updateKubeconfig(ctx context.Context, args []string) error {
	flags := newFlags("update-kubeconfig")
	server := flags.String("server", "", "")
	clusterArg := flags.String("cluster", "", "")
	namespace := flags.String("namespace", "", "")
	alias := flags.String("alias", "", "")
	path := flags.String("kubeconfig", "", "")
	noSetContext := flags.Bool("no-set-context", false, "")
	static := flags.Bool("static", false, "")
	ttl := flags.Duration("ttl", 0, "")
	caFile := flags.String("ca-file", "", "")
	if err := parse(flags, args); err != nil {
		return err
	}
	if strings.TrimSpace(*clusterArg) == "" {
		return usageError("--cluster is required (kubemg clusters lists them)")
	}
	if *ttl != 0 && !*static {
		return usageError("--ttl applies only with --static")
	}
	resolved, err := a.resolveServer(*server)
	if err != nil {
		return err
	}
	target, err := kubeconfig.TargetPath(*path)
	if err != nil {
		return err
	}
	file, err := kubeconfig.Load(target)
	if err != nil {
		return err
	}

	// A machine token cannot list clusters — it is bound to one — so the CI
	// path names the cluster by id and the entries are built here.
	if token := strings.TrimSpace(a.getenv("KUBEMG_TOKEN")); token != "" {
		if _, loadErr := a.store.LoadSession(resolved); errors.Is(loadErr, store.ErrNoSession) {
			var caPEM []byte
			if *caFile != "" {
				if caPEM, err = os.ReadFile(*caFile); err != nil {
					return fmt.Errorf("read the CA file: %w", err)
				}
			}
			return a.machineKubeconfig(file, resolved, *clusterArg, *namespace, *alias, caPEM, !*noSetContext)
		}
	}

	session, c, err := a.session(resolved)
	if err != nil {
		return err
	}
	list, err := c.Clusters(ctx, session.RefreshToken)
	if err != nil {
		return a.explain(resolved, err)
	}
	idx := slices.IndexFunc(list, func(cl client.Cluster) bool {
		return cl.Name == *clusterArg || strconv.FormatUint(uint64(cl.ID), 10) == *clusterArg
	})
	if idx < 0 {
		return fmt.Errorf("no cluster %q that %s can reach — kubemg clusters lists them", *clusterArg, session.Username)
	}
	cluster := list[idx]
	if cluster.Server == "" {
		note := cluster.Note
		if note == "" {
			note = "it cannot be reached through the CLI"
		}
		return fmt.Errorf("%s: %s", cluster.Name, note)
	}
	// The address goes into the kubeconfig, and kubectl sends credentials to
	// it: it has to be https whatever the server said.
	if parsed, err := url.Parse(cluster.Server); err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("the server described %s with an address that is not https (%q); refusing to write it", cluster.Name, cluster.Server)
	}
	ns := *namespace
	if ns == "" && len(cluster.Namespaces) > 0 {
		ns = cluster.Namespaces[0]
	}
	if ns != "" && len(cluster.Namespaces) > 0 && !slices.Contains(cluster.Namespaces, ns) {
		return fmt.Errorf("%s is outside your grant on %s, which covers %s", ns, cluster.Name, strings.Join(cluster.Namespaces, ", "))
	}

	contextName := cluster.ContextName
	if *static {
		rendered, err := c.Static(ctx, session.RefreshToken, cluster.ID, ns, *ttl)
		if err != nil {
			return a.explain(resolved, err)
		}
		token, err := kubeconfig.TokenFrom([]byte(rendered.Kubeconfig))
		if err != nil {
			return err
		}
		if *alias != "" {
			contextName = *alias
		}
		file.SetCluster(cluster.Name, cluster.Server, cluster.CAData)
		file.SetTokenUser(cluster.ContextName, token)
		file.SetContext(contextName, cluster.Name, cluster.ContextName, ns)
		if !*noSetContext {
			file.SetCurrentContext(contextName)
		}
		if err := file.Save(); err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "Wrote a static kubeconfig for %s to %s — its token works until %s, whatever happens to this session.\n",
			cluster.Name, file.Path(), rendered.ExpiresAt.Local().Format("Mon 2 Jan 15:04"))
		return nil
	}

	if *alias != "" {
		contextName = *alias
	}
	file.SetCluster(cluster.Name, cluster.Server, cluster.CAData)
	file.SetExecUser(cluster.ContextName, kubeconfig.Exec{
		Command:     a.executable(),
		Args:        []string{"credential", "--server", resolved, "--cluster-id", strconv.FormatUint(uint64(cluster.ID), 10)},
		InstallHint: "kubemg is needed for this context: " + docsURL,
	})
	file.SetContext(contextName, cluster.Name, cluster.ContextName, ns)
	if !*noSetContext {
		file.SetCurrentContext(contextName)
	}
	if err := file.Save(); err != nil {
		return err
	}
	current := ""
	if !*noSetContext {
		current = ", and made it the current context"
	}
	fmt.Fprintf(a.stdout, "Added context %s to %s%s.\n", contextName, file.Path(), current)
	return nil
}

// machineKubeconfig writes the entries for a machine account, which has no
// session to list clusters with: the cluster is named by id, and the proxy
// address follows from the server.
func (a *app) machineKubeconfig(
	file *kubeconfig.File, server, clusterArg, namespace, alias string, caPEM []byte, setCurrent bool,
) error {
	id, err := strconv.ParseUint(clusterArg, 10, 64)
	if err != nil || id == 0 {
		return usageError("with KUBEMG_TOKEN, --cluster must be the cluster's numeric id (a machine token is bound to one cluster)")
	}
	name := "kubemg-" + store.Key(server) + "-" + clusterArg
	contextName := name
	if alias != "" {
		contextName = alias
	}
	caData := ""
	if len(caPEM) > 0 {
		caData = base64.StdEncoding.EncodeToString(caPEM)
	}
	file.SetCluster(name, fmt.Sprintf("%s/api/v1/clusters/%d/proxy", server, id), caData)
	file.SetExecUser(name, kubeconfig.Exec{
		Command:     a.executable(),
		Args:        []string{"credential", "--server", server, "--cluster-id", clusterArg},
		InstallHint: "kubemg is needed for this context: " + docsURL,
	})
	file.SetContext(contextName, name, name, namespace)
	if setCurrent {
		file.SetCurrentContext(contextName)
	}
	if err := file.Save(); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "Added context %s to %s, using KUBEMG_TOKEN.\n", contextName, file.Path())
	return nil
}

// execCredential is what a client-go exec plugin prints.
type execCredential struct {
	APIVersion string               `json:"apiVersion"`
	Kind       string               `json:"kind"`
	Status     execCredentialStatus `json:"status"`
}

type execCredentialStatus struct {
	Token               string `json:"token"`
	ExpirationTimestamp string `json:"expirationTimestamp,omitempty"`
}

// credential is the exec plugin. It never prompts — kubectl runs it with no
// terminal behind it in scripts — and an ended session is an error on stderr
// with the command to run, which is what kubectl shows the person.
func (a *app) credential(ctx context.Context, args []string) error {
	flags := newFlags("credential")
	server := flags.String("server", "", "")
	clusterID := flags.Uint("cluster-id", 0, "")
	if err := parse(flags, args); err != nil {
		return err
	}
	resolved := strings.TrimRight(strings.TrimSpace(*server), "/")
	if resolved == "" || *clusterID == 0 {
		return usageError("--server and --cluster-id are required")
	}
	id := uint(*clusterID)

	if token := strings.TrimSpace(a.getenv("KUBEMG_TOKEN")); token != "" {
		return a.printCredential(token, time.Time{})
	}

	if cached, ok := a.store.LoadCredential(resolved, id); ok && cached.ExpiresAt.Sub(a.now()) > refreshAhead {
		return a.printCredential(cached.Token, cached.ExpiresAt)
	}
	session, c, err := a.session(resolved)
	if err != nil {
		return err
	}
	minted, err := c.Credential(ctx, session.RefreshToken, id)
	if err != nil {
		a.store.ForgetCredential(resolved, id)
		return a.explain(resolved, err)
	}
	if err := a.store.SaveCredential(resolved, id, store.Credential{Token: minted.Token, ExpiresAt: minted.ExpiresAt}); err != nil {
		// A cache that cannot be written costs a mint per kubectl call, not
		// the call itself.
		fmt.Fprintln(a.stderr, "kubemg: could not cache the credential:", err)
	}
	return a.printCredential(minted.Token, minted.ExpiresAt)
}

func (a *app) printCredential(token string, expiresAt time.Time) error {
	out := execCredential{
		APIVersion: kubeconfig.ExecAPIVersion,
		Kind:       "ExecCredential",
		Status:     execCredentialStatus{Token: token},
	}
	if !expiresAt.IsZero() {
		out.Status.ExpirationTimestamp = expiresAt.UTC().Format(time.RFC3339)
	}
	return json.NewEncoder(a.stdout).Encode(out)
}

// explain turns an ended session into the one instruction that fixes it.
func (a *app) explain(server string, err error) error {
	if errors.Is(err, client.ErrSessionEnded) {
		return fmt.Errorf("%w — run: kubemg login --server %s", err, server)
	}
	return err
}

// execCommand is what the exec entry runs: plain `kubemg` when that is this
// binary on the PATH — so an upgrade in place keeps working — and the absolute
// path otherwise.
func execCommand() string {
	self, err := os.Executable()
	if err != nil {
		return "kubemg"
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	if onPath, err := exec.LookPath("kubemg"); err == nil {
		if resolved, err := filepath.EvalSymlinks(onPath); err == nil && resolved == self {
			return "kubemg"
		}
	}
	return self
}

// safeBrowserURL accepts only an https address with a host before it is handed
// to the platform's opener. The URL comes from the server, and `open`,
// `xdg-open` and Windows' protocol handler will all launch whatever a file:,
// smb: or custom scheme names — so anything else is printed for the person to
// follow, never opened.
func safeBrowserURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil &&
		!strings.ContainsAny(raw, " \t\n\r\"'`<>^|")
}

// openBrowser opens a URL the platform's way. With no display it does
// nothing and says so by failing, and the printed URL is the way in.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return errors.New("no display")
		}
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
