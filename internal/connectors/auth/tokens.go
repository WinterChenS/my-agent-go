// Package auth 是连接器的授权机制：OAuth 授权码 + PKCE、RFC 8252 回环重定向，
// 以及"令牌只以 credential ref 出现"的存放方式。
//
// 边界：
//   - 回调只绑 127.0.0.1 的随机端口，只接受一次请求，默认 5 分钟超时；
//   - state 与 PKCE verifier 全程校验，失败即 fail-closed（不"重试一下看看"）；
//   - 令牌（以及用户提供的 client secret）只进 OS 凭证库，对外只有 ref。
//
// 本机没有真实 OAuth 客户端注册，因此本包的端到端证据来自回环 fixture
// （internal/connectors/fixtures）。真实 provider 的授权是 BLOCKED，
// 缺的是每个平台的 client id/secret 与登记的 loopback redirect。
package auth

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/credentials"
)

// TokenSet 是一次授权得到的令牌集合。
//
// 它只允许存在于凭证库或调用栈上的临时变量里：JSON 序列化只有"写进
// credentials.Store"这一个用途，String/GoString 一律遮蔽。
type TokenSet struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	TokenType    string    `json:"tokenType"`
	Expiry       time.Time `json:"expiry"`
}

// String 遮蔽令牌内容。
func (t TokenSet) String() string {
	return "TokenSet(type=" + t.TokenType + ", expiry=" + t.Expiry.UTC().Format(time.RFC3339) + ", secret=REDACTED)"
}

// GoString 让 %#v 也不泄露。
func (t TokenSet) GoString() string { return t.String() }

// Expired 判定是否需要在 skew 之前刷新。
func (t TokenSet) Expired(now time.Time, skew time.Duration) bool {
	if t.AccessToken == "" {
		return true
	}
	if t.Expiry.IsZero() {
		return false
	}
	return !now.Add(skew).Before(t.Expiry)
}

// Exchanger 是刷新令牌所需的最小能力（由 Client 实现）。
type Exchanger interface {
	Refresh(ctx context.Context, refreshToken string) (TokenSet, error)
}

// Store 是令牌的凭证库包装：对外只有 ref。
type Store struct {
	creds credentials.Store
}

// NewStore 绑定凭证库。
func NewStore(creds credentials.Store) (*Store, error) {
	if creds == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "令牌存储缺少凭证库", false)
	}
	return &Store{creds: creds}, nil
}

// Save 把令牌集合写入凭证库，返回可以对外展示的 ref。
//
// ref 由调用方给定（例如 "connector:mail:oauth"），它同时也是定义里唯一记录的
// 凭证标识；令牌本体不会出现在任何配置/日志/审计/DTO 中。
func (s *Store) Save(ctx context.Context, ref string, set TokenSet) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", contracts.NewAppError(contracts.CodeUnsupported, "令牌 ref 为空", false)
	}
	if strings.TrimSpace(set.AccessToken) == "" {
		return "", contracts.NewAppError(contracts.CodeUnsupported, "令牌集合没有 access token", false)
	}
	if set.TokenType == "" {
		set.TokenType = "Bearer"
	}
	raw, err := json.Marshal(set)
	if err != nil {
		return "", contracts.NewAppError(contracts.CodeInternal, "序列化令牌失败: "+err.Error(), false)
	}
	if err := s.creds.Put(ctx, ref, raw); err != nil {
		return "", err
	}
	return ref, nil
}

// Load 读回令牌集合。
func (s *Store) Load(ctx context.Context, ref string) (TokenSet, error) {
	raw, err := s.creds.Get(ctx, strings.TrimSpace(ref))
	if err != nil {
		return TokenSet{}, err
	}
	var set TokenSet
	if err := json.Unmarshal(raw, &set); err != nil {
		return TokenSet{}, contracts.NewAppError(contracts.CodeInternal, "凭证库中的令牌格式损坏: "+err.Error(), false)
	}
	if strings.TrimSpace(set.AccessToken) == "" {
		return TokenSet{}, contracts.NewAppError(contracts.CodeUnauthorized, "凭证库中的令牌为空，需要重新授权", false)
	}
	return set, nil
}

// Revoke 撤销授权：删除凭证（幂等）。
//
// 撤销之后工具必须从下一个 run 消失：定义里的 ref 还在，但取不到材料，
// 任何调用都会在派发前失败。
func (s *Store) Revoke(ctx context.Context, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return contracts.NewAppError(contracts.CodeUnsupported, "令牌 ref 为空", false)
	}
	return s.creds.Delete(ctx, ref)
}

// AccessToken 返回可用的 access token；临近过期则先刷新并写回凭证库。
//
// 刷新失败（没有 refresh token、端点拒绝、网络不可达）一律返回需要重新授权的
// 结构化错误——不能"继续用过期令牌试试"，那只会得到难解释的 401。
func (s *Store) AccessToken(ctx context.Context, ref string, now time.Time, skew time.Duration, ex Exchanger) (string, error) {
	set, err := s.Load(ctx, ref)
	if err != nil {
		return "", err
	}
	if !set.Expired(now, skew) {
		return set.AccessToken, nil
	}
	if strings.TrimSpace(set.RefreshToken) == "" {
		return "", contracts.NewAppError(contracts.CodeUnauthorized, "令牌已过期且没有刷新令牌，需要重新授权", false)
	}
	if ex == nil {
		return "", contracts.NewAppError(contracts.CodeConfigMissing, "没有配置刷新端点，需要重新授权", false)
	}
	refreshed, err := ex.Refresh(ctx, set.RefreshToken)
	if err != nil {
		return "", contracts.NewAppError(contracts.CodeUnauthorized, "刷新令牌失败，需要重新授权: "+err.Error(), false)
	}
	if strings.TrimSpace(refreshed.RefreshToken) == "" {
		// 部分平台刷新时不返回新的 refresh token：沿用旧的，避免下一轮无法刷新。
		refreshed.RefreshToken = set.RefreshToken
	}
	if _, err := s.Save(ctx, ref, refreshed); err != nil {
		return "", err
	}
	return refreshed.AccessToken, nil
}
