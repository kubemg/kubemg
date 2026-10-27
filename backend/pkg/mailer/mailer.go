// Package mailer sends KubeMG's own mail — today, password expiry reminders —
// through one SMTP relay an administrator configures.
//
// It is the standard library on purpose: net/smtp speaks the three things a
// relay needs (STARTTLS, implicit TLS, AUTH PLAIN) and nothing about a
// reminder justifies a dependency. Two rules are not negotiable here. A relay
// configured for STARTTLS that does not offer it is refused, never fallen back
// to plain text; and a password is never sent over an unencrypted connection
// to anything but the local host (net/smtp's PlainAuth enforces that too, and
// Validate says so before anything is dialled).
package mailer

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Connection security, as stored.
const (
	SecurityStartTLS = "starttls"
	SecurityTLS      = "tls"
	SecurityNone     = "none"
)

// Timeouts. A reminder is never urgent, and a relay that does not answer must
// not hold a background pass or a test request for long.
const (
	dialTimeout    = 10 * time.Second
	sessionTimeout = 30 * time.Second
)

// Config is one relay.
type Config struct {
	Host     string
	Port     int
	Security string
	Username string
	Password string
	From     string
}

// Validate refuses a relay that cannot work, by name, before it is dialled.
func (c Config) Validate() error {
	host := strings.TrimSpace(c.Host)
	switch {
	case host == "":
		return errors.New("the SMTP host is required")
	case strings.Contains(host, "://"):
		return errors.New("the SMTP host is a host name, without a scheme")
	case strings.ContainsAny(host, " /\r\n\t"):
		return errors.New("the SMTP host is a host name or address only")
	}
	if _, _, err := net.SplitHostPort(host); err == nil && !strings.HasPrefix(host, "[") {
		return errors.New("the SMTP host takes no port — set the port on its own")
	}
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("the SMTP port must be between 1 and 65535")
	}
	switch c.Security {
	case SecurityStartTLS, SecurityTLS, SecurityNone:
	default:
		return fmt.Errorf("security must be %s, %s or %s", SecurityStartTLS, SecurityTLS, SecurityNone)
	}
	if strings.ContainsAny(c.Username, "\r\n") {
		return errors.New("the SMTP username cannot contain a line break")
	}
	if c.Security == SecurityNone && c.Username != "" && !isLoopback(host) {
		return errors.New("a password is never sent over an unencrypted connection — " +
			"use starttls or tls, or a relay on this host that needs no login")
	}
	if _, err := parseAddress(c.From); err != nil {
		return fmt.Errorf("the sender address: %w", err)
	}
	return nil
}

// Message is one mail to one person.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Send delivers msg through the relay. The error says what the relay answered,
// in its own words, because that is what an administrator debugging a relay
// needs to read.
func Send(ctx context.Context, config Config, msg Message) error {
	if err := config.Validate(); err != nil {
		return err
	}
	from, _ := parseAddress(config.From)
	to, err := parseAddress(msg.To)
	if err != nil {
		return fmt.Errorf("the recipient address: %w", err)
	}
	data, err := compose(from, to, msg.Subject, msg.Body, time.Now())
	if err != nil {
		return err
	}

	host := strings.TrimSpace(config.Host)
	address := net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(config.Port))
	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("could not reach %s: %w", address, err)
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(sessionTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	serverName := strings.Trim(host, "[]")
	tlsConfig := &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12}
	if config.Security == SecurityTLS {
		tlsConn := tls.Client(conn, tlsConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("TLS with %s failed: %w", address, err)
		}
		conn = tlsConn
	}

	client, err := smtp.NewClient(conn, serverName)
	if err != nil {
		return fmt.Errorf("%s did not answer as an SMTP server: %w", address, err)
	}
	defer func() { _ = client.Close() }()

	if config.Security == SecurityStartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("%s does not offer STARTTLS; choose tls or none explicitly rather than "+
				"have mail fall back to plain text", address)
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("STARTTLS with %s failed: %w", address, err)
		}
	}
	if config.Username != "" {
		if ok, _ := client.Extension("AUTH"); !ok {
			return fmt.Errorf("%s does not accept a login; clear the username to send without one", address)
		}
		if err := client.Auth(smtp.PlainAuth("", config.Username, config.Password, serverName)); err != nil {
			return fmt.Errorf("the relay refused the login: %w", err)
		}
	}
	if err := client.Mail(from.Address); err != nil {
		return fmt.Errorf("the relay refused the sender: %w", err)
	}
	if err := client.Rcpt(to.Address); err != nil {
		return fmt.Errorf("the relay refused the recipient: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("the relay refused the message: %w", err)
	}
	if _, err := writer.Write(data); err != nil {
		return fmt.Errorf("could not send the message: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("the relay refused the message: %w", err)
	}
	_ = client.Quit()
	return nil
}

// parseAddress reads one address, refusing a list or anything carrying a line
// break — a header is assembled from it.
func parseAddress(raw string) (*mail.Address, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("an address is required")
	}
	if strings.ContainsAny(raw, "\r\n") {
		return nil, errors.New("an address cannot contain a line break")
	}
	address, err := mail.ParseAddress(raw)
	if err != nil {
		return nil, fmt.Errorf("%q is not an email address", raw)
	}
	return address, nil
}

// compose renders a plain-text UTF-8 message. The subject is the only header
// built from text, and it is Q-encoded with line breaks refused, so nothing a
// caller passes becomes a header of its own.
func compose(from, to *mail.Address, subject, body string, now time.Time) ([]byte, error) {
	if strings.ContainsAny(subject, "\r\n") {
		return nil, errors.New("a subject cannot contain a line break")
	}
	id := make([]byte, 12)
	if _, err := rand.Read(id); err != nil {
		return nil, fmt.Errorf("generate message id: %w", err)
	}
	domain := "kubemg"
	if at := strings.LastIndex(from.Address, "@"); at >= 0 {
		domain = from.Address[at+1:]
	}

	var b strings.Builder
	header := func(name, value string) { b.WriteString(name + ": " + value + "\r\n") }
	header("From", from.String())
	header("To", to.String())
	header("Subject", mime.QEncoding.Encode("utf-8", subject))
	header("Date", now.Format(time.RFC1123Z))
	header("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	header("MIME-Version", "1.0")
	header("Content-Type", `text/plain; charset="utf-8"`)
	header("Content-Transfer-Encoding", "quoted-printable")
	header("Auto-Submitted", "auto-generated")
	b.WriteString("\r\n")

	qp := quotedprintable.NewWriter(&b)
	normalized := strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
	if _, err := qp.Write([]byte(normalized)); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}
	b.WriteString("\r\n")
	return []byte(b.String()), nil
}

func isLoopback(host string) bool {
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
