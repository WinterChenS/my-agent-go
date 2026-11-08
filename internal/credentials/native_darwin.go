//go:build darwin

package credentials

import (
	"context"
	"os/exec"
	"strconv"

	"my-agent-go/internal/contracts"
)

// nativeStore 用 macOS Keychain 保存凭证。
//
// 用系统自带的 /usr/bin/security（不是第三方库）：它随 OS 发布、无需安装、
// 直接操作 login keychain，密钥由 Keychain 用用户登录密钥加密。
type nativeStore struct{}

const securityBin = "/usr/bin/security"

// errSecItemNotFound 是 security 命令"条目不存在"的退出码。
const errSecItemNotFound = 44

// NewNativeStore 打开 Keychain 凭证库。
func NewNativeStore(root string) (Store, error) {
	_ = root // Keychain 不落文件，root 仅用于对齐接口签名
	if _, err := exec.LookPath(securityBin); err != nil {
		return nil, unavailable("未找到 " + securityBin + ": " + err.Error())
	}
	// 探测 Keychain 是否可访问。锁定的 keychain 会让后续每次读写都失败，
	// 而失败原因很容易被误读成"凭证不存在"，所以在构造时就把话说清楚。
	if _, code, err := runWithInput(context.Background(), securityBin, []string{"list-keychains"}, ""); err != nil || code != 0 {
		return nil, unavailable("Keychain 不可访问（可能已锁定）: " + describe(code, err))
	}
	return &nativeStore{}, nil
}

// Put 写入或更新一条通用密码条目。
//
// 密码通过 stdin 传入：`-w` 不带值让 security 从标准输入读取（无 tty 时它读 stdin，
// add-generic-password 会要求重复输入一次，所以写两行）。argv 里不出现凭证明文。
func (s *nativeStore) Put(ctx context.Context, ref string, secret []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRef(ref); err != nil {
		return err
	}
	if len(secret) == 0 {
		return contracts.NewAppError(contracts.CodeUnsupported, "凭证内容为空", false)
	}
	encoded := encodeSecret(secret)
	args := []string{
		"add-generic-password",
		"-U", // 已存在则更新；不存在则创建
		"-a", ref,
		"-s", credentialService,
		"-w", // 值从 stdin 读，不放 argv
	}
	_, code, err := runWithInput(ctx, securityBin, args, encoded+"\n"+encoded+"\n")
	if err != nil {
		return unavailable("写入 Keychain 失败: " + err.Error())
	}
	if code != 0 {
		return unavailable("写入 Keychain 失败（security 退出码 " + strconv.Itoa(code) + "）")
	}
	return nil
}

// Get 读取并解码凭证。
func (s *nativeStore) Get(ctx context.Context, ref string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateRef(ref); err != nil {
		return nil, err
	}
	out, code, err := runWithInput(ctx, securityBin,
		[]string{"find-generic-password", "-a", ref, "-s", credentialService, "-w"}, "")
	if err != nil {
		return nil, unavailable("读取 Keychain 失败: " + err.Error())
	}
	if code != 0 {
		if code == errSecItemNotFound {
			return nil, contracts.NewAppError(contracts.CodeNotFound, "凭证不存在: "+ref, false)
		}
		return nil, unavailable("读取 Keychain 失败（security 退出码 " + strconv.Itoa(code) + "）")
	}
	return decodeSecret(out)
}

// Delete 删除凭证（幂等：条目本就不存在视为成功）。
func (s *nativeStore) Delete(ctx context.Context, ref string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRef(ref); err != nil {
		return err
	}
	_, code, err := runWithInput(ctx, securityBin,
		[]string{"delete-generic-password", "-a", ref, "-s", credentialService}, "")
	if err != nil {
		return unavailable("删除 Keychain 条目失败: " + err.Error())
	}
	if code == 0 || code == errSecItemNotFound {
		return nil
	}
	return unavailable("删除 Keychain 条目失败（security 退出码 " + strconv.Itoa(code) + "）")
}

func describe(code int, err error) string {
	if err != nil {
		return err.Error()
	}
	return "security 退出码 " + strconv.Itoa(code)
}

// nativeBackend 是本平台 OS 保护存储的名字（设置页按它选文案）。
const nativeBackend = BackendMacOSKeychain

// nativeSelfTest 在 macOS 上不需要额外步骤：NewNativeStore 已经跑过
// `security list-keychains`，Keychain 锁定/不可访问在那里就报不可用了。
// 这里保留显式钩子而不是删掉调用点：三个平台的 Probe 走同一条路径。
func nativeSelfTest(ctx context.Context) error { return ctx.Err() }
