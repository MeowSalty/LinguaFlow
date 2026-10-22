package accountidentity

import (
	"strings"
	"testing"
)

func TestNormalizeDomainContract(t *testing.T) {
	for input, want := range map[string]string{
		"Example.COM":    "example.com",
		"München.de":     "xn--mnchen-3ya.de",
		"Bücher.example": "xn--bcher-kva.example",
		"ß.example":      "xn--zca.example",
		"LOCALHOST":      "localhost",
	} {
		got, err := NormalizeDomain(input)
		if err != nil || got != want {
			t.Errorf("NormalizeDomain(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range append([]string{
		"", ".", "example.com.", "*", "*.example.com", "example.*.com",
		"192.0.2.1", "::1", "[IPv6:::1]", "-example.com", "example-.com",
		"bad..example", "bad_label.example", "xn--.example", "xn--a.example",
		"example.com。", "。example.com", "https://example.com", "@example.com",
	}, strings.Repeat("a", 64)+".example") {
		if got, err := NormalizeDomain(input); err == nil {
			t.Errorf("NormalizeDomain accepted %q as %q", input, got)
		}
	}
	if DomainNormalizationVersion() == "" {
		t.Fatal("normalization version is empty")
	}
}

func TestNormalizeDomainDNSLengthBoundaries(t *testing.T) {
	label63 := strings.Repeat("a", 63)
	domain253 := label63 + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	for _, input := range []string{label63 + ".example", domain253} {
		if got, err := NormalizeDomain(input); err != nil || got != input {
			t.Errorf("valid DNS boundary (%d bytes) = %q, %v", len(input), got, err)
		}
		if got, err := NormalizeEmailDomain("u@" + input); err != nil || got != input {
			t.Errorf("address DNS boundary (%d bytes) = %q, %v", len(input), got, err)
		}
	}
	for _, input := range []string{strings.Repeat("a", 64) + ".example", domain253 + "d"} {
		if _, err := NormalizeDomain(input); err == nil {
			t.Errorf("accepted oversized DNS domain (%d bytes)", len(input))
		}
		if _, err := NormalizeEmailDomain("u@" + input); err == nil {
			t.Errorf("accepted oversized address DNS domain (%d bytes)", len(input))
		}
	}
}

func TestNormalizeEmailDomainContract(t *testing.T) {
	for input, want := range map[string]string{
		"User@Example.COM": "example.com",
		"user@münchen.de":  "xn--mnchen-3ya.de",
	} {
		got, err := NormalizeEmailDomain(input)
		if err != nil || got != want {
			t.Errorf("NormalizeEmailDomain(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{
		"", "@", "user@", "example.com", "User <user@example.com>",
		`"user"@example.com`, "user@example.com.", "user@192.0.2.1",
	} {
		if got, err := NormalizeEmailDomain(input); err == nil {
			t.Errorf("NormalizeEmailDomain accepted %q as %q", input, got)
		}
	}
}
