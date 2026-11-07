package auth_test

// C08a 的授权机制测试：PKCE + state + 回环回调 + 令牌只以 ref 出现。
//
// 真实 provider 的 OAuth 是 BLOCKED（本机没有 client id/secret 与登记的
// loopback redirect），因此端到端证据来自 internal/connectors/fixtures 的
// OAuth fixture：它实现了真实的授权码一次性/有效期/verifier 校验。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/connectors/auth"
	"my-agent-go/internal/connectors/fixtures"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/credentials"
)

const fixtureClientID = "fixture-desktop-client"

// noRedirectClient 不自动跟随 302：我们要读 Location 里的 code/state。
func noRedirectClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// authorizeOnce 模拟浏览器走一遍授权端点，返回回调 URL。
func authorizeOnce(t *testing.T, oauth *fixtures.OAuth, redirectURI, state, challenge string) string {
	t.Helper()
	provider := auth.Provider{
		AuthorizeURL: oauth.AuthorizeURL(),
		TokenURL:     oauth.TokenURL(),
		ClientID:     fixtureClientID,
	}
	authURL, err := provider.BuildAuthorizeURL(redirectURI, state, challenge)
	if err != nil {
		t.Fatalf("拼授权 URL: %v", err)
	}
	resp, err := noRedirectClient().Get(authURL)
	if err != nil {
		t.Fatalf("访问授权端点: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("授权端点应 302，实际 %d", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("解析 Location: %v", err)
	}
	if loc.Query().Get("code") == "" {
		t.Fatalf("回调 URL 里没有 code: %s", loc)
	}
	return loc.String()
}

// TestOAuthLoopbackPKCEAndStateValidation 是 brief 指定的核心用例：
// 正常路径成功；state 不匹配、授权码过期、授权码重放都被拒绝；令牌只以 ref 出现。
func TestOAuthLoopbackPKCEAndStateValidation(t *testing.T) {
	ctx := context.Background()
	oauth, err := fixtures.NewOAuth(fixtures.Options{})
	if err != nil {
		t.Fatalf("启动 OAuth fixture: %v", err)
	}
	t.Cleanup(func() { _ = oauth.Close() })

	pkce, err := auth.NewPKCE()
	if err != nil {
		t.Fatalf("生成 PKCE: %v", err)
	}
	if len(pkce.Verifier) < 43 {
		t.Fatalf("PKCE verifier 太短: %d", len(pkce.Verifier))
	}
	if pkce.Challenge != auth.ChallengeFor(pkce.Verifier) {
		t.Fatal("challenge 必须是 verifier 的 S256")
	}

	loopback, err := auth.NewLoopback()
	if err != nil {
		t.Fatalf("启动回环监听: %v", err)
	}
	t.Cleanup(func() { _ = loopback.Close() })
	if !strings.HasPrefix(loopback.RedirectURI(), "http://127.0.0.1:") {
		t.Fatalf("回调必须是回环随机端口: %s", loopback.RedirectURI())
	}
	if err := auth.ValidateRedirectURI(loopback.RedirectURI()); err != nil {
		t.Fatalf("回环 redirect_uri 应合法: %v", err)
	}

	state := "state-abc-123"
	callbackURL := authorizeOnce(t, oauth, loopback.RedirectURI(), state, pkce.Challenge)
	resp, err := noRedirectClient().Get(callbackURL)
	if err != nil {
		t.Fatalf("访问回调: %v", err)
	}
	_ = resp.Body.Close()

	cb, err := loopback.Wait(ctx, state)
	if err != nil {
		t.Fatalf("等待回调: %v", err)
	}
	if cb.State != state {
		t.Fatalf("回调 state 不对: %q", cb.State)
	}

	client, err := auth.NewClient(auth.Provider{
		AuthorizeURL: oauth.AuthorizeURL(),
		TokenURL:     oauth.TokenURL(),
		ClientID:     fixtureClientID,
	})
	if err != nil {
		t.Fatalf("auth.NewClient: %v", err)
	}
	set, err := client.Exchange(ctx, auth.ExchangeRequest{
		Code:        cb.Code,
		Verifier:    pkce.Verifier,
		RedirectURI: loopback.RedirectURI(),
	})
	if err != nil {
		t.Fatalf("换码失败: %v", err)
	}
	if !strings.HasPrefix(set.AccessToken, "at-") {
		t.Fatalf("access token 形状不对: %s", set.String())
	}
	if set.Expired(time.Now(), 0) {
		t.Fatal("刚签发的令牌不应过期")
	}
	if set.String() == "" || strings.Contains(set.String(), set.AccessToken) {
		t.Fatalf("auth.TokenSet 的字符串形式必须遮蔽令牌: %s", set.String())
	}

	creds := credentials.NewMemStore()
	store, err := auth.NewStore(creds)
	if err != nil {
		t.Fatalf("令牌存储: %v", err)
	}
	ref, err := store.Save(ctx, "connector:mail:oauth", set)
	if err != nil {
		t.Fatalf("保存令牌: %v", err)
	}
	if ref != "connector:mail:oauth" {
		t.Fatalf("ref 不对: %s", ref)
	}
	// 配置只允许出现 ref：把它序列化成"配置行"并扫描明文令牌。
	configRow, _ := json.Marshal(map[string]any{
		"id":            "mail-1",
		"kind":          "mail",
		"credentialRef": ref,
	})
	if strings.Contains(string(configRow), set.AccessToken) || strings.Contains(string(configRow), set.RefreshToken) {
		t.Fatalf("配置里出现了令牌明文: %s", configRow)
	}
	// 反过来：凭证库里确实有它（否则"配置里没有"只是因为根本没存）。
	raw, err := creds.Get(ctx, ref)
	if err != nil {
		t.Fatalf("凭证库应保存令牌: %v", err)
	}
	if !strings.Contains(string(raw), set.AccessToken) {
		t.Fatal("凭证库内容不含 access token")
	}

	// 重放同一个授权码必须被拒绝。
	if _, err := client.Exchange(ctx, auth.ExchangeRequest{
		Code:        cb.Code,
		Verifier:    pkce.Verifier,
		RedirectURI: loopback.RedirectURI(),
	}); err == nil {
		t.Fatal("重放授权码必须失败")
	} else {
		requireAuthCode(t, err, "already used")
	}

	// 授权码过期。
	expiredOAuth, err := fixtures.NewOAuth(fixtures.Options{CodeTTL: -time.Second})
	if err != nil {
		t.Fatalf("启动过期码 fixture: %v", err)
	}
	t.Cleanup(func() { _ = expiredOAuth.Close() })
	expiredLoop, err := auth.NewLoopback()
	if err != nil {
		t.Fatalf("回环: %v", err)
	}
	t.Cleanup(func() { _ = expiredLoop.Close() })
	expiredURL := authorizeOnce(t, expiredOAuth, expiredLoop.RedirectURI(), "state-exp", pkce.Challenge)
	resp2, err := noRedirectClient().Get(expiredURL)
	if err != nil {
		t.Fatalf("访问过期回调: %v", err)
	}
	_ = resp2.Body.Close()
	expiredCB, err := expiredLoop.Wait(ctx, "state-exp")
	if err != nil {
		t.Fatalf("等待过期回调: %v", err)
	}
	expiredClient, err := auth.NewClient(auth.Provider{
		AuthorizeURL: expiredOAuth.AuthorizeURL(),
		TokenURL:     expiredOAuth.TokenURL(),
		ClientID:     fixtureClientID,
	})
	if err != nil {
		t.Fatalf("auth.NewClient: %v", err)
	}
	if _, err := expiredClient.Exchange(ctx, auth.ExchangeRequest{
		Code:        expiredCB.Code,
		Verifier:    pkce.Verifier,
		RedirectURI: expiredLoop.RedirectURI(),
	}); err == nil {
		t.Fatal("过期授权码必须失败")
	} else {
		requireAuthCode(t, err, "expired")
	}

	// state 不匹配：授权时用 A，校验时用 B。
	mismatchLoop, err := auth.NewLoopback()
	if err != nil {
		t.Fatalf("回环: %v", err)
	}
	t.Cleanup(func() { _ = mismatchLoop.Close() })
	mismatchURL := authorizeOnce(t, oauth, mismatchLoop.RedirectURI(), "state-real", pkce.Challenge)
	resp3, err := noRedirectClient().Get(mismatchURL)
	if err != nil {
		t.Fatalf("访问回调: %v", err)
	}
	_ = resp3.Body.Close()
	if _, err := mismatchLoop.Wait(ctx, "state-forged"); err == nil {
		t.Fatal("state 不匹配必须被拒绝")
	} else {
		var appErr *contracts.AppError
		if !errors.As(err, &appErr) || appErr.Code != contracts.CodeUnauthorized {
			t.Fatalf("期望 UNAUTHORIZED，实际 %v", err)
		}
	}

	// PKCE verifier 不匹配：换取时用另一个 verifier。
	otherPKCE, err := auth.NewPKCE()
	if err != nil {
		t.Fatalf("PKCE: %v", err)
	}
	verifyLoop, err := auth.NewLoopback()
	if err != nil {
		t.Fatalf("回环: %v", err)
	}
	t.Cleanup(func() { _ = verifyLoop.Close() })
	verifyURL := authorizeOnce(t, oauth, verifyLoop.RedirectURI(), "state-pkce", pkce.Challenge)
	resp4, err := noRedirectClient().Get(verifyURL)
	if err != nil {
		t.Fatalf("访问回调: %v", err)
	}
	_ = resp4.Body.Close()
	verifyCB, err := verifyLoop.Wait(ctx, "state-pkce")
	if err != nil {
		t.Fatalf("等待回调: %v", err)
	}
	if _, err := client.Exchange(ctx, auth.ExchangeRequest{
		Code:        verifyCB.Code,
		Verifier:    otherPKCE.Verifier,
		RedirectURI: verifyLoop.RedirectURI(),
	}); err == nil {
		t.Fatal("PKCE 不匹配必须失败")
	} else {
		requireAuthCode(t, err, "PKCE")
	}
}

// TestRedirectMustBeLoopback 覆盖"非回环重定向一律拒绝"。
func TestRedirectMustBeLoopback(t *testing.T) {
	for _, bad := range []string{
		"https://evil.example/callback",
		"http://evil.example/callback",
		"anvil://callback",
		"http://127.0.0.1/callback", // 没有端口
		"http://10.1.2.3:8080/callback",
		"http://127.0.0.1:8080/other", // 路径不是 /callback
	} {
		if err := auth.ValidateRedirectURI(bad); err == nil {
			t.Fatalf("必须拒绝非回环 redirect_uri: %s", bad)
		}
	}
	provider := auth.Provider{AuthorizeURL: "https://example.test/authorize", TokenURL: "https://example.test/token", ClientID: "c"}
	if _, err := provider.BuildAuthorizeURL("https://evil.example/callback", "s", "c"); err == nil {
		t.Fatal("拼授权 URL 时也必须拒绝非回环 redirect")
	}
	if _, err := provider.BuildAuthorizeURL("http://127.0.0.1:1234/callback", "", "c"); err == nil {
		t.Fatal("state 为空必须拒绝")
	}
	if _, err := provider.BuildAuthorizeURL("http://127.0.0.1:1234/callback", "s", ""); err == nil {
		t.Fatal("PKCE challenge 为空必须拒绝")
	}
}

// TestRefreshPersistsAndRevokeDeletesCredential 覆盖刷新与撤销。
func TestRefreshPersistsAndRevokeDeletesCredential(t *testing.T) {
	ctx := context.Background()
	oauth, err := fixtures.NewOAuth(fixtures.Options{})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	t.Cleanup(func() { _ = oauth.Close() })
	client, err := auth.NewClient(auth.Provider{
		AuthorizeURL: oauth.AuthorizeURL(),
		TokenURL:     oauth.TokenURL(),
		ClientID:     fixtureClientID,
	})
	if err != nil {
		t.Fatalf("auth.NewClient: %v", err)
	}

	creds := credentials.NewMemStore()
	store, err := auth.NewStore(creds)
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	// 直接造一个"已过期但有 refresh token"的集合：刷新端点会接受它。
	expired := auth.TokenSet{
		AccessToken:  "at-stale",
		RefreshToken: "rt-unknown",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(-time.Hour),
	}
	ref, err := store.Save(ctx, "connector:mail:oauth", expired)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	// 未知的 refresh token：端点拒绝，必须提示重新授权（而不是继续用旧令牌）。
	if _, err := store.AccessToken(ctx, ref, time.Now(), time.Minute, client); err == nil {
		t.Fatal("刷新失败必须报错")
	} else if !strings.Contains(err.Error(), "重新授权") {
		t.Fatalf("刷新失败应提示重新授权: %v", err)
	}

	// 真正可刷新的集合：走一遍完整授权拿到 rt-。
	loop, err := auth.NewLoopback()
	if err != nil {
		t.Fatalf("回环: %v", err)
	}
	t.Cleanup(func() { _ = loop.Close() })
	pkce, _ := auth.NewPKCE()
	callbackURL := authorizeOnce(t, oauth, loop.RedirectURI(), "state-refresh", pkce.Challenge)
	resp, err := noRedirectClient().Get(callbackURL)
	if err != nil {
		t.Fatalf("回调: %v", err)
	}
	_ = resp.Body.Close()
	cb, err := loop.Wait(ctx, "state-refresh")
	if err != nil {
		t.Fatalf("等待回调: %v", err)
	}
	set, err := client.Exchange(ctx, auth.ExchangeRequest{Code: cb.Code, Verifier: pkce.Verifier, RedirectURI: loop.RedirectURI()})
	if err != nil {
		t.Fatalf("换码: %v", err)
	}
	ref2, err := store.Save(ctx, "connector:mail:oauth2", set)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	// 把过期时间压到过去，强制走刷新。
	set.Expiry = time.Now().Add(-time.Minute)
	if _, err := store.Save(ctx, ref2, set); err != nil {
		t.Fatalf("改写过期为过去: %v", err)
	}
	token, err := store.AccessToken(ctx, ref2, time.Now(), time.Minute, client)
	if err != nil {
		t.Fatalf("刷新失败: %v", err)
	}
	if token == set.AccessToken || !strings.HasPrefix(token, "at-") {
		t.Fatalf("刷新后应得到新令牌: %q", token)
	}
	reloaded, err := store.Load(ctx, ref2)
	if err != nil {
		t.Fatalf("读回刷新后的令牌: %v", err)
	}
	if reloaded.AccessToken != token {
		t.Fatal("刷新后的令牌必须写回凭证库")
	}

	// 撤销之后取不到材料。
	if err := store.Revoke(ctx, ref2); err != nil {
		t.Fatalf("撤销: %v", err)
	}
	if _, err := store.Load(ctx, ref2); err == nil {
		t.Fatal("撤销后不应还能读到令牌")
	}
}

// requireAuthCode 断言错误是"令牌端点拒绝"，并带上可区分的原因。
func requireAuthCode(t *testing.T, err error, want string) {
	t.Helper()
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) || appErr == nil {
		t.Fatalf("期望 AppError，实际 %v", err)
	}
	if appErr.Code != contracts.CodeUnauthorized {
		t.Fatalf("期望 UNAUTHORIZED，实际 %s（%s）", appErr.Code, appErr.Message)
	}
	if !strings.Contains(appErr.Message, want) {
		t.Fatalf("错误信息应包含 %q，实际 %q", want, appErr.Message)
	}
}
