package pi

// F08-E 的测试：生产路径的契约探测自带隔离 stub provider。
//
// 要钉住的三件事：
//  1. Provider/Model 都没给时，探测自己在隔离 agentDir 里写 models.json，且 URL 只绑回环；
//  2. 探测结束（成功或失败）后 stub 已经关了，不留下任何监听端口；
//  3. 探测不碰用户 ~/.pi（用真实二进制跑一遍，前后哈希一致）。
//
// 只给一半 Provider/Model 属于配置错误：models.json 归谁写说不清，静默挑一边会让
// "探测通过"取决于运气。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// readProbeModels 读隔离 agentDir 里的 models.json，返回 provider 的 baseUrl。
func readProbeModels(t *testing.T, agentDir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(agentDir, "models.json"))
	if err != nil {
		t.Fatalf("隔离 agentDir 里没有 models.json: %v", err)
	}
	var doc struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析 models.json 失败: %v", err)
	}
	p, ok := doc.Providers[contractStubProviderID]
	if !ok {
		t.Fatalf("models.json 里没有探测 provider %q: %s", contractStubProviderID, string(raw))
	}
	if p.BaseURL == "" {
		t.Fatalf("provider %q 没有 baseUrl: %s", contractStubProviderID, string(raw))
	}
	return p.BaseURL
}

func TestContractProbeWritesIsolatedStubProvider(t *testing.T) {
	agentDir := t.TempDir()
	// fakeworker 只是"能说话的 worker"，与 stub 的模型无关：这里要证明的是探测
	// 会自己写出模型配置、并在结束时关掉端口，不需要一个真的模型回合成功。
	report, err := ContractProbe(context.Background(), contractInstallation(t, "1.5.0"), ContractOptions{
		AgentDir:      agentDir,
		WorkspaceDir:  t.TempDir(),
		CheckTimeout:  2 * time.Second,
		WorkerStarter: contractStarter(t, "echo"),
	})
	if err != nil {
		t.Fatalf("探测本身不应报错: %v", err)
	}
	_ = report

	baseURL := readProbeModels(t, agentDir)
	if !strings.HasPrefix(baseURL, "http://127.0.0.1:") {
		t.Fatalf("探测 provider 的 baseUrl 必须是回环地址，实际 %q", baseURL)
	}

	// 探测返回后 stub 必须已经关闭：端口不再接受连接。
	addr := strings.TrimPrefix(baseURL, "http://")
	addr = strings.TrimSuffix(addr, "/v1")
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, dialErr := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if dialErr != nil {
			return // 已经关掉了
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatalf("探测结束后 stub provider 仍在监听 %s（端口没关）", addr)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestContractProbeRejectsHalfSpecifiedProvider(t *testing.T) {
	for _, tc := range []struct{ provider, model string }{
		{"fixture", ""},
		{"", "fixture-model"},
	} {
		_, err := ContractProbe(context.Background(), contractInstallation(t, "1.5.0"), ContractOptions{
			AgentDir:      t.TempDir(),
			WorkspaceDir:  t.TempDir(),
			Provider:      tc.provider,
			Model:         tc.model,
			CheckTimeout:  time.Second,
			WorkerStarter: contractStarter(t, "echo"),
		})
		if err == nil {
			t.Fatalf("Provider=%q Model=%q 只给一半应当报配置错误", tc.provider, tc.model)
		}
	}
}

func TestContractProbeNeverTouchesUserPiHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("读取用户主目录: %v", err)
	}
	userPi := filepath.Join(home, ".pi")
	if _, statErr := os.Stat(userPi); statErr != nil {
		// 没有 ~/.pi 本身就说明探测没得可写；直接断言目录仍不存在。
		before := pathAbsent(t, userPi)
		runProbeForUserHomeTest(t)
		if !before {
			return
		}
		if _, statErr := os.Stat(userPi); statErr == nil {
			t.Fatalf("探测创建了用户 ~/.pi（%s）", userPi)
		}
		return
	}

	// 会话/日志由**正在跑这条测试的** Pi harness 自己持续写入，不能算作探测的副作用。
	// 这与 internal/piinstall/safety_test.go 的排除口径一致。
	exclude := []string{
		filepath.Join(userPi, "agent", "sessions"),
		filepath.Join(userPi, "agent", "logs"),
	}
	before := hashTreeExcluding(t, userPi, exclude)
	runProbeForUserHomeTest(t)
	after := hashTreeExcluding(t, userPi, exclude)
	if before != after {
		t.Fatalf("探测改动了用户 ~/.pi：before=%s after=%s", before, after)
	}
}

func runProbeForUserHomeTest(t *testing.T) {
	t.Helper()
	_, _ = ContractProbe(context.Background(), contractInstallation(t, "1.5.0"), ContractOptions{
		AgentDir:      t.TempDir(),
		WorkspaceDir:  t.TempDir(),
		CheckTimeout:  time.Second,
		WorkerStarter: contractStarter(t, "echo"),
	})
}

func pathAbsent(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Stat(p)
	return os.IsNotExist(err)
}

// hashTreeExcluding 对目录树做稳定哈希（路径|大小|内容），跳过 exclude 前缀。
func hashTreeExcluding(t *testing.T, root string, exclude []string) string {
	t.Helper()
	type entry struct {
		rel  string
		size int64
		sum  string
	}
	var entries []entry
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 并发写入导致的瞬时错误不计入
		}
		for _, ex := range exclude {
			if strings.HasPrefix(p, ex) {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return nil
		}
		sum := ""
		if f, openErr := os.Open(p); openErr == nil {
			h := sha256.New()
			_, _ = io.Copy(h, f)
			_ = f.Close()
			sum = hex.EncodeToString(h.Sum(nil))
		}
		entries = append(entries, entry{rel: rel, size: info.Size(), sum: sum})
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s 失败: %v", root, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	h := sha256.New()
	for _, e := range entries {
		_, _ = io.WriteString(h, e.rel+"|"+e.sum+"\n")
	}
	return hex.EncodeToString(h.Sum(nil))
}
