package service

import (
	"context"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/accountidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

type UserService struct {
	client *ent.Client
	auth   *AuthService
}

type UpdateProfileInput struct {
	DisplayName *string
	Email       *string
}

func NewUserService(client *ent.Client, auth *AuthService) *UserService {
	return &UserService{client: client, auth: auth}
}

func (s *UserService) GetMe(ctx context.Context, userID int) (*ent.User, error) {
	return s.client.User.Get(ctx, userID)
}

func (s *UserService) UpdateMe(ctx context.Context, userID int, input UpdateProfileInput) (*ent.User, error) {
	if input.DisplayName == nil && input.Email == nil {
		return s.GetMe(ctx, userID)
	}

	var email string
	if input.Email != nil {
		var err error
		email, err = accountidentity.NormalizeEmail(*input.Email)
		if err != nil {
			return nil, ErrInvalidInput
		}
	}

	var updated *ent.User
	err := withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		if err := lockAuthUser(ctx, tx, userID); err != nil {
			return err
		}
		target, err := tx.User.Get(ctx, userID)
		if err != nil {
			return err
		}
		if input.Email != nil && (target.EmailVerifiedAt != nil || target.EmailVerificationSource != "" || target.VerifiedEmailKey != "") {
			return ErrAccountConflict
		}
		update := tx.User.UpdateOneID(userID)
		if input.DisplayName != nil {
			update.SetDisplayName(strings.TrimSpace(*input.DisplayName))
		}
		if input.Email != nil {
			if email == "" {
				update.ClearEmail()
			} else {
				update.SetEmail(email)
			}
		}
		updated, err = update.Save(ctx)
		return err
	})
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, ErrUserExists
		}
		return nil, err
	}
	return updated.Unwrap(), nil
}

func (s *UserService) ChangeMyPassword(ctx context.Context, userID int, currentPassword, newPassword string) error {
	return s.auth.ChangePassword(ctx, userID, currentPassword, newPassword)
}
