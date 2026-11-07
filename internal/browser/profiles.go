package browser

// B06：workspace 级浏览器 profile 与持久登录（BR15）。
//
// 这一层的产品承诺只有四条，其余都是实现细节：
//  1. **一个 workspace 一份 profile**。cookie / localStorage / 缓存都在这个 profile
//     目录里，所以 A 的登录态在 B 的 tab 里根本不存在——不是"查到不同行"，而是
//     "根本没有那份字节"。
//  2. **重启后还在**。profile id 与路径都由 workspace id 决定（不是每次启动随机），
//     所以同一个 workspace 下次启动拿回的是同一个目录。
//  3. **ClearProfile 真的清掉**。目录递归删除并**再 Stat 一次**确认不存在，同时删掉
//     应用侧登录态行；只删数据库行而留着 cookie 文件是最典型的假清除。
//  4. **绝不碰用户个人浏览器配置**。profile 根必须是本应用受管目录；构造时直接拒绝
//     Chrome/Edge/Firefox/Chromium 的 user data 目录（含它们的子目录）。
//
// 关于 cookie 的两处存储（必须写清楚，否则后来者会以为是重复记账）：
//   - 引擎（WebView2/WKWebView/WebKitGTK）把自己的 cookie 库写在 profile 目录里，
//     格式私有不复制；
//   - `browser_session_cookies` 是**应用侧**的登录态记录，供非引擎 HTTP（下载）与
//     "这个 workspace 现在有没有登录态"的查询使用。两处由同一个 profile id 绑定，
//     ClearProfile 同时清掉，不会出现"库说已清除、目录里还留着会话"的分叉。
//
// 值的安全边界：cookie 值只落在数据库列与内存里，不进日志、不进证据（证据只写
// name/origin 与值的 sha256）；profile 目录与应用受管下载根都用 0700。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/storage"
)

// tsLayout 是落库时间列的统一格式。
//
// 固定宽度（纳秒补零）是刻意的：TEXT 比较要与时间顺序一致，RFC3339Nano 会裁掉
// 尾随 0，于是 "…05.1Z" 与 "…05.09Z" 的字面顺序与时间顺序相反。
const tsLayout = "2006-01-02T15:04:05.000000000Z07:00"

// BrowserProfile 是一个 workspace 的浏览器登录态归属。
//
// Path 是**引擎的 user data folder**：把同一个目录交给引擎，登录态才能跨重启保留；
// 交给不同目录，就是不同的登录态。Ephemeral 表示这份 profile 不保证跨机器重启
// 存活（根目录落在系统临时目录下时由构造方判定，测试沙箱会命中）。
type BrowserProfile struct {
	ID          contracts.ProfileID   `json:"id"`
	WorkspaceID contracts.WorkspaceID `json:"workspaceId"`
	Path        string                `json:"path"`
	Ephemeral   bool                  `json:"ephemeral"`
}

// StoredCookie 是一条应用侧登录态记录。
//
// Value 刻意不打 JSON 标签进日志：它等同于会话凭据。
type StoredCookie struct {
	Origin    string `json:"origin"`
	Name      string `json:"name"`
	Value     string `json:"-"`
	Path      string `json:"path,omitempty"`
	Secure    bool   `json:"secure,omitempty"`
	HTTPOnly  bool   `json:"httpOnly,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}

// ProfileOptions 是 ProfileStore 的装配参数。
type ProfileOptions struct {
	// Root 是本应用受管的 profile 根（生产为 <appData>/browser/profiles）。
	// 不能是用户个人浏览器配置目录，构造时会被拒绝。
	Root string
	// DB 是 profile 行与登录态行的存储，必填。
	DB *storage.DB
	// Now 注入时钟（测试确定性），<=nil 用 time.Now。
	Now func() time.Time
}

// ProfileStore 管理 workspace → profile 的映射与登录态记录。并发安全。
type ProfileStore struct {
	root      string
	db        *storage.DB
	ephemeral bool
	now       func() time.Time

	mu sync.Mutex
}

// NewProfileStore 校验根目录并返回 store。
//
// 拒绝顺序是刻意的：先证明"这不是用户个人浏览器配置"，再创建目录——否则一次
// 误配置就会在别人的 Chrome profile 目录里建出 profiles/ 来。
func NewProfileStore(opts ProfileOptions) (*ProfileStore, error) {
	if opts.DB == nil {
		return nil, appErr(contracts.CodeConfigMissing,
			"浏览器 profile 存储缺少数据库句柄：登录态与 profile 归属必须可查询")
	}
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		return nil, appErr(contracts.CodeConfigMissing, "浏览器 profile 根为空")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, appErr(contracts.CodeInternal, "解析 profile 根失败: "+err.Error())
	}
	abs = filepath.Clean(abs)
	if err := rejectPersonalBrowserRoot(abs); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, appErr(contracts.CodeInternal, "创建 profile 根失败: "+abs+" : "+err.Error())
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &ProfileStore{
		root:      abs,
		db:        opts.DB,
		ephemeral: pathInside(os.TempDir(), abs),
		now:       now,
	}, nil
}

// Root 返回受管 profile 根（证据/诊断用）。
func (s *ProfileStore) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// ProfileForWorkspace 返回（必要时创建）该 workspace 的 profile。
//
// id 与路径都由 workspace id 决定，因此"重启后还是同一份登录态"不是靠额外的
// 记忆，而是靠确定性：同名 workspace 永远映射到同一个目录。
func (s *ProfileStore) ProfileForWorkspace(ctx context.Context, ws contracts.WorkspaceID) (BrowserProfile, error) {
	if s == nil || s.db == nil {
		return BrowserProfile{}, appErr(contracts.CodeInternal, "浏览器 profile 存储未初始化")
	}
	if strings.TrimSpace(string(ws)) == "" {
		return BrowserProfile{}, appErr(contracts.CodeInvalidInput, "缺少 workspace id：不能在没有归属的情况下建 profile")
	}
	if err := ctx.Err(); err != nil {
		return BrowserProfile{}, err
	}
	id := profileIDForWorkspace(ws)
	path := s.profilePath(id)
	if err := os.MkdirAll(path, 0o700); err != nil {
		return BrowserProfile{}, appErr(contracts.CodeInternal,
			"创建 profile 目录失败: "+path+" : "+err.Error())
	}
	now := s.ts()
	if _, err := s.db.SQL().ExecContext(ctx, `
		INSERT INTO browser_profiles(id, workspace_id, path, ephemeral, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id) DO UPDATE SET
		    path       = excluded.path,
		    ephemeral  = excluded.ephemeral,
		    updated_at = excluded.updated_at`,
		string(id), string(ws), path, boolInt(s.ephemeral), now, now); err != nil {
		return BrowserProfile{}, appErr(contracts.CodeInternal, "写入 profile 行失败: "+err.Error())
	}
	return s.loadProfile(ctx, ws)
}

// Profiles 返回全部已知 profile（按 workspace id 排序）。
func (s *ProfileStore) Profiles(ctx context.Context) ([]BrowserProfile, error) {
	if s == nil || s.db == nil {
		return nil, appErr(contracts.CodeInternal, "浏览器 profile 存储未初始化")
	}
	rows, err := s.db.SQL().QueryContext(ctx,
		`SELECT id, workspace_id, path, ephemeral FROM browser_profiles ORDER BY workspace_id`)
	if err != nil {
		return nil, appErr(contracts.CodeInternal, "读取 profile 列表失败: "+err.Error())
	}
	defer rows.Close()
	var out []BrowserProfile
	for rows.Next() {
		var item BrowserProfile
		var ephemeral int
		if err := rows.Scan(&item.ID, &item.WorkspaceID, &item.Path, &ephemeral); err != nil {
			return nil, appErr(contracts.CodeInternal, "扫描 profile 行失败: "+err.Error())
		}
		item.Ephemeral = ephemeral != 0
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, appErr(contracts.CodeInternal, "遍历 profile 行失败: "+err.Error())
	}
	return out, nil
}

// ClearProfile 真的清掉一个 workspace 的登录态。
//
// "真的清掉"= 目录递归删除后**再 Stat 一次**确认不存在 + 应用侧登录态行删除。
// 任何一步失败都返回结构化错误（CLEANUP_FAILED），不返回 nil 让调用方以为清干净了：
// 用户点"退出登录"之后再打开页面还是登录态，是这类 bug 最坏的表现形式。
func (s *ProfileStore) ClearProfile(ctx context.Context, ws contracts.WorkspaceID) error {
	if s == nil || s.db == nil {
		return appErr(contracts.CodeInternal, "浏览器 profile 存储未初始化")
	}
	if strings.TrimSpace(string(ws)) == "" {
		return appErr(contracts.CodeInvalidInput, "缺少 workspace id：拒绝在归属不明时删除目录")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id := profileIDForWorkspace(ws)
	path := s.profilePath(id)
	// 只允许删受管根下的 profile 目录：路径由 store 自己拼出，这里再证一次，
	// 防止将来有人把 root 换成别处之后这里变成任意递归删除。
	if !pathInside(s.root, path) || filepath.Clean(path) == filepath.Clean(s.root) {
		return appErr(contracts.CodeInternal, "拒绝删除受管根之外的路径: "+path)
	}
	if err := os.RemoveAll(path); err != nil {
		return appErr(contracts.CodeCleanupFailed, "清除 profile 目录失败: "+path+" : "+err.Error())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return appErr(contracts.CodeCleanupFailed, "profile 目录清除后仍然存在: "+path)
	}

	now := s.ts()
	if _, err := s.db.SQL().ExecContext(ctx,
		`DELETE FROM browser_session_cookies WHERE profile_id = ? OR workspace_id = ?`,
		string(id), string(ws)); err != nil {
		return appErr(contracts.CodeCleanupFailed, "清除登录态记录失败: "+err.Error())
	}
	if _, err := s.db.SQL().ExecContext(ctx,
		`UPDATE browser_profiles SET cleared_at = ?, updated_at = ? WHERE workspace_id = ?`,
		now, now, string(ws)); err != nil {
		return appErr(contracts.CodeCleanupFailed, "记录清除时间失败: "+err.Error())
	}
	return nil
}

// SaveCookies 写入/更新若干条登录态记录（同一 (origin,name,path) 覆盖）。
func (s *ProfileStore) SaveCookies(ctx context.Context, ws contracts.WorkspaceID, cookies []StoredCookie) error {
	if s == nil || s.db == nil {
		return appErr(contracts.CodeInternal, "浏览器 profile 存储未初始化")
	}
	profile, err := s.ProfileForWorkspace(ctx, ws)
	if err != nil {
		return err
	}
	now := s.ts()
	for _, c := range cookies {
		origin, name, cookiePath, err := normalizeCookie(c)
		if err != nil {
			return err
		}
		if _, err := s.db.SQL().ExecContext(ctx, `
			INSERT INTO browser_session_cookies(profile_id, workspace_id, origin, name, path,
			                                    value, secure, http_only, expires_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(profile_id, origin, name, path) DO UPDATE SET
			    value      = excluded.value,
			    secure     = excluded.secure,
			    http_only  = excluded.http_only,
			    expires_at = excluded.expires_at,
			    updated_at = excluded.updated_at`,
			string(profile.ID), string(ws), origin, name, cookiePath,
			c.Value, boolInt(c.Secure), boolInt(c.HTTPOnly), strings.TrimSpace(c.ExpiresAt), now); err != nil {
			return appErr(contracts.CodeInternal, "写入登录态记录失败: "+err.Error())
		}
	}
	return nil
}

// Cookies 返回该 workspace 的登录态记录（按 origin/name/path 稳定排序）。
func (s *ProfileStore) Cookies(ctx context.Context, ws contracts.WorkspaceID) ([]StoredCookie, error) {
	if s == nil || s.db == nil {
		return nil, appErr(contracts.CodeInternal, "浏览器 profile 存储未初始化")
	}
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT origin, name, path, value, secure, http_only, expires_at
		  FROM browser_session_cookies WHERE workspace_id = ?
		 ORDER BY origin, name, path`, string(ws))
	if err != nil {
		return nil, appErr(contracts.CodeInternal, "读取登录态记录失败: "+err.Error())
	}
	defer rows.Close()
	var out []StoredCookie
	for rows.Next() {
		var c StoredCookie
		var secure, httpOnly int
		if err := rows.Scan(&c.Origin, &c.Name, &c.Path, &c.Value, &secure, &httpOnly, &c.ExpiresAt); err != nil {
			return nil, appErr(contracts.CodeInternal, "扫描登录态记录失败: "+err.Error())
		}
		c.Secure = secure != 0
		c.HTTPOnly = httpOnly != 0
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, appErr(contracts.CodeInternal, "遍历登录态记录失败: "+err.Error())
	}
	return out, nil
}

// CookieHeader 把该 workspace 的登录态拼成 HTTP Cookie 头的值。
//
// 只给非引擎 HTTP（下载）用：引擎自己的请求由引擎带 cookie，不需要我们代劳。
// 已过期（ExpiresAt 早于现在）的记录会被跳过——用一条过期会话去请求，
// 得到的是 401，而调用方会把它误读成"下载坏了"。
func (s *ProfileStore) CookieHeader(ctx context.Context, ws contracts.WorkspaceID) (string, error) {
	cookies, err := s.Cookies(ctx, ws)
	if err != nil {
		return "", err
	}
	now := s.now().UTC()
	parts := make([]string, 0, len(cookies))
	for _, c := range cookies {
		if c.ExpiresAt != "" {
			if exp, perr := time.Parse(time.RFC3339, c.ExpiresAt); perr == nil && !now.Before(exp) {
				continue
			}
		}
		parts = append(parts, c.Name+"="+c.Value)
	}
	sort.Strings(parts)
	return strings.Join(parts, "; "), nil
}

// profilePath 返回某个 profile id 的目录。id 由本站点生成，仍然再校验一次。
func (s *ProfileStore) profilePath(id contracts.ProfileID) string {
	clean := sanitizeProfileSegment(string(id))
	return filepath.Join(s.root, "profiles", clean)
}

func (s *ProfileStore) loadProfile(ctx context.Context, ws contracts.WorkspaceID) (BrowserProfile, error) {
	var item BrowserProfile
	var ephemeral int
	err := s.db.SQL().QueryRowContext(ctx,
		`SELECT id, workspace_id, path, ephemeral FROM browser_profiles WHERE workspace_id = ?`,
		string(ws)).Scan(&item.ID, &item.WorkspaceID, &item.Path, &ephemeral)
	if err != nil {
		return BrowserProfile{}, appErr(contracts.CodeInternal, "读回 profile 行失败: "+err.Error())
	}
	item.Ephemeral = ephemeral != 0
	return item, nil
}

func (s *ProfileStore) ts() string { return s.now().UTC().Format(tsLayout) }

// profileIDForWorkspace 由 workspace id 派生稳定的 profile id。
//
// 用哈希而不是直接拼 workspace id：workspace id 来自用户输入，可能含路径分隔符、
// 控制字符或 ".."，拼进文件系统就是一条越界写入通道。哈希后的 id 只含
// [0-9a-f]，且同一个 workspace 永远得到同一个 id（重启保留的前提）。
func profileIDForWorkspace(ws contracts.WorkspaceID) contracts.ProfileID {
	sum := sha256.Sum256([]byte("browser-profile\x00" + string(ws)))
	return contracts.ProfileID("profile-" + hex.EncodeToString(sum[:])[:16])
}

// sanitizeProfileSegment 只允许 [a-z0-9-] 留在目录名里。
func sanitizeProfileSegment(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "profile"
	}
	return b.String()
}

// normalizeCookie 校验并规范化一条登录态记录。
func normalizeCookie(c StoredCookie) (origin, name, path string, err error) {
	origin = strings.TrimSpace(c.Origin)
	parsed, perr := url.Parse(origin)
	if perr != nil || parsed == nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", "", "", appErr(contracts.CodeInvalidInput,
			"登录态记录的 origin 必须是 scheme://host: "+origin)
	}
	origin = parsed.Scheme + "://" + parsed.Host
	name = strings.TrimSpace(c.Name)
	if name == "" {
		return "", "", "", appErr(contracts.CodeInvalidInput, "登录态记录缺少 cookie 名")
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == ';' || r == '=' || r == ' ' {
			return "", "", "", appErr(contracts.CodeInvalidInput,
				"cookie 名含非法字符: "+name)
		}
	}
	path = strings.TrimSpace(c.Path)
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		return "", "", "", appErr(contracts.CodeInvalidInput, "cookie path 必须以 / 开头: "+path)
	}
	return origin, name, path, nil
}

// rejectPersonalBrowserRoot 拒绝把用户个人浏览器配置目录当作 profile 根。
//
// 为什么必须显式拒绝而不是"反正我们只写自己的子目录"：ClearProfile 会递归删除
// 受管根下的 profile 目录；一旦根被配成 "…/Chrome/User Data"，用户的浏览数据就在
// 一次"退出登录"里消失。这里在构造时就把它挡住，属于产品边界而不是使用说明。
func rejectPersonalBrowserRoot(abs string) error {
	for _, candidate := range personalBrowserRoots() {
		if pathInside(candidate, abs) || pathInside(abs, candidate) {
			return appErr(contracts.CodeUnauthorized,
				"浏览器 profile 根不能是用户个人浏览器配置目录（"+candidate+"）: "+abs)
		}
	}
	return nil
}

// personalBrowserRoots 列出各平台常见浏览器 user data 目录（从环境变量推出）。
//
// 不写死绝对路径：同一台机器上换用户、换安装位置都会变；用环境变量推导，
// 测试也能通过 t.Setenv 构造一个"看起来像个人浏览器 profile"的目录来验证拒绝。
func personalBrowserRoots() []string {
	var out []string
	add := func(base string, parts ...string) {
		base = strings.TrimSpace(base)
		if base == "" {
			return
		}
		out = append(out, filepath.Clean(filepath.Join(append([]string{base}, parts...)...)))
	}
	home := firstNonEmpty(os.Getenv("HOME"), os.Getenv("USERPROFILE"))
	local := os.Getenv("LOCALAPPDATA")
	roaming := os.Getenv("APPDATA")
	add(local, "Google", "Chrome", "User Data")
	add(local, "Microsoft", "Edge", "User Data")
	add(local, "BraveSoftware", "Brave-Browser", "User Data")
	add(local, "Chromium", "User Data")
	add(local, "Vivaldi", "User Data")
	add(roaming, "Mozilla", "Firefox", "Profiles")
	add(roaming, "Opera Software", "Opera Stable")
	add(home, "AppData", "Local", "Google", "Chrome", "User Data")
	add(home, "AppData", "Local", "Microsoft", "Edge", "User Data")
	add(home, "AppData", "Roaming", "Mozilla", "Firefox", "Profiles")
	add(home, "Library", "Application Support", "Google", "Chrome")
	add(home, "Library", "Application Support", "Firefox", "Profiles")
	add(home, "Library", "Application Support", "Microsoft Edge")
	add(home, ".mozilla", "firefox")
	add(home, ".config", "google-chrome")
	add(home, ".config", "chromium")
	add(home, ".config", "microsoft-edge")
	add(home, ".config", "BraveSoftware", "Brave-Browser")
	return out
}

// pathInside 报告 candidate 是否等于 root 或位于 root 之下（大小写不敏感的平台上
// 也成立：两侧都先 Clean，Windows 的 volume 名保留原样由 filepath 处理）。
func pathInside(root, candidate string) bool {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	if root == "" || candidate == "" {
		return false
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
