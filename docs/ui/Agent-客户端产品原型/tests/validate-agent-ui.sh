#!/usr/bin/env bash
set -euo pipefail

fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }

pages=(index chat agents skills mcp connectors schedules library settings)
for page in "${pages[@]}"; do
  file="${page}.html"
  [[ -f "$file" ]] || fail "缺少页面 $file"
  rg -q '<html lang="zh-CN">' "$file" || fail "$file 未声明中文语言"
  rg -q 'assets/studio\.js' "$file" || fail "$file 未加载共享交互"
  rg -q 'data-od-id=' "$file" || fail "$file 缺少可检查区域标识"
  ! rg -q 'https?://[^" ]+\.(png|jpe?g|webp|gif|svg)' "$file" || fail "$file 含远程图片依赖"
  ! rg -q 'lorem ipsum|placeholder text|sample content' "$file" -i || fail "$file 含未完成占位内容"
done

rg -q 'data-od-id="activity-heatmap"' index.html || fail '工作台缺少年度工作活跃方块图'
rg -q 'id="activity-grid"' index.html || fail '工作活跃方块图缺少网格容器'
rg -q 'id="activity-range"' index.html || fail '工作活跃方块图缺少时间范围切换'
rg -q 'id="activity-tooltip"' index.html || fail '工作活跃方块图缺少日期提示'
rg -q 'generateActivityHeatmap' index.html || fail '工作活跃方块图缺少数据生成逻辑'
rg -q 'data-activity-level' index.html || fail '工作活跃方块图缺少强度等级'
rg -q 'activity-heatmap' assets/studio.css || fail '工作活跃方块图缺少共享样式'

settings=settings.html
for target in general models runtime observability security; do
  rg -q "data-settings-target=\"$target\"" "$settings" || fail "设置导航缺少 $target"
  rg -q "data-settings-pane=\"$target\"" "$settings" || fail "设置内容缺少 $target"
done

rg -q 'data-permission-mode=' "$settings" || fail '设置页缺少权限模式交互'
rg -q 'id="settings-search"' "$settings" || fail '设置页缺少分区搜索'
rg -q 'id="model-search"' "$settings" || fail '模型管理缺少搜索'
rg -q 'id="trace-table"' "$settings" || fail '观测页缺少链路表格'
rg -q 'id="execution-table"' "$settings" || fail '观测页缺少执行记录'
rg -Fq "option.addEventListener('click', function (event)" "$settings" || fail '权限模式未隔离共享点击逻辑'
rg -Fq 'event.stopPropagation()' "$settings" || fail '权限模式点击事件会被共享复选逻辑二次处理'

rg -q 'class="sidebar-history"' assets/studio.js || fail '共享侧栏缺少历史会话区'
rg -q 'nav-compact' assets/studio.js || fail '共享侧栏缺少缩略菜单'
rg -q 'class="nav-flyout"' assets/studio.js || fail '缩略菜单缺少悬浮子菜单'
! rg -q 'Team ·|席位|邀请成员' assets/studio.js settings.html || fail '本地个人客户端仍包含团队成员文案'

rg -q 'id="thil-dialog"' chat.html || fail '对话页缺少 THIL 内容确认弹窗'
rg -q 'data-thil-choice=' chat.html || fail 'THIL 弹窗缺少方案选择交互'
rg -q 'data-dialog-open="thil-dialog"' chat.html || fail 'THIL 弹窗缺少打开入口'
rg -q 'data-od-id="inline-thil-card"' chat.html || fail '对话线程缺少 THIL 内容确认卡片'
rg -q 'data-open-rail="artifacts"' chat.html || fail '对话轮次缺少输出物快捷入口'
rg -q 'data-od-id="recovery-card"' chat.html || fail '对话页缺少异常恢复状态示例'
rg -q 'id="project-picker"' chat.html || fail '输入区缺少项目选择弹窗'
rg -q 'id="project-trigger"' chat.html || fail '输入区缺少附件入口'
rg -q 'id="attachment-drawer"' chat.html || fail '输入区加号缺少轻量附件菜单'
rg -q 'role="menu"[^>]*aria-label="添加上下文"[^>]*tabindex="-1"' chat.html || fail '附件菜单初始焦点会误展开第一个子菜单'
rg -Fq 'attachmentDrawer.focus()' chat.html || fail '附件菜单打开后未聚焦菜单容器'
rg -q 'attachment-submenu.*bottom: -4px' assets/pages.css || fail '附件子菜单未采用底部对齐，可能超出视口'
rg -q 'data-attachment-action="files"' chat.html || fail '附件菜单第一项不是文件或目录'
rg -q 'id="attachment-file-input"[^>]*multiple' chat.html || fail '文件子菜单缺少多文件选择'
rg -q 'id="attachment-directory-input"[^>]*webkitdirectory' chat.html || fail '文件子菜单缺少目录选择'
rg -q 'data-attachment-action="skills"' chat.html || fail '附件菜单第二项不是 Skill'
rg -q 'class="attachment-submenu"' chat.html || fail '附件菜单缺少悬浮子菜单'
rg -q 'data-skill-name=' chat.html || fail 'Skill 子菜单缺少已安装 Skill'
rg -q 'id="current-project-trigger"' chat.html || fail '输入框上方缺少当前项目按钮'
rg -q 'id="current-project-menu"' chat.html || fail '当前项目按钮缺少管理菜单'
rg -q 'data-project-action="change"' chat.html || fail '当前项目菜单缺少更换项目目录'
rg -q 'data-project-action="detach"' chat.html || fail '当前项目菜单缺少取消项目'
rg -q 'aria-controls="attachment-drawer"' chat.html || fail '附件入口未关联菜单状态'
rg -q 'detachCurrentProject' chat.html || fail '取消项目缺少状态处理'
rg -q 'attachment-drawer' assets/pages.css || fail '轻量附件菜单缺少样式'
rg -q 'id="rail-scrim"' chat.html || fail '小屏输出物缺少抽屉遮罩'
rg -q 'data-artifact-turn=' chat.html || fail '输出物缺少轮次关联'
rg -q 'id="artifact-return"' chat.html || fail '输出物面板缺少返回原始轮次入口'
rg -q 'mobile-chat-head' assets/pages.css || fail '窄屏聊天头部未做信息降噪'
rg -Fq "savedRail === 'open' && !window.matchMedia('(max-width: 900px)').matches" chat.html || fail '移动端不应继承桌面右栏打开状态'
rg -q 'mobile-rail-drawer' assets/studio.css || fail '小屏输出物未实现抽屉布局'

rg -q 'loadHistoryState' assets/studio.js || fail '历史会话缺少持久化状态读取'
rg -Fq 'saveHistoryState(fallback)' assets/studio.js || fail '默认历史状态首次加载后未持久化'
rg -q 'saveHistoryState' assets/studio.js || fail '历史会话缺少持久化状态保存'
rg -q 'currentProject = state.projects.find' chat.html || fail '刷新后未从历史状态恢复当前会话项目'
rg -q 'assignSessionToProject' assets/studio.js chat.html || fail '项目选择未同步会话归属'
rg -q 'renderPersonalPage' assets/studio.js || fail '普通会话未实现真实增量 DOM 分页'
rg -q 'data-history-action' assets/studio.js || fail '历史会话缺少置顶、重命名、归档与删除操作'

rg -q 'history-search' assets/studio.js || fail '历史会话缺少搜索'
rg -q 'history-project' assets/studio.js || fail '历史会话缺少项目分组'
rg -q 'data-project-toggle' assets/studio.js || fail '项目会话缺少展开交互'
rg -q 'history-sentinel' assets/studio.js || fail '普通会话缺少滚动分页哨兵'
rg -q 'IntersectionObserver' assets/studio.js || fail '历史会话未实现滚动分页加载'
rg -q 'anvil.locale' assets/studio.js settings.html || fail '缺少持久化国际化语言偏好'
rg -q 'id="interface-language"' settings.html || fail '设置页缺少界面语言选择'
rg -q 'addTranslations' assets/studio.js chat.html || fail '国际化缺少页面级词典注册'
rg -q 'data-scenario-route' assets/studio.js || fail '历史会话缺少独立场景路由'
for scenario in tool-failure model-fallback connector-expired stopped context-limit; do
  rg -q "data-scenario-template=\"$scenario\"" chat.html || fail "对话页缺少 $scenario 场景"
done
rg -q 'id="export-dialog"' chat.html || fail '本地客户端缺少会话导出弹窗'
rg -q 'cloud-share-disclosure' chat.html || fail '云端分享缺少上传披露'

rg -q 'lastDialogOpener' assets/studio.js || fail '弹窗关闭后未恢复焦点'
rg -Fq 'openDialog: openDialog' assets/studio.js || fail '页面组件无法指定可见的弹窗焦点恢复入口'
rg -Fq 'closeDialog: closeDialog' assets/studio.js || fail '页面组件无法通过共享能力关闭弹窗并恢复焦点'
rg -q 'trapDialogFocus' assets/studio.js || fail '弹窗缺少键盘焦点约束'
rg -q 'aria-live="polite"' chat.html assets/studio.js || fail '动态状态缺少无障碍播报'

rg -q 'id="skill-url"' skills.html || fail 'Skill 缺少链接导入'
rg -q 'data-install-step=' skills.html || fail 'Skill 链接导入缺少 Agent 安装进度'
rg -q 'data-skill-step=' skills.html || fail 'Skill 创建缺少渐进式步骤'
rg -q 'id="skill-ai-input"' skills.html || fail 'Skill 创建缺少 AI 对话输入'

rg -q 'id="agent-runtime"' agents.html || fail 'Agent 编辑缺少运行时绑定'
rg -q 'id="agent-model"' agents.html || fail 'Agent 编辑缺少运行时对应模型'
rg -q 'id="new-agent-runtime"' agents.html || fail '新建 Agent 缺少运行时选择'
rg -q 'data-runtime-models=' agents.html || fail 'Agent 缺少运行时模型映射'

rg -q 'data-runtime-card="pi-agent"' settings.html || fail '设置缺少 Pi Agent 运行时'
rg -q 'data-runtime-card="codex-cli"' settings.html || fail '设置缺少 Codex CLI 运行时'
! rg -q 'data-settings-target="members"|data-settings-pane="members"|id="invite-dialog"' settings.html || fail '设置仍包含成员与团队分区'

printf 'PASS: %s 个产品页面结构完整，核心本地 Agent 工作流齐全\n' "${#pages[@]}"
