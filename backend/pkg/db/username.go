package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// ErrUnsafeUsername is returned when a new or renamed account would carry a
// name Kubernetes reads as something other than a plain user.
var ErrUnsafeUsername = errors.New(
	"a username may not contain ':' or control characters — Kubernetes reads " +
		"colon-separated names such as system:serviceaccount:<namespace>:<name> as " +
		"identities of its own",
)

// CheckUsername is the rule every door that writes a username applies: local
// create and rename, federated provisioning, the bootstrap administrator.
//
// A username reaches the cluster as the impersonated user. The gateway prefixes
// it (`kubemg:u:`), which is what actually closes the escalation; this is the
// second line, so that no stored account ever looks like `system:masters` or a
// ServiceAccount to a person reading the users list, the audit trail or a
// RoleBinding. A colon is refused outright rather than only a `system:` prefix,
// because every reserved Kubernetes form is colon-separated and an email or a
// directory login never needs one.
//
// It is applied on write only. A row stored before the rule existed keeps
// working — the prefix already makes it harmless — and is reported at boot by
// UnsafeUsernames rather than renamed out from under its grants.
func CheckUsername(name string) error {
	if strings.Contains(name, ":") {
		return ErrUnsafeUsername
	}
	if strings.ContainsFunc(name, unicode.IsControl) {
		return ErrUnsafeUsername
	}
	return nil
}

// UnsafeUsernames lists stored accounts whose name CheckUsername would now
// refuse. They are reported, never renamed: a username is what grants, the
// audit trail and any RoleBinding written against it refer to, so changing it
// belongs to an administrator in the user editor.
func (s *Store) UnsafeUsernames(ctx context.Context) ([]string, error) {
	var names []string
	if err := s.gdb.WithContext(ctx).Model(&User{}).Order("username").
		Pluck("username", &names).Error; err != nil {
		return nil, fmt.Errorf("list usernames: %w", err)
	}
	unsafe := names[:0]
	for _, name := range names {
		if CheckUsername(name) != nil {
			unsafe = append(unsafe, name)
		}
	}
	return unsafe, nil
}
