// Package previewtoken provides stateless HMAC/JWT apply tokens for segment
// translation previews. Tokens are signed with the server's JWT secret and
// include all claims needed for conflict-safe apply.
package previewtoken

import (
	"errors"
	"fmt"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrTokenInvalid = errors.New("preview token: invalid")
	ErrTokenExpired = errors.New("preview token: expired")
	ErrTokenIssuer  = errors.New("preview token: issuer mismatch")
	ErrTokenType    = errors.New("preview token: type mismatch")
)

const (
	tokenIssuer = "linguaflow-preview"
	tokenType   = "preview-apply"
)

// ApplyClaims are the JWT claims embedded in a preview apply token.
type ApplyClaims struct {
	jwt.RegisteredClaims

	// Type is always "preview-apply" for domain isolation.
	Type string `json:"type"`

	// ActorUserID is the user who requested the preview.
	ActorUserID int `json:"uid"`
	// ProjectID is the project containing the segment.
	ProjectID int `json:"pid"`
	// ResourceID is the resource containing the segment.
	ResourceID int `json:"rid"`
	// SegmentID is the specific segment.
	SegmentID int `json:"sid"`
	// ExecutionPlanID identifies the plan used for preview.
	ExecutionPlanID int `json:"epid"`

	// SourceHash is a hash of the source text at preview time.
	SourceHash string `json:"sh"`
	// PreviewSource is the source text used by the virtual preview document.
	PreviewSource string `json:"ps"`
	// TargetHash is a hash of the preview target text (empty if no target).
	TargetHash string `json:"th"`

	// BaselineSource is the database source text at preview time.
	BaselineSource string `json:"bs"`
	// BaselineTarget is the nullable database target text at preview time.
	BaselineTarget *string `json:"bt,omitempty"`
	// BaselineStatus is the database status at preview time.
	BaselineStatus string `json:"bst"`

	// FinalIssues are the quality issues determined by the preview run.
	FinalIssues []qa.QualityIssue `json:"fi,omitempty"`

	// QAConfig encodes the deterministic QA configuration used during preview
	// so that apply can re-run deterministic QA if the user modified the target.
	QAConfig QAConfigClaims `json:"qc"`
}

// QAConfigClaims captures the deterministic QA configuration.
type QAConfigClaims struct {
	Enabled        bool     `json:"enabled"`
	Checks         []string `json:"checks,omitempty"`
	LengthMethod   string   `json:"length_method,omitempty"`
	LengthRatioMin float64  `json:"length_ratio_min,omitempty"`
	LengthRatioMax float64  `json:"length_ratio_max,omitempty"`
	SourceLang     string   `json:"src_lang,omitempty"`
	TargetLang     string   `json:"tgt_lang,omitempty"`
	Format         string   `json:"fmt,omitempty"`
}

// Codec creates and validates preview apply tokens.
type Codec struct {
	secret []byte
	ttl    time.Duration
}

// NewCodec creates a token codec with the given HMAC secret and TTL.
func NewCodec(secret string, ttl time.Duration) *Codec {
	return &Codec{secret: []byte(secret), ttl: ttl}
}

// Encode creates a signed apply token from the given claims.
func (c *Codec) Encode(claims ApplyClaims) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(c.ttl)
	claims.RegisteredClaims = jwt.RegisteredClaims{
		Issuer:    tokenIssuer,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(exp),
		ID:        fmt.Sprintf("preview-%d-%d", claims.SegmentID, now.UnixMilli()),
	}
	claims.Type = tokenType
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(c.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("preview token: sign: %w", err)
	}
	return signed, exp, nil
}

// Decode verifies and parses the token, returning the claims.
func (c *Codec) Decode(tokenStr string) (*ApplyClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &ApplyClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("preview token: unexpected signing method %v", t.Header["alg"])
		}
		return c.secret, nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, fmt.Errorf("%w: %w", ErrTokenInvalid, err)
	}

	claims, ok := token.Claims.(*ApplyClaims)
	if !ok || !token.Valid {
		return nil, ErrTokenInvalid
	}

	if claims.Issuer != tokenIssuer {
		return nil, ErrTokenIssuer
	}
	if claims.Type != tokenType {
		return nil, ErrTokenType
	}

	return claims, nil
}

// VerifyOwnership checks that the token belongs to the given actor, project,
// resource, and segment. Returns nil on success.
func VerifyOwnership(claims *ApplyClaims, actorUserID, projectID, resourceID, segmentID int) error {
	if claims.ActorUserID != actorUserID {
		return fmt.Errorf("%w: user mismatch", ErrTokenInvalid)
	}
	if claims.ProjectID != projectID {
		return fmt.Errorf("%w: project mismatch", ErrTokenInvalid)
	}
	if claims.ResourceID != resourceID {
		return fmt.Errorf("%w: resource mismatch", ErrTokenInvalid)
	}
	if claims.SegmentID != segmentID {
		return fmt.Errorf("%w: segment mismatch", ErrTokenInvalid)
	}
	return nil
}
