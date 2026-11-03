package pi

// 本文件是"真实宿主环境"的集成测试，默认跳过（需显式 MYAGENT_PI_REAL=1）。
// 目的：对宿主真实存在的 Pi 做一次定位与探测，记录真实路径/版本/来源/状态，
// 作为 PI01 的真实证据。probe 全程使用隔离 agentDir，不触碰用户真实配置。

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestRealHostPiLocateAndProbe(t *testing.T) {
	if os.Getenv("MYAGENT_PI_REAL") != "1" {
		t.Skip("需显式设置 MYAGENT_PI_REAL=1 才运行真实宿主测试")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	insts, err := Locate(ctx, "")
	if err != nil {
		t.Logf("Locate 返回诊断错误（可接受，例如已保存路径失效）: %v", err)
	}
	if len(insts) == 0 {
		t.Fatalf("宿主应至少发现一个 Pi，实际为 0")
	}
	for _, in := range insts {
		t.Logf("发现 Pi: version=%q origin=%s source=%s arch=%s runnable=%v path=%s note=%q",
			in.Version, in.Origin, in.Source, in.Architecture, in.Runnable, in.Executable, in.Note)
	}

	// 对可执行的那个做隔离探测。基线只认证 1.5.0，其余必须是 unverified。
	for _, in := range insts {
		if !in.Runnable {
			continue
		}
		res, perr := Probe(ctx, in)
		t.Logf("探测 %s => state=%s version=%q diagnostics=%v", in.Executable, res.State, res.Version, res.Diagnostics)
		if perr != nil {
			t.Logf("Probe 返回错误: %v", perr)
		}
		if res.Version == "" {
			t.Fatalf("真实 Pi 的版本不应为空（探测失败）")
		}
		// 认证版本才允许 Ready；其它必须 unverified（UnsupportedVersion）。
		if res.Version == "1.5.0" {
			if res.State != StateReady {
				t.Fatalf("认证版本 1.5.0 应 Ready，实际 %s", res.State)
			}
		} else if res.State == StateReady {
			t.Fatalf("未认证版本 %s 不得标 Ready", res.Version)
		}
		break
	}
}
