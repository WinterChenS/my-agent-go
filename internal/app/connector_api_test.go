package app

// C08a 绑定层测试：连接器列表/保存/授权/撤销/健康，以及"秘密只进凭证库"。
//
// 事实来自外部 fixture（SMTP/IMAP）与磁盘文件，而不是"绑定层说自己没问题"：
//   - 健康状态必须来自 fixture 真实接受认证；
//   - 密码不得出现在 connectors.db / DTO JSON 里；
//   - 重启后定义仍在（端点），凭证仍在 OS 凭证库，DTO 里仍然只有 ref。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"my-agent-go/internal/connectors"
	"my-agent-go/internal/connectors/fixtures"
	"my-agent-go/internal/contracts"
)

const (
	appFxUser = "user@fixture.test"
	appFxPass = "S3NT1NEL-app-password-4c1d"
	appFxFrom = "user@fixture.test"
)

func startMailFixtures(t *testing.T) (*fixtures.SMTP, *fixtures.IMAP) {
	t.Helper()
	opts := fixtures.Options{Username: appFxUser, Password: appFxPass}
	smtp, err := fixtures.NewSMTP(opts)
	if err != nil {
		t.Fatalf("SMTP fixture: %v", err)
	}
	t.Cleanup(func() { _ = smtp.Close() })
	imap, err := fixtures.NewIMAP(opts)
	if err != nil {
		t.Fatalf("IMAP fixture: %v", err)
	}
	t.Cleanup(func() { _ = imap.Close() })
	return smtp, imap
}

// TestConnectorsAppAPIRoundTrip 覆盖 CONN01/CONN03 的绑定层部分。
func TestConnectorsAppAPIRoundTrip(t *testing.T) {
	smtp, imap := startMailFixtures(t)
	d, root := newTestDesktop(t)

	// 首次打开：有一条"未配置"的 mail 定义，健康状态不是 connected。
	list := d.ListConnectors()
	if list.Error != nil {
		t.Fatalf("ListConnectors: %+v", list.Error)
	}
	if len(list.Items) != 1 || list.Items[0].ID != defaultMailConnectorID {
		t.Fatalf("应播种一条默认 mail 定义: %+v", list.Items)
	}
	if list.Items[0].Health.Status == connectors.HealthConnected {
		t.Fatalf("没配置/没授权时不得 connected: %+v", list.Items[0].Health)
	}

	// 保存一条指向 fixture 的定义。
	saved := d.SaveConnector(ConnectorSaveRequest{
		ID:               "mail-fx",
		DisplayName:      "测试邮箱",
		Endpoints:        connectors.Endpoints{IMAPAddr: imap.Addr(), SMTPAddr: smtp.Addr(), Username: appFxUser, From: appFxFrom},
		Enabled:          true,
		Capabilities:     []string{string(connectors.CapabilityMailRead), string(connectors.CapabilityMailSend)},
		AllowedResources: []string{"@fixture.test"},
	})
	if saved.Error != nil {
		t.Fatalf("SaveConnector: %+v", saved.Error)
	}
	if saved.Item.Revision != 1 {
		t.Fatalf("首次保存 revision 应为 1: %+v", saved.Item)
	}
	if saved.Item.Endpoints.IMAPAddr != imap.Addr() {
		t.Fatalf("端点未保存: %+v", saved.Item.Endpoints)
	}

	// 未授权：健康必须 blocked，且提示补凭证。
	status := d.ConnectorStatus("mail-fx")
	if status.Error != nil {
		t.Fatalf("ConnectorStatus: %+v", status.Error)
	}
	if status.Item.Health.Status == connectors.HealthConnected {
		t.Fatalf("未授权不得 connected: %+v", status.Item.Health)
	}
	if status.Item.Health.Remediation != connectors.RemediationAddCredentials {
		t.Fatalf("应提示补凭证: %+v", status.Item.Health)
	}

	// 授权：密码只进 OS 凭证库；返回状态来自真实认证。
	connected := d.ConnectConnector("mail-fx", appFxPass)
	if connected.Error != nil {
		t.Fatalf("ConnectConnector: %+v", connected.Error)
	}
	if connected.Item.Health.Status != connectors.HealthConnected {
		t.Fatalf("fixture 认证通过后应 connected: %+v", connected.Item.Health)
	}

	// 另一个未配置的定义不能被"借用"到这条连接的端点/凭证（驱动缓存按定义隔离）。
	afterConnect := d.ListConnectors()
	for _, item := range afterConnect.Items {
		if item.ID == defaultMailConnectorID && item.Health.Status == connectors.HealthConnected {
			t.Fatalf("未配置的默认定义不得报 connected: %+v", item)
		}
	}

	// 健康单独查询也应为 connected。
	health := d.ConnectorHealth("mail-fx")
	if health.Error != nil || health.Health.Status != connectors.HealthConnected {
		t.Fatalf("ConnectorHealth: %+v %+v", health.Health, health.Error)
	}

	// DTO 里不得出现密码。
	rawList, err := json.Marshal(d.ListConnectors())
	if err != nil {
		t.Fatalf("序列化 DTO: %v", err)
	}
	if strings.Contains(string(rawList), appFxPass) {
		t.Fatalf("DTO 里出现了凭证材料: %s", rawList)
	}
	rawHealth, _ := json.Marshal(health)
	if strings.Contains(string(rawHealth), appFxPass) {
		t.Fatalf("健康 DTO 里出现了凭证材料: %s", rawHealth)
	}

	// 配置与审计库文件里不得出现密码。
	if dbBytes := readConnectorDB(t, root); strings.Contains(dbBytes, appFxPass) {
		t.Fatal("connectors.db 里出现了凭证材料")
	}

	// 重启：定义仍在（端点/能力），凭证仍在凭证库，DTO 仍只有 ref。
	d.Shutdown()
	d2, err := New(root)
	if err != nil {
		t.Fatalf("重启 Desktop: %v", err)
	}
	t.Cleanup(d2.Shutdown)
	after := d2.ConnectorStatus("mail-fx")
	if after.Error != nil {
		t.Fatalf("重启后 ConnectorStatus: %+v", after.Error)
	}
	if after.Item.Endpoints.SMTPAddr != smtp.Addr() {
		t.Fatalf("重启后端点丢了: %+v", after.Item.Endpoints)
	}
	if after.Item.CredentialRef != "connector:mail-fx" {
		t.Fatalf("重启后 ref 不对: %+v", after.Item)
	}
	if after.Item.Health.Status != connectors.HealthConnected {
		t.Fatalf("重启后凭证仍可用，应 connected: %+v", after.Item.Health)
	}

	// 撤销：凭证删除，健康回到不可用。
	revoked := d2.DisconnectConnector("mail-fx")
	if revoked.Error != nil {
		t.Fatalf("DisconnectConnector: %+v", revoked.Error)
	}
	if revoked.Item.Health.Status == connectors.HealthConnected {
		t.Fatalf("撤销后不得 connected: %+v", revoked.Item.Health)
	}
	if revoked.Item.Health.Remediation != connectors.RemediationAddCredentials {
		t.Fatalf("撤销后应提示重新授权: %+v", revoked.Item.Health)
	}

	// 撤销后仍不得在配置里留下秘密。
	if dbBytes := readConnectorDB(t, root); strings.Contains(dbBytes, appFxPass) {
		t.Fatal("撤销后 connectors.db 里出现了凭证材料")
	}

	// CONN03 的外部观察：appData 根的真实遍历 + 重新扫描磁盘得到的明文命中数（应为 0）。
	observeConnectorApp(t, root, appFxPass, map[string]any{
		"credentialRevoked":  true,
		"definitionSurvives": true,
	})
}

// TestConnectRejectsUnknownConnector 覆盖错误路径：未知 ID 是结构化 NOT_FOUND。
func TestConnectRejectsUnknownConnector(t *testing.T) {
	d, _ := newTestDesktop(t)
	res := d.ConnectConnector("nope", "whatever")
	if res.Error == nil {
		t.Fatal("未知连接器必须报错")
	}
	if res.Error.Code != contracts.CodeNotFound {
		t.Fatalf("期望 NOT_FOUND，实际 %+v", res.Error)
	}
	if strings.Contains(res.Error.Message, "whatever") {
		t.Fatal("错误信息不得回显凭证内容")
	}
}

// TestSaveConnectorRejectsUnsupportedKind 覆盖"没实现的平台不许被当成已实现"。
func TestSaveConnectorRejectsUnsupportedKind(t *testing.T) {
	d, _ := newTestDesktop(t)
	res := d.SaveConnector(ConnectorSaveRequest{
		ID:           "team-chat",
		Kind:         "jira",
		DisplayName:  "Jira（V1 不提供的平台）",
		Enabled:      true,
		Capabilities: []string{"chat.send"},
	})
	if res.Error == nil {
		t.Fatal("未实现的 kind 必须被拒绝")
	}
	if res.Error.Code != contracts.CodeUnsupported {
		t.Fatalf("期望 UNSUPPORTED，实际 %+v", res.Error)
	}
	// 拒绝之后不得留下半条定义。
	list := d.ListConnectors()
	for _, item := range list.Items {
		if item.ID == "team-chat" {
			t.Fatalf("被拒的 kind 不应落库: %+v", item)
		}
	}
}

// readConnectorDB 读回连接器库（含 WAL）的字节。
func readConnectorDB(t *testing.T, root string) string {
	t.Helper()
	var out strings.Builder
	for _, name := range []string{"connectors.db", "connectors.db-wal", "connectors.db-shm"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		out.Write(data)
	}
	if out.Len() == 0 {
		t.Fatal("连接器库文件不存在")
	}
	return out.String()
}
