// Package history 维护产品侧 run 索引，并只读定位 Pi transcript（RUN07）。
//
// 权威边界（基线 §5 / 001_initial.sql 头部声明）：
//   - Pi transcript 是消息与分支历史权威，本包**只读**它，绝不改写；
//   - SQLite 只承载索引与结构化记录，本包不把消息正文写进数据库，
//     只保存定位信息（session id 是 transcript 定位键）与计数；
//   - 因此库里出现正文就是越界，TestIndexStoresLocatorsNotContent 直接检查
//     原始数据库字节。
//
// RUN07 不新增迁移：003 归 C08、005 归 C06，本包只用 001 已有的
// workspaces / sessions / runs 三张表。transcript 文件的绝对路径在读取时
// 现场解析（<AgentDir>/sessions/*.jsonl），不落库；将来 C06 的迁移 003
// 若要持久化这份 locator，应该由它自己声明，而不是被本包偷偷建表。
//
// 解析容错：Pi 的会话文件格式没有对本产品做过承诺，所以逐行按 JSON 对象解析，
// 只认两种形状：{"type":"message","message":{"role":...}} 与裸 {"role":...}。
// 认不出的行计入 Skipped 而不报错——将来 Pi 换格式时应该退化成
// "定位信息 + 跳过计数"，而不是猜出一份错误的消息列表。
//
// 不常驻 worker：本包没有任何 goroutine、ticker 或后台 flush。
// 索引在调用方（run.Service 的状态迁移）里同步写入，写完即返回；
// TestNoPersistentHistoryWorker 用 goroutine dump 断言这一点。
//
// 依赖方向：history → run（run 只声明 HistorySink 接口，不认识本包）。
package history

import (
	"context"
	"strings"
	"sync"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/storage"
)

// Options 是历史索引的定位参数。
type Options struct {
	// AgentDir 是 Pi agentDir；transcript 在 <AgentDir>/sessions/ 下
	// （run.Service 用 --session-dir 指到这里）。
	AgentDir string
	// WorkspaceID 是索引归属的工作区。
	WorkspaceID contracts.WorkspaceID
	// WorkspaceRoot 是 workspaces.root_path（用户可见的工作区根目录）。
	WorkspaceRoot string
}

// Service 是 run 索引与 transcript 定位服务。并发安全。
type Service struct {
	db   *storage.DB
	opts Options

	// mu 保护 locks；locks 保证"同一会话只有一个索引写者"。
	mu    sync.Mutex
	locks map[contracts.SessionID]*sync.Mutex
}

// RunRecord 是索引里一行 run。
type RunRecord struct {
	RunID          contracts.RunID     `json:"runId"`
	SessionID      contracts.SessionID `json:"sessionId"`
	State          string              `json:"state"`
	LastSeq        uint64              `json:"lastSeq"`
	ConfigRevision uint64              `json:"configRevision"`
	UpdatedAt      time.Time           `json:"updatedAt"`
}

// Message 是 transcript 里一条可识别的消息。
type Message struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// Transcript 是 transcript 的只读视图：定位信息 + 消息 + 跳过计数。
type Transcript struct {
	SessionID contracts.SessionID `json:"sessionId"`
	// Path 是指向 Pi 会话文件的定位信息（locator），不是内容副本。
	Path     string    `json:"path"`
	Messages []Message `json:"messages"`
	// Skipped 是无法识别（或不是消息）的行数。
	Skipped int `json:"skipped"`
}

// Open 打开历史索引服务。
//
// 它不建表、不改 schema、不启动任何后台任务：数据库由 storage 的迁移负责，
// 索引写入由调用方同步驱动。
func Open(ctx context.Context, db *storage.DB, opts Options) (*Service, error) {
	if db == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "历史索引缺少数据库句柄", false)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	opts.AgentDir = strings.TrimSpace(opts.AgentDir)
	opts.WorkspaceRoot = strings.TrimSpace(opts.WorkspaceRoot)
	if strings.TrimSpace(string(opts.WorkspaceID)) == "" {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "历史索引缺少 workspace id", false)
	}
	return &Service{
		db:    db,
		opts:  opts,
		locks: map[contracts.SessionID]*sync.Mutex{},
	}, nil
}

// RecordRun 实现 run.HistorySink：把一次 run 快照写进索引。
//
// upsert 在**一个事务**里按 workspaces → sessions → runs 的顺序完成：
// 外键（sessions.workspace_id、runs.session_id）要求父行先存在，分两次提交
// 会在中间断言窗口里露出半截数据。同一 run 重复记录只更新行，不产生副本。
//
// 同一会话的写入经 per-session 互斥量串行化：这是 RUN07 的"单会话无并发
// writer"，不是吞吐优化（数据库本来就是单连接）。互斥量按会话惰性创建并
// 保留到进程结束——删除它需要引用计数，删除窗口里同一会话会拿到两个不同的
// 锁，那正好破坏了本函数要保证的性质。
func (s *Service) RecordRun(ctx context.Context, snap contracts.RunSnapshot) error {
	sessionID := snap.SessionID
	if strings.TrimSpace(string(sessionID)) == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing, "run 快照缺少 session id，不能落索引", false)
	}
	if strings.TrimSpace(string(snap.RunID)) == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing, "run 快照缺少 run id，不能落索引", false)
	}

	unlock := s.lockSession(sessionID)
	defer unlock()

	return s.recordLocked(ctx, snap)
}

// lockSession 取得该会话的写者锁，返回解锁函数。
func (s *Service) lockSession(sessionID contracts.SessionID) func() {
	s.mu.Lock()
	mu, ok := s.locks[sessionID]
	if !ok {
		mu = &sync.Mutex{}
		s.locks[sessionID] = mu
	}
	s.mu.Unlock()

	mu.Lock()
	return mu.Unlock
}

// recordLocked 在一个事务里 upsert 三张表。调用方必须持有会话写者锁。
func (s *Service) recordLocked(ctx context.Context, snap contracts.RunSnapshot) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)

	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "开启索引事务失败: "+err.Error(), true)
	}
	defer func() { _ = tx.Rollback() }()

	workspaceID := snap.WorkspaceID
	if strings.TrimSpace(string(workspaceID)) == "" {
		workspaceID = s.opts.WorkspaceID
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO workspaces(id, root_path, created_at) VALUES(?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET root_path = excluded.root_path`,
		string(workspaceID), s.opts.WorkspaceRoot, now); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "写入 workspace 索引失败: "+err.Error(), true)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sessions(id, workspace_id, created_at) VALUES(?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET workspace_id = excluded.workspace_id`,
		string(snap.SessionID), string(workspaceID), now); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "写入 session 索引失败: "+err.Error(), true)
	}

	// last_seq 只增不减：max() 挡住"迟到的旧快照把序号改回去"。
	// 状态与配置版本按最新写入覆盖——它们是"当前事实"，不是累积值。
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO runs(id, session_id, state, last_seq, config_revision, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		     state = excluded.state,
		     last_seq = max(runs.last_seq, excluded.last_seq),
		     config_revision = excluded.config_revision,
		     updated_at = excluded.updated_at`,
		string(snap.RunID), string(snap.SessionID), snap.State, int64(snap.LastSeq), int64(snap.ConfigRevision), now); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "写入 run 索引失败: "+err.Error(), true)
	}

	if err := tx.Commit(); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "提交索引事务失败: "+err.Error(), true)
	}
	return nil
}

// Runs 返回一个会话的 run 索引，按 run id 稳定排序。
//
// 空结果不是错误：新会话确实可以还没有 run。但读取失败必须报错——
// 把"I/O 失败"和"没有记录"混成同一个空切片，界面就无法区分
// "这个会话没跑过"和"索引读不出来"。
func (s *Service) Runs(ctx context.Context, sessionID contracts.SessionID) ([]RunRecord, error) {
	if strings.TrimSpace(string(sessionID)) == "" {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "缺少 session id，无法读取 run 索引", false)
	}
	rows, err := s.db.SQL().QueryContext(ctx,
		`SELECT id, session_id, state, last_seq, config_revision, updated_at
		   FROM runs WHERE session_id = ? ORDER BY id`, string(sessionID))
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取 run 索引失败: "+err.Error(), true)
	}
	defer rows.Close()

	out := make([]RunRecord, 0, 4)
	for rows.Next() {
		var (
			rec       RunRecord
			id        string
			session   string
			updatedAt string
			lastSeq   int64
			revision  int64
		)
		if err := rows.Scan(&id, &session, &rec.State, &lastSeq, &revision, &updatedAt); err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "扫描 run 索引失败: "+err.Error(), true)
		}
		rec.RunID = contracts.RunID(id)
		rec.SessionID = contracts.SessionID(session)
		if lastSeq > 0 {
			rec.LastSeq = uint64(lastSeq)
		}
		if revision > 0 {
			rec.ConfigRevision = uint64(revision)
		}
		if ts, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
			rec.UpdatedAt = ts
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取 run 索引异常: "+err.Error(), true)
	}
	return out, nil
}
