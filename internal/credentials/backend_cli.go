//go:build darwin || linux

package credentials

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os/exec"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
)

// 非 Windows 的原生凭证库都通过系统自带的命令行工具访问：
//   - macOS：/usr/bin/security（Keychain，系统内置）
//   - Linux：secret-tool（libsecret → Secret Service / D-Bus）
//
// 不选 CGO 绑定（Security.framework / libsecret）的原因：项目全平台构建均为
// CGO_ENABLED=0（见 build/versions.lock.json），引入 CGO 会连带要求每个目标都有
// 对应 SDK，等于用"凭证存储"绑架整个交叉编译矩阵。
//
// 传参纪律：密码**不写进 argv**。argv 在进程存活期间对同机任何用户可见（ps），
// 属于可避免的泄漏面。两个后端都支持从 stdin 提供密码，统一走 stdin。

// credentialService 是 Keychain / Secret Service 里的 service 名。
// 所有 ref 共用同一个 service，用 account=ref 区分，便于整体清理。
const credentialService = "cn.myagent.credentials"

// encodeSecret 把任意字节编码成可安全穿过命令行文本接口的形式。
//
// 为什么必须编码：Keychain 与 Secret Service 的 CLI 都以"文本行"形式返回密码，
// 原始字节里的换行、NUL、非 UTF-8 会被截断或损坏；base64 只有可打印 ASCII，
// 不改变长度上限的表现，也便于在错误诊断里安全打印。
// 它不是加密——真正的机密性来自 Keychain / Secret Service 本身。
func encodeSecret(secret []byte) string {
	return base64.StdEncoding.EncodeToString(secret)
}

// decodeSecret 解码并拒绝被外部篡改的内容。
func decodeSecret(encoded string) ([]byte, error) {
	trimmed := strings.TrimRight(encoded, "\r\n")
	if trimmed == "" {
		return nil, contracts.NewAppError(contracts.CodeInternal, "凭证内容为空，疑似被外部删除或损坏", false)
	}
	raw, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return nil, contracts.NewAppError(
			contracts.CodeInternal,
			"凭证内容不是合法的 base64，疑似被外部篡改: "+err.Error(),
			false,
		)
	}
	if len(raw) == 0 {
		return nil, contracts.NewAppError(contracts.CodeInternal, "凭证解码后为空", false)
	}
	return raw, nil
}

// exitCodeOf 提取 exec 错误里的退出码（非 ExitError 时返回 false）。
func exitCodeOf(err error) (int, bool) {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), true
	}
	return 0, false
}

// cliTimeout 是单次外部命令的上限。
//
// 不加超时会挂死：macOS 的 security 在找不到 tty 时可能一直等待密码输入，
// 而 CI 与守护进程场景里"一直等"等于整个进程冻结。20s 远大于正常读写耗时，
// 又不至于让一次卡死拖垮整个测试矩阵。
const cliTimeout = 20 * time.Second

// runWithInput 执行命令并把 stdin 作为密码通道，返回 stdout。
// 失败时把 stderr 拼进错误信息——否则"exit 1"什么都说明不了。
func runWithInput(ctx context.Context, bin string, args []string, stdin string) (string, int, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cliTimeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			return "", -1, err
		}
	}
	return stdout.String(), code, nil
}
