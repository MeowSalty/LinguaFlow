package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgmembership"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

var errSharedObjectNotFound = errors.New("shared object not found")

// checkSharedAccess authorizes an object's actual ownership, never a caller supplied scope.
// Invalid ownership and inaccessible IDs are deliberately indistinguishable from missing IDs.
func checkSharedAccess(ctx context.Context, client *ent.Client, actorID int, scope string, ownerUserID, ownerOrgID *int, write bool) error {
	if actorID <= 0 {
		return errSharedObjectNotFound
	}
	switch scope {
	case ScopeUser:
		if ownerUserID == nil || *ownerUserID != actorID || ownerOrgID != nil {
			return errSharedObjectNotFound
		}
	case ScopeOrg:
		if ownerOrgID == nil || *ownerOrgID <= 0 || ownerUserID != nil {
			return errSharedObjectNotFound
		}
		membership, err := client.OrgMembership.Query().Where(
			orgmembership.HasOrganizationWith(organization.IDEQ(*ownerOrgID)),
			orgmembership.HasUserWith(user.IDEQ(actorID)),
			orgmembership.RoleIn(OrgRoleMember, OrgRoleAdmin, OrgRoleOwner),
		).Only(ctx)
		if ent.IsNotFound(err) {
			return errSharedObjectNotFound
		}
		if err != nil {
			return err
		}
		if write && membership.Role == OrgRoleMember {
			return ErrForbidden
		}
	case "system":
		if write || ownerUserID != nil || ownerOrgID != nil {
			return errSharedObjectNotFound
		}
	default:
		return errSharedObjectNotFound
	}
	return nil
}

// requireSharedOrganization preserves the explicit organization request's 404/403 contract.
func requireSharedOrganization(ctx context.Context, client *ent.Client, actorID, orgID int, write bool) error {
	if orgID <= 0 || actorID <= 0 {
		return ErrInvalidInput
	}
	exists, err := client.Organization.Query().Where(organization.IDEQ(orgID)).Exist(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return ErrOrganizationNotFound
	}
	err = checkSharedAccess(ctx, client, actorID, ScopeOrg, nil, &orgID, write)
	if errors.Is(err, errSharedObjectNotFound) {
		return ErrForbidden
	}
	return err
}

// validateSharedReference prevents private or foreign content from entering an organization snapshot.
// The caller must independently authorize reading the referenced entity first.
func validateSharedReference(scope string, ownerOrgID, targetOrgID *int) error {
	if targetOrgID == nil {
		return nil
	}
	if *targetOrgID <= 0 {
		return ErrInvalidInput
	}
	if scope == "system" && ownerOrgID == nil {
		return nil
	}
	if scope == ScopeOrg && ownerOrgID != nil && *ownerOrgID == *targetOrgID {
		return nil
	}
	return ErrForbidden
}

// withSharedMutation keeps each change and its audit atomic. Organization writes also
// serialize with member mutations, and recheck authorization inside the locked callback.
func withSharedMutation(ctx context.Context, client *ent.Client, orgID *int, fn func(*ent.Client) error) error {
	if orgID != nil {
		if *orgID <= 0 {
			return ErrInvalidInput
		}
		return withOrganizationMutation(ctx, client, *orgID, fn)
	}
	tx, err := client.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx.Client()); err != nil {
		return err
	}
	return tx.Commit()
}

func sharedAccessError(err, notFound error) error {
	if errors.Is(err, errSharedObjectNotFound) || ent.IsNotFound(err) {
		return notFound
	}
	return err
}

func sharedCreateScope(ctx context.Context, client *ent.Client, actorID int, orgID *int) (string, error) {
	if actorID <= 0 {
		return "", ErrInvalidInput
	}
	if orgID != nil {
		if err := requireSharedOrganization(ctx, client, actorID, *orgID, true); err != nil {
			return "", err
		}
		return ScopeOrg, nil
	}
	return ScopeUser, nil
}

func recordSharedAudit(ctx context.Context, client *ent.Client, actorID int, orgID *int, resourceType, action string, id int) error {
	visibility := "personal"
	if orgID != nil {
		visibility = "organization"
	}
	if err := recordAuditEvent(ctx, client, AuditEvent{
		ActorUserID: actorID, OrgID: orgID, VisibilityScope: visibility,
		Action: resourceType + "." + action, ResourceType: resourceType, ResourceID: id,
	}); err != nil {
		return fmt.Errorf("record shared object audit: %w", err)
	}
	return nil
}
