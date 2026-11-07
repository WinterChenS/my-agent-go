package connectors_test

// C08b 的跨适配器用例：五个平台共用**同一条**服务级授权门。
//
// 单平台用例证明"我的适配器拒绝时计数为 0"；这条用例证明"没有任何一个适配器
// 拥有自己的放行路径"——同一个硬拒绝策略门下，五个平台的写计数器必须都是 0，
// 并且（正控）同一批动作在允许时各产生 1 次副作用。没有正控，"计数永远是 0"
// 也可能只是"什么都没接上"。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"my-agent-go/internal/connectors"
	"my-agent-go/internal/connectors/connectortest"
	"my-agent-go/internal/connectors/dingtalk"
	"my-agent-go/internal/connectors/feishu"
	"my-agent-go/internal/connectors/fixtures"
	"my-agent-go/internal/connectors/github"
	"my-agent-go/internal/connectors/notion"
	"my-agent-go/internal/connectors/slack"
)

const (
	gateAppKey    = "gate-app-key"
	gateAppSecret = "gate-app-secret-S3NT1NEL-c08b"
	gateToken     = "gate-fixture-token"
	gateRepo      = "acme/app"
	gateChat      = "oc_gate_chat"
	gateChannel   = "C0GATE"
	gateDoc       = "doc-fixture"
	gatePage      = "page-1"
	gateGroup     = "cidGateGroup"
)

// adapterCase 描述一个平台上的"最危险的那条写动作"。
type adapterCase struct {
	kind         connectors.Kind
	platform     string
	capabilities []connectors.Capability
	resource     string
	credential   string
	action       string
	args         map[string]any
	writePath    string
	build        func(def connectors.Definition) (connectors.Driver, error)
	writes       func(fixtures.Snapshot) int
}

func adapterCases() []adapterCase {
	return []adapterCase{
		{
			kind:         connectors.KindDingtalk,
			platform:     fixtures.PlatformDingtalk,
			capabilities: []connectors.Capability{connectors.CapabilityDingtalkRead, connectors.CapabilityDingtalkSend},
			resource:     gateGroup,
			credential:   gateAppKey + ":" + gateAppSecret,
			action:       "send_group",
			args:         map[string]any{"conversationId": gateGroup, "text": "共用门"},
			writePath:    "/v1.0/robot/groupMessages/send",
			build: func(def connectors.Definition) (connectors.Driver, error) {
				return dingtalk.New(dingtalk.Settings{Endpoints: def.Endpoints})
			},
			writes: func(s fixtures.Snapshot) int { return s.DingtalkGroupMessages },
		},
		{
			kind:         connectors.KindFeishu,
			platform:     fixtures.PlatformFeishu,
			capabilities: []connectors.Capability{connectors.CapabilityFeishuRead, connectors.CapabilityFeishuWrite, connectors.CapabilityFeishuSend},
			resource:     gateDoc,
			credential:   gateAppKey + ":" + gateAppSecret,
			action:       "update_doc",
			args:         map[string]any{"documentId": gateDoc, "content": "共用门"},
			writePath:    "/open-apis/docx/v1/documents/" + gateDoc + "/blocks/batch_update",
			build: func(def connectors.Definition) (connectors.Driver, error) {
				return feishu.New(feishu.Settings{Endpoints: def.Endpoints})
			},
			writes: func(s fixtures.Snapshot) int { return s.FeishuDocUpdated },
		},
		{
			kind:         connectors.KindSlack,
			platform:     fixtures.PlatformSlack,
			capabilities: []connectors.Capability{connectors.CapabilitySlackRead, connectors.CapabilitySlackSend},
			resource:     gateChannel,
			credential:   gateToken,
			action:       "reply_thread",
			args:         map[string]any{"channel": gateChannel, "threadTs": "1700000001.000001", "text": "共用门"},
			writePath:    "/api/chat.postMessage",
			build: func(def connectors.Definition) (connectors.Driver, error) {
				return slack.New(slack.Settings{Endpoints: def.Endpoints})
			},
			writes: func(s fixtures.Snapshot) int { return s.SlackReplies },
		},
		{
			kind:         connectors.KindNotion,
			platform:     fixtures.PlatformNotion,
			capabilities: []connectors.Capability{connectors.CapabilityNotionRead, connectors.CapabilityNotionWrite},
			resource:     gatePage,
			credential:   gateToken,
			action:       "update_page",
			args:         map[string]any{"pageId": gatePage, "title": "共用门"},
			writePath:    "/v1/pages/" + gatePage,
			build: func(def connectors.Definition) (connectors.Driver, error) {
				return notion.New(notion.Settings{Endpoints: def.Endpoints})
			},
			writes: func(s fixtures.Snapshot) int { return s.NotionPagesUpdated },
		},
		{
			kind:         connectors.KindGitHub,
			platform:     fixtures.PlatformGitHub,
			capabilities: []connectors.Capability{connectors.CapabilityGitHubRead, connectors.CapabilityGitHubWrite},
			resource:     gateRepo,
			credential:   gateToken,
			action:       "create_issue",
			args:         map[string]any{"repo": gateRepo, "title": "共用门"},
			writePath:    "/repos/acme/app/issues",
			build: func(def connectors.Definition) (connectors.Driver, error) {
				return github.New(github.Settings{Endpoints: def.Endpoints})
			},
			writes: func(s fixtures.Snapshot) int { return s.GitHubIssuesCreated },
		},
	}
}

// TestAllAdaptersShareTheSameGate 是 brief 指定的跨适配器用例。
func TestAllAdaptersShareTheSameGate(t *testing.T) {
	for _, tc := range adapterCases() {
		tc := tc
		t.Run(string(tc.kind), func(t *testing.T) {
			run := func(deny bool) (*fixtures.API, *fixtures.Counter, int) {
				counter := fixtures.NewCounter("")
				opts := fixtures.APIOptions{
					Platform:  tc.platform,
					Counter:   counter,
					AppKey:    gateAppKey,
					AppSecret: gateAppSecret,
					Token:     gateToken,
				}
				api, err := fixtures.NewAPI(opts)
				if err != nil {
					t.Fatalf("启动 %s fixture: %v", tc.platform, err)
				}
				t.Cleanup(func() { _ = api.Close() })
				env := connectortest.New(t, connectortest.Config{
					Kind:             tc.kind,
					Endpoints:        connectors.Endpoints{APIBase: api.BaseURL()},
					Capabilities:     tc.capabilities,
					AllowedResources: []string{tc.resource},
					Credential:       []byte(tc.credential),
					Build:            tc.build,
					Deny:             deny,
					RateLimit:        connectors.RateLimit{Requests: 100, PerSeconds: 60},
				})
				res, err := env.Invoke(t, tc.action, tc.args)
				if err != nil {
					t.Fatalf("%s %s: %v", tc.platform, tc.action, err)
				}
				writes := tc.writes(counter.Snapshot())
				if deny {
					if res.Dispatched {
						t.Fatalf("%s 被拒绝的调用不得被标记为已派发: %+v", tc.platform, res)
					}
				} else if res.Outcome != connectors.OutcomeDone {
					t.Fatalf("%s 允许路径应完成: %+v", tc.platform, res)
				}
				return api, counter, writes
			}

			// 拒绝：任何平台的写计数器都必须是 0，且没有任何写请求被发出。
			api, _, writes := run(true)
			if writes != 0 {
				t.Fatalf("%s 在统一策略拒绝下产生了副作用（计数 %d）", tc.platform, writes)
			}
			if got := len(api.RequestsFor(tc.writePath)); got != 0 {
				t.Fatalf("%s 在统一策略拒绝下仍发出了 %d 个写请求", tc.platform, got)
			}
			if snap := api.Counter().Snapshot(); snap.ExternalWrites() != 0 {
				t.Fatalf("%s 拒绝时总外部写计数必须为 0: %+v", tc.platform, snap)
			}

			// 正控：同一个动作在允许时恰好产生 1 次副作用（证明上面的 0 不是没接上）。
			_, _, writes = run(false)
			if writes != 1 {
				t.Fatalf("%s 允许一次应恰好 1 次副作用，实际 %d", tc.platform, writes)
			}
		})
	}
}

// TestAllAdaptersDeclareHealthAndCapabilities 保证五个适配器都在注册表里、
// 都能被 policy 包接受为合法的工具通道来源，并且 catalog 与驱动自述一致。
func TestAllAdaptersDeclareHealthAndCapabilities(t *testing.T) {
	for _, tc := range adapterCases() {
		tc := tc
		t.Run(string(tc.kind), func(t *testing.T) {
			if !tc.kind.Known() {
				t.Fatalf("%s 必须是已知类型", tc.kind)
			}
			actions := connectors.Catalog(tc.kind)
			if len(actions) == 0 {
				t.Fatalf("%s 没有 catalog 动作", tc.kind)
			}
			for _, action := range actions {
				if action.Risk == "" || action.Capability == "" || action.Idempotency == "" || action.InputSchema == nil {
					t.Fatalf("%s/%s 声明不完整: %+v", tc.kind, action.ID, action)
				}
				if !connectors.KnownCapability(tc.kind, action.Capability) {
					t.Fatalf("%s 的能力 %s 不在 catalog 里", tc.kind, action.Capability)
				}
				if action.Risk != connectors.RiskRead && action.Idempotency == connectors.IdempotencyReadOnly {
					t.Fatalf("%s/%s 是写动作却声明 read-only 幂等", tc.kind, action.ID)
				}
			}
			// 健康检查必须给出结构化结论，而不是 panic 或空状态。
			api, err := fixtures.NewAPI(fixtures.APIOptions{
				Platform: tc.platform, Counter: fixtures.NewCounter(""),
				AppKey: gateAppKey, AppSecret: gateAppSecret, Token: gateToken,
			})
			if err != nil {
				t.Fatalf("启动 %s fixture: %v", tc.platform, err)
			}
			defer func() { _ = api.Close() }()
			env := connectortest.New(t, connectortest.Config{
				Kind:             tc.kind,
				Endpoints:        connectors.Endpoints{APIBase: api.BaseURL()},
				Capabilities:     tc.capabilities,
				AllowedResources: []string{tc.resource},
				Credential:       []byte(tc.credential),
				Build:            tc.build,
			})
			health, herr := env.Svc.Health(context.Background(), env.DefinitionID)
			if herr != nil {
				t.Fatalf("%s 健康检查出错: %v", tc.kind, herr)
			}
			if !health.Connected() {
				t.Fatalf("%s 正确凭证应 connected: %+v", tc.kind, health)
			}
		})
	}
}

// TestAdapterFixtureObservation 是五个 HTTP 平台的"外部 oracle"用例：
// 设置 ANVIL_OBSERVATION_DIR 之后，fixture 写 connector-counter.json 与
// connector-http-requests.jsonl，服务写 audit.jsonl；所有事实都从**文件**读回，
// 并检查整个目录里没有凭证材料。
func TestAdapterFixtureObservation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(connectors.ObservationDirEnv, dir)

	counter := fixtures.NewCounter(dir)
	api, err := fixtures.NewAPI(fixtures.APIOptions{
		Platform:       fixtures.PlatformSlack,
		ObservationDir: dir,
		Counter:        counter,
		Token:          gateToken,
	})
	if err != nil {
		t.Fatalf("启动 Slack fixture: %v", err)
	}
	t.Cleanup(func() { _ = api.Close() })

	env := connectortest.New(t, connectortest.Config{
		Kind:             connectors.KindSlack,
		Endpoints:        connectors.Endpoints{APIBase: api.BaseURL()},
		Capabilities:     []connectors.Capability{connectors.CapabilitySlackRead, connectors.CapabilitySlackSend},
		AllowedResources: []string{gateChannel},
		Credential:       []byte(gateToken),
		Build: func(def connectors.Definition) (connectors.Driver, error) {
			return slack.New(slack.Settings{Endpoints: def.Endpoints})
		},
		ObservationDir: dir,
		Tune: func(o *connectors.Options) {
			// 故意不传 Audit：让服务按观察目录自动落到 JSONL。
			o.Audit = nil
			o.ObservationDir = dir
		},
	})
	res, err := env.Invoke(t, "reply_thread", map[string]any{
		"channel": gateChannel, "threadTs": "1700000001.000001", "text": "观察用例",
	})
	if err != nil || res.Outcome != connectors.OutcomeDone {
		t.Fatalf("send: %+v %v", res, err)
	}

	rawCounter, err := os.ReadFile(filepath.Join(dir, connectors.ObservationCounterFile))
	if err != nil {
		t.Fatalf("fixture 没有写 %s: %v", connectors.ObservationCounterFile, err)
	}
	var snap fixtures.Snapshot
	if err := json.Unmarshal(rawCounter, &snap); err != nil {
		t.Fatalf("计数快照不是合法 JSON: %v", err)
	}
	if snap.SlackReplies != 1 {
		t.Fatalf("从文件读回的外部副作用应为 1，实际 %+v", snap)
	}

	rawLog, err := os.ReadFile(filepath.Join(dir, fixtures.RequestLogFile))
	if err != nil {
		t.Fatalf("fixture 没有写 %s: %v", fixtures.RequestLogFile, err)
	}
	writes := 0
	lines := strings.Split(strings.TrimSpace(string(rawLog)), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec fixtures.RequestRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("请求日志行不是合法 JSON: %v", err)
		}
		if rec.Path == "/api/chat.postMessage" {
			if !rec.WriteApplied {
				t.Fatalf("写请求必须被标记为已应用: %+v", rec)
			}
			writes++
		}
	}
	if writes != 1 {
		t.Fatalf("请求日志里应恰好有 1 个写请求，实际 %d", writes)
	}

	rawAudit, err := os.ReadFile(filepath.Join(dir, connectors.ObservationAuditFile))
	if err != nil {
		t.Fatalf("没有写 %s: %v", connectors.ObservationAuditFile, err)
	}
	auditLines := strings.Split(strings.TrimSpace(string(rawAudit)), "\n")
	var entry connectors.AuditEntry
	if err := json.Unmarshal([]byte(auditLines[len(auditLines)-1]), &entry); err != nil {
		t.Fatalf("审计行不是合法 JSON: %v", err)
	}
	if entry.Decision != "allow" || entry.Outcome != "done" ||
		entry.CredentialRef != connectortest.DefaultCredentialRef {
		t.Fatalf("审计行内容不对: %+v", entry)
	}

	// sentinel 扫描：整个观察目录里不得出现凭证材料。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读观察目录: %v", err)
	}
	for _, item := range entries {
		body, rerr := os.ReadFile(filepath.Join(dir, item.Name()))
		if rerr != nil {
			t.Fatalf("读 %s: %v", item.Name(), rerr)
		}
		if strings.Contains(string(body), gateToken) {
			t.Fatalf("观察目录文件 %s 里出现了凭证材料", item.Name())
		}
	}
}
