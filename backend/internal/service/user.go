package service

import (
	"context"
	"net/mail"
	"strings"
	"unicode"

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
		email = normalizeIdentity(*input.Email)
		address, err := mail.ParseAddress(email)
		if err != nil || address.Name != "" || address.Address != email || strings.ContainsFunc(email, unicode.IsSpace) {
			return nil, ErrInvalidInput
		}
	}

	update := s.client.User.UpdateOneID(userID)
	if input.DisplayName != nil {
		update.SetDisplayName(strings.TrimSpace(*input.DisplayName))
	}
	if input.Email != nil {
		update.SetEmail(email)
	}
	updated, err := update.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, ErrUserExists
		}
		return nil, err
	}
	return updated, nil
}

func (s *UserService) ChangeMyPassword(ctx context.Context, userID int, currentPassword, newPassword string) error {
	return s.auth.ChangePassword(ctx, userID, currentPassword, newPassword)
}
