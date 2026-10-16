// Command kubemg signs a command line in to a kubemg server and keeps kubectl
// supplied with short-lived credentials for its agent-mode clusters.
//
// It is aws eks update-kubeconfig's shape, on purpose: the CLI writes an exec
// entry into the kubeconfig and steps aside. kubectl, k9s, helm and every IDE
// plugin keep working as they do; each asks `kubemg credential` for a token
// when it needs one. It is not a kubectl wrapper and never will be.
package main

import (
	"bufio"
	"context"
	"crypto/x509"
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
  kubemg clusters [--server URL] [-o table|json]       (also: kubemg cluster list)
  kubemg update-kubeconfig [NAME|ID ...] [--all] [--server URL] [--namespace NS]
                           [--alias NAME] [--kubeconfig FILE] [--no-set-context]
                           [--static [--ttl 8h]] [--ca-file FILE  (with KUBEMG_TOKEN)]
  kubemg logout [--server URL]
  kubemg credential --server URL --cluster-id N     (run by kubectl, not by you)
  kubemg version

update-kubeconfig with no cluster named lists the ones you can reach and asks
which to add; --all adds every one. --cluster NAME is the same as naming it.

The first login to a server whose certificate this system does not trust shows
its SHA-256 fingerprint and asks before trusting it; --ca-file trusts a CA
without asking. Verification is never turned off.

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
	// peerChain reads the chain a server presents, unverified, so that login
	// can show a certificate this system does not trust before pinning it.
	peerChain func(ctx context.Context, server string) ([]*x509.Certificate, error)
	// stdin and interactive are the person at the keyboard: questions are asked
	// only when there is one, never of a script.
	stdin       io.Reader
	interactive func() bool
	lines       *bufio.Reader
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
		peerChain:   client.PeerChain,
		stdin:       os.Stdin,
		interactive: stdinIsTerminal,
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
	case "cluster":
		// `kubemg cluster list` is the noun-verb spelling of `kubemg clusters`.
		if len(rest) == 0 || (rest[0] != "list" && rest[0] != "ls") {
			fmt.Fprint(a.stderr, "Usage: kubemg cluster list [--server URL] [-o table|json]\n")
			return 2
		}
		err = a.clusters(ctx, rest[1:])
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
	} else {
		trusted, err := a.trustServer(ctx, resolved)
		if err != nil {
			return err
		}
		caPEM = trusted
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
	fmt.Fprintln(a.stdout, "Next: kubemg update-kubeconfig   (lists your clusters and asks which to add)")
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
	output := flags.String("output", "table", "")
	flags.StringVar(output, "o", "table", "")
	if err := parse(flags, args); err != nil {
		return err
	}
	if *output != "table" && *output != "json" {
		return usageError("--output is table or json")
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
	if *output == "json" {
		type row struct {
			ID             uint     `json:"id"`
			Name           string   `json:"name"`
			Environment    string   `json:"environment,omitempty"`
			ConnectionMode string   `json:"connection_mode"`
			Role           string   `json:"role"`
			Namespaces     []string `json:"namespaces"`
			CLI            bool     `json:"cli"`
			Note           string   `json:"note,omitempty"`
		}
		rows := make([]row, 0, len(list))
		for _, cluster := range list {
			namespaces := cluster.Namespaces
			if namespaces == nil {
				namespaces = []string{}
			}
			rows = append(rows, row{
				ID: cluster.ID, Name: cluster.Name, Environment: cluster.Environment,
				ConnectionMode: cluster.ConnectionMode, Role: cluster.Role, Namespaces: namespaces,
				CLI: cluster.Server != "", Note: cluster.Note,
			})
		}
		encoder := json.NewEncoder(a.stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(rows)
	}
	if len(list) == 0 {
		fmt.Fprintf(a.stdout, "%s can reach no cluster on %s.\n", session.Username, resolved)
		return nil
	}
	table := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tENVIRONMENT\tROLE\tNAMESPACES\tACCESS")
	for _, cluster := range list {
		access := "kubemg update-kubeconfig " + cluster.Name
		if cluster.Server == "" {
			access = "console download (direct API access)"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", cluster.Name, cluster.Environment, cluster.Role, namespacesWord(cluster), access)
	}
	return table.Flush()
}

func namespacesWord(cluster client.Cluster) string {
	if len(cluster.Namespaces) == 0 {
		return "all"
	}
	return strings.Join(cluster.Namespaces, ",")
}

// entryOptions is what update-kubeconfig was asked for, applied to each
// cluster it adds.
type entryOptions struct {
	namespace, alias string
	static           bool
	ttl              time.Duration
}

func (a *app) updateKubeconfig(ctx context.Context, args []string) error {
	flags := newFlags("update-kubeconfig")
	server := flags.String("server", "", "")
	clusterArg := flags.String("cluster", "", "")
	all := flags.Bool("all", false, "")
	namespace := flags.String("namespace", "", "")
	alias := flags.String("alias", "", "")
	path := flags.String("kubeconfig", "", "")
	noSetContext := flags.Bool("no-set-context", false, "")
	static := flags.Bool("static", false, "")
	ttl := flags.Duration("ttl", 0, "")
	caFile := flags.String("ca-file", "", "")
	names, err := parseInterspersed(flags, args)
	if err != nil {
		return err
	}
	if name := strings.TrimSpace(*clusterArg); name != "" {
		names = append([]string{name}, names...)
	}
	if *all && len(names) > 0 {
		return usageError("--all takes no cluster names")
	}
	if *ttl != 0 && !*static {
		return usageError("--ttl applies only with --static")
	}
	if *alias != "" && (*all || len(names) != 1) {
		return usageError("--alias names one context — give exactly one cluster")
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
			if *all || len(names) != 1 {
				return usageError("with KUBEMG_TOKEN, name exactly one cluster by its numeric id (a machine token is bound to one cluster)")
			}
			var caPEM []byte
			if *caFile != "" {
				if caPEM, err = os.ReadFile(*caFile); err != nil {
					return fmt.Errorf("read the CA file: %w", err)
				}
			}
			return a.machineKubeconfig(file, resolved, names[0], *namespace, *alias, caPEM, !*noSetContext)
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
	selected, err := a.selectClusters(list, names, *all, resolved, session.Username)
	if err != nil || len(selected) == 0 {
		return err
	}

	opts := entryOptions{namespace: *namespace, alias: *alias, static: *static, ttl: *ttl}
	var contexts, notes []string
	for _, cluster := range selected {
		contextName, note, err := a.addCluster(ctx, file, resolved, session, c, cluster, opts)
		if err != nil {
			return err
		}
		contexts = append(contexts, contextName)
		if note != "" {
			notes = append(notes, note)
		}
	}
	// One cluster becomes the current context, as aws eks does; several leave
	// the current one alone rather than picking one of them for the person.
	setCurrent := !*noSetContext && len(contexts) == 1
	if setCurrent {
		file.SetCurrentContext(contexts[0])
	}
	if err := file.Save(); err != nil {
		return err
	}
	for _, note := range notes {
		fmt.Fprintln(a.stdout, note)
	}
	if *static {
		return nil
	}
	switch {
	case setCurrent:
		fmt.Fprintf(a.stdout, "Added context %s to %s, and made it the current context.\n", contexts[0], file.Path())
	case len(contexts) == 1:
		fmt.Fprintf(a.stdout, "Added context %s to %s.\n", contexts[0], file.Path())
	default:
		fmt.Fprintf(a.stdout, "Added %d contexts to %s:\n", len(contexts), file.Path())
		for _, name := range contexts {
			fmt.Fprintf(a.stdout, "  %s\n", name)
		}
		fmt.Fprintf(a.stdout, "Switch with: kubectl config use-context %s\n", contexts[0])
	}
	return nil
}

// selectClusters turns what update-kubeconfig was given into clusters: the
// names asked for, every one the CLI can reach with --all, or — with neither,
// and a person at the keyboard — the ones they pick from a numbered list.
func (a *app) selectClusters(list []client.Cluster, names []string, all bool, server, username string) ([]client.Cluster, error) {
	var reachable []client.Cluster
	for _, cluster := range list {
		if cluster.Server != "" {
			reachable = append(reachable, cluster)
		}
	}
	if all {
		if len(reachable) == 0 {
			return nil, fmt.Errorf("%s can reach no cluster through the CLI on %s", username, server)
		}
		for _, cluster := range list {
			if cluster.Server == "" {
				fmt.Fprintf(a.stderr, "Skipped %s: %s\n", cluster.Name, directNote(cluster))
			}
		}
		return reachable, nil
	}
	if len(names) > 0 {
		var out []client.Cluster
		for _, name := range names {
			idx := slices.IndexFunc(list, func(cl client.Cluster) bool {
				return cl.Name == name || strconv.FormatUint(uint64(cl.ID), 10) == name
			})
			if idx < 0 {
				return nil, fmt.Errorf("no cluster %q that %s can reach — kubemg clusters lists them", name, username)
			}
			cluster := list[idx]
			if cluster.Server == "" {
				return nil, fmt.Errorf("%s: %s", cluster.Name, directNote(cluster))
			}
			if !slices.ContainsFunc(out, func(cl client.Cluster) bool { return cl.ID == cluster.ID }) {
				out = append(out, cluster)
			}
		}
		return out, nil
	}
	if !a.interactive() {
		return nil, usageError("name a cluster (kubemg clusters lists them), or pass --all")
	}
	if len(reachable) == 0 {
		return nil, fmt.Errorf("%s can reach no cluster through the CLI on %s", username, server)
	}

	fmt.Fprintf(a.stderr, "Clusters %s can reach on %s:\n\n", username, server)
	table := tabwriter.NewWriter(a.stderr, 0, 0, 2, ' ', 0)
	for i, cluster := range reachable {
		fmt.Fprintf(table, "  %d)\t%s\t%s\t%s\t%s\n", i+1, cluster.Name, cluster.Environment, cluster.Role, namespacesWord(cluster))
	}
	for _, cluster := range list {
		if cluster.Server == "" {
			fmt.Fprintf(table, "  \t%s\t%s\t\t%s\n", cluster.Name, cluster.Environment, "not through the CLI — "+directNote(cluster))
		}
	}
	_ = table.Flush()
	answer, err := a.ask("\nAdd which? Numbers like 1,3 — or all. Enter adds none: ")
	if err != nil {
		return nil, err
	}
	if answer == "" {
		fmt.Fprintln(a.stderr, "Nothing added.")
		return nil, nil
	}
	if answer == "all" || answer == "a" {
		return reachable, nil
	}
	var out []client.Cluster
	for _, field := range strings.FieldsFunc(answer, func(r rune) bool { return r == ',' || r == ' ' }) {
		n, err := strconv.Atoi(field)
		if err != nil || n < 1 || n > len(reachable) {
			return nil, usageError(fmt.Sprintf("%q is not one of the numbers above", field))
		}
		if cluster := reachable[n-1]; !slices.ContainsFunc(out, func(cl client.Cluster) bool { return cl.ID == cluster.ID }) {
			out = append(out, cluster)
		}
	}
	return out, nil
}

func directNote(cluster client.Cluster) string {
	if cluster.Note != "" {
		return cluster.Note
	}
	return "it cannot be reached through the CLI"
}

// addCluster writes one cluster's entries into the loaded file, unsaved. It
// returns the context's name and, for a static entry, the line that says how
// long its token lives.
func (a *app) addCluster(
	ctx context.Context, file *kubeconfig.File, server string, session store.Session, c *client.Client,
	cluster client.Cluster, opts entryOptions,
) (string, string, error) {
	// The address goes into the kubeconfig, and kubectl sends credentials to
	// it: it has to be https whatever the server said.
	if parsed, err := url.Parse(cluster.Server); err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", "", fmt.Errorf("the server described %s with an address that is not https (%q); refusing to write it", cluster.Name, cluster.Server)
	}
	ns := opts.namespace
	if ns == "" && len(cluster.Namespaces) > 0 {
		ns = cluster.Namespaces[0]
	}
	if ns != "" && len(cluster.Namespaces) > 0 && !slices.Contains(cluster.Namespaces, ns) {
		return "", "", fmt.Errorf("%s is outside your grant on %s, which covers %s", ns, cluster.Name, strings.Join(cluster.Namespaces, ", "))
	}
	// kubectl must trust what this CLI trusts. The server names its own CA
	// when it serves a certificate of its own; when it names none, the CA this
	// session was signed in with — a --ca-file, or a certificate pinned at
	// login — is the one kubectl needs too.
	caData := cluster.CAData
	if caData == "" && session.CAPEM != "" {
		caData = base64.StdEncoding.EncodeToString([]byte(session.CAPEM))
	}

	// Every name is under the server's host as you typed it, with the server's
	// own part reduced to safe characters: a server cannot name an entry into
	// somebody else's — and the ownership marker refuses anything it did not
	// write even if it tried.
	clusterEntry := entryBase(server) + "/" + safeName(cluster.Name)
	userEntry := clusterEntry + "/" + safeName(session.Username)
	contextName := clusterEntry
	if opts.alias != "" {
		contextName = opts.alias
	}
	if opts.static {
		rendered, err := c.Static(ctx, session.RefreshToken, cluster.ID, ns, opts.ttl)
		if err != nil {
			return "", "", a.explain(server, err)
		}
		token, err := kubeconfig.TokenFrom([]byte(rendered.Kubeconfig))
		if err != nil {
			return "", "", err
		}
		if err := writeEntries(file,
			func() error { return file.SetCluster(server, clusterEntry, cluster.Server, caData) },
			func() error { return file.SetTokenUser(server, userEntry, token) },
			func() error { return file.SetContext(server, contextName, clusterEntry, userEntry, ns) },
		); err != nil {
			return "", "", err
		}
		return contextName, fmt.Sprintf("Wrote a static kubeconfig for %s to %s — its token works until %s, whatever happens to this session.",
			cluster.Name, file.Path(), rendered.ExpiresAt.Local().Format("Mon 2 Jan 15:04")), nil
	}

	exec := kubeconfig.Exec{
		Command:     a.executable(),
		Args:        []string{"credential", "--server", server, "--cluster-id", strconv.FormatUint(uint64(cluster.ID), 10)},
		InstallHint: "kubemg is needed for this context: " + docsURL,
	}
	if err := writeEntries(file,
		func() error { return file.SetCluster(server, clusterEntry, cluster.Server, caData) },
		func() error { return file.SetExecUser(server, userEntry, exec) },
		func() error { return file.SetContext(server, contextName, clusterEntry, userEntry, ns) },
	); err != nil {
		return "", "", err
	}
	return contextName, "", nil
}

// parseInterspersed parses flags that may come before, between or after the
// positional arguments, and returns the positional ones.
func parseInterspersed(flags *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := flags.Parse(args); err != nil {
			return nil, usageError(err.Error())
		}
		args = flags.Args()
		if len(args) == 0 {
			return positional, nil
		}
		if args[0] == "--" {
			return append(positional, args[1:]...), nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

// writeEntries runs the entry writes in order and stops at the first refusal,
// before anything is saved — a half-written set never reaches the disk.
func writeEntries(file *kubeconfig.File, writes ...func() error) error {
	for _, write := range writes {
		if err := write(); err != nil {
			var foreign *kubeconfig.ForeignEntryError
			if errors.As(err, &foreign) {
				return fmt.Errorf("%s: %w", file.Path(), err)
			}
			return err
		}
	}
	return nil
}

// entryBase is the prefix of every entry name kubemg writes: the server's host
// as the person typed it at login — never a name the server chose.
func entryBase(server string) string {
	if parsed, err := url.Parse(server); err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return safeName(server)
}

// safeName reduces a name the server supplied to characters that cannot
// change what an entry name means.
func safeName(name string) string {
	out := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '-'
	}, name)
	if out == "" {
		return "unnamed"
	}
	return out
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
	name := entryBase(server) + "/cluster-" + clusterArg
	user := name + "/machine"
	contextName := name
	if alias != "" {
		contextName = alias
	}
	caData := ""
	if len(caPEM) > 0 {
		caData = base64.StdEncoding.EncodeToString(caPEM)
	}
	exec := kubeconfig.Exec{
		Command:     a.executable(),
		Args:        []string{"credential", "--server", server, "--cluster-id", clusterArg},
		InstallHint: "kubemg is needed for this context: " + docsURL,
	}
	if err := writeEntries(file,
		func() error {
			return file.SetCluster(server, name, fmt.Sprintf("%s/api/v1/clusters/%d/proxy", server, id), caData)
		},
		func() error { return file.SetExecUser(server, user, exec) },
		func() error { return file.SetContext(server, contextName, name, user, namespace) },
	); err != nil {
		return err
	}
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
