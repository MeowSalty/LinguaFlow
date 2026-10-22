// Package accountidentity defines account names independently of organization
// slugs, passwords, email addresses and external issuer/subject identifiers.
package accountidentity

import (
	"errors"
	"net/mail"
	"regexp"
	"strings"
	"unicode"
)

type CreationPurpose uint8

const (
	Public CreationPurpose = iota
	Administrator
	Local
)

var usernamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[-_][a-z0-9]+)*$`)

func NormalizeUsername(raw string) (string, error) {
	if len(raw) > 128 {
		return "", errors.New("username input exceeds 128 bytes")
	}
	for i := range len(raw) {
		if raw[i] < 32 || raw[i] >= 127 {
			return "", errors.New("username contains non-ASCII or control characters")
		}
	}
	value := []byte(strings.Trim(raw, " "))
	for i, b := range value {
		if b >= 'A' && b <= 'Z' {
			value[i] = b + ('a' - 'A')
		}
	}
	name := string(value)
	if err := ValidateUsername(name); err != nil {
		return "", err
	}
	return name, nil
}

// ValidateUsername validates a stored canonical value without normalizing it.
func ValidateUsername(name string) error {
	if len(name) < 3 || len(name) > 32 || !usernamePattern.MatchString(name) {
		return errors.New("username must be 3–32 canonical ASCII characters with separated interior - or _")
	}
	return nil
}

// CreationPurpose is chosen by trusted server entrypoints, never a request flag.
// Existing account lookups call NormalizeUsername without allocation checks.
func ValidateAllocation(name string, purpose CreationPurpose) error {
	if err := ValidateUsername(name); err != nil {
		return err
	}
	if purpose != Public && purpose != Administrator && purpose != Local {
		return errors.New("unsupported username allocation purpose")
	}
	if name == "local" {
		if purpose == Local {
			return nil
		}
		return errors.New("local is reserved for internal local initialization")
	}
	if purpose == Local {
		return errors.New("local initialization requires the local username")
	}
	switch name {
	case "admin", "administrator", "root":
		if purpose != Administrator {
			return errors.New("username is reserved for administrator creation")
		}
	case "system", "anonymous", "api", "auth", "login", "logout", "register", "signup", "settings", "users", "support", "security":
		return errors.New("username is reserved for system use")
	}
	return nil
}

// NormalizeEmail preserves the existing plain-address profile contract. Empty
// means absent; no provider-specific dot/tag folding or ownership is inferred.
func NormalizeEmail(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "", nil
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Name != "" || address.Address != value || strings.ContainsFunc(value, unicode.IsSpace) {
		return "", errors.New("email must be a single plain address")
	}
	return value, nil
}
