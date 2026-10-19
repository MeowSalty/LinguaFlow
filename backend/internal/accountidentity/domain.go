// Package accountidentity defines canonical account identity contracts.
package accountidentity

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"golang.org/x/net/idna"
)

const maxDomainInputBytes = 1024

// Lookup profile is deliberately explicit: every upgrade must be reflected in
// the dependency fingerprint, and no transitional or display-domain behavior is
// enabled accidentally.
var emailDomainProfile = idna.New(
	idna.MapForLookup(),
	idna.Transitional(false),
	idna.ValidateLabels(true),
	idna.StrictDomainName(true),
	idna.VerifyDNSLength(true),
	idna.BidiRule(),
	idna.RemoveLeadingDots(false),
)

// DomainNormalizationVersion binds domain values to the explicit normalization
// profile. The identifier is tied to this B5 contract and its profile options;
// changing the x/net module or Unicode tables makes the module version change
// visible in the overall build.
func DomainNormalizationVersion() string {
	return fmt.Sprintf("account_email_domain/v1;profile=idna-lookup;transitional=0;validate-labels=1;strict-domain-name=1;verify-dns-length=1;bidi=1;remove-leading-dots=0;idna-unicode=%s;x/net=v0.60.0;x/text=v0.43.0", idna.UnicodeVersion)
}

// NormalizeEmail extracts the email domain only after the established strict
// plain-address normalization succeeds. It never guesses around an @ and never
// trims or normalizes the domain separately from the address.
func NormalizeEmailDomain(rawEmail string) (string, error) {
	address, err := NormalizeEmail(rawEmail)
	if err != nil {
		return "", errors.New("invalid email address")
	}
	at := strings.LastIndex(address, "@")
	if at < 1 || at == len(address)-1 {
		return "", errors.New("email address has no domain")
	}
	return NormalizeDomain(address[at+1:])
}

// NormalizeDomain canonicalizes one DNS domain to lowercase ASCII A-labels.
// It rejects quoted/display values, root dots, IP literals, wildcards and
// invalid labels, and revalidates the normalized result.
func NormalizeDomain(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("domain must not be empty")
	}
	if len(raw) > maxDomainInputBytes {
		return "", errors.New("domain input exceeds 1024 bytes")
	}
	if strings.HasPrefix(raw, ".") || strings.HasSuffix(raw, ".") {
		return "", errors.New("domain must not have a root dot")
	}
	normalized, err := emailDomainProfile.ToASCII(raw)
	if err != nil || strings.HasSuffix(normalized, ".") {
		return "", errors.New("invalid DNS domain")
	}
	if net.ParseIP(normalized) != nil || strings.ContainsAny(normalized, ":[%") {
		return "", errors.New("domain must be a DNS name, not an IP literal")
	}
	labels := strings.Split(normalized, ".")
	total := 0
	for i, label := range labels {
		if label == "" || len(label) > 63 {
			return "", errors.New("invalid DNS label")
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", errors.New("DNS label must not begin or end with a hyphen")
		}
		if i != 0 {
			total++
		}
		total += len(label)
	}
	if total > 253 {
		return "", errors.New("DNS domain exceeds 253 bytes")
	}
	revalidated, err := emailDomainProfile.ToASCII(normalized)
	if err != nil || revalidated != normalized {
		return "", errors.New("domain is not canonical")
	}
	return normalized, nil
}
