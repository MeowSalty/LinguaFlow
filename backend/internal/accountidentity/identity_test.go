package accountidentity

import (
	"strings"
	"testing"
)

func TestUsernameContract(t *testing.T) {
	valid := map[string]string{"Alice": "alice", " Alice ": "alice", "liang_01": "liang_01", "team-a2": "team-a2", "a-b": "a-b", "aaa": "aaa", strings.Repeat("a", 32): strings.Repeat("a", 32), strings.Repeat(" ", 123) + "Alice": "alice"}
	for input, want := range valid {
		got, err := NormalizeUsername(input)
		if err != nil || got != want {
			t.Errorf("%q = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"", "ab", strings.Repeat("a", 33), strings.Repeat(" ", 124) + "Alice", "12345", "1alice", "-alice", "alice_", "a--b", "a__b", "a-_b", "a_-b", "alice@example.com", "alice.smith", "ali ce", "a/b", "a\\b", "a:b", "张三", "Ａlice", "álìce", "Kelvin", "ali\u200bce", "\tAlice", "Alice\n", "Alice\r", "Alice\x7f"} {
		if got, err := NormalizeUsername(input); err == nil {
			t.Errorf("accepted %q as %q", input, got)
		}
	}
	if ValidateUsername("Alice") == nil || ValidateUsername(" alice ") == nil {
		t.Fatal("stored values were normalized instead of validated")
	}
}

func TestAllocationAndLookupAreSeparate(t *testing.T) {
	for _, name := range []string{"admin", "administrator", "root"} {
		if ValidateAllocation(name, Public) == nil || ValidateAllocation(name, Administrator) != nil {
			t.Fatalf("administrator allocation: %s", name)
		}
		if got, err := NormalizeUsername(" " + strings.ToUpper(name) + " "); err != nil || got != name {
			t.Fatal("reserved name cannot be looked up")
		}
	}
	for _, name := range []string{"system", "anonymous", "api", "auth", "login", "logout", "register", "signup", "settings", "users", "support", "security"} {
		for _, purpose := range []CreationPurpose{Public, Administrator, Local} {
			if ValidateAllocation(name, purpose) == nil {
				t.Fatalf("allocated system name %s", name)
			}
		}
	}
	if ValidateAllocation("local", Local) != nil || ValidateAllocation("local", Public) == nil || ValidateAllocation("local", Administrator) == nil {
		t.Fatal("local reservation")
	}
	if ValidateAllocation("admin-team", Public) != nil || ValidateAllocation("my-support", Public) != nil {
		t.Fatal("reserved names matched as substrings")
	}
}

func TestEmailContract(t *testing.T) {
	for input, want := range map[string]string{"": "", "  ": "", " Alice+TAG@Example.com ": "alice+tag@example.com", "a.b@example.com": "a.b@example.com"} {
		got, err := NormalizeEmail(input)
		if err != nil || got != want {
			t.Errorf("%q = %q %v", input, got, err)
		}
	}
	for _, input := range []string{"@", "alice@", "example.com", "Alice <alice@example.com>", "<alice@example.com>", "alice @example.com", "alice@example.com,bob@example.com"} {
		if _, err := NormalizeEmail(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}
