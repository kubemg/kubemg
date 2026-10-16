package main

import (
	"bufio"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/kubemg/kubemg/cli/internal/client"
	"github.com/kubemg/kubemg/cli/internal/store"
)

// trustServer decides what login trusts when no --ca-file was given, the way
// ssh does on first contact: the system's roots if they vouch for the server;
// else the certificate pinned at an earlier login if it still matches; else
// the certificate the server presents — shown with its fingerprint, and pinned
// only when the person at the keyboard says yes. Verification is never turned
// off: what is pinned is the only thing trusted afterwards, by this CLI and by
// every kubeconfig entry it writes.
//
// It returns the PEM to keep with the session, or nil for the system's roots.
func (a *app) trustServer(ctx context.Context, server string) ([]byte, error) {
	chain, err := a.peerChain(ctx, server)
	if err != nil {
		return nil, err
	}
	if client.VerifyChain(chain, server, nil) == nil {
		return nil, nil
	}
	parsed, _ := url.Parse(server)
	if err := chain[0].VerifyHostname(parsed.Hostname()); err != nil {
		// A certificate for another name is not something to pin: kubectl
		// would refuse it anyway, and agreeing to it is how an interception
		// gets waved through.
		return nil, fmt.Errorf("the certificate %s presents is not for %s: %w", server, parsed.Hostname(), err)
	}

	previous, err := a.store.LoadSession(server)
	changed := false
	switch {
	case err == nil && previous.CAPEM != "":
		if client.VerifyChain(chain, server, []byte(previous.CAPEM)) == nil {
			return []byte(previous.CAPEM), nil
		}
		changed = true
	case err != nil && !errors.Is(err, store.ErrNoSession):
		return nil, err
	}

	anchor := client.Anchor(chain)
	candidate := client.PEM(anchor)
	if err := client.VerifyChain(chain, server, candidate); err != nil {
		return nil, fmt.Errorf("the certificate %s presents cannot be trusted, even pinned: %w", server, err)
	}
	fingerprint := client.Fingerprint(anchor)
	if !a.interactive() {
		return nil, fmt.Errorf("the certificate %s presents is not trusted by this system (SHA-256 %s) — "+
			"pass --ca-file, or run kubemg login in a terminal to check the fingerprint and confirm it", server, fingerprint)
	}

	if changed {
		fmt.Fprintf(a.stderr, "WARNING: the certificate %s presents is not the one you trusted at your last sign-in.\n"+
			"If nobody replaced it, somebody may be intercepting the connection — stop and ask your administrator.\n\n", server)
	} else {
		fmt.Fprintf(a.stderr, "The certificate %s presents is not trusted by this system.\n\n", server)
	}
	describeCertificate(a.stderr, anchor, fingerprint)
	fmt.Fprint(a.stderr, "\nCompare the fingerprint with the one the console shows under My credentials → Use the command line,\n"+
		"or the one your administrator gave you.\n\n")
	answer, err := a.ask(fmt.Sprintf("Trust this certificate for %s? [y/N] ", server))
	if err != nil {
		return nil, err
	}
	if answer != "y" && answer != "yes" {
		return nil, errors.New("the certificate was not trusted; nothing was saved")
	}
	fmt.Fprintln(a.stderr)
	return candidate, nil
}

func describeCertificate(w io.Writer, cert *x509.Certificate, fingerprint string) {
	issuer := cert.Issuer.String()
	if issuer == cert.Subject.String() {
		issuer += " (self-signed)"
	}
	fmt.Fprintf(w, "  Subject:     %s\n", cert.Subject)
	fmt.Fprintf(w, "  Issued by:   %s\n", issuer)
	fmt.Fprintf(w, "  Valid until: %s\n", cert.NotAfter.Local().Format("2 Jan 2006"))
	fmt.Fprintf(w, "  SHA-256:     %s\n", fingerprint)
}

// ask prints a question on stderr and reads one line, lower-cased and trimmed.
func (a *app) ask(question string) (string, error) {
	fmt.Fprint(a.stderr, question)
	if a.lines == nil {
		a.lines = bufio.NewReader(a.stdin)
	}
	line, err := a.lines.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", errors.New("no answer was given")
	}
	return strings.ToLower(strings.TrimSpace(line)), nil
}

// stdinIsTerminal is whether a person can be asked something. A terminal is a
// character device — but so is the null device a script redirects stdin from,
// which is told apart by being the same file.
func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if null, err := os.Stat(os.DevNull); err == nil && os.SameFile(info, null) {
		return false
	}
	return true
}
