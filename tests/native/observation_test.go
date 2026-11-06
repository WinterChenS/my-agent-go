//go:build windows

package nativetest

// 原生用例的外部观察装置（公共部分）。
//
// 契约与 tests/fixtures/browser/observation_test.go、internal/memory/observation_test.go
// 一致：
//   - 观察目录来自 ANVIL_OBSERVATION_DIR（由 cmd/devtools 的 native runner 注入）；
//     没设置时（本地 focused 运行）什么也不写，不留垃圾文件；
//   - artifact 覆盖写、observation.json 合并写；
//   - **observation.json 里的每个事实都必须从磁盘上的 artifact 重新读出来算**，
//     不允许直接拿内存里的变量冒充观察值。读回来的值还要能支持结论，否则
//     观察装置本身就是坏的，用例直接失败。
//
// 各 case 的观察值分别在 observation_br01/br04/br05/br06_test.go 里，按 case 提交。

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- 基础装置 ----------

// observationDir 返回 runner 注入的观察目录；空表示本地 focused 运行。
func observationDir() string {
	return strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
}

// observationEnabled 说明本次运行是否需要写观察文件。
func observationEnabled() bool { return observationDir() != "" }

func observationPath(name string) string {
	return filepath.Join(observationDir(), filepath.FromSlash(name))
}

var observationMu sync.Mutex

// observationWrite 落盘一个 artifact（覆盖写，父目录自动创建）。
func observationWrite(t *testing.T, name string, data []byte) {
	t.Helper()
	dir := observationDir()
	if dir == "" {
		return
	}
	path := observationPath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建观察目录 %s 失败: %v", filepath.Dir(path), err)
	}
	observationMu.Lock()
	defer observationMu.Unlock()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写观察 artifact %s 失败: %v", path, err)
	}
}

// observationAppendJSONL 往 jsonl artifact 追加一行。
func observationAppendJSONL(t *testing.T, name string, v any) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	line, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化 %s 的记录失败: %v", name, err)
	}
	path := observationPath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建观察目录失败: %v", err)
	}
	observationMu.Lock()
	defer observationMu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("打开 %s 失败: %v", path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, 0x0a)); err != nil {
		t.Fatalf("追加 %s 失败: %v", path, err)
	}
}

// observationRead 读回 artifact 原始字节（观察值必须从这些字节算出来）。
func observationRead(t *testing.T, name string) []byte {
	t.Helper()
	if !observationEnabled() {
		t.Fatalf("没有 ANVIL_OBSERVATION_DIR，无法读回 %s", name)
	}
	raw, err := os.ReadFile(observationPath(name))
	if err != nil {
		t.Fatalf("读回 artifact %s 失败: %v", name, err)
	}
	return raw
}

// observationReadJSON 读回 JSON artifact。
func observationReadJSON(t *testing.T, name string, out any) {
	t.Helper()
	raw := observationRead(t, name)
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("artifact %s 不是合法 JSON: %v", name, err)
	}
}

// observationReadJSONL 读回 jsonl artifact（跳过空行）。
func observationReadJSONL(t *testing.T, name string) []obsFacts {
	t.Helper()
	raw := observationRead(t, name)
	out := make([]obsFacts, 0, 32)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		rec := obsFacts{}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("artifact %s 的行不是合法 JSON: %v（原文 %s）", name, err, truncate(line, 160))
		}
		out = append(out, rec)
	}
	if len(out) == 0 {
		t.Fatalf("artifact %s 一行都没有：观察值无从计算", name)
	}
	return out
}

// observationUpdateSection 在 name 这份 JSON 对象里写/覆盖一个 section。
//
// 一个 case 有多条测试、每条测试各写自己那一段（BR05 的焦点/最小化/剪贴板/IME），
// 用 section 分开，避免后跑的测试把前面的观察值盖掉。
func observationUpdateSection(t *testing.T, name, section string, value any) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	observationMu.Lock()
	defer observationMu.Unlock()
	path := observationPath(name)
	merged := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &merged); err != nil {
			t.Fatalf("已有的 %s 不是合法 JSON: %v", name, err)
		}
	}
	merged[section] = value
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		t.Fatalf("序列化 %s 失败: %v", name, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建观察目录失败: %v", err)
	}
	if err := os.WriteFile(path, append(data, 0x0a), 0o644); err != nil {
		t.Fatalf("写 %s 失败: %v", name, err)
	}
}

// observationSection 从 JSON 对象 artifact 里读回一个 section。
func observationSection(t *testing.T, name, section string) obsFacts {
	t.Helper()
	all := map[string]obsFacts{}
	observationReadJSON(t, name, &all)
	got, ok := all[section]
	if !ok {
		t.Fatalf("artifact %s 里没有 section %q", name, section)
	}
	return got
}

// observationMergeFacts 把事实合并进 observation.json（runner 只负责封装）。
func observationMergeFacts(t *testing.T, facts obsFacts) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	observationMu.Lock()
	defer observationMu.Unlock()
	path := observationPath("observation.json")
	merged := obsFacts{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &merged); err != nil {
			t.Fatalf("已有的 observation.json 不是合法 JSON: %v", err)
		}
	}
	for k, v := range facts {
		merged[k] = v
	}
	var tests []any
	if cur, ok := merged["tests"].([]any); ok {
		tests = cur
	}
	seen := false
	for _, v := range tests {
		if s, ok := v.(string); ok && s == t.Name() {
			seen = true
			break
		}
	}
	if !seen {
		tests = append(tests, t.Name())
	}
	merged["tests"] = tests
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		t.Fatalf("序列化 observation.json 失败: %v", err)
	}
	if err := os.WriteFile(path, append(data, 0x0a), 0o644); err != nil {
		t.Fatalf("写 observation.json 失败: %v", err)
	}
}

// ---------- obsFacts：带断言的读回取值 ----------

// obsFacts 是从文件读回来的一个 JSON 对象。
//
// 取值一律带 t.Fatalf：读不到 = 观察装置坏了，不能当成 false 蒙过去。
type obsFacts map[string]any

func (f obsFacts) num(t *testing.T, key string) float64 {
	t.Helper()
	v, ok := f[key]
	if !ok {
		t.Fatalf("观察值缺少字段 %q（现有 %v）", key, f.keys())
	}
	n, ok := v.(float64)
	if !ok {
		t.Fatalf("观察值 %q 不是数字: %T", key, v)
	}
	return n
}

func (f obsFacts) flag(t *testing.T, key string) bool {
	t.Helper()
	v, ok := f[key]
	if !ok {
		t.Fatalf("观察值缺少字段 %q（现有 %v）", key, f.keys())
	}
	b, ok := v.(bool)
	if !ok {
		t.Fatalf("观察值 %q 不是布尔: %T", key, v)
	}
	return b
}

func (f obsFacts) text(t *testing.T, key string) string {
	t.Helper()
	v, ok := f[key]
	if !ok {
		t.Fatalf("观察值缺少字段 %q（现有 %v）", key, f.keys())
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("观察值 %q 不是字符串: %T", key, v)
	}
	return s
}

func (f obsFacts) obj(t *testing.T, key string) obsFacts {
	t.Helper()
	v, ok := f[key]
	if !ok {
		t.Fatalf("观察值缺少字段 %q（现有 %v）", key, f.keys())
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("观察值 %q 不是对象: %T", key, v)
	}
	return obsFacts(m)
}

func (f obsFacts) list(t *testing.T, key string) []any {
	t.Helper()
	v, ok := f[key]
	if !ok {
		t.Fatalf("观察值缺少字段 %q（现有 %v）", key, f.keys())
	}
	l, ok := v.([]any)
	if !ok {
		t.Fatalf("观察值 %q 不是数组: %T", key, v)
	}
	return l
}

func (f obsFacts) keys() []string {
	out := make([]string, 0, len(f))
	for k := range f {
		out = append(out, k)
	}
	return out
}

// ---------- PNG 事实（从文件读回，不用内存里的字节） ----------

// pngFacts 是一份 PNG artifact 的文件级事实。
type pngFacts struct {
	File           string
	Width          int
	Height         int
	DistinctColors int
	Bytes          int
	SHA256         string
}

// readPNGFacts 从磁盘上的 PNG 文件读回尺寸 / 采样颜色数 / sha256。
func readPNGFacts(t *testing.T, name string) pngFacts {
	t.Helper()
	raw := observationRead(t, name)
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("PNG artifact %s 头部无法解码: %v", name, err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("PNG artifact %s 无法解码: %v", name, err)
	}
	sum := sha256.Sum256(raw)
	return pngFacts{
		File:           name,
		Width:          cfg.Width,
		Height:         cfg.Height,
		DistinctColors: statsOf(img),
		Bytes:          len(raw),
		SHA256:         hex.EncodeToString(sum[:]),
	}
}

// obsFacts 把 PNG 事实摊平成可写进 JSON 的 map。
func (p pngFacts) obsFacts() obsFacts {
	return obsFacts{
		"file":           p.File,
		"width":          p.Width,
		"height":         p.Height,
		"distinctColors": p.DistinctColors,
		"bytes":          p.Bytes,
		"sha256":         p.SHA256,
	}
}

// ---------- Win32 直读辅助（只加观察需要的，不改已有断言） ----------

// dpiForWindow 是 GetDpiForWindow 直读（Win10 1607+）。
func dpiForWindow(hwnd uintptr) uint32 {
	r, _, _ := tUser32.NewProc("GetDpiForWindow").Call(hwnd)
	return uint32(r)
}

// foregroundWindow 是 GetForegroundWindow 直读。
func foregroundWindow() uintptr {
	r, _, _ := tUser32.NewProc("GetForegroundWindow").Call()
	return r
}

// obsRect 把屏幕矩形摊平成 JSON 友好的形状。
func obsRect(r tRect) obsFacts {
	return obsFacts{
		"left": r.Left, "top": r.Top, "right": r.Right, "bottom": r.Bottom,
		"width": r.Right - r.Left, "height": r.Bottom - r.Top,
	}
}

// fixtureCounter 走一次 HTTP 去 fixture 服务端读计数器（服务端自己的状态）。
//
// 不读页面文案、不读内存 map：计数器的权威值在服务端，这里用 HTTP 取。
func fixtureCounter(t *testing.T, originURL, name string) int {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Get(originURL + "/__counters")
	if err != nil {
		t.Fatalf("HTTP 读取 fixture 计数器失败: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("HTTP 读取 fixture 计数器状态 %d", res.StatusCode)
	}
	var counters map[string]int
	if err := json.NewDecoder(res.Body).Decode(&counters); err != nil {
		t.Fatalf("解析 fixture 计数器失败: %v", err)
	}
	return counters[name]
}

// ---------- 另一个进程写文件 ----------

// runPowerShellToFile 跑一个**独立的 PowerShell 进程**，让它自己把结果写进文件。
//
// 观察值必须由另一个进程写出：测试进程只负责把路径给它、再读回文件。
// 返回该进程的退出码与合并输出（只用于失败时的诊断信息）。
func runPowerShellToFile(t *testing.T, script string) (int, string) {
	t.Helper()
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), string(out)
		}
		return 2, fmt.Sprintf("%v / %s", err, string(out))
	}
	return 0, string(out)
}
