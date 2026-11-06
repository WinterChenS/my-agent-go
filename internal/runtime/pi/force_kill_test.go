package pi

// ForceKill 是"停止升级"用的窄接口（基线 §6：5 秒未退出则终止 worker 进程树）。
//
// 它刻意不进 Worker 接口：那是 RPC 协议契约，塞进一个只有停机路径用的方法，
// 会让所有实现与 fake 被迫跟着改，且让"能不能强杀"看起来像协议的一部分。

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestForceKillTerminatesOwnedTree 覆盖 RUN04 的升级终止：
// 不经过 Close 也要能把本 worker 拥有的进程树杀掉，并且可重复调用。
func TestForceKillTerminatesOwnedTree(t *testing.T) {
	w := startFake(t, "hang")
	fk, ok := w.(interface{ ForceKill() error })
	if !ok {
		t.Fatal("worker 必须实现 ForceKill（停止升级需要它）")
	}
	if err := fk.ForceKill(); err != nil {
		t.Fatalf("强制终止进程树失败: %v", err)
	}

	// 进程必须真的退出：ExitCode 变成已知值，而不是"大概被杀了"。
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, known := w.ExitCode(); known {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ForceKill 之后进程没有退出（pid=%d）", w.PID())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 幂等：上层可能在 Close 与 ForceKill 之间重复触发，第二次不能 panic。
	if err := fk.ForceKill(); err != nil {
		t.Fatalf("重复 ForceKill 不应报错，实际 %v", err)
	}
}

// TestForceKillAfterCloseReportsMissingTree 守"句柄没了就报错"：
// 进程树已经关闭还回 nil，等于让上层以为强杀有效、清理有结果。
func TestForceKillAfterCloseReportsMissingTree(t *testing.T) {
	w := startFake(t, "hang")
	if err := w.Close(context.Background()); err != nil {
		t.Fatalf("关闭 worker 失败: %v", err)
	}
	fk, ok := w.(interface{ ForceKill() error })
	if !ok {
		t.Fatal("worker 必须实现 ForceKill")
	}
	if err := fk.ForceKill(); err == nil {
		t.Fatal("进程树句柄已关闭时 ForceKill 必须报错")
	}
}

// TestForceKillConcurrentWithClose 覆盖"可并发调用"这条要求。
// 真正的判据是：并发下不 panic、不死锁；数据竞争由 -race 抓。
func TestForceKillConcurrentWithClose(t *testing.T) {
	w := startFake(t, "hang")
	fk, ok := w.(interface{ ForceKill() error })
	if !ok {
		t.Fatal("worker 必须实现 ForceKill")
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = fk.ForceKill()
	}()
	go func() {
		defer wg.Done()
		_ = w.Close(context.Background())
	}()
	wg.Wait()
	// Close 之后再强杀只能是"句柄已关闭"的错误，不能是 panic 或成功。
	if err := fk.ForceKill(); err == nil {
		t.Fatal("Close 之后 ForceKill 必须报错")
	}
}
