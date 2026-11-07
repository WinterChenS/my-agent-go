package auth

// OAuth 授权码 + PKCE 的客户端实现。
//
// 只做桌面公共客户端真正需要的那部分：授权 URL、换码、刷新、撤销。不实现
// "客户端凭据"或"内置 secret 的隐式流"——把 client secret 编进二进制等于
// 公开发布它。

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
)

// maxTokenBody 是令牌端点响应体上限。
const maxTokenBody = 64 << 10

// defaultHTTPTimeout 是令牌端点单次请求的上限。
const defaultHTTPTimeout = 30 * time.Second

// PKCE 是一对 verifier/challenge。
type PKCE struct {
	Verifier  string
	Challenge string
}

// NewPKCE 生成 32 字节随机 verifier 及其 S256 challenge。
func NewPKCE() (PKCE, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return PKCE{}, contracts.NewAppError(contracts.CodeInternal, "生成 PKCE verifier 失败: "+err.Error(), false)
	}
	verifier := base64.RawURLEncoding.EncodeToString(buf)
	return PKCE{Verifier: verifier, Challenge: ChallengeFor(verifier)}, nil
}

// ChallengeFor 计算 S256 challenge。
func ChallengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Provider 是一个平台的 OAuth 端点与客户端注册信息。
//
// ClientSecret 只允许来自用户提供的应用注册，并且只能存在于凭证库/内存里。
type Provider struct {
	AuthorizeURL string
	TokenURL     string
	ClientID     string
	// ClientSecret 为空表示公共客户端（只有 PKCE）。
	ClientSecret []byte
	Scopes       []string
}

// Validate 校验端点与注册信息。
func (p Provider) Validate() error {
	if strings.TrimSpace(p.AuthorizeURL) == "" || strings.TrimSpace(p.TokenURL) == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing, "OAuth 授权/令牌端点缺失", false)
	}
	for _, raw := range []string{p.AuthorizeURL, p.TokenURL} {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return contracts.NewAppError(contracts.CodeConfigMissing, "OAuth 端点不是合法 URL: "+raw, false)
		}
	}
	if strings.TrimSpace(p.ClientID) == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing, "缺少用户提供的 OAuth client id（内置客户端注册不允许）", false)
	}
	return nil
}

// BuildAuthorizeURL 拼出要交给浏览器/系统浏览器的授权地址。
func (p Provider) BuildAuthorizeURL(redirectURI, state, challenge string) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	if err := rejectNonLoopbackRedirect(redirectURI); err != nil {
		return "", err
	}
	if strings.TrimSpace(state) == "" {
		return "", contracts.NewAppError(contracts.CodeUnsupported, "OAuth state 不能为空", false)
	}
	if strings.TrimSpace(challenge) == "" {
		return "", contracts.NewAppError(contracts.CodeUnsupported, "PKCE challenge 不能为空", false)
	}
	u, err := url.Parse(p.AuthorizeURL)
	if err != nil {
		return "", contracts.NewAppError(contracts.CodeConfigMissing, "授权端点不合法: "+err.Error(), false)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if len(p.Scopes) > 0 {
		q.Set("scope", strings.Join(p.Scopes, " "))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ExchangeRequest 是一次换码请求。
type ExchangeRequest struct {
	Code        string
	Verifier    string
	RedirectURI string
}

// Client 是令牌端点的真实 HTTP 客户端。
type Client struct {
	Provider Provider
	HTTP     *http.Client
	Now      func() time.Time
}

// NewClient 构造客户端。
func NewClient(p Provider) (*Client, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &Client{
		Provider: p,
		HTTP:     &http.Client{Timeout: defaultHTTPTimeout},
		Now:      time.Now,
	}, nil
}

// Exchange 用授权码换令牌。
func (c *Client) Exchange(ctx context.Context, req ExchangeRequest) (TokenSet, error) {
	if err := rejectNonLoopbackRedirect(req.RedirectURI); err != nil {
		return TokenSet{}, err
	}
	if strings.TrimSpace(req.Code) == "" {
		return TokenSet{}, contracts.NewAppError(contracts.CodeUnauthorized, "授权码为空", false)
	}
	if strings.TrimSpace(req.Verifier) == "" {
		return TokenSet{}, contracts.NewAppError(contracts.CodeUnauthorized, "缺少 PKCE verifier，拒绝换码", false)
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", req.Code)
	form.Set("redirect_uri", req.RedirectURI)
	form.Set("client_id", c.Provider.ClientID)
	form.Set("code_verifier", req.Verifier)
	if len(c.Provider.ClientSecret) > 0 {
		form.Set("client_secret", string(c.Provider.ClientSecret))
	}
	return c.token(ctx, form)
}

// Refresh 用刷新令牌换新令牌。
func (c *Client) Refresh(ctx context.Context, refreshToken string) (TokenSet, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return TokenSet{}, contracts.NewAppError(contracts.CodeUnauthorized, "刷新令牌为空", false)
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", c.Provider.ClientID)
	if len(c.Provider.ClientSecret) > 0 {
		form.Set("client_secret", string(c.Provider.ClientSecret))
	}
	return c.token(ctx, form)
}

// token 执行一次表单 POST 并解析令牌响应。
func (c *Client) token(ctx context.Context, form url.Values) (TokenSet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Provider.TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return TokenSet{}, contracts.NewAppError(contracts.CodeInternal, "构造令牌请求失败: "+err.Error(), false)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return TokenSet{}, contracts.NewAppError(contracts.CodeProviderUnreachable, "令牌端点不可达: "+err.Error(), true)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenBody))
	if err != nil {
		return TokenSet{}, contracts.NewAppError(contracts.CodeProviderError, "读取令牌响应失败: "+err.Error(), true)
	}
	if resp.StatusCode/100 != 2 {
		var failure struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &failure)
		detail := strings.TrimSpace(failure.Error)
		if strings.TrimSpace(failure.Description) != "" {
			detail += " (" + strings.TrimSpace(failure.Description) + ")"
		}
		if detail == "" {
			detail = "HTTP " + resp.Status
		}
		// 换码/刷新失败都是"需要重新授权"，不是可重试的网络错误。
		return TokenSet{}, contracts.NewAppError(contracts.CodeUnauthorized, "令牌端点拒绝: "+detail, false)
	}
	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return TokenSet{}, contracts.NewAppError(contracts.CodeProviderError, "令牌响应不是合法 JSON: "+err.Error(), false)
	}
	if strings.TrimSpace(raw.AccessToken) == "" {
		return TokenSet{}, contracts.NewAppError(contracts.CodeProviderError, "令牌响应缺少 access_token", false)
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	set := TokenSet{
		AccessToken:  raw.AccessToken,
		RefreshToken: raw.RefreshToken,
		TokenType:    raw.TokenType,
	}
	if set.TokenType == "" {
		set.TokenType = "Bearer"
	}
	if raw.ExpiresIn > 0 {
		set.Expiry = now().Add(time.Duration(raw.ExpiresIn) * time.Second)
	}
	return set, nil
}
