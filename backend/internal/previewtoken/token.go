// Package previewtoken 提供用于分段翻译预览的无状态 HMAC/JWT apply 令牌。
// 令牌使用服务器的 JWT 密钥签名，并携带冲突安全 apply 所需的全部声明。
package previewtoken

import (
	"errors"
	"fmt"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
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

	// KindTranslate 沿用翻译预览历史上遗留的空字符串 kind。
	KindTranslate = ""
	// KindRevision 标记由修订预览流程签发的令牌。
	KindRevision = "fix"
)

// ApplyClaims 是预览 apply 令牌中内嵌的 JWT 声明。
type ApplyClaims struct {
	jwt.RegisteredClaims

	// Type 恒为 "preview-apply"，用于领域隔离。
	Type string `json:"type"`

	// Kind 区分预览用途，供审计路由使用。空值表示 "translate" 翻译预览；
	// "fix" 表示修订预览。它不改变令牌校验与应用语义。
	Kind string `json:"kd,omitempty"`

	// ActorUserID 是请求预览的用户。
	ActorUserID int `json:"uid"`
	// ProjectID 是分段所属的项目。
	ProjectID int `json:"pid"`
	// ResourceID 是分段所属的资源。
	ResourceID       int   `json:"rid"`
	SourceRevisionID *int  `json:"srid,omitempty"`
	SourceGeneration int64 `json:"sg"`
	// SegmentID 是具体的分段。
	SegmentID int `json:"sid"`
	// ExecutionPlanID 标识预览所用的执行计划。
	ExecutionPlanID int `json:"epid"`

	// SourceHash 是预览时 source 文本的哈希。
	SourceHash string `json:"sh"`
	// PreviewSource 是虚拟预览文档使用的 source 文本。
	PreviewSource string `json:"ps"`
	// TargetHash 是预览 target 文本的哈希（无 target 时为空）。
	TargetHash string `json:"th"`

	// BaselineSource 是预览时数据库中的 source 文本。
	BaselineSource string `json:"bs"`
	// BaselineTarget 是预览时数据库中可空的 target 文本。
	BaselineTarget *string `json:"bt,omitempty"`
	// BaselineStatus 是预览时数据库中的状态。
	BaselineStatus  string `json:"bst"`
	BaselineVersion int64  `json:"bv,omitempty"`

	// FinalIssues 是预览运行确定的质检 issue。
	FinalIssues []qa.QualityIssue `json:"fi,omitempty"`

	// ResolvedCodes 是修订预览声明已修复的 issue code 集合（仅 KindRevision 令牌
	// 携带；翻译预览为空，维持整体替换语义）。用户 apply 前改写文本时，仍按此
	// 集合从段落既有 issue 中剔除 pending 项。旧令牌无此字段时为空，退化为旧行为。
	ResolvedCodes []string `json:"rc,omitempty"`

	// QAConfig 编码预览期间使用的确定性 QA 配置，
	// 用户改写 target 时 apply 可据此重跑确定性 QA。
	QAConfig QAConfigClaims `json:"qc"`
}

// QAConfigClaims 捕获确定性 QA 配置。
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

// Codec 创建并校验预览 apply 令牌。
type Codec struct {
	secret []byte
	ttl    time.Duration
}

// NewCodec 使用给定的 HMAC 密钥与 TTL 创建令牌编解码器。
func NewCodec(secret string, ttl time.Duration) *Codec {
	return &Codec{secret: []byte(secret), ttl: ttl}
}

// Encode 根据给定声明创建签名的 apply 令牌。
func (c *Codec) Encode(claims ApplyClaims) (string, time.Time, error) {
	now := timeutil.NowUTC()
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
	return signed, timeutil.Normalize(claims.ExpiresAt.Time), nil
}

// Decode 校验并解析令牌，返回其中的声明。
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

// VerifyOwnership 检查令牌是否属于给定的用户、项目、资源与分段。
// 成功时返回 nil。
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
