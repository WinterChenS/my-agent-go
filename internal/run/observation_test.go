package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"my-agent-go/internal/contracts"
)

// 观察值输出：集成 runner 通过 ANVIL_OBSERVATION_DIR 指定本 case 的证据目录，
// 测试把「外部可核对的观察值」和原始 artifact 自己写进去，runner 再据此生成
// external-observation oracle。
//
// 为什么必须由 case 自己写：RUN* 在用例索引里声明 oracleKind=external，门禁拒绝
// test-process 自证（见 internal/devtools/gate.go 的 validateOraclePath）。runner
// 不替 case 编观察值，也不允许 case 不产出——缺 observation.json 就是 FAIL。
//
// 没设置该变量时（本地 focused 运行）直接返回，不产生任何文件、也不影响断言。

// 本包内的测试共用一个观察目录（同一个 case 的多条测试串行跑）：
// 第一次写入时清掉上一轮残留（同一 revision 重跑不累积），之后的写入是**追加/合并**。
var caseObservationStarted sync.Once

func observationDir() string {
	return strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
}

// prepareCaseObservation 在进程内第一次写观察时清掉陈旧的观察产物。
//
// 为什么需要：证据目录按 revision 复用，重跑同一 case 时旧文件还在。
// 只清本 case 的观察产物（observation.json / events.jsonl），不动 runner 写的
// result.json / stdout.log，也不动由副作用产生的 counter.txt（它会整体重写）。
func prepareCaseObservation(dir string) {
	caseObservationStarted.Do(func() {
		_ = os.Remove(filepath.Join(dir, "observation.json"))
		_ = os.Remove(filepath.Join(dir, "events.jsonl"))
	})
}

// writeCaseObservation 把观察值与原始 artifact 写入 ANVIL_OBSERVATION_DIR。
//
// 同一 case 的多个测试共用一份：
//   - observation.json 是**合并**写：新 facts 的 key 并进已有 JSON（同 key 覆盖），
//     并累积 "tests": [每个写入者的 t.Name()]，这样审查者能看到这个 case 到底
//     跑了哪几条测试、各自的观察值是什么；
//   - events.jsonl 是**追加**写（一行一个完整 contracts.Event，保留 runId）。
func writeCaseObservation(t *testing.T, facts map[string]any, files map[string][]byte) {
	t.Helper()
	dir := observationDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录 %s 失败: %v", dir, err)
	}
	prepareCaseObservation(dir)

	for name, content := range files {
		if name == "events.jsonl" {
			f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				t.Fatalf("追加 artifact %s 失败: %v", name, err)
			}
			_, writeErr := f.Write(content)
			closeErr := f.Close()
			if writeErr != nil || closeErr != nil {
				t.Fatalf("追加 artifact %s 失败: write=%v close=%v", name, writeErr, closeErr)
			}
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatalf("写 artifact %s 失败: %v", name, err)
		}
	}

	merged := map[string]any{}
	if raw, err := os.ReadFile(filepath.Join(dir, "observation.json")); err == nil {
		if err := json.Unmarshal(raw, &merged); err != nil {
			// 不静默重建：已有文件损坏时继续写会让“合并”变成“混脏数据”。
			t.Fatalf("已有 observation.json 不是合法 JSON: %v", err)
		}
	}
	for k, v := range facts {
		merged[k] = v
	}
	var tests []any
	if cur, ok := merged["tests"].([]any); ok {
		tests = cur
	}
	self := t.Name()
	seen := false
	for _, v := range tests {
		if s, ok := v.(string); ok && s == self {
			seen = true
			break
		}
	}
	if !seen {
		tests = append(tests, self)
	}
	merged["tests"] = tests

	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		t.Fatalf("序列化观察值失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "observation.json"), data, 0o644); err != nil {
		t.Fatalf("写 observation.json 失败: %v", err)
	}
}

// writeCaseJournal 把该 service 上所有 run 的事件日志**追加**到 events.jsonl
// （一行一个 contracts.Event，原样保留 runId 与 seq）。
//
// 诚实说明：这是产品自己持久化的事件日志，属于产品输出，不是实现边界之外的真实
// 副作用。RUN01/02/03/04/06 用它当 artifact 是因为它们的验收断言本身就是「产品把
// 事件记成了什么样」；唯一真正的边界外副作用是 RUN05 的 counter.txt（由 Pi 子进程
// 里的 fixture 工具写出，不是测试进程写出的）。
//
// 同一个 case 的多个测试共用同一份 artifact：events.jsonl 追加写，不同 run 的事件
// 混在一个文件里（靠每行的 runId 区分），observation.json 合并写（见上）。
// artifact 于是表示「这个 case 全部测试的聚合输出」，而不是最后一条测试的覆盖结果——
// 覆盖写会让 RUN01 只剩 TestUnknownSettledPayloadIsCompatibilityError 的事件，
// 核心的“重试耗尽”证据反而丢失。
func writeCaseJournal(t *testing.T, svc *Service) {
	t.Helper()
	if observationDir() == "" {
		return
	}
	svc.mu.Lock()
	ids := make([]contracts.RunID, 0, len(svc.runs))
	for id := range svc.runs {
		ids = append(ids, id)
	}
	svc.mu.Unlock()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	var b strings.Builder
	own := map[string]any{"runs": 0}
	ownStates := map[string]string{}
	ownEvents := 0
	for _, id := range ids {
		evs, err := svc.Replay(id, 0)
		if err != nil {
			t.Fatalf("重放 run %s 事件失败: %v", id, err)
		}
		for _, ev := range evs {
			line, err := json.Marshal(ev)
			if err != nil {
				t.Fatalf("序列化事件失败: %v", err)
			}
			b.Write(line)
			b.WriteByte('\n')
			ownEvents++
			if ev.Type == EventRunState {
				var p statePayload
				if err := json.Unmarshal(ev.Payload, &p); err == nil {
					ownStates[string(id)] = p.State
				}
			}
		}
	}
	own["runs"] = len(ids)
	own["events"] = ownEvents
	own["lastStates"] = ownStates

	// 先追加 artifact（进程内第一次写会先清掉上一轮残留），再**从文件本身**汇总事实。
	// 不在这里算整体统计：observation 里的数字必须与 events.jsonl 说的是同一件事，
	// 否则聚合之后两者会各说各话。
	writeCaseObservation(t, nil, map[string][]byte{"events.jsonl": []byte(b.String())})
	facts := summarizeJournal(t, filepath.Join(observationDir(), "events.jsonl"))
	// 每个测试自己的观察值单独留一份：run id 是「秒级时间戳 + 每个 Service 的序号」，
	// 同一 case 的多个测试很可能拿到同一个 runId，整体统计会把它们合并成一格。
	facts["byTest:"+t.Name()] = own
	writeCaseObservation(t, facts, nil)
}

// summarizeJournal 从聚合后的 events.jsonl 汇总观察事实（事件总数、run 数、各 run 的最后状态）。
func summarizeJournal(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 events.jsonl 汇总失败: %v", err)
	}
	total := 0
	var runOrder []string
	seenIDs := map[string]bool{}
	lastStates := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev contracts.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			// events.jsonl 只能是一行一个完整 contracts.Event；多余的东西宁可判错。
			t.Fatalf("events.jsonl 含非 contracts.Event 行: %v", err)
		}
		total++
		id := string(ev.RunID)
		if _, seen := lastStates[id]; !seen {
			seenIDs[id] = true
			runOrder = append(runOrder, id)
		}
		if ev.Type == EventRunState {
			var p statePayload
			if err := json.Unmarshal(ev.Payload, &p); err == nil {
				lastStates[id] = p.State
			}
		}
	}
	return map[string]any{
		"eventsTotal":   total,
		"runs":          len(runOrder),
		"lastRunStates": lastStates,
	}
}
