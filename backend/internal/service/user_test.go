package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func profileTestString(value string) *string { return &value }

func TestUpdateMePartialProfile(t *testing.T) {
	tests := []struct {
		name        string
		input       UpdateProfileInput
		displayName string
		email       string
	}{
		{name: "empty object", displayName: "Original", email: "profile@test.com"},
		{name: "display name only", input: UpdateProfileInput{DisplayName: profileTestString("  New name\u2003")}, displayName: "New name", email: "profile@test.com"},
		{name: "clear display name", input: UpdateProfileInput{DisplayName: profileTestString("")}, email: "profile@test.com"},
		{name: "whitespace display name", input: UpdateProfileInput{DisplayName: profileTestString(" \t\u2003")}, email: "profile@test.com"},
		{name: "email only", input: UpdateProfileInput{Email: profileTestString(" \tNew.Address+Tag@Example.COM\u2003")}, displayName: "Original", email: "new.address+tag@example.com"},
		{name: "both fields", input: UpdateProfileInput{DisplayName: profileTestString(" Updated "), Email: profileTestString(" Updated@Example.COM ")}, displayName: "Updated", email: "updated@example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			client := testClient(t)
			original := createProfileTestUser(t, client)
			svc := NewUserService(client, nil)
			updated, err := svc.UpdateMe(ctx, original.ID, tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if updated.DisplayName != tt.displayName || updated.Email != tt.email {
				t.Fatalf("profile = (%q, %q), want (%q, %q)", updated.DisplayName, updated.Email, tt.displayName, tt.email)
			}
			stored, err := client.User.Get(ctx, original.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.DisplayName != tt.displayName || stored.Email != tt.email {
				t.Fatalf("persisted profile = (%q, %q), want (%q, %q)", stored.DisplayName, stored.Email, tt.displayName, tt.email)
			}
			if stored.Username != original.Username || stored.Role != original.Role || stored.Active != original.Active || stored.PasswordHash != original.PasswordHash {
				t.Fatal("profile update changed account identity or credentials")
			}
			if tt.input.DisplayName == nil && tt.input.Email == nil && (!stored.UpdatedAt.Equal(original.UpdatedAt) || !updated.UpdatedAt.Equal(original.UpdatedAt)) {
				t.Fatal("empty profile update changed updated_at")
			}
		})
	}
}

func TestUpdateMeRejectsInvalidEmailWithoutPartialWrite(t *testing.T) {
	ctx := context.Background()
	client := testClient(t)
	original := createProfileTestUser(t, client)
	svc := NewUserService(client, nil)
	tests := []string{
		"", " \t\u2003", "@", "missing-at", "user@", "@example.com",
		"user name@example.com", "user\tname@example.com", "user\u00a0name@example.com",
		"Name <user@example.com>", "<user@example.com>", "user@example.com (Name)",
		"one@example.com,two@example.com", "user@example.com\nother@example.com",
	}
	for _, email := range tests {
		t.Run(email, func(t *testing.T) {
			_, err := svc.UpdateMe(ctx, original.ID, UpdateProfileInput{
				DisplayName: profileTestString("Must not be stored"),
				Email:       profileTestString(email),
			})
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
			assertProfileUnchanged(t, client, original)
		})
	}
}

func TestUpdateMeEmailConflictIsAtomic(t *testing.T) {
	ctx := context.Background()
	client := testClient(t)
	original := createProfileTestUser(t, client)
	other := createTestUser(t, client, "other")
	svc := NewUserService(client, nil)
	_, err := svc.UpdateMe(ctx, original.ID, UpdateProfileInput{
		DisplayName: profileTestString("Must not be stored"),
		Email:       profileTestString("  OTHER@Test.COM  "),
	})
	if !errors.Is(err, ErrUserExists) {
		t.Fatalf("error = %v, want ErrUserExists", err)
	}
	assertProfileUnchanged(t, client, original)
	assertProfileUnchanged(t, client, other)
}

func createProfileTestUser(t *testing.T, client *ent.Client) *ent.User {
	t.Helper()
	account := createTestUser(t, client, "profile")
	account, err := client.User.UpdateOneID(account.ID).
		SetDisplayName("Original").
		SetUpdatedAt(time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)).
		Save(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func assertProfileUnchanged(t *testing.T, client *ent.Client, original *ent.User) {
	t.Helper()
	stored, err := client.User.Get(context.Background(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DisplayName != original.DisplayName || stored.Email != original.Email || !stored.UpdatedAt.Equal(original.UpdatedAt) {
		t.Fatalf("failed update changed profile: display_name=%q, email=%q, updated_at=%v", stored.DisplayName, stored.Email, stored.UpdatedAt)
	}
}
