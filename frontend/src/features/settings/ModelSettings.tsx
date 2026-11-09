// 模型设置（C02 + MP1）。
//
// 这里是**唯一**的模型维护入口：新建/编辑/删除/测试连通/设为当前，
// 加上本地扫描导入与供应商管理。之前的实现把"定义模型"推给资源库，
// 而资源库在 F3 之后只剩技能/Agent/MCP —— 用户找不到入口，这是真实缺陷。
//
// 密钥不进这一层：模型定义里只有 credentialRef，凭证明文只存在 OS 凭证库。

import { ModelsMaintenance } from "../models/ModelsMaintenance";
import { ModelDiagnosis } from "./ModelDiagnosis";
import { ModelProvenance } from "./ModelProvenance";
import { SettingsSection } from "./Section";

export function ModelSettings() {
  return (
    <SettingsSection id="model" titleKey="settings.model.title" hintKey="settings.model.hint">
      {/* M1ENV：来源必须显示在列表之前——先回答"现在用的是谁给的模型"，再谈维护。 */}
      <ModelProvenance />
      {/* MDIAG："保存了却起不来"的第一步应该是"点一下，看它说为什么"。 */}
      <ModelDiagnosis />
      <ModelsMaintenance />
    </SettingsSection>
  );
}
