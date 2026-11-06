package history

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/run"
	"my-agent-go/internal/runtime/pi"
	"my-agent-go/internal/storage"
)

// 本文件是 RUN07 的索引侧测试：
//   - 重启之后产品索引与 transcript 仍然可查；
//   - 库里只有定位信息，没有正文；
//   - 同一会话没有并发写者；
//   - 没有常驻的历史 worker。

const (
	historyWorkspaceID contracts.WorkspaceID = "ws-history"
	historySessionID   contracts.SessionID   = "sess-history"
)

// openTestDB 打开一个临时文件数据库（storage.Open 明确拒绝 :memory:）。
func openTestDB(t *testing.T, path string) *storage.DB {
	t.Helper()
	db, err := storage.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	// 产品启动路径同样要跑迁移：这里不跑就等于在"没有表"的库上测索引，
	// 得到的结论跟真实安装无关。
	if err := db.Migrate(context.Background()); err != nil {
		_ = db.Close()
		t.Fatalf("应用迁移失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// openTestHistory 打开索引服务。
func openTestHistory(t *testing.T, db *storage.DB, agentDir, workspaceRoot string) *Service {
	t.Helper()
	svc, err := Open(context.Background(), db, Options{
		AgentDir:      agentDir,
		WorkspaceID:   historyWorkspaceID,
		WorkspaceRoot: workspaceRoot,
	})
	if err != nil {
		t.Fatalf("打开历史索引失败: %v", err)
	}
	return svc
}

// readDatabaseBytes 读回数据库文件的原始字节（含 WAL/SHM）。
//
// WAL 模式下新写入的页可能还在 -wal 里，只读 .db 会让"正文没写进库"这类
// 负向断言自动成立——那等于没测。所以三份都读，并由调用方先验证一个
// 正向控制（定位键必须出现），证明读到的是真实数据区。
func readDatabaseBytes(t *testing.T, dbPath string) []byte {
	t.Helper()
	var out []byte
	for _, suffix := range []string{"", "-wal", "-shm"} {
		raw, err := os.ReadFile(dbPath + suffix)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("读取数据库文件 %s%s 失败: %v", dbPath, suffix, err)
		}
		out = append(out, raw...)
		out = append(out, '\n')
	}
	if len(out) == 0 {
		t.Fatalf("数据库文件 %s 为空，无法检查权威边界", dbPath)
	}
	return out
}

// scriptWorker 是进程内的假 Pi：只吐测试塞进去的事件。
type scriptWorker struct {
	events chan pi.RawEvent
	once   sync.Once
}

func newScriptWorker() *scriptWorker {
	return &scriptWorker{events: make(chan pi.RawEvent, 32)}
}

func (w *scriptWorker) Call(ctx context.Context, command json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"type":"response","command":"prompt","success":true}`), nil
}

func (w *scriptWorker) Events() <-chan pi.RawEvent { return w.events }

func (w *scriptWorker) Close(ctx context.Context) error {
	w.once.Do(func() { close(w.events) })
	return nil
}

func (w *scriptWorker) PID() int                   { return 0 }
func (w *scriptWorker) StderrTail() (string, bool) { return "", false }
func (w *scriptWorker) ExitCode() (int, bool)      { return 0, true }
func (w *scriptWorker) Limitations() []string      { return nil }

// workerFactory 每次启动都给一个新的假 worker，并按启动顺序交出来。
type workerFactory struct {
	created chan *scriptWorker
}

func newWorkerFactory() *workerFactory {
	return &workerFactory{created: make(chan *scriptWorker, 8)}
}

func (f *workerFactory) start(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
	w := newScriptWorker()
	f.created <- w
	return w, nil
}

// newRunService 构造一个把索引写进 history.Service 的 run.Service。
func newRunService(t *testing.T, workspaceDir, agentDir string, sink run.HistorySink) (*run.Service, *workerFactory) {
	t.Helper()
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatalf("创建工作区目录失败: %v", err)
	}
	policyExt := filepath.Join(workspaceDir, "harness-policy.ts")
	if err := os.WriteFile(policyExt, []byte("export default function(){}"), 0o644); err != nil {
		t.Fatalf("写策略扩展桩失败: %v", err)
	}
	factory := newWorkerFactory()
	svc := run.New(run.Options{
		WorkspaceID:  historyWorkspaceID,
		SessionID:    historySessionID,
		WorkspaceDir: workspaceDir,
		AgentDir:     agentDir,
		Installation: pi.PiInstallation{ID: "test:pi", Executable: filepath.Join(workspaceDir, "pi.exe")},
		Isolation:    pi.IsolationSpec{PolicyExtension: policyExt},
		Now:          time.Now,
		StartWorker:  factory.start,
		History:      sink,
	})
	return svc, factory
}

// waitRunState 轮询到 run 进入目标状态。
func waitRunState(t *testing.T, svc *run.Service, id contracts.RunID, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snap, err := svc.Get(id)
		if err == nil && snap.State == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	snap, err := svc.Get(id)
	t.Fatalf("run %s 未在 5 秒内进入 %s（当前 %q，err=%v）", id, want, snap.State, err)
}

// startRun 启动一个 run 并等到它的 worker 出现，返回 run id 与 worker。
func startRun(t *testing.T, svc *run.Service, factory *workerFactory, text string) (contracts.RunID, *scriptWorker) {
	t.Helper()
	snap, err := svc.Start(context.Background(), run.StartRunRequest{Message: text})
	if err != nil {
		t.Fatalf("启动 run 失败: %v", err)
	}
	select {
	case w := <-factory.created:
		return snap.RunID, w
	case <-time.After(5 * time.Second):
		t.Fatalf("run %s 没有在 5 秒内启动 worker", snap.RunID)
	}
	return "", nil
}

func settledRecord(aborted bool) string {
	return fmt.Sprintf(`{"type":"agent_settled","aborted":%t}`, aborted)
}

// dumpRunRows 用**第二个** storage.DB 句柄读数据库文件里的 runs 行。
//
// 观察值必须来自数据库文件本身（source:"database"），不能来自内存里的服务：
// 用同一个句柄读等于问被测对象"你记对了吗"。
func dumpRunRows(t *testing.T, dbPath string, sessionID contracts.SessionID) []byte {
	t.Helper()
	db := openTestDB(t, dbPath)
	defer db.Close()

	rows, err := db.SQL().Query(
		`SELECT id, session_id, state, last_seq, config_revision, updated_at FROM runs WHERE session_id = ? ORDER BY id`,
		string(sessionID))
	if err != nil {
		t.Fatalf("读取 runs 行失败: %v", err)
	}
	defer rows.Close()

	out := []map[string]any{}
	for rows.Next() {
		var id, session, state, updatedAt string
		var lastSeq, configRevision int64
		if err := rows.Scan(&id, &session, &state, &lastSeq, &configRevision, &updatedAt); err != nil {
			t.Fatalf("扫描 runs 行失败: %v", err)
		}
		out = append(out, map[string]any{
			"id": id, "sessionId": session, "state": state,
			"lastSeq": lastSeq, "configRevision": configRevision, "updatedAt": updatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("读取 runs 行异常: %v", err)
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("序列化 runs 行失败: %v", err)
	}
	return data
}

// TestRunIndexSurvivesRestart 覆盖 RUN07 的第一条事实：
// 重启（新服务 + 重开数据库）后，产品索引仍然回答"这个会话有哪些 run、
// 什么状态、last_seq 多少"，transcript 仍然可读，且没有重复行。
func TestRunIndexSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")
	workspaceDir := filepath.Join(dir, "ws")
	agentDir := filepath.Join(dir, "agent")
	writeSyntheticTranscript(t, agentDir, historySessionID, transcriptSentinel, 3)

	db := openTestDB(t, dbPath)
	idx := openTestHistory(t, db, agentDir, workspaceDir)
	svc, factory := newRunService(t, workspaceDir, agentDir, idx)

	// run 1：正常完成。
	run1, w1 := startRun(t, svc, factory, "第一条")
	w1.events <- pi.RawEvent{Type: "message_end", Record: json.RawMessage(
		`{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"stop"}}`)}
	w1.events <- pi.RawEvent{Type: pi.TypeAgentSettled, Record: json.RawMessage(settledRecord(false))}
	waitRunState(t, svc, run1, contracts.RunStateCompleted)

	// run 2：provider 报错，终态 failed。
	run2, w2 := startRun(t, svc, factory, "第二条")
	w2.events <- pi.RawEvent{Type: "message_end", Record: json.RawMessage(
		`{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"provider 失败"}}`)}
	w2.events <- pi.RawEvent{Type: pi.TypeAgentSettled, Record: json.RawMessage(settledRecord(false))}
	waitRunState(t, svc, run2, contracts.RunStateFailed)

	if err := svc.Close(ctx); err != nil {
		t.Fatalf("关闭 run 服务失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	// 重启：新数据库句柄 + 新索引服务，模拟新进程。
	db2 := openTestDB(t, dbPath)
	idx2 := openTestHistory(t, db2, agentDir, workspaceDir)

	recs, err := idx2.Runs(ctx, historySessionID)
	if err != nil {
		t.Fatalf("重启后读取 run 索引失败: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("重启后应有 2 条 run 索引，实际 %d 条: %+v", len(recs), recs)
	}
	states := []string{recs[0].State, recs[1].State}
	sort.Strings(states)
	if want := []string{contracts.RunStateCompleted, contracts.RunStateFailed}; states[0] != want[0] || states[1] != want[1] {
		t.Fatalf("重启后状态应为 %v，实际 %v", want, states)
	}
	for _, r := range recs {
		if r.LastSeq == 0 {
			t.Fatalf("run %s 的 last_seq 在重启后为 0（索引没落下）", r.RunID)
		}
		if r.SessionID != historySessionID {
			t.Fatalf("run %s 的 session 归属错误: %q", r.RunID, r.SessionID)
		}
	}

	// transcript 仍然可读。
	tr, err := idx2.Transcript(ctx, historySessionID)
	if err != nil {
		t.Fatalf("重启后读取 transcript 失败: %v", err)
	}
	if len(tr.Messages) != 3 {
		t.Fatalf("transcript 应有 3 条消息，实际 %d 条: %+v", len(tr.Messages), tr.Messages)
	}

	// 数据库文件本身的行数与 dump（外部观察值）。
	dump := dumpRunRows(t, dbPath, historySessionID)
	var dumped []map[string]any
	if err := json.Unmarshal(dump, &dumped); err != nil {
		t.Fatalf("解析 runs dump 失败: %v", err)
	}
	if len(dumped) != 2 {
		t.Fatalf("数据库文件里应有 2 行 runs，实际 %d 行: %s", len(dumped), dump)
	}

	writeCaseObservation(t, map[string]any{
		"runs":               2,
		"states":             []string{contracts.RunStateCompleted, contracts.RunStateFailed},
		"transcriptMessages": len(tr.Messages),
		"dbRows":             len(dumped),
		"restart":            true,
	}, map[string][]byte{"index-dump.json": dump})
}

// TestIndexStoresLocatorsNotContent 覆盖 RUN07 的权威边界：
// 数据库里只有定位信息（session id / run 索引），没有 transcript 正文。
func TestIndexStoresLocatorsNotContent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")
	agentDir := filepath.Join(dir, "agent")
	writeSyntheticTranscript(t, agentDir, historySessionID, transcriptSentinel, 3)

	db := openTestDB(t, dbPath)
	t.Cleanup(func() { _ = db.Close() })
	idx := openTestHistory(t, db, agentDir, filepath.Join(dir, "ws"))

	if err := idx.RecordRun(ctx, contracts.RunSnapshot{
		RunID: "run-locator", SessionID: historySessionID, WorkspaceID: historyWorkspaceID,
		State: contracts.RunStateRunning, LastSeq: 7,
	}); err != nil {
		t.Fatalf("写入索引失败: %v", err)
	}

	// transcript 确实带着正文（否则下面的负向断言没有意义）。
	rawTranscript, err := os.ReadFile(filepath.Join(agentDir, "sessions", string(historySessionID)+".jsonl"))
	if err != nil {
		t.Fatalf("读取合成 transcript 失败: %v", err)
	}
	if !bytes.Contains(rawTranscript, []byte(transcriptSentinel)) {
		t.Fatalf("合成 transcript 不含哨兵正文，负向断言会失去意义")
	}

	blob := readDatabaseBytes(t, dbPath)
	// 正向控制：定位键必须真的在库里，证明读到的确实是数据区。
	if !bytes.Contains(blob, []byte(historySessionID)) {
		t.Fatalf("数据库字节里找不到定位键 %q：无法证明读到了数据区", historySessionID)
	}
	if bytes.Contains(blob, []byte(transcriptSentinel)) {
		t.Fatalf("数据库里出现了 transcript 正文 %q：违反 001_initial.sql 的权威边界", transcriptSentinel)
	}

	// 索引行确实在（定位信息 + 序号），说明上面不是"因为什么都没写"才通过。
	recs, err := idx.Runs(ctx, historySessionID)
	if err != nil {
		t.Fatalf("读取 run 索引失败: %v", err)
	}
	if len(recs) != 1 || recs[0].LastSeq != 7 || recs[0].State != contracts.RunStateRunning {
		t.Fatalf("索引行不符合预期: %+v", recs)
	}

	// 不得偷偷加表/加迁移：F05 不占用 003(C08)/005(C06) 的迁移号。
	rows, err := db.SQL().QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatalf("读取表清单失败: %v", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("扫描表名失败: %v", err)
		}
		tables = append(tables, name)
	}
	allowed := map[string]bool{
		"workspaces": true, "sessions": true, "runs": true, "memories": true,
		"memory_fts": true, "memory_scope_settings": true, "schema_migrations": true,
	}
	for _, name := range tables {
		if !allowed[name] && !strings.HasPrefix(name, "memory_fts_") {
			t.Fatalf("history 包不得新增表 %q（无新迁移号）：现有 %v", name, tables)
		}
	}
	var versions int
	if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&versions); err != nil {
		t.Fatalf("读取迁移记录失败: %v", err)
	}
	if versions != 2 {
		t.Fatalf("迁移号不得增加（003 归 C08、005 归 C06）：实际 %d 条迁移记录", versions)
	}
}

// TestSingleWriterPerSession 覆盖 RUN07 的第二条事实：
// 同一会话的并发 RecordRun 串行化，行集一致，last_seq 不倒退。
//
// 8 个 goroutine × 每个 5 次写入，写的是 8 个不同 run：
// 每个 run 只应有一行，其 last_seq 等于该 run 见过的最大值。
func TestSingleWriterPerSession(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")
	db := openTestDB(t, dbPath)
	idx := openTestHistory(t, db, filepath.Join(dir, "agent"), filepath.Join(dir, "ws"))

	const (
		goroutines = 8
		calls      = 5
	)
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			runID := contracts.RunID(fmt.Sprintf("run-%02d", g))
			for i := 1; i <= calls; i++ {
				snap := contracts.RunSnapshot{
					RunID:          runID,
					SessionID:      historySessionID,
					WorkspaceID:    historyWorkspaceID,
					State:          contracts.RunStateRunning,
					LastSeq:        uint64(i),
					ConfigRevision: uint64(g),
				}
				if err := idx.RecordRun(ctx, snap); err != nil {
					errs <- fmt.Errorf("run %s 第 %d 次写入失败: %w", runID, i, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发写入出现错误: %v", err)
	}

	recs, err := idx.Runs(ctx, historySessionID)
	if err != nil {
		t.Fatalf("读取 run 索引失败: %v", err)
	}
	if len(recs) != goroutines {
		t.Fatalf("应有 %d 行（每个 run 一行，不重复），实际 %d 行: %+v", goroutines, len(recs), recs)
	}
	seen := map[contracts.RunID]bool{}
	for _, r := range recs {
		if seen[r.RunID] {
			t.Fatalf("run %s 出现重复行", r.RunID)
		}
		seen[r.RunID] = true
		if r.LastSeq != calls {
			t.Fatalf("run %s 的 last_seq 应为最大值 %d，实际 %d", r.RunID, calls, r.LastSeq)
		}
	}

	var rows int
	if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE session_id = ?`, string(historySessionID)).Scan(&rows); err != nil {
		t.Fatalf("统计 runs 行失败: %v", err)
	}
	if rows != goroutines {
		t.Fatalf("数据库里应有 %d 行，实际 %d 行", goroutines, rows)
	}

	// last_seq 不倒退：迟到的旧快照不能把序号改回去。
	if err := idx.RecordRun(ctx, contracts.RunSnapshot{
		RunID: "run-00", SessionID: historySessionID, WorkspaceID: historyWorkspaceID,
		State: contracts.RunStateRunning, LastSeq: 1,
	}); err != nil {
		t.Fatalf("写入迟到快照失败: %v", err)
	}
	recs, err = idx.Runs(ctx, historySessionID)
	if err != nil {
		t.Fatalf("复读 run 索引失败: %v", err)
	}
	for _, r := range recs {
		if r.RunID == "run-00" && r.LastSeq != calls {
			t.Fatalf("last_seq 倒退了：run-00 应为 %d，实际 %d", calls, r.LastSeq)
		}
	}
}

// TestNoPersistentHistoryWorker 覆盖 RUN07 的第三条事实：
// 索引是同步完成的，history 包不留下任何 goroutine。
//
// 用 goroutine dump 断言而不是"睡 100ms 再看"，是因为时间断言会把
// "恰好还没起来"当成通过，也会把慢机器判死。
func TestNoPersistentHistoryWorker(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := openTestDB(t, filepath.Join(dir, "index.db"))
	t.Cleanup(func() { _ = db.Close() })
	idx := openTestHistory(t, db, filepath.Join(dir, "agent"), filepath.Join(dir, "ws"))

	if err := idx.RecordRun(ctx, contracts.RunSnapshot{
		RunID: "run-no-worker", SessionID: historySessionID, WorkspaceID: historyWorkspaceID,
		State: contracts.RunStateCompleted, LastSeq: 3,
	}); err != nil {
		t.Fatalf("写入索引失败: %v", err)
	}
	if _, err := idx.Runs(ctx, historySessionID); err != nil {
		t.Fatalf("读取索引失败: %v", err)
	}

	var buf bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&buf, 1); err != nil {
		t.Fatalf("导出 goroutine dump 失败: %v", err)
	}
	dump := buf.String()
	if strings.Contains(dump, "internal/history.(*Service)") {
		t.Fatalf("history 包留下了常驻 goroutine（RUN07 禁止常驻历史 worker）：\n%s", dump)
	}
}
