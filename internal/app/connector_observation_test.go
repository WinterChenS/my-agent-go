package app

// CONN03/CONN05 的绑定层外部观察。
//
// 事实来源都是 appData 里的真实文件：connectors.db（定义/审计的落点）、
// 配置存储层文件、隔离 agentDir 里的 models.json / mcp.json。
// 明文 sentinel 的命中数由**重新扫描这些文件**算出（含 base64 变体），
// 不看测试进程里的 DTO 变量。

import (
	"os"
	"path/filepath"
	"testing"
)

// observeConnectorApp 遍历 appData 根并把明文命中数写进观察目录。
func observeConnectorApp(t *testing.T, root, secret string, extra map[string]any) {
	t.Helper()
	if appObservationDir() == "" {
		return
	}
	observeTree(t, root, "connector-app-tree.json", nil)

	hits := []string{}
	if secret != "" {
		hits = scanForSecret(t, root, secret)
	}
	dbBytes := 0
	for _, name := range []string{"connectors.db", "connectors.db-wal", "connectors.db-shm"} {
		if info, err := os.Stat(filepath.Join(root, name)); err == nil {
			dbBytes += int(info.Size())
		}
	}
	facts := map[string]any{
		"appRoot":            filepath.ToSlash(root),
		"connectorDBBytes":   dbBytes,
		"sentinelHitCount":   len(hits),
		"sentinelHits":       hits,
		"bindingObservation": true,
	}
	for k, v := range extra {
		facts[k] = v
	}
	facts["byTest:"+t.Name()] = map[string]any{
		"connectorDBBytes": dbBytes,
		"sentinelHitCount": len(hits),
		"sentinelHits":     hits,
	}
	writeAppObservation(t, facts, nil)
}
