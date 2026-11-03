//go:build linux

package credentials

import (
	"context"
	"os/exec"
	"strconv"

	"my-agent-go/internal/contracts"
)

// nativeStore 用 Secret Service（freedesktop.org）保存凭证。
//
// 通过 secret-tool 访问：它是 libsecret 的官方 CLI，背后是运行中的
// gnome-keyring / KWallet 提供的 D-Bus 服务。密码从 stdin 传入，不进 argv。
//
// 为什么不用 CGO 直连 libsecret：全平台构建均为 CGO_ENABLED=0。
// 为什么不做"文件 + chmod 600"的降级：那等于明文落盘，违反基线 §5；
// 后端不可用时必须明确 CREDENTIAL_STORE_UNAVAILABLE，让上层改用 MemStore。
type nativeStore struct{}

const secretToolBin = "secret-tool"

// NewNativeStore 打开 Secret Service 凭证库。
func NewNativeStore(root string) (Store, error) {
	_ = root // Secret Service 不落文件，root 仅用于对齐接口签名
	if _, err := exec.LookPath(secretToolBin); err != nil {
		return nil, unavailable("未找到 secret-tool（需安装 libsecret-tools）: " + err.Error())
	}
	// 探测服务是否真的可达：lookup 一个必然不存在的属性。
	// 退出码 1 表示"查不到"——说明服务是活的；其它情况（连不上 D-Bus、
	// 没有 keyring daemon）必须在这里暴露，否则会被误读成"凭证不存在"。
	cmd := exec.Command(secretToolBin, "lookup", "service", credentialService, "account", "__myagent_probe__")
	err := cmd.Run()
	if err == nil {
		return &nativeStore{}, nil
	}
	if code, ok := exitCodeOf(err); ok {
		switch code {
		case 1:
			return &nativeStore{}, nil
		case 2:
			return nil, unavailable("Secret Service 不可用：系统上没有可用的 keyring daemon（D-Bus 服务 org.freedesktop.secrets 未注册）")
		}
		return nil, unavailable("Secret Service 探测失败（secret-tool 退出码 " + strconv.Itoa(code) + "）")
	}
	return nil, unavailable("Secret Service 探测失败: " + err.Error())
}

// Put 写入或覆盖一条凭证。
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
	args := []string{
		"store",
		"--label", "MyAgent credential: " + ref,
		"service", credentialService,
		"account", ref,
	}
	// secret-tool store 从 stdin 一直读到 EOF，且**不要**补换行——
	// 多写一个 \n 会让密码末尾多一个换行，读回来 base64 解码就失败了。
	if _, code, err := runWithInput(ctx, secretToolBin, args, encodeSecret(secret)); err != nil {
		return unavailable("写入 Secret Service 失败: " + err.Error())
	} else if code != 0 {
		return unavailable("写入 Secret Service 失败（secret-tool 退出码 " + strconv.Itoa(code) + "）")
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
	out, code, err := runWithInput(ctx, secretToolBin,
		[]string{"lookup", "service", credentialService, "account", ref}, "")
	if err != nil {
		return nil, unavailable("读取 Secret Service 失败: " + err.Error())
	}
	if code == 1 {
		return nil, contracts.NewAppError(contracts.CodeNotFound, "凭证不存在: "+ref, false)
	}
	if code != 0 {
		return nil, unavailable("读取 Secret Service 失败（secret-tool 退出码 " + strconv.Itoa(code) + "）")
	}
	return decodeSecret(out)
}

// Delete 删除凭证（幂等：不存在也返回成功）。
func (s *nativeStore) Delete(ctx context.Context, ref string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRef(ref); err != nil {
		return err
	}
	_, code, err := runWithInput(ctx, secretToolBin,
		[]string{"clear", "service", credentialService, "account", ref}, "")
	if err != nil {
		return unavailable("删除 Secret Service 条目失败: " + err.Error())
	}
	// secret-tool clear 对不存在的条目也返回 0；非 0 才是真故障。
	if code != 0 {
		return unavailable("删除 Secret Service 条目失败（secret-tool 退出码 " + strconv.Itoa(code) + "）")
	}
	return nil
}
