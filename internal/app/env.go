package app

// 环境变量的品牌迁移：ANVIL_* 优先，MYAGENT_* 保留一个开发兼容周期。
//
// 为什么不是"直接改名"：CI、证据脚本、本机 .env、以及用户手里已经写好的
// 启动快捷方式都还在用旧名字。突然不认旧变量，症状是"应用启动后配置全空"，
// 而错误现场（命令历史）往往已经找不回来了。
//
// 为什么也不是"两个都读、谁先谁后随便"：同时存在时必须能确定哪个生效，
// 并且**必须留痕**——否则兼容周期永远不会结束，没人知道还有谁在用旧名字。

import (
	"log"
	"os"
	"sort"
	"strings"
	"sync"
)

// 品牌环境变量前缀。旧前缀只保留一个开发兼容周期（AGENTS.md §3）。
const (
	envPrefix       = "ANVIL_"
	legacyEnvPrefix = "MYAGENT_"
)

var (
	envDeprecationMu sync.Mutex
	// envDeprecations 保存"旧变量被实际命中过"的记录，每个变量名只留一条。
	// 顺序按首次命中，EnvDeprecations 返回前会排序以保证输出稳定。
	envDeprecations    []string
	envDeprecationSeen map[string]struct{}
)

// envLookup 读取一个**不带前缀**的环境变量名，先查 ANVIL_ 再查 MYAGENT_。
//
// 返回值：
//   - value：去掉首尾空白后的值（空白视为未设置，避免 `MYAGENT_X=` 把路径清空）；
//   - source：实际命中的完整变量名，未命中为空串。诊断/配置来源据此标注"这个值从哪来"。
//
// 命中旧变量时记录一次弃用（每个变量名一次），并写一行 log。
func envLookup(name string) (value, source string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ""
	}
	if v := strings.TrimSpace(os.Getenv(envPrefix + name)); v != "" {
		return v, envPrefix + name
	}
	legacyName := legacyEnvPrefix + name
	v := strings.TrimSpace(os.Getenv(legacyName))
	if v == "" {
		return "", ""
	}
	recordEnvDeprecation(legacyName, envPrefix+name)
	return v, legacyName
}

// EnvLookup 是 envLookup 的导出形式，仅供 cmd/myagent 这类壳层复用同一套规则。
//
// 两个名字并存不是为了好看：应用内一律用 envLookup（写起来短、也更难误用成
// "每次读环境变量都开一条兼容路径"的借口），壳层需要跨包调用才用导出版。
func EnvLookup(name string) (value, source string) { return envLookup(name) }

// recordEnvDeprecation 记录一次旧变量命中（同名前缀只记一次）。
func recordEnvDeprecation(legacyName, anvilName string) {
	envDeprecationMu.Lock()
	if envDeprecationSeen == nil {
		envDeprecationSeen = map[string]struct{}{}
	}
	_, seen := envDeprecationSeen[legacyName]
	if !seen {
		envDeprecationSeen[legacyName] = struct{}{}
		envDeprecations = append(envDeprecations,
			"环境变量 "+legacyName+" 已弃用，请改用 "+anvilName+"（旧名仅保留一个开发兼容周期）")
	}
	envDeprecationMu.Unlock()

	if !seen {
		// 只记一次：每次读取都打日志会把真正的启动错误挤出日志窗口。
		log.Printf("[deprecation] %s 已弃用，请改用 %s", legacyName, anvilName)
	}
}

// EnvDeprecations 返回本次进程内命中的旧变量弃用记录（按变量名排序，便于比对）。
//
// F09/诊断页会把它展示出来：兼容期的结束条件就是这份列表重新变空。
func EnvDeprecations() []string {
	envDeprecationMu.Lock()
	out := make([]string, len(envDeprecations))
	copy(out, envDeprecations)
	envDeprecationMu.Unlock()
	sort.Strings(out)
	return out
}

// resetEnvDeprecations 清空弃用记录表，仅供测试从零开始。
func resetEnvDeprecations() {
	envDeprecationMu.Lock()
	envDeprecations = nil
	envDeprecationSeen = nil
	envDeprecationMu.Unlock()
}
