package service

import (
	"context"
	"fmt"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/refreshsession"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/refreshtoken"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

func lockAuthUser(ctx context.Context, client *ent.Client, userID int) error {
	if userID < 1 {
		return ErrTokenInvalid
	}
	r, err := client.ExecContext(ctx, fmt.Sprintf("UPDATE users SET auth_version = auth_version WHERE id = %d", userID))
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrTokenInvalid
	}
	return nil
}

func lockRefreshSession(ctx context.Context, client *ent.Client, id int) error {
	if id < 1 {
		return ErrTokenInvalid
	}
	r, err := client.ExecContext(ctx, fmt.Sprintf("UPDATE refresh_sessions SET generation = generation WHERE id = %d", id))
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrTokenInvalid
	}
	return nil
}

// Caller owns the user lock. Invalidating the version and all refresh chains is
// one transaction; a later re-enable must never restore these credentials.
func revokeUserSessions(ctx context.Context, client *ent.Client, userID int, now time.Time) error {
	n, err := client.User.Update().Where(user.IDEQ(userID), user.AuthVersionLT(maxAuthVersion)).AddAuthVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAccountConflict
	}
	if _, err := client.RefreshSession.Update().Where(refreshsession.UserIDEQ(userID), refreshsession.RevokedAtIsNil()).SetRevokedAt(now).Save(ctx); err != nil {
		return err
	}
	_, err = client.RefreshToken.Update().Where(refreshtoken.HasUserWith(user.IDEQ(userID)), refreshtoken.RevokedAtIsNil()).SetRevokedAt(now).Save(ctx)
	return err
}

func revokeRefreshSession(ctx context.Context, client *ent.Client, id int, now time.Time) error {
	if _, err := client.RefreshSession.Update().Where(refreshsession.IDEQ(id), refreshsession.RevokedAtIsNil()).SetRevokedAt(now).Save(ctx); err != nil {
		return err
	}
	_, err := client.RefreshToken.Update().Where(refreshtoken.SessionIDEQ(id), refreshtoken.RevokedAtIsNil()).SetRevokedAt(now).Save(ctx)
	return err
}
