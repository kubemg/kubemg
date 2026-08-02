package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrSSONoAccount is returned when a directory authenticated someone KubeMG
	// has no account for and the provider does not provision on first sign-in.
	ErrSSONoAccount = errors.New("no KubeMG account is provisioned for this identity")
	// ErrSSOAccountConflict is returned when the username an IdP asserted is
	// already a local account, or an account belonging to another provider.
	//
	// It is refused rather than adopted on purpose: silently attaching a
	// directory to an existing local login turns "can you create a user in your
	// IdP" into "can you become the KubeMG administrator". Linking the two is an
	// administrative act, so it happens in the user editor.
	ErrSSOAccountConflict = errors.New("an account with this username already exists")
	// ErrSSOAccountDisabled is returned when the matched account is disabled
	// here, whatever the directory thinks of it.
	ErrSSOAccountDisabled = errors.New("this account is disabled")
)

// SSOIdentity is what a federation engine resolved about the person signing in.
// It is the only thing the engines have in common, and deliberately so: the
// sync below knows nothing about tokens, assertions or binds.
type SSOIdentity struct {
	// ExternalID is the directory's stable identifier. Empty falls back to
	// matching on username, which is all an LDAP directory without a stable
	// entry UUID can offer.
	ExternalID string
	Username   string
	Email      string
	// Groups are the group names the directory asserted, unfiltered. Mapping
	// rules decide what any of them are worth.
	Groups []string
}

// SSOSyncResult reports what a federated sign-in did, so the audit line and the
// admin UI can say more than "ok".
type SSOSyncResult struct {
	User *User
	// Created is true when this sign-in provisioned the account.
	Created bool
	// MatchedGroups are the asserted groups at least one rule matched.
	MatchedGroups []string
	// LocalGroupIDs are the local groups the person now belongs to by federation.
	LocalGroupIDs []uint
	// ClusterGrants is how many cluster grants the rules derived.
	ClusterGrants int
	// SystemRole is the role the account ended up with.
	SystemRole string
}

// ListSSOProviders returns every configured identity provider.
func (s *Store) ListSSOProviders(ctx context.Context) ([]SSOProviderConfig, error) {
	providers := []SSOProviderConfig{}
	if err := s.gdb.WithContext(ctx).Order("name asc").Find(&providers).Error; err != nil {
		return nil, fmt.Errorf("list sso providers: %w", err)
	}
	return providers, nil
}

// SSOProviderByID loads one provider.
func (s *Store) SSOProviderByID(ctx context.Context, id uint) (*SSOProviderConfig, error) {
	var provider SSOProviderConfig
	err := s.gdb.WithContext(ctx).First(&provider, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("sso provider by id: %w", err)
	}
	return &provider, nil
}

// CreateSSOProvider inserts a provider.
func (s *Store) CreateSSOProvider(ctx context.Context, provider *SSOProviderConfig) error {
	provider.Normalize()
	if err := s.gdb.WithContext(ctx).Create(provider).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return ErrConflict
		}
		return fmt.Errorf("create sso provider: %w", err)
	}
	return nil
}

// UpdateSSOProvider replaces a provider's configuration. The caller has already
// resolved whether each secret is a new one or the stored one being kept, so
// what arrives here is written as given — the same contract the observability
// datasources use, and for the same reason: changing a port must not mean
// re-typing a client secret.
func (s *Store) UpdateSSOProvider(ctx context.Context, provider *SSOProviderConfig) error {
	provider.Normalize()
	provider.UpdatedAt = time.Now().UTC()

	res := s.gdb.WithContext(ctx).Model(&SSOProviderConfig{}).
		Where("id = ?", provider.ID).
		Select(
			"name", "protocol", "enabled",
			"issuer_url", "client_id", "client_secret", "scopes",
			"saml_metadata_url", "saml_metadata_xml", "saml_entity_id",
			"ldap_host", "ldap_port", "ldap_use_tls", "ldap_start_tls", "ldap_skip_verify",
			"ldap_bind_dn", "ldap_bind_password", "ldap_base_dn",
			"ldap_user_filter", "ldap_user_attribute", "ldap_email_attribute",
			"ldap_group_attribute", "ldap_group_filter", "ldap_group_base_dn",
			"ldap_group_name_attribute",
			"username_claim", "email_claim", "groups_claim",
			"allow_jit", "default_system_role",
			"updated_at",
		).
		Updates(provider)
	if errors.Is(res.Error, gorm.ErrDuplicatedKey) {
		return ErrConflict
	}
	if res.Error != nil {
		return fmt.Errorf("update sso provider: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateSSOProviderHealth records the outcome of a provider connectivity check.
func (s *Store) UpdateSSOProviderHealth(ctx context.Context, id uint, status, message string) error {
	now := time.Now().UTC()
	res := s.gdb.WithContext(ctx).Model(&SSOProviderConfig{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"last_status":     status,
			"last_message":    message,
			"last_checked_at": now,
			"updated_at":      now,
		})
	if res.Error != nil {
		return fmt.Errorf("update sso provider health: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSSOProvider removes a provider and its mapping rules.
//
// The accounts it provisioned are deliberately left in place: they hold audit
// history and hand-written grants, and deleting people because a configuration
// row was removed is not a decision this should make quietly. They simply stop
// being able to sign in, which is what disabling the provider does too.
func (s *Store) DeleteSSOProvider(ctx context.Context, id uint) error {
	return s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Delete(&SSOProviderConfig{}, id)
		if res.Error != nil {
			return fmt.Errorf("delete sso provider: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		if err := tx.Where("provider_id = ?", id).Delete(&SSOGroupMapping{}).Error; err != nil {
			return fmt.Errorf("delete sso mappings: %w", err)
		}
		return nil
	})
}

// ListSSOMappings returns the mapping rules for one provider, or for every
// provider when providerID is zero.
func (s *Store) ListSSOMappings(ctx context.Context, providerID uint) ([]SSOGroupMapping, error) {
	mappings := []SSOGroupMapping{}
	query := s.gdb.WithContext(ctx).Order("provider_id asc, external_group_pattern asc")
	if providerID != 0 {
		query = query.Where("provider_id = ?", providerID)
	}
	if err := query.Find(&mappings).Error; err != nil {
		return nil, fmt.Errorf("list sso mappings: %w", err)
	}
	return mappings, nil
}

// CreateSSOMapping inserts a mapping rule.
func (s *Store) CreateSSOMapping(ctx context.Context, mapping *SSOGroupMapping) error {
	if err := s.gdb.WithContext(ctx).Create(mapping).Error; err != nil {
		return fmt.Errorf("create sso mapping: %w", err)
	}
	return nil
}

// UpdateSSOMapping replaces a mapping rule's terms.
func (s *Store) UpdateSSOMapping(ctx context.Context, mapping *SSOGroupMapping) error {
	mapping.UpdatedAt = time.Now().UTC()
	res := s.gdb.WithContext(ctx).Model(&SSOGroupMapping{}).
		Where("id = ?", mapping.ID).
		Select(
			"external_group_pattern", "target_group_id", "target_k8s_role",
			"environment_filter", "namespaces", "target_system_role", "updated_at",
		).
		Updates(mapping)
	if res.Error != nil {
		return fmt.Errorf("update sso mapping: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSSOMapping removes a mapping rule. Access it derived is withdrawn on the
// affected accounts' next sign-in, since that is when the rules are evaluated.
func (s *Store) DeleteSSOMapping(ctx context.Context, id uint) error {
	res := s.gdb.WithContext(ctx).Delete(&SSOGroupMapping{}, id)
	if res.Error != nil {
		return fmt.Errorf("delete sso mapping: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

/*
 * The sync.
 *
 * Everything below happens in one transaction on every federated sign-in,
 * because a half-applied federation is worse than a refused one: an account
 * that exists with none of its groups looks exactly like an account whose
 * access was deliberately revoked.
 *
 * It is a *reconcile*, not an insert. The rules say what the directory entitles
 * this person to right now; rows KubeMG previously derived and the rules no
 * longer produce are deleted, and rows an administrator wrote by hand are never
 * touched in either direction. Without that, leaving a group in the IdP would
 * leave the cluster access behind, which is the entire point of federating.
 */

// SyncSSOUserAndGroups provisions the account an identity provider vouched for
// and reconciles its group memberships and cluster grants against the provider's
// mapping rules.
func (s *Store) SyncSSOUserAndGroups(
	ctx context.Context, provider *SSOProviderConfig, identity SSOIdentity,
) (*SSOSyncResult, error) {
	username := strings.TrimSpace(identity.Username)
	if username == "" {
		return nil, errors.New("the identity provider returned no username")
	}
	identity.Username = username
	identity.ExternalID = strings.TrimSpace(identity.ExternalID)
	identity.Email = strings.TrimSpace(identity.Email)

	result := &SSOSyncResult{MatchedGroups: []string{}, LocalGroupIDs: []uint{}}
	err := s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		user, err := resolveFederatedUser(tx, provider, identity)
		if err != nil {
			return err
		}

		if user == nil {
			if !provider.AllowJIT {
				return ErrSSONoAccount
			}
			user = &User{
				Username:      identity.Username,
				Email:         identity.Email,
				SystemRole:    provider.DefaultSystemRole,
				IsActive:      true,
				AuthSource:    provider.Protocol,
				SSOProviderID: provider.ID,
				ExternalID:    identity.ExternalID,
			}
			user.Normalize()
			if err := tx.Create(user).Error; err != nil {
				if errors.Is(err, gorm.ErrDuplicatedKey) {
					return ErrSSOAccountConflict
				}
				return fmt.Errorf("provision sso user: %w", err)
			}
			result.Created = true
		}

		if !user.IsActive {
			return ErrSSOAccountDisabled
		}

		matched, err := applyMappings(tx, provider, user, identity, result)
		if err != nil {
			return err
		}
		result.MatchedGroups = matched

		// The directory's view of the person: their email, the identifier they
		// are matched on from now on, and the fact that they just signed in.
		// The username is deliberately not re-synced — it is what the audit
		// trail, the impersonation header and every grant refer to, so renaming
		// it out from under those belongs in the user editor.
		now := time.Now().UTC()
		fields := map[string]any{
			"last_login_at": now,
			"updated_at":    now,
			"auth_source":   provider.Protocol,
		}
		if identity.Email != "" && identity.Email != user.Email {
			fields["email"] = identity.Email
			user.Email = identity.Email
		}
		if identity.ExternalID != "" && identity.ExternalID != user.ExternalID {
			fields["external_id"] = identity.ExternalID
			user.ExternalID = identity.ExternalID
		}
		if user.SSOProviderID != provider.ID {
			fields["sso_provider_id"] = provider.ID
			user.SSOProviderID = provider.ID
		}
		if err := tx.Model(&User{}).Where("id = ?", user.ID).Updates(fields).Error; err != nil {
			return fmt.Errorf("update sso user: %w", err)
		}

		user.AuthSource = provider.Protocol
		user.LastLoginAt = &now
		user.Normalize()
		result.User = user
		result.SystemRole = user.SystemRole
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// resolveFederatedUser finds the account an assertion belongs to, refusing the
// collisions that would let a directory take over a login it was never given.
func resolveFederatedUser(
	tx *gorm.DB, provider *SSOProviderConfig, identity SSOIdentity,
) (*User, error) {
	if identity.ExternalID != "" {
		var user User
		err := tx.Where("sso_provider_id = ? AND external_id = ?", provider.ID, identity.ExternalID).
			First(&user).Error
		if err == nil {
			return &user, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("lookup federated user: %w", err)
		}
	}

	var user User
	err := tx.Where("username = ?", identity.Username).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup user by username: %w", err)
	}

	// A local account, or one another provider already owns, keeps its own
	// login. Adopting it here would mean an IdP administrator could take over
	// any KubeMG account by creating a user with a matching name.
	if !user.IsFederated() || (user.SSOProviderID != 0 && user.SSOProviderID != provider.ID) {
		return nil, ErrSSOAccountConflict
	}
	return &user, nil
}

// applyMappings evaluates the provider's rules against the asserted groups and
// reconciles what they produce, returning the asserted groups that matched at
// least one rule.
func applyMappings(
	tx *gorm.DB, provider *SSOProviderConfig, user *User, identity SSOIdentity, result *SSOSyncResult,
) ([]string, error) {
	mappings := []SSOGroupMapping{}
	if err := tx.Where("provider_id = ?", provider.ID).Find(&mappings).Error; err != nil {
		return nil, fmt.Errorf("load sso mappings: %w", err)
	}

	var (
		matched      []string
		rules        []SSOGroupMapping
		matchedRules = map[uint]bool{}
	)
	for _, group := range identity.Groups {
		hit := false
		for _, mapping := range mappings {
			if !mapping.Matches(group) {
				continue
			}
			hit = true
			if !matchedRules[mapping.ID] {
				matchedRules[mapping.ID] = true
				rules = append(rules, mapping)
			}
		}
		if hit && !slices.Contains(matched, group) {
			matched = append(matched, group)
		}
	}
	// A rule keyed on "*" is about the provider rather than about a group, so it
	// applies to someone the directory returned no groups for at all.
	for _, mapping := range mappings {
		if strings.TrimSpace(mapping.ExternalGroupPattern) == "*" && !matchedRules[mapping.ID] {
			matchedRules[mapping.ID] = true
			rules = append(rules, mapping)
		}
	}
	if matched == nil {
		matched = []string{}
	}

	groupIDs, err := reconcileMemberships(tx, user.ID, rules)
	if err != nil {
		return nil, err
	}
	result.LocalGroupIDs = groupIDs

	grants, err := reconcileGrants(tx, user.ID, rules)
	if err != nil {
		return nil, err
	}
	result.ClusterGrants = grants

	if err := applySystemRole(tx, user, rules); err != nil {
		return nil, err
	}
	return matched, nil
}

// reconcileMemberships makes the user's federated group memberships exactly the
// set the matched rules name.
func reconcileMemberships(tx *gorm.DB, userID uint, rules []SSOGroupMapping) ([]uint, error) {
	desired := []uint{}
	for _, rule := range rules {
		if rule.TargetGroupID != 0 && !slices.Contains(desired, rule.TargetGroupID) {
			desired = append(desired, rule.TargetGroupID)
		}
	}

	// Only the rows federation wrote are its to remove. A membership an
	// administrator added by hand is not evidence about the directory.
	drop := tx.Where("user_id = ? AND source = ?", userID, GrantSourceSSO)
	if len(desired) > 0 {
		drop = drop.Where("group_id NOT IN ?", desired)
	}
	if err := drop.Delete(&UserGroup{}).Error; err != nil {
		return nil, fmt.Errorf("prune federated memberships: %w", err)
	}

	for _, groupID := range desired {
		member := UserGroup{UserID: userID, GroupID: groupID, Source: GrantSourceSSO}
		err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&member).Error
		if err != nil {
			return nil, fmt.Errorf("apply federated membership: %w", err)
		}
	}
	return desired, nil
}

// reconcileGrants makes the user's federated cluster grants exactly what the
// matched rules derive, cluster by cluster.
func reconcileGrants(tx *gorm.DB, userID uint, rules []SSOGroupMapping) (int, error) {
	clusters := []Cluster{}
	if err := tx.Find(&clusters).Error; err != nil {
		return 0, fmt.Errorf("load clusters for federation: %w", err)
	}

	// Several rules can name the same cluster — one for the environment, one for
	// a specific team — so they are merged the same way a direct grant and an
	// inherited one are: the stronger role wins and the scopes union.
	desired := map[uint]UserClusterAccess{}
	for _, rule := range rules {
		if !slices.Contains([]string{K8sRoleView, K8sRoleEdit, K8sRoleClusterAdmin}, rule.TargetK8sRole) {
			continue
		}
		for _, cluster := range clusters {
			if rule.EnvironmentFilter != "" && cluster.Environment != rule.EnvironmentFilter {
				continue
			}
			grant := UserClusterAccess{
				UserID:     userID,
				ClusterID:  cluster.ID,
				K8sRole:    rule.TargetK8sRole,
				Namespaces: JoinNamespaces(rule.NamespaceList()),
				Source:     GrantSourceSSO,
			}
			if existing, ok := desired[cluster.ID]; ok {
				merged := MergeAccess(existing, grant)
				merged.Source = GrantSourceSSO
				desired[cluster.ID] = merged
				continue
			}
			desired[cluster.ID] = grant
		}
	}

	// A cluster an administrator granted by hand is left exactly as it is: a
	// derived grant written over it would replace a decision someone made
	// deliberately, and the sync would then reconcile that decision away on some
	// later login.
	//
	// A *temporary* elevation is deliberately not treated that way. It has its own
	// row and its own clock, so a directory can go on asserting the standing grant
	// beside it — and reading a JIT row as "hand-written" would be worse than
	// harmless: this cluster would be skipped, the federated grant below would be
	// pruned as no longer asserted, and the user would come out of their own login
	// with less access than they had, until the elevation expired.
	existing := []UserClusterAccess{}
	if err := tx.Where("user_id = ?", userID).Find(&existing).Error; err != nil {
		return 0, fmt.Errorf("load existing grants: %w", err)
	}
	local := map[uint]bool{}
	for _, grant := range existing {
		if grant.Source != GrantSourceSSO && grant.Source != GrantSourceJIT {
			local[grant.ClusterID] = true
		}
	}

	keep := make([]uint, 0, len(desired))
	for clusterID := range desired {
		if local[clusterID] {
			delete(desired, clusterID)
			continue
		}
		keep = append(keep, clusterID)
	}

	drop := tx.Where("user_id = ? AND source = ?", userID, GrantSourceSSO)
	if len(keep) > 0 {
		drop = drop.Where("cluster_id NOT IN ?", keep)
	}
	if err := drop.Delete(&UserClusterAccess{}).Error; err != nil {
		return 0, fmt.Errorf("prune federated grants: %w", err)
	}

	for _, clusterID := range keep {
		grant := desired[clusterID]
		err := tx.Clauses(clause.OnConflict{
			// (user, cluster, source): the derived row is the only one this sync
			// owns, and the only one it may overwrite.
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "cluster_id"}, {Name: "source"}},
			DoUpdates: clause.AssignmentColumns([]string{"k8s_role", "namespaces"}),
		}).Create(&grant).Error
		if err != nil {
			return 0, fmt.Errorf("apply federated grant: %w", err)
		}
	}
	return len(keep), nil
}

// applySystemRole lets an IdP group carry the KubeMG administrator tier.
//
// Rules are authoritative only when at least one of them says something about
// the role: that is what makes administrator access revocable by removing
// someone from a directory group. When no rule mentions it, the stored role is
// left alone, so an administrator promoted here by hand is not demoted by their
// own next sign-in. A super admin is never touched at all — that tier exists to
// be the account an IdP outage cannot lock you out of.
func applySystemRole(tx *gorm.DB, user *User, rules []SSOGroupMapping) error {
	if user.IsSuperAdmin() {
		return nil
	}

	desired := ""
	for _, rule := range rules {
		role := rule.TargetSystemRole
		if role != SystemRoleAdmin && role != SystemRoleUser {
			continue
		}
		if desired == "" || (role == SystemRoleAdmin && desired == SystemRoleUser) {
			desired = role
		}
	}
	if desired == "" || desired == user.SystemRole {
		return nil
	}

	err := tx.Model(&User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"system_role": desired,
		"role":        LegacyRoleFor(desired),
		"updated_at":  time.Now().UTC(),
	}).Error
	if err != nil {
		return fmt.Errorf("apply federated system role: %w", err)
	}
	user.SystemRole = desired
	user.Normalize()
	return nil
}
