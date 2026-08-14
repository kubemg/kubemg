package auth

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * LDAP.
 *
 * The odd one out: there is no redirect and no assertion, KubeMG is handed the
 * password and asks the directory to check it. That means the order of
 * operations matters more than usual, and it is this:
 *
 *   1. bind as the service account (or anonymously),
 *   2. find the user's entry and read its groups,
 *   3. bind as the user with the supplied password — last.
 *
 * The authentication bind is last because it replaces the connection's identity:
 * doing it first would leave the searches running as a user who may not be able
 * to read their own group memberships, and doing it in the middle would mean
 * re-binding as the service account afterwards, which is a second place to get
 * the credentials wrong.
 *
 * The empty password is refused explicitly. An LDAP bind with a DN and no
 * password is not a failed authentication — it is an *unauthenticated bind*,
 * which the directory answers with success, and a login form that permits it
 * accepts any username with a blank password.
 */

// ldapTimeout bounds the whole exchange. A directory that is slow to answer is
// indistinguishable from one that is down, and a login form must not hang on it.
const ldapTimeout = 12 * time.Second

// ErrLDAPCredentials is an invalid username or password, kept distinct from a
// directory that could not be reached so the two are not reported as the same
// thing to the person signing in.
var ErrLDAPCredentials = errors.New("invalid credentials")

// LDAPAuthenticate checks a username and password against a directory and reads
// back the identity, groups included.
func LDAPAuthenticate(
	ctx context.Context, config *db.SSOProviderConfig, username, password string,
) (db.SSOIdentity, error) {
	if config == nil || config.Protocol != db.ProtocolLDAP {
		return db.SSOIdentity{}, errors.New("provider is not an LDAP provider")
	}
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		// See the note above: an empty password is an unauthenticated bind, so
		// it never reaches the directory.
		return db.SSOIdentity{}, ErrLDAPCredentials
	}

	conn, err := dialLDAP(ctx, config)
	if err != nil {
		return db.SSOIdentity{}, err
	}
	defer conn.Close()

	if err := serviceBind(conn, config); err != nil {
		return db.SSOIdentity{}, err
	}

	entry, err := findUser(conn, config, username)
	if err != nil {
		return db.SSOIdentity{}, err
	}

	groups := entry.GetAttributeValues(config.LDAPGroupAttribute)
	if len(groups) == 0 && config.LDAPGroupFilter != "" {
		// A directory that keeps membership on the group entry rather than on
		// the user — plain OpenLDAP with groupOfNames — needs the reverse
		// search, and it has to happen while the service account is still bound.
		found, err := findGroups(conn, config, entry.DN)
		if err != nil {
			return db.SSOIdentity{}, err
		}
		groups = found
	}

	// Last: the bind that actually authenticates.
	if err := conn.Bind(entry.DN, password); err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
			return db.SSOIdentity{}, ErrLDAPCredentials
		}
		return db.SSOIdentity{}, fmt.Errorf("authenticate against the directory: %w", err)
	}

	resolved := strings.TrimSpace(entry.GetAttributeValue(config.LDAPUserAttribute))
	if resolved == "" {
		resolved = username
	}

	return db.SSOIdentity{
		// The DN is the directory's own stable handle for the entry — the
		// closest LDAP has to a subject claim.
		ExternalID: entry.DN,
		Username:   resolved,
		Email:      strings.TrimSpace(entry.GetAttributeValue(config.LDAPEmailAttribute)),
		Groups:     dedupe(groupNames(groups, config.LDAPGroupNameAttribute)),
	}, nil
}

// CheckLDAP dials the directory and binds as the service account, which is what
// an operator wants confirmed before they save a provider nobody has used yet.
func CheckLDAP(ctx context.Context, config *db.SSOProviderConfig) (string, error) {
	conn, err := dialLDAP(ctx, config)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	if err := serviceBind(conn, config); err != nil {
		return "", err
	}
	if strings.TrimSpace(config.LDAPBaseDN) == "" {
		return "", errors.New("this provider has no base DN to search")
	}

	// A bind proves the credentials; a search proves the base DN is real, which
	// is the other half of what makes a login work.
	search := ldap.NewSearchRequest(
		config.LDAPBaseDN, ldap.ScopeBaseObject, ldap.NeverDerefAliases,
		1, int(ldapTimeout.Seconds()), false,
		"(objectClass=*)", []string{"dn"}, nil,
	)
	if _, err := conn.Search(search); err != nil {
		return "", fmt.Errorf("search %s: %w", config.LDAPBaseDN, err)
	}

	who := "anonymously"
	if config.LDAPBindDN != "" {
		who = "as " + config.LDAPBindDN
	}
	return fmt.Sprintf("Bound %s; base DN %s is readable", who, config.LDAPBaseDN), nil
}

func dialLDAP(ctx context.Context, config *db.SSOProviderConfig) (*ldap.Conn, error) {
	host := strings.TrimSpace(config.LDAPHost)
	if host == "" {
		return nil, errors.New("this provider has no LDAP host")
	}
	port := config.LDAPPort
	if port <= 0 {
		port = db.DefaultLDAPPort(config.LDAPUseTLS)
	}

	scheme := "ldap"
	if config.LDAPUseTLS {
		scheme = "ldaps"
	}
	address := (&url.URL{Scheme: scheme, Host: net.JoinHostPort(host, fmt.Sprint(port))}).String()

	tlsConfig := &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: config.LDAPSkipVerify, //nolint:gosec // operator's explicit choice, surfaced as a warning in the UI
		MinVersion:         tls.VersionTLS12,
	}

	conn, err := ldap.DialURL(
		address,
		ldap.DialWithDialer(&net.Dialer{Timeout: ldapTimeout}),
		ldap.DialWithTLSConfig(tlsConfig),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", address, err)
	}
	// The request context can be tighter than the dial timeout — an operator who
	// gave up on a slow login should not leave a search running behind them.
	timeout := ldapTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	conn.SetTimeout(timeout)

	if config.LDAPStartTLS && !config.LDAPUseTLS {
		if err := conn.StartTLS(tlsConfig); err != nil {
			// Best effort: the connection is being abandoned because StartTLS
			// already failed, and that error is the one worth returning.
			_ = conn.Close()
			return nil, fmt.Errorf("start TLS on %s: %w", address, err)
		}
	}
	return conn, nil
}

// serviceBind binds as the configured search account, or leaves the connection
// anonymous when there is none.
func serviceBind(conn *ldap.Conn, config *db.SSOProviderConfig) error {
	if strings.TrimSpace(config.LDAPBindDN) == "" {
		return nil
	}
	if strings.TrimSpace(config.LDAPBindPassword) == "" {
		return errors.New("the LDAP bind DN has no password configured")
	}
	if err := conn.Bind(config.LDAPBindDN, config.LDAPBindPassword); err != nil {
		return fmt.Errorf("bind as %s: %w", config.LDAPBindDN, err)
	}
	return nil
}

// findUser locates exactly one entry for a username. More than one match is an
// error rather than a choice: a filter that is ambiguous would otherwise
// authenticate whichever entry the directory happened to return first.
func findUser(conn *ldap.Conn, config *db.SSOProviderConfig, username string) (*ldap.Entry, error) {
	if strings.TrimSpace(config.LDAPBaseDN) == "" {
		return nil, errors.New("this provider has no base DN to search")
	}

	filter := userFilter(config, username)
	attributes := dedupe([]string{
		config.LDAPUserAttribute,
		config.LDAPEmailAttribute,
		config.LDAPGroupAttribute,
	})

	search := ldap.NewSearchRequest(
		config.LDAPBaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		2, int(ldapTimeout.Seconds()), false,
		filter, attributes, nil,
	)
	result, err := conn.Search(search)
	if err != nil {
		return nil, fmt.Errorf("search the directory: %w", err)
	}

	switch len(result.Entries) {
	case 0:
		// Not "no such user": telling an unauthenticated caller which usernames
		// exist is the one thing a login form must not do.
		return nil, ErrLDAPCredentials
	case 1:
		return result.Entries[0], nil
	default:
		return nil, fmt.Errorf("the user filter matched %d entries; it must match one", len(result.Entries))
	}
}

// userFilter builds the search filter, escaping the username rather than
// trusting it: everything here ends up inside LDAP filter syntax, where an
// unescaped ")" or "*" rewrites the query rather than failing it.
func userFilter(config *db.SSOProviderConfig, username string) string {
	escaped := ldap.EscapeFilter(username)
	configured := strings.TrimSpace(config.LDAPUserFilter)

	switch {
	case configured == "":
		return fmt.Sprintf("(%s=%s)", config.LDAPUserAttribute, escaped)
	case strings.Contains(configured, "%s"):
		return strings.ReplaceAll(configured, "%s", escaped)
	default:
		// A filter that does not name the username is a *restriction* — "only
		// entries in this OU, only enabled accounts" — so it is combined with
		// the username lookup rather than used in place of it.
		return fmt.Sprintf("(&%s(%s=%s))", configured, config.LDAPUserAttribute, escaped)
	}
}

// findGroups runs the reverse membership search for directories that do not
// carry memberOf on the user entry.
func findGroups(conn *ldap.Conn, config *db.SSOProviderConfig, userDN string) ([]string, error) {
	base := config.LDAPGroupBaseDN
	if strings.TrimSpace(base) == "" {
		base = config.LDAPBaseDN
	}

	filter := config.LDAPGroupFilter
	if strings.Contains(filter, "%s") {
		filter = strings.ReplaceAll(filter, "%s", ldap.EscapeFilter(userDN))
	}

	attribute := config.LDAPGroupNameAttribute
	attributes := []string{"dn"}
	if attribute != "" {
		attributes = []string{attribute}
	}

	search := ldap.NewSearchRequest(
		base, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		0, int(ldapTimeout.Seconds()), false,
		filter, attributes, nil,
	)
	result, err := conn.Search(search)
	if err != nil {
		return nil, fmt.Errorf("search groups: %w", err)
	}

	groups := make([]string, 0, len(result.Entries))
	for _, entry := range result.Entries {
		if attribute != "" {
			if value := strings.TrimSpace(entry.GetAttributeValue(attribute)); value != "" {
				groups = append(groups, value)
				continue
			}
		}
		groups = append(groups, entry.DN)
	}
	return groups, nil
}

// groupNames renders the group list the mapping rules will be matched against.
// With a name attribute configured, a DN is reduced to that attribute's value,
// so a rule can be written as "platform-admins" rather than as a full DN — but
// the DN is kept when it cannot be reduced, because matching nothing is worse
// than matching something verbose.
func groupNames(groups []string, nameAttribute string) []string {
	if strings.TrimSpace(nameAttribute) == "" {
		return groups
	}

	prefix := strings.ToLower(nameAttribute) + "="
	out := make([]string, 0, len(groups))
	for _, group := range groups {
		parsed, err := ldap.ParseDN(group)
		if err != nil || len(parsed.RDNs) == 0 {
			out = append(out, group)
			continue
		}
		named := ""
		for _, attribute := range parsed.RDNs[0].Attributes {
			if strings.ToLower(attribute.Type)+"=" == prefix {
				named = attribute.Value
				break
			}
		}
		if named == "" {
			out = append(out, group)
			continue
		}
		out = append(out, named)
	}
	return out
}
