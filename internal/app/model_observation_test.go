package app

// MOD04 的外部观察：换模型这一刻磁盘上到底是什么。
//
// 为什么必须读盘：MOD04 要回答的是"新模型在下一个安全点生效、正在跑的那一轮不受影响"，
// 而盘上有两份互相独立的证据——<appRoot>/settings.json（下一次运行会用的 provider/model）
// 与 agentDir 里的 models.json（Pi 真正读的那份）。测试进程里的快照/状态不能替代它们。
//
// 纪律：
//   - 只写"是否为空 / 是否 env 引用"这类布尔事实，不把 apiKey 的值写进证据；
//   - artifact 按测试追加（同一条 case 的多条测试各有自己的 appData 根）；
//   - 未设置 ANVIL_OBSERVATION_DIR（本地 focused 运行）时什么也不写。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// agentDirEntry 是隔离 agentDir 里的一份磁盘事实。
type agentDirEntry struct {
	Name   string `json:"name"`
	IsDir  bool   `json:"isDir"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256,omitempty"`
}

// modelsJSONFacts 是从磁盘上的 models.json 解析出来的事实（不含任何密钥值）。
type modelsJSONFacts struct {
	Exists          bool     `json:"exists"`
	Bytes           int      `json:"bytes,omitempty"`
	Sha256          string   `json:"sha256,omitempty"`
	Providers       []string `json:"providers,omitempty"`
	ModelCount      int      `json:"modelCount"`
	UsesEnvRef      bool     `json:"usesEnvRef"`
	HasInlineAPIKey bool     `json:"hasInlineApiKeyValue"`
}

// observeModelSwitch 把换模型前后的磁盘事实写进观察目录。
func observeModelSwitch(t *testing.T, root, agentDir string, extra map[string]any) {
	t.Helper()
	if appObservationDir() == "" {
		return
	}

	// 1) appData 根的真实遍历：settings.json 和配置存储层文件都在里面。
	observeTree(t, root, "model-switch-tree.json", nil)

	settingsPath := filepath.Join(root, "settings.json")
	rawSettings, settingsErr := os.ReadFile(settingsPath)
	facts := map[string]any{
		"appRoot":        filepath.ToSlash(root),
		"agentDir":       filepath.ToSlash(agentDir),
		"settingsExists": settingsErr == nil,
		"capturedAt":     time.Now().UTC().Format(time.RFC3339),
	}
	if settingsErr == nil {
		var s Settings
		if err := json.Unmarshal(rawSettings, &s); err != nil {
			t.Fatalf("磁盘上的 settings.json 不是合法 JSON: %v", err)
		}
		sum := sha256.Sum256(rawSettings)
		facts["settingsSha256"] = "sha256:" + hex.EncodeToString(sum[:])
		facts["settingsProvider"] = s.Provider
		facts["settingsModel"] = s.Model
		facts["settingsModelRef"] = s.ModelRef
		// 只记"是否为空"，绝不把密钥值写进证据文件。
		facts["settingsAPIKeyEmpty"] = s.APIKey == ""
	}

	// 2) 隔离 agentDir 的真实条目 + 从盘上 models.json 解析出的事实。
	entries := []agentDirEntry{}
	if list, err := os.ReadDir(agentDir); err == nil {
		for _, e := range list {
			entry := agentDirEntry{Name: e.Name(), IsDir: e.IsDir()}
			if !e.IsDir() {
				if info, ierr := e.Info(); ierr == nil {
					entry.Bytes = info.Size()
				}
				if body, rerr := os.ReadFile(filepath.Join(agentDir, e.Name())); rerr == nil {
					sum := sha256.Sum256(body)
					entry.SHA256 = "sha256:" + hex.EncodeToString(sum[:])
				}
			}
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	facts["agentDirEntries"] = entries

	var models modelsJSONFacts
	if body, err := os.ReadFile(filepath.Join(agentDir, "models.json")); err == nil {
		sum := sha256.Sum256(body)
		models.Exists = true
		models.Bytes = len(body)
		models.Sha256 = "sha256:" + hex.EncodeToString(sum[:])
		var doc struct {
			Providers map[string]struct {
				APIKey string            `json:"apiKey"`
				Models []json.RawMessage `json:"models"`
			} `json:"providers"`
		}
		if jerr := json.Unmarshal(body, &doc); jerr != nil {
			t.Fatalf("磁盘上的 models.json 不是合法 JSON: %v", jerr)
		}
		for id, p := range doc.Providers {
			models.Providers = append(models.Providers, id)
			models.ModelCount += len(p.Models)
			if p.APIKey != "" {
				if p.APIKey[0] == '$' || p.APIKey[0] == '{' {
					models.UsesEnvRef = true
				} else {
					models.HasInlineAPIKey = true
				}
			}
		}
		sort.Strings(models.Providers)
	}
	facts["modelsJSON"] = models

	for k, v := range extra {
		facts[k] = v
	}
	facts["byTest:"+t.Name()] = map[string]any{
		"settingsModelRef":    facts["settingsModelRef"],
		"settingsProvider":    facts["settingsProvider"],
		"settingsModel":       facts["settingsModel"],
		"agentDirEntryCount":  len(entries),
		"modelsJSONProviders": models.Providers,
	}
	data, err := json.MarshalIndent(facts, "", "  ")
	if err != nil {
		t.Fatalf("序列化模型磁盘事实失败: %v", err)
	}
	// observation.json 与 artifact 用同一份磁盘事实：前者给门禁读，
	// 后者是原始文件，审查者可以自己再算一遍。
	writeAppObservation(t, facts, map[string][]byte{"model-state.json": data})
}
