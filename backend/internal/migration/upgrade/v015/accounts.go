package v015

import (
	"fmt"
	"sort"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/accountidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"golang.org/x/crypto/bcrypt"
)

type Mapping map[string]struct {
	Username   *string `json:"username,omitempty"`
	Email      *string `json:"email,omitempty"`
	ClearEmail bool    `json:"-"`
}

func ReadMapping(path string) (Mapping, error) {
	if path == "" {
		return Mapping{}, nil
	}
	var raw map[string]map[string]*string
	if err := operation.Read(path, &raw); err != nil {
		return nil, err
	}
	out := Mapping{}
	for id, fields := range raw {
		m := out[id]
		for k, v := range fields {
			switch k {
			case "username":
				if v == nil {
					return nil, operation.Fail(operation.MappingRequired, "username mapping cannot be null")
				}
				m.Username = v
			case "email":
				m.Email = v
				m.ClearEmail = v == nil
			default:
				return nil, operation.Fail(operation.MappingRequired, "mapping only accepts username and email")
			}
		}
		out[id] = m
	}
	return out, nil
}

// MapAccounts validates the complete allocation before any target is changed.
func MapAccounts(users []SourceUser, mapping Mapping, mode string, localID int) ([]operation.Account, error) {
	result := make([]operation.Account, 0, len(users))
	used := map[string]bool{}
	known := map[string]bool{}
	administrators := 0
	localFound := false
	for _, u := range users {
		if u.ID <= 0 || (u.Role != "user" && u.Role != "admin") {
			return nil, operation.Fail(operation.UnsupportedSource, "source user identity or role is outside the released contract")
		}
		key := fmt.Sprint(u.ID)
		known[key] = true
		m := mapping[key]
		name := u.Username
		email := u.Email
		if m.Username != nil {
			name = *m.Username
		}
		if m.ClearEmail {
			email = ""
		} else if m.Email != nil {
			email = *m.Email
		}
		canonical, err := accountidentity.NormalizeUsername(name)
		if err != nil {
			return nil, operation.Fail(operation.MappingRequired, fmt.Sprintf("user %d requires an explicit valid username mapping", u.ID))
		}
		purpose := accountidentity.Public
		if u.Role == "admin" {
			purpose = accountidentity.Administrator
		}
		if mode == "local" && u.ID == localID {
			purpose = accountidentity.Local
			localFound = true
			if canonical != "local" || !u.Active || u.Role != "admin" {
				return nil, operation.Fail(operation.MappingRequired, "the original local administrator must remain local and active")
			}
		}
		if err := accountidentity.ValidateAllocation(canonical, purpose); err != nil || used[canonical] {
			return nil, operation.Fail(operation.MappingRequired, fmt.Sprintf("user %d has a reserved or conflicting username", u.ID))
		}
		used[canonical] = true
		normalized, err := accountidentity.NormalizeEmail(email)
		if err != nil {
			return nil, operation.Fail(operation.MappingRequired, fmt.Sprintf("user %d requires explicit email replacement or null", u.ID))
		}
		if !validBcrypt(u.PasswordHash) {
			return nil, operation.Fail(operation.UnsupportedSource, fmt.Sprintf("user %d has an invalid released password encoding; repair with the source release", u.ID))
		}
		if u.Role == "admin" && u.Active {
			administrators++
		}
		a := operation.Account{UserID: u.ID, Username: canonical}
		if normalized != "" {
			a.Email = &normalized
		}
		result = append(result, a)
	}
	for id := range mapping {
		if !known[id] {
			return nil, operation.Fail(operation.MappingRequired, "mapping contains an unknown or noncanonical user ID")
		}
	}
	if administrators == 0 || mode == "local" && !localFound {
		return nil, operation.Fail(operation.NeedsAction, "source requires an original usable administrator")
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UserID < result[j].UserID })
	return result, nil
}

func validBcrypt(hash string) bool {
	if len(hash) != 60 || (!strings.HasPrefix(hash, "$2a$") && !strings.HasPrefix(hash, "$2b$") && !strings.HasPrefix(hash, "$2y$")) || hash[6] != '$' {
		return false
	}
	if _, err := bcrypt.Cost([]byte(hash)); err != nil {
		return false
	}
	const alphabet = "./ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	for _, c := range hash[7:] {
		if !strings.ContainsRune(alphabet, c) {
			return false
		}
	}
	// bcrypt's final salt and digest characters must have zero unused bits.
	return strings.ContainsRune(".Oeu", rune(hash[28])) && strings.ContainsRune(".CGKOSWaeimquy26", rune(hash[59]))
}
