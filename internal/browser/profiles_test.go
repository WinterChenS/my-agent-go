package browser_test

// B06：workspace 级 profile 与持久登录（BR15）。
//
// 判定只落在两类可观测量上，不落在错误文案：
//   - **磁盘**：profile 目录的真实遍历（路径、每个文件的大小与 sha256）。"重启后
//     还在"与"清除后没了"都必须能在目录里看到差异；
//   - **数据库**：cookie 行由**第二个 storage.DB 句柄**从磁盘读回（不是问被测
//     ProfileStore"你还记得吗"）。
//
// 另外两条边界也在这里被钉住：
//   - 用户个人浏览器 profile（Chrome/Edge/Firefox 的 user data 目录）**不可**被
//     当作 profile 根；本测试用 sentinel 文件证明它前后一个字节都没变；
//   - 同一 workspace 的两个 session 共用同一份登录态（cookie 是 workspace 级的），
//     但它们不能互相控制 tab（跨 session 拒绝由 B05 的 TestCrossSessionTabDenied
//     负责，这里只核对"共用 cookie ≠ 共用 tab"这条组合事实）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"my-agent-go/internal/browser"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/storage"
)

// ---------- 通用：磁盘事实 ----------

// treeEntry 是一个文件在某一时刻的磁盘事实。
type treeEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// walkTree 遍历目录并逐文件算 sha256（不跟随链接）。
//
// 目录不存在时返回空切片而不是错误："什么都没写"本身就是一条要断言的事实。
func walkTree(t *testing.T, root string) []treeEntry {
	t.Helper()
	out := []treeEntry{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return statErr
		}
		sum := ""
		if info.Mode().IsRegular() && info.Size() > 0 {
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			h := sha256.Sum256(raw)
			sum = hex.EncodeToString(h[:])
		}
		out = append(out, treeEntry{Path: filepath.ToSlash(rel), Size: info.Size(), SHA256: sum})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("遍历 %s: %v", root, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// openSecondHandle 打开同一个数据库文件的**第二个句柄**。
//
// 证据里的"cookie 行/产物行"必须由它读回：问被测服务要答案等于让它自证。
func openSecondHandle(t *testing.T, path string) *storage.DB {
	t.Helper()
	db, err := storage.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("打开第二句柄 %s: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ---------- profile 环境 ----------

type profileEnv struct {
	root      string // profile 根（受管 appData）
	dbPath    string
	wantLocal string // 伪造的 %LOCALAPPDATA%
	home      string // 伪造的家目录
	personal  string // 伪造的个人浏览器 profile（Chrome User Data）
	sentinel  string // personal 目录里的 sentinel 文件
	store     *browser.ProfileStore
	wsA       contracts.WorkspaceID
	wsB       contracts.WorkspaceID
}

// newProfileEnv 在临时目录里搭出"应用受管根 + 伪造的用户家目录 + 伪造的个人浏览器 profile"。
//
// 伪造家目录是刻意的：BR15 要求"不访问系统个人浏览器配置"，那就必须有一个**看起来
// 像**个人浏览器 profile 的目录，并用它的 sentinel 前后哈希证明没人碰过它。
func newProfileEnv(t *testing.T) *profileEnv {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "appdata", "browser", "profiles")
	home := filepath.Join(base, "home")
	local := filepath.Join(home, "AppData", "Local")
	personal := filepath.Join(local, "Google", "Chrome", "User Data")
	sentinel := filepath.Join(personal, "Default", "Cookies")
	if err := os.MkdirAll(filepath.Dir(sentinel), 0o700); err != nil {
		t.Fatalf("建伪造个人 profile: %v", err)
	}
	if err := os.WriteFile(sentinel, []byte("personal-browser-cookie-sentinel"), 0o600); err != nil {
		t.Fatalf("写 sentinel: %v", err)
	}
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))

	dbPath := filepath.Join(base, "app.db")
	ctx := context.Background()
	db, err := storage.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("打开临时库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移临时库: %v", err)
	}

	store, err := browser.NewProfileStore(browser.ProfileOptions{Root: root, DB: db})
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	return &profileEnv{
		root: root, dbPath: dbPath, wantLocal: local, home: home,
		personal: personal, sentinel: sentinel, store: store,
		wsA: contracts.WorkspaceID("ws-alpha"), wsB: contracts.WorkspaceID("ws-beta"),
	}
}

// ---- BR15：cookie 按 workspace 隔离、重启保留、清除生效 ----

func TestWorkspaceCookiesIsolatedAndPersisted(t *testing.T) {
	if testing.Short() {
		t.Fatal("BR15 需要真实磁盘与真实 SQLite：不允许用 -short 跳过（不写 t.Skip）")
	}
	env := newProfileEnv(t)
	ctx := context.Background()

	originA := "https://alpha.example.test"
	cookieA := browser.StoredCookie{
		Origin: originA, Name: "fixture_session", Value: "ok:alpha-secret",
		Path: "/", HTTPOnly: true, Secure: true,
	}
	cookieB := browser.StoredCookie{
		Origin: "https://beta.example.test", Name: "fixture_session", Value: "ok:beta-secret",
		Path: "/",
	}

	sentinelBefore := walkTree(t, env.personal)

	profA, err := env.store.ProfileForWorkspace(ctx, env.wsA)
	if err != nil {
		t.Fatalf("ProfileForWorkspace(A): %v", err)
	}
	profB, err := env.store.ProfileForWorkspace(ctx, env.wsB)
	if err != nil {
		t.Fatalf("ProfileForWorkspace(B): %v", err)
	}
	if profA.ID == "" || profB.ID == "" {
		t.Fatalf("profile id 为空: A=%q B=%q", profA.ID, profB.ID)
	}
	if profA.ID == profB.ID || profA.Path == profB.Path {
		t.Fatalf("两个 workspace 拿到同一个 profile: A=%+v B=%+v", profA, profB)
	}
	for _, p := range []browser.BrowserProfile{profA, profB} {
		if !pathWithin(env.root, p.Path) {
			t.Fatalf("profile 路径不在受管根 %s 之内: %s", env.root, p.Path)
		}
		if pathWithin(env.personal, p.Path) || pathWithin(p.Path, env.personal) {
			t.Fatalf("profile 路径与个人浏览器 profile 重叠: %s vs %s", p.Path, env.personal)
		}
		if p.WorkspaceID != env.wsA && p.WorkspaceID != env.wsB {
			t.Fatalf("profile 归属错乱: %+v", p)
		}
	}
	if info, statErr := os.Stat(profA.Path); statErr != nil || !info.IsDir() {
		t.Fatalf("profile 目录没有真的建出来: %s (%v)", profA.Path, statErr)
	}

	// 引擎会在 profile 目录里写自己的存储（真机是 WebView2 的 Network/Cookies 等）。
	// 这里用一段固定字节代表"引擎数据"，它必须跟着 profile 一起留存/一起消失。
	engineFile := filepath.Join(profA.Path, "Network", "Cookies")
	if err := os.MkdirAll(filepath.Dir(engineFile), 0o700); err != nil {
		t.Fatalf("建引擎子目录: %v", err)
	}
	if err := os.WriteFile(engineFile, []byte("engine-cookie-db-alpha"), 0o600); err != nil {
		t.Fatalf("写引擎数据: %v", err)
	}

	if err := env.store.SaveCookies(ctx, env.wsA, []browser.StoredCookie{cookieA}); err != nil {
		t.Fatalf("SaveCookies(A): %v", err)
	}
	if err := env.store.SaveCookies(ctx, env.wsB, []browser.StoredCookie{cookieB}); err != nil {
		t.Fatalf("SaveCookies(B): %v", err)
	}

	cookiesA, err := env.store.Cookies(ctx, env.wsA)
	if err != nil {
		t.Fatalf("Cookies(A): %v", err)
	}
	if len(cookiesA) != 1 || cookiesA[0].Value != cookieA.Value || cookiesA[0].Origin != originA {
		t.Fatalf("A 的 cookie 不是刚写的那条: %+v", cookiesA)
	}
	cookiesB, err := env.store.Cookies(ctx, env.wsB)
	if err != nil {
		t.Fatalf("Cookies(B): %v", err)
	}
	if len(cookiesB) != 1 || cookiesB[0].Value != cookieB.Value {
		t.Fatalf("B 的 cookie 不对: %+v", cookiesB)
	}
	if cookiesB[0].Value == cookiesA[0].Value {
		t.Fatalf("A 与 B 拿到了同一条 cookie 值：隔离不成立")
	}

	// 第二句柄直接读库：A 的行只属于 A 的 profile。
	second := openSecondHandle(t, env.dbPath)
	rowsA := readCookieRows(t, second, string(profA.ID))
	rowsB := readCookieRows(t, second, string(profB.ID))
	if len(rowsA) != 1 || rowsA[0]["origin"] != originA {
		t.Fatalf("第二句柄读到的 A 行不对: %+v", rowsA)
	}
	if len(rowsB) != 1 || rowsB[0]["origin"] != cookieB.Origin {
		t.Fatalf("第二句柄读到的 B 行不对: %+v", rowsB)
	}

	// ---- 重启：同一根 + 同一库文件的**新 store / 新句柄** ----
	restartedStore, err := browser.NewProfileStore(browser.ProfileOptions{
		Root: env.root, DB: openSecondHandle(t, env.dbPath),
	})
	if err != nil {
		t.Fatalf("重启后 NewProfileStore: %v", err)
	}
	profARestart, err := restartedStore.ProfileForWorkspace(ctx, env.wsA)
	if err != nil {
		t.Fatalf("重启后 ProfileForWorkspace(A): %v", err)
	}
	if profARestart.ID != profA.ID || profARestart.Path != profA.Path {
		t.Fatalf("重启后 profile 身份变了: %+v（原 %+v）", profARestart, profA)
	}
	restartCookies, err := restartedStore.Cookies(ctx, env.wsA)
	if err != nil {
		t.Fatalf("重启后 Cookies(A): %v", err)
	}
	if len(restartCookies) != 1 || restartCookies[0].Value != cookieA.Value {
		t.Fatalf("重启后 A 的登录态丢了: %+v", restartCookies)
	}
	if raw, readErr := os.ReadFile(engineFile); readErr != nil || string(raw) != "engine-cookie-db-alpha" {
		t.Fatalf("重启后 profile 目录里的引擎数据丢了: %v (%q)", readErr, string(raw))
	}
	// 同一 workspace 的第二个 session 看到的是同一份登录态（cookie 是 workspace 级）。
	headerA, err := restartedStore.CookieHeader(ctx, env.wsA)
	if err != nil {
		t.Fatalf("CookieHeader(A): %v", err)
	}
	if !strings.Contains(headerA, "fixture_session="+cookieA.Value) {
		t.Fatalf("下载/HTTP 用的 cookie 头里没有 A 的会话: %q", headerA)
	}
	treeAfterRestart := walkTree(t, profA.Path)

	// ---- 清除 A：目录与 cookie 行都必须真的没了，B 不受影响 ----
	if err := restartedStore.ClearProfile(ctx, env.wsA); err != nil {
		t.Fatalf("ClearProfile(A): %v", err)
	}
	if _, statErr := os.Stat(profA.Path); !os.IsNotExist(statErr) {
		t.Fatalf("ClearProfile 之后 A 的 profile 目录还在: %v", statErr)
	}
	afterClear, err := restartedStore.Cookies(ctx, env.wsA)
	if err != nil {
		t.Fatalf("清除后 Cookies(A): %v", err)
	}
	if len(afterClear) != 0 {
		t.Fatalf("ClearProfile 之后 A 还有 cookie: %+v", afterClear)
	}
	secondAfter := openSecondHandle(t, env.dbPath)
	if rows := readCookieRows(t, secondAfter, string(profA.ID)); len(rows) != 0 {
		t.Fatalf("ClearProfile 之后库里仍有 A 的 cookie 行: %+v", rows)
	}
	if raw, readErr := os.ReadFile(engineFile); readErr == nil {
		t.Fatalf("ClearProfile 之后引擎数据文件仍在: %q", string(raw))
	}
	// B 完全不受影响。
	profBAfter, err := restartedStore.ProfileForWorkspace(ctx, env.wsB)
	if err != nil {
		t.Fatalf("清除 A 后 ProfileForWorkspace(B): %v", err)
	}
	if profBAfter.Path != profB.Path {
		t.Fatalf("清除 A 改变了 B 的 profile 路径: %s → %s", profB.Path, profBAfter.Path)
	}
	cookiesBAfter, err := restartedStore.Cookies(ctx, env.wsB)
	if err != nil {
		t.Fatalf("清除 A 后 Cookies(B): %v", err)
	}
	if len(cookiesBAfter) != 1 || cookiesBAfter[0].Value != cookieB.Value {
		t.Fatalf("清除 A 把 B 的 cookie 也清了: %+v", cookiesBAfter)
	}
	// 清除后再次取 A：目录重建且是空的（不会把旧字节"找回来"）。
	profAFresh, err := restartedStore.ProfileForWorkspace(ctx, env.wsA)
	if err != nil {
		t.Fatalf("清除后重取 ProfileForWorkspace(A): %v", err)
	}
	if tree := walkTree(t, profAFresh.Path); len(tree) != 0 {
		t.Fatalf("清除后重建的 profile 目录不是空的: %+v", tree)
	}

	// 个人浏览器 profile 一个字节都没动。
	sentinelAfter := walkTree(t, env.personal)
	if !sameTree(sentinelBefore, sentinelAfter) {
		t.Fatalf("个人浏览器 profile 被改动了: before=%+v after=%+v", sentinelBefore, sentinelAfter)
	}

	observeBR15(t, env, map[string]any{
		"profileA":            profA,
		"profileB":            profB,
		"restartProfileA":     profARestart,
		"cookieCountA":        len(cookiesA),
		"cookieCountB":        len(cookiesB),
		"cookieCountAfterSet": len(restartCookies),
		"cookieCountAfterClr": len(afterClear),
		"treeAfterRestart":    treeAfterRestart,
		"personalSentinel":    sentinelAfter,
		"profileDirGone":      true,
	})
}

// TestProfileStoreRefusesPersonalBrowserRoots 是"不访问系统个人浏览器配置"的可执行版本。
func TestProfileStoreRefusesPersonalBrowserRoots(t *testing.T) {
	env := newProfileEnv(t)
	ctx := context.Background()
	db := openSecondHandle(t, env.dbPath)

	for _, root := range []string{
		env.personal,
		filepath.Join(env.personal, "Default"),
		filepath.Join(env.wantLocal, "Microsoft", "Edge", "User Data"),
		filepath.Join(env.home, ".mozilla", "firefox"),
		filepath.Join(env.home, ".config", "google-chrome"),
		filepath.Join(env.home, "Library", "Application Support", "Google", "Chrome"),
	} {
		if _, err := browser.NewProfileStore(browser.ProfileOptions{Root: root, DB: db}); err == nil {
			t.Fatalf("把个人浏览器目录当 profile 根被接受了: %s", root)
		} else if code := errCode(err); code != contracts.CodeUnauthorized {
			t.Fatalf("%s 的拒绝错误码 %q，期望 UNAUTHORIZED（消息: %s）", root, code, err.Error())
		}
	}

	// 受管根可以正常创建；其中不存在的子目录也会被建出来（但**不**碰个人目录）。
	managed, err := browser.NewProfileStore(browser.ProfileOptions{
		Root: filepath.Join(env.wantLocal, "Anvil", "browser", "profiles"), DB: db,
	})
	if err != nil {
		t.Fatalf("受管根被误拒: %v", err)
	}
	if _, err := managed.ProfileForWorkspace(ctx, env.wsA); err != nil {
		t.Fatalf("受管根下建 profile 失败: %v", err)
	}
	if _, statErr := os.Stat(env.sentinel); statErr != nil {
		t.Fatalf("个人浏览器 sentinel 文件不见了: %v", statErr)
	}
}

// ---- 小工具 ----

func pathWithin(root, candidate string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func sameTree(a, b []treeEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// readCookieRows 用第二个句柄读某 profile 的 cookie 行（值只给 sha256）。
func readCookieRows(t *testing.T, db *storage.DB, profileID string) []map[string]string {
	t.Helper()
	rows, err := db.SQL().Query(
		`SELECT profile_id, workspace_id, origin, name, path, value, expires_at
		   FROM browser_session_cookies WHERE profile_id = ? ORDER BY origin, name`, profileID)
	if err != nil {
		t.Fatalf("读 cookie 行: %v", err)
	}
	defer rows.Close()
	var out []map[string]string
	for rows.Next() {
		var profile, ws, origin, name, path, value, expires string
		if err := rows.Scan(&profile, &ws, &origin, &name, &path, &value, &expires); err != nil {
			t.Fatalf("扫描 cookie 行: %v", err)
		}
		out = append(out, map[string]string{
			"profileId": profile, "workspaceId": ws, "origin": origin, "name": name,
			"path": path, "valueSha256": sha256Hex([]byte(value)), "expiresAt": expires,
		})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历 cookie 行: %v", err)
	}
	return out
}

// ---------- BR15 证据 ----------

// observeBR15 落盘 BR15 的 artifact：profile 目录真实遍历 + 第二句柄读出的 cookie 行。
func observeBR15(t *testing.T, env *profileEnv, facts map[string]any) {
	t.Helper()
	if browserObsDir() == "" {
		return
	}
	profiles := []map[string]any{}
	for _, ws := range []contracts.WorkspaceID{env.wsA, env.wsB} {
		p, err := env.store.ProfileForWorkspace(context.Background(), ws)
		if err != nil {
			// A 已被清除并且这里只是留档：如实记录事实，不掩盖。
			continue
		}
		profiles = append(profiles, map[string]any{
			"id": string(p.ID), "workspaceId": string(p.WorkspaceID),
			"path": p.Path, "ephemeral": p.Ephemeral,
			"files": walkTree(t, p.Path),
		})
	}
	raw, err := json.MarshalIndent(map[string]any{
		"profileRoot": env.root,
		"profiles":    profiles,
		"rootTree":    walkTree(t, env.root),
	}, "", "  ")
	if err != nil {
		t.Fatalf("序列化 profiles.json: %v", err)
	}
	browserObsWrite(t, "profiles.json", append(raw, '\n'))

	second := openSecondHandle(t, env.dbPath)
	cookieRows, err := second.SQL().Query(
		`SELECT profile_id, workspace_id, origin, name, path, value, expires_at
		   FROM browser_session_cookies ORDER BY workspace_id, origin, name`)
	if err != nil {
		t.Fatalf("读 cookie 行: %v", err)
	}
	defer cookieRows.Close()
	rows := []map[string]string{}
	for cookieRows.Next() {
		var profile, ws, origin, name, path, value, expires string
		if err := cookieRows.Scan(&profile, &ws, &origin, &name, &path, &value, &expires); err != nil {
			t.Fatalf("扫描 cookie 行: %v", err)
		}
		rows = append(rows, map[string]string{
			"profileId": profile, "workspaceId": ws, "origin": origin, "name": name,
			"path": path, "valueSha256": sha256Hex([]byte(value)), "expiresAt": expires,
		})
	}
	rawCookies, err := json.MarshalIndent(map[string]any{
		"dbPath": env.dbPath,
		"rows":   rows,
	}, "", "  ")
	if err != nil {
		t.Fatalf("序列化 cookies.json: %v", err)
	}
	browserObsWrite(t, "cookies.json", append(rawCookies, '\n'))

	rawPreserve, err := json.MarshalIndent(map[string]any{
		"personalProfileRoot": env.personal,
		"personalTree":        walkTree(t, env.personal),
		"profileRootTree":     walkTree(t, env.root),
		"facts":               facts,
	}, "", "  ")
	if err != nil {
		t.Fatalf("序列化 preservation.json: %v", err)
	}
	browserObsWrite(t, "preservation.json", append(rawPreserve, '\n'))
	browserObsMerge(t, prefixFacts("BR15", facts))
}
