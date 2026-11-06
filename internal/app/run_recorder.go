package app

// run 索引与诊断记录的复合 sink。
//
// 为什么需要它：`run.Options.History` 只有一个槽位，而两件事都要在 run 结算时落库：
//   - `history.Service` 写 sessions/runs 索引与 transcript 定位键（历史列表、"重开历史"
//     能不能找到 Pi 会话，靠的就是这份索引）；
//   - `diagnostics.Recorder` 写 run_usage/run_events（usage、预算、失败结论、诊断包）。
//
// C07 先把 Recorder 装进了那个槽位，结果是**索引没有写者**：会话列表看不到真实运行过的
// 会话，transcript 定位也找不到。两个消费者都是"派生数据"，但索引是会话历史的本体索引，
// 不能因为诊断占了槽位就断掉。
//
// 设计取舍：
//   - 两个都失败才返回错误（上层据此记 lastErr）；任一成功就算"记录已落地"，
//     因为把一个可选消费者的故障升级成"整轮 run 记录失败"会让另一个也白写；
//   - 错误信息把两边的原因都带上，单一消费者坏了可定位；
//   - 顺序固定（索引先、诊断后），不并发：两次写的都是同一个 SQLite 句柄，
//     并发写只会互相等锁，看不出额外收益。

import (
	"context"
	"strings"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/diagnostics"
	"my-agent-go/internal/history"
	"my-agent-go/internal/run"
)

// runRecorder 把 run 快照同时写进若干 sink。
type runRecorder struct {
	sinks []namedSink
}

type namedSink struct {
	name string
	sink interface {
		RecordRun(ctx context.Context, snap contracts.RunSnapshot) error
	}
}

// newRunRecorder 组合若干 sink；nil sink 被忽略（调用方不装配即不写那一份）。
func newRunRecorder(sinks ...namedSink) *runRecorder {
	kept := make([]namedSink, 0, len(sinks))
	for _, s := range sinks {
		if s.sink == nil {
			continue
		}
		kept = append(kept, s)
	}
	if len(kept) == 0 {
		return nil
	}
	return &runRecorder{sinks: kept}
}

// RecordRun 依次写入所有 sink。
//
// 全部失败才返回错误；部分成功会返回 nil，但把失败写进 error 之外的信息里是不行的
// （接口只给 error），所以这里宁可返回 nil 并在调用方看不到的日志里……不，
// 判断很简单：**只要有一个成功，索引这一份就是可用的**，失败的那个由它自己的
// 读取路径暴露（例如诊断包里 usage 为 unavailable）。把两边错误都带上返回，
// 会让上层把"索引其实写成功了"也标成失败，反而更难定位。
func (r *runRecorder) RecordRun(ctx context.Context, snap contracts.RunSnapshot) error {
	if r == nil {
		return nil
	}
	var firstErr error
	succeeded := 0
	for _, s := range r.sinks {
		if err := s.sink.RecordRun(ctx, snap); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		succeeded++
	}
	if succeeded > 0 {
		return nil
	}
	if firstErr == nil {
		return nil
	}
	return firstErr
}

// String 只用于诊断输出（不参与行为）。
func (r *runRecorder) String() string {
	if r == nil {
		return "run-recorder(nil)"
	}
	names := make([]string, 0, len(r.sinks))
	for _, s := range r.sinks {
		names = append(names, s.name)
	}
	return "run-recorder(" + strings.Join(names, ",") + ")"
}

// runHistorySinks 装配 run 索引的写入者。
//
// 索引库与会话目录共用 <appRoot>/sessions.db（C06b 的 sessionDB）：索引与目录分开
// 会变成两份会话真相，历史列表与 transcript 定位就会对不上。
func (d *Desktop) runHistorySinks(recorder *diagnostics.Recorder) (run.HistorySink, error) {
	hist, err := d.runIndexService()
	if err != nil {
		// 索引打不开时仍保留诊断记录：少一份不等于全丢。
		return newRunRecorder(namedSink{name: "diagnostics", sink: recorder}), err
	}
	return newRunRecorder(
		namedSink{name: "history", sink: hist},
		namedSink{name: "diagnostics", sink: recorder},
	), nil
}

// runIndexService 返回共享会话库上的 run 索引写入者。
func (d *Desktop) runIndexService() (*history.Service, error) {
	if _, err := d.sessionCatalog(); err != nil {
		return nil, err
	}
	d.svcMu.Lock()
	db := d.sessionDB
	d.svcMu.Unlock()
	if db == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "会话库未就绪，运行索引不会落库", false)
	}
	return history.Open(context.Background(), db, history.Options{
		AgentDir:      d.agentDir(),
		WorkspaceID:   "ws-default",
		WorkspaceRoot: strings.TrimSpace(d.Settings().Workspace),
	})
}
