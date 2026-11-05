/* Anvil · shared shell + interaction primitives */
(function () {
  'use strict';

  const ICONS = {
    grid: '<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>',
    message: '<path d="M21 11.5a8.4 8.4 0 0 1-9 8.4 9.3 9.3 0 0 1-3.9-.8L3 21l1.9-4.9A8.4 8.4 0 0 1 12 3.1a8.4 8.4 0 0 1 9 8.4z"/>',
    folder: '<path d="M3.5 7.5a2 2 0 0 1 2-2h3.2l2 2.4h7.8a2 2 0 0 1 2 2v8.6a2 2 0 0 1-2 2H5.5a2 2 0 0 1-2-2z"/>',
    bot: '<rect x="4" y="8" width="16" height="12" rx="3"/><path d="M12 4v4M9 14h.01M15 14h.01M9.5 17.5h5"/>',
    puzzle: '<path d="M10 4.5h2.2a1.4 1.4 0 0 1 1.4 1.4c0 .8.7 1.4 1.5 1.4s1.5-.6 1.5-1.5V4.5H20v4.2a1.4 1.4 0 0 1-2.8 0c0-.8-.6-1.4-1.4-1.4s-1.4.6-1.4 1.4V12h4.4v5.8h-3.9a1.4 1.4 0 0 0-1.4 1.4V21H10v-2.4a1.4 1.4 0 0 1 2.8 0"/><path d="M10 4.5H5.8A1.8 1.8 0 0 0 4 6.3v3.9h2.4a1.4 1.4 0 0 1 1.4 1.4 1.4 1.4 0 0 1-1.4 1.4H4v4.2a1.8 1.8 0 0 0 1.8 1.8H10"/>',
    plug: '<path d="M9 3v5M15 3v5M6.5 8h11v3.2a5.5 5.5 0 0 1-11 0z"/><path d="M12 16.7V21"/>',
    link: '<path d="M10.5 13.5a3.5 3.5 0 0 0 5 0l3-3a3.5 3.5 0 0 0-5-5l-1.2 1.2"/><path d="M13.5 10.5a3.5 3.5 0 0 0-5 0l-3 3a3.5 3.5 0 0 0 5 5l1.2-1.2"/>',
    clock: '<circle cx="12" cy="12" r="8.5"/><path d="M12 7.5V12l3 2"/>',
    settings: '<circle cx="12" cy="12" r="3"/><path d="M19.4 14a1.7 1.7 0 0 0 .3 1.9l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-2.9 1.2v.2a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-2.9-1.2l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1A1.7 1.7 0 0 0 3.2 13H3a2 2 0 1 1 0-4h.2a1.7 1.7 0 0 0 1.2-2.9l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1A1.7 1.7 0 0 0 10 3.2V3a2 2 0 1 1 4 0v.2a1.7 1.7 0 0 0 2.9 1.2l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0 1.2 2.9h.2a2 2 0 1 1 0 4h-.2a1.7 1.7 0 0 0-1.5.9z"/>',
    plus: '<path d="M12 5v14M5 12h14"/>',
    search: '<circle cx="11" cy="11" r="6.5"/><path d="M16 16l4.5 4.5"/>',
    chevron: '<path d="M9 6l6 6-6 6"/>',
    chevDown: '<path d="M6 9l6 6 6-6"/>',
    check: '<path d="M4.5 12.5l5 5 10-10"/>',
    x: '<path d="M6 6l12 12M18 6L6 18"/>',
    alert: '<path d="M12 4.5l8.5 15h-17z"/><path d="M12 10v4M12 17h.01"/>',
    shield: '<path d="M12 3.5l7.5 3v5.2c0 4.3-3 7.6-7.5 8.8-4.5-1.2-7.5-4.5-7.5-8.8V6.5z"/>',
    brain: '<path d="M9.5 4.5A3 3 0 0 0 6.5 7.5a2.7 2.7 0 0 0-1.8 4.4A3 3 0 0 0 6 17.3a3 3 0 0 0 5.5 1.2V5.6a2.5 2.5 0 0 0-2-1.1z"/><path d="M14.5 4.5A3 3 0 0 1 17.5 7.5a2.7 2.7 0 0 1 1.8 4.4A3 3 0 0 1 18 17.3a3 3 0 0 1-5.5 1.2"/>',
    globe: '<circle cx="12" cy="12" r="8.5"/><path d="M3.5 12h17M12 3.5c2.4 2.3 3.7 5.3 3.7 8.5S14.4 18.2 12 20.5c-2.4-2.3-3.7-5.3-3.7-8.5S9.6 5.8 12 3.5z"/>',
    file: '<path d="M13.5 3.5H7a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V9z"/><path d="M13.5 3.5V9H19"/>',
    code: '<path d="M8.5 8.5L4.5 12l4 3.5M15.5 8.5l4 3.5-4 3.5M13.5 5l-3 14"/>',
    terminal: '<rect x="3.5" y="4.5" width="17" height="15" rx="2"/><path d="M7.5 10l2.5 2.2-2.5 2.2M12.5 14.5H16"/>',
    mail: '<rect x="3" y="5" width="18" height="14" rx="2"/><path d="M3.5 6.5l8.5 6 8.5-6"/>',
    refresh: '<path d="M20 11.5a8 8 0 1 0-2.4 5.6"/><path d="M20 5v6h-6"/>',
    more: '<circle cx="5.5" cy="12" r="1.2"/><circle cx="12" cy="12" r="1.2"/><circle cx="18.5" cy="12" r="1.2"/>',
    upload: '<path d="M12 16V4.5M7.5 9L12 4.5 16.5 9"/><path d="M4.5 16v3a1.5 1.5 0 0 0 1.5 1.5h12A1.5 1.5 0 0 0 19.5 19v-3"/>',
    download: '<path d="M12 4.5V16M7.5 11.5L12 16l4.5-4.5"/><path d="M4.5 16v3A1.5 1.5 0 0 0 6 20.5h12A1.5 1.5 0 0 0 19.5 19v-3"/>',
    play: '<path d="M8 5.5l10 6.5-10 6.5z"/>',
    pause: '<path d="M9 5.5v13M15 5.5v13"/>',
    trash: '<path d="M4.5 7h15M9.5 7V4.5h5V7M6.5 7l.8 12a1.7 1.7 0 0 0 1.7 1.6h6a1.7 1.7 0 0 0 1.7-1.6l.8-12"/>',
    external: '<path d="M14 4.5h5.5V10M19 5l-8 8"/><path d="M18.5 14v5a1.5 1.5 0 0 1-1.5 1.5H5A1.5 1.5 0 0 1 3.5 19V7A1.5 1.5 0 0 1 5 5.5h5"/>',
    copy: '<rect x="8.5" y="8.5" width="11" height="11" rx="2"/><path d="M15.5 8.5v-2a2 2 0 0 0-2-2h-8a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2"/>',
    filter: '<path d="M4 6.5h16M7 12h10M10 17.5h4"/>',
    branch: '<circle cx="7" cy="6" r="2.2"/><circle cx="7" cy="18" r="2.2"/><circle cx="17" cy="9" r="2.2"/><path d="M7 8.2v7.6M17 11.2c0 3-2.5 4.3-5 4.8"/>',
    layers: '<path d="M12 3.5l8 4-8 4-8-4z"/><path d="M4 12l8 4 8-4M4 16.5l8 4 8-4"/>',
    sparkle: '<path d="M12 3.5l1.8 5.2 5.2 1.8-5.2 1.8L12 17.5l-1.8-5.2L5 10.5l5.2-1.8z"/><path d="M18.5 16.5l.8 2 2 .8-2 .8-.8 2-.8-2-2-.8 2-.8z"/>',
    arrowLeft: '<path d="M19 12H5M11 6l-6 6 6 6"/>',
    panel: '<rect x="3.5" y="4.5" width="17" height="15" rx="2"/><path d="M15 4.5v15"/>',
    archive: '<rect x="3.5" y="4.5" width="17" height="4" rx="1"/><path d="M5 8.5v10a1.5 1.5 0 0 0 1.5 1.5h11A1.5 1.5 0 0 0 19 18.5v-10M10 12.5h4"/>',
    send: '<path d="M20 4L3.8 10.4l6.4 2.4 2.4 6.4z"/>',
    paperclip: '<path d="M20 11.5l-8.2 8.2a5 5 0 0 1-7.1-7.1L13 4.3a3.4 3.4 0 0 1 4.8 4.8l-8.2 8.2a1.7 1.7 0 0 1-2.4-2.4l7.6-7.6"/>',
    pin: '<path d="M8 4.5h8l-1.2 5 2.7 2.7v1.3h-11v-1.3l2.7-2.7zM12 13.5v7"/>',
    user: '<circle cx="12" cy="8.5" r="3.5"/><path d="M5 20c.8-3.4 3.6-5.2 7-5.2s6.2 1.8 7 5.2"/>',
    database: '<ellipse cx="12" cy="6" rx="7.5" ry="3"/><path d="M4.5 6v12c0 1.7 3.4 3 7.5 3s7.5-1.3 7.5-3V6M4.5 12c0 1.7 3.4 3 7.5 3s7.5-1.3 7.5-3"/>',
    key: '<circle cx="8" cy="14" r="3.5"/><path d="M10.5 11.5L20 2M16.5 5.5l3 3M14 8l2.5 2.5"/>',
    eye: '<path d="M2.5 12S6 6 12 6s9.5 6 9.5 6-3.5 6-9.5 6-9.5-6-9.5-6z"/><circle cx="12" cy="12" r="2.6"/>',
    zap: '<path d="M13.5 3L5 13.5h6L10.5 21 19 10.5h-6z"/>'
  };

  const I18N = {
    'zh-CN': {
      start: '开始', dashboard: '工作台', chat: '对话', agents: 'Agent', tools: '工具与自动化',
      library: '资源库', skills: 'Skill', mcp: 'MCP', connectors: '连接器', schedules: '定时任务', settings: '设置',
      history: '历史会话', searchHistory: '搜索会话或项目…', projects: '项目会话', personal: '普通会话', share: '分享',
      clarifications: '1 项待澄清', pendingAuth: '1 项待授权', browser: '浏览器', artifacts: '输出物 3', sources: '引用 4', execution: '执行',
      composerPlaceholder: '继续追问，或 @ 引用文件与 Agent…',
      page: '页面', automation: '自动化', action: '操作', quickSearch: '搜索页面、Agent、任务…', noMatches: '没有匹配的页面或操作',
      newChat: '新建对话', newSchedule: '新建定时任务', addMcp: '添加 MCP 服务', manageRuntime: '管理本地运行时', tokenAudit: 'Token 用量审计',
      loadMore: '继续滚动加载', loadedAll: '已加载全部会话', noHistory: '没有匹配的会话', localClient: '本地智能体客户端 · v0.9',
      localWorkspace: '个人 · 本地工作区', running: '本机运行中 · 4 个任务'
    },
    'en-US': {
      start: 'Start', dashboard: 'Dashboard', chat: 'Chats', agents: 'Agents', tools: 'Tools & Automation',
      library: 'Library', skills: 'Skills', mcp: 'MCP', connectors: 'Connectors', schedules: 'Schedules', settings: 'Settings',
      history: 'History', searchHistory: 'Search chats or projects…', projects: 'Project chats', personal: 'Personal chats', share: 'Share',
      clarifications: '1 clarification', pendingAuth: '1 approval', browser: 'Browser', artifacts: 'Artifacts 3', sources: 'Sources 4', execution: 'Run',
      composerPlaceholder: 'Ask a follow-up, or @ mention files and agents…',
      page: 'Page', automation: 'Automation', action: 'Action', quickSearch: 'Search pages, agents, and tasks…', noMatches: 'No matching pages or actions',
      newChat: 'New chat', newSchedule: 'New schedule', addMcp: 'Add MCP server', manageRuntime: 'Manage local runtimes', tokenAudit: 'Token usage audit',
      loadMore: 'Scroll to load more', loadedAll: 'All chats loaded', noHistory: 'No matching chats', localClient: 'Local agent client · v0.9',
      localWorkspace: 'Personal · Local workspace', running: 'Running locally · 4 tasks'
    }
  };

  function getLocale() {
    const saved = localStorage.getItem('anvil.locale');
    return I18N[saved] ? saved : 'zh-CN';
  }

  function t(key) { return (I18N[getLocale()] || I18N['zh-CN'])[key] || key; }

  const PRIMARY_NAV = [
    { page: 'index', href: 'index.html', icon: 'grid', textKey: 'dashboard' },
    { page: 'chat', href: 'chat.html', icon: 'message', textKey: 'chat', count: '4' },
    { page: 'agents', href: 'agents.html', icon: 'bot', textKey: 'agents' }
  ];

  const COMPACT_NAV = [
    { page: 'library', href: 'library.html', icon: 'folder', textKey: 'library', meta: '28 个输出物' },
    { page: 'skills', href: 'skills.html', icon: 'puzzle', textKey: 'skills', meta: '9 个已安装' },
    { page: 'mcp', href: 'mcp.html', icon: 'plug', textKey: 'mcp', meta: '3 个服务' },
    { page: 'connectors', href: 'connectors.html', icon: 'link', textKey: 'connectors', meta: '5 个连接' },
    { page: 'schedules', href: 'schedules.html', icon: 'clock', textKey: 'schedules', meta: '6 个任务' },
    { page: 'settings', href: 'settings.html', icon: 'settings', textKey: 'settings', meta: '模型与运行时' }
  ];

  const DEFAULT_PROJECT_HISTORY = [
    { id: 'project-agent-ui', name: 'my-agent-go-ui', path: '~/work/my-agent-go-ui', open: true, sessions: [
      { id: 'session-api-migration', title: 'API 迁移评估：v1 → v2', meta: '迁移工程师 · 刚刚', state: 'pending', scenario: 'main' },
      { id: 'session-runtime-health', title: 'CLI 运行时健康检查', meta: '系统工程师 · 2 小时前', scenario: 'tool-failure' },
      { id: 'session-responsive', title: '前端响应式回归', meta: '设计工程师 · 昨天', scenario: 'model-fallback' }
    ]},
    { id: 'project-notes', name: '个人知识库', path: '~/Documents/notes', open: false, sessions: [
      { id: 'session-reading-notes', title: '整理本周阅读笔记', meta: '文档助手 · 周一', scenario: 'context-limit' },
      { id: 'session-topic-index', title: '合并重复主题索引', meta: '知识管家 · 10 月 28 日', scenario: 'connector-expired' }
    ]}
  ];

  const DEFAULT_PERSONAL_HISTORY = [
    { id: 'session-weekly-report', title: '周报数据核对与图表生成', meta: '数据分析师 · 1 小时前', scenario: 'complete', pinned: true },
    { id: 'session-pricing', title: '竞品定价页差异整理', meta: '研究员 · 昨天', scenario: 'stopped' },
    { id: 'session-feishu', title: '飞书文档结构整理', meta: '文档助手 · 周一', scenario: 'connector-expired' },
    { id: 'session-pnpm', title: '依赖升级：pnpm 9 → 10', meta: '迁移工程师 · 11 月 2 日', scenario: 'tool-failure' },
    { id: 'session-meeting', title: '会议录音提取待办', meta: '文档助手 · 10 月 31 日', scenario: 'context-limit' },
    { id: 'session-invoices', title: '发票文件批量重命名', meta: '文件助手 · 10 月 29 日', scenario: 'complete' },
    { id: 'session-travel', title: '旅行路线对比', meta: '研究员 · 10 月 25 日', scenario: 'model-fallback' },
    { id: 'session-usage', title: '月度用量异常说明', meta: '数据分析师 · 10 月 20 日', scenario: 'complete' },
    { id: 'session-bookmarks', title: '整理浏览器书签', meta: '文档助手 · 10 月 18 日', scenario: 'stopped' },
    { id: 'session-database', title: '数据库字段释义', meta: '数据分析师 · 10 月 15 日', scenario: 'complete' }
  ];

  function cloneDefaultHistory() {
    return { projects: JSON.parse(JSON.stringify(DEFAULT_PROJECT_HISTORY)), personal: JSON.parse(JSON.stringify(DEFAULT_PERSONAL_HISTORY)) };
  }

  function loadHistoryState() {
    try {
      const saved = JSON.parse(localStorage.getItem('anvil.history.v2') || 'null');
      if (saved && Array.isArray(saved.projects) && Array.isArray(saved.personal)) return saved;
    } catch (error) {}
    const fallback = cloneDefaultHistory();
    saveHistoryState(fallback);
    return fallback;
  }

  function saveHistoryState(state) {
    localStorage.setItem('anvil.history.v2', JSON.stringify(state));
  }

  function sessionHref(item) {
    return 'chat.html?scenario=' + encodeURIComponent(item.scenario || 'main') + '&session=' + encodeURIComponent(item.id || '');
  }

  function escapeText(value) {
    return String(value || '').replace(/[&<>"']/g, function (char) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[char];
    });
  }

  function personalRowHtml(item) {
    return '<div class="history-row' + (item.pinned ? ' is-pinned' : '') + '" data-session-id="' + escapeText(item.id) + '">' +
      '<a class="history-item history-item-personal" href="' + sessionHref(item) + '" data-scenario-route="' + escapeText(item.scenario || 'main') + '" data-history-search="' + escapeText((item.title + ' ' + item.meta).toLowerCase()) + '">' +
        '<span class="history-chat-icon">' + svg(item.pinned ? 'pin' : 'message', 11) + '</span><span class="history-copy"><span class="history-title">' + escapeText(item.title) + '</span>' +
        '<span class="history-meta">' + escapeText(item.meta) + '</span></span>' +
      '</a><button class="history-actions-trigger" type="button" data-history-action="menu" aria-label="会话操作">' + svg('more', 11) + '</button></div>';
  }

  function renderPersonalPage(host, state, start, count) {
    const list = host.querySelector('.sidebar-history-list');
    if (!list) return start;
    const ordered = state.personal.slice().sort(function (a, b) { return Number(Boolean(b.pinned)) - Number(Boolean(a.pinned)); });
    const page = ordered.slice(start, start + count);
    list.insertAdjacentHTML('beforeend', page.map(personalRowHtml).join(''));
    return start + page.length;
  }

  function assignSessionToProject(project, session) {
    const state = loadHistoryState();
    const targetName = project.name || '未命名项目';
    let target = state.projects.find(function (item) { return item.path === project.path || item.name === targetName; });
    if (!target) {
      target = { id: 'project-' + Date.now(), name: targetName, path: project.path, open: true, sessions: [] };
      state.projects.unshift(target);
    }
    const sessionId = (session && session.id) || 'session-api-migration';
    state.projects.forEach(function (item) { item.sessions = item.sessions.filter(function (entry) { return entry.id !== sessionId; }); });
    state.personal = state.personal.filter(function (entry) { return entry.id !== sessionId; });
    target.sessions.unshift(session || { id: sessionId, title: 'API 迁移评估：v1 → v2', meta: '迁移工程师 · 刚刚', scenario: 'main' });
    target.open = true;
    saveHistoryState(state);
    renderSidebar();
    return target;
  }

  function svg(name, size) {
    const body = ICONS[name] || ICONS.file;
    return '<svg width="' + (size || 16) + '" height="' + (size || 16) + '" viewBox="0 0 24 24" fill="none" ' +
      'stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
      body + '</svg>';
  }

  function renderSidebar() {
    const host = document.getElementById('sidebar');
    if (!host) return;
    const active = document.body.dataset.page || '';
    const state = loadHistoryState();
    const projectHtml = state.projects.map(function (project, projectIndex) {
      return '<div class="history-project' + (project.open ? ' is-open' : '') + '" data-history-search="' + project.name.toLowerCase() + ' ' + project.path.toLowerCase() + '">' +
        '<button class="history-project-head" type="button" data-project-toggle aria-expanded="' + String(project.open) + '">' +
          '<span class="history-project-icon">' + svg('folder', 12) + '</span>' +
          '<span><b>' + project.name + '</b><small>' + project.sessions.length + ' 个会话 · ' + project.path + '</small></span>' +
          '<span class="history-project-caret">' + svg('chevron', 10) + '</span>' +
        '</button>' +
        '<div class="history-project-sessions">' + project.sessions.map(function (item, sessionIndex) {
          return '<div class="history-row" data-session-id="' + escapeText(item.id) + '" data-project-id="' + escapeText(project.id) + '">' +
            '<a class="history-item history-item-project' + (active === 'chat' && projectIndex === 0 && sessionIndex === 0 ? ' is-active' : '') + '" href="' + sessionHref(item) + '" data-scenario-route="' + escapeText(item.scenario || 'main') + '" data-history-search="' + escapeText((item.title + ' ' + item.meta).toLowerCase()) + '">' +
              '<span class="history-branch">' + svg('branch', 10) + '</span><span class="history-copy"><span class="history-title">' + escapeText(item.title) + '</span>' +
              '<span class="history-meta">' + (item.state === 'pending' ? '<i></i>' : '') + escapeText(item.meta) + '</span></span>' +
            '</a><button class="history-actions-trigger" type="button" data-history-action="menu" aria-label="会话操作">' + svg('more', 11) + '</button></div>';
        }).join('') + '</div>' +
      '</div>';
    }).join('');
    const personalHtml = '';

    let html = '' +
      '<a class="brand" href="index.html" aria-label="Anvil">' +
        '<span class="brand-mark">' + svg('layers', 15) + '</span>' +
        '<span class="brand-copy"><span class="brand-name">Anvil</span><br>' +
        '<span class="brand-sub">' + t('localClient') + '</span></span>' +
      '</a>' +
      '<div class="nav-group"><div class="nav-label">' + t('start') + '</div>';
    PRIMARY_NAV.forEach(function (item) {
      html += '<a class="nav-item' + (item.page === active ? ' is-active' : '') + '" href="' + item.href + '"' +
        (item.page === active ? ' aria-current="page"' : '') + '>' +
        svg(item.icon, 15) + '<span>' + t(item.textKey) + '</span>' +
        (item.count ? '<span class="nav-count">' + item.count + '</span>' : '') +
      '</a>';
    });
    const compactActive = COMPACT_NAV.some(function (item) { return item.page === active; });
    html += '</div>' +
      '<div class="nav-compact' + (compactActive ? ' is-active' : '') + '">' +
        '<button class="nav-item nav-compact-trigger" type="button" aria-haspopup="true">' +
          svg('layers', 15) + '<span>' + t('tools') + '</span>' + svg('chevron', 12) +
        '</button>' +
        '<div class="nav-flyout" role="menu" aria-label="' + t('tools') + '">' +
          '<div class="nav-flyout-head">' + t('tools') + '</div>' +
          COMPACT_NAV.map(function (item) {
            return '<a class="nav-flyout-item' + (item.page === active ? ' is-active' : '') + '" role="menuitem" href="' + item.href + '">' +
              '<span class="nav-flyout-icon">' + svg(item.icon, 14) + '</span>' +
              '<span><b>' + t(item.textKey) + '</b><small>' + item.meta + '</small></span>' +
            '</a>';
          }).join('') +
        '</div>' +
      '</div>' +
      '<section class="sidebar-history" aria-label="' + t('history') + '">' +
        '<div class="sidebar-history-head"><span>' + t('history') + '</span><a href="chat.html" aria-label="新建对话">' + svg('plus', 12) + '</a></div>' +
        '<label class="history-search"><span>' + svg('search', 11) + '</span><input id="history-search" type="search" placeholder="' + t('searchHistory') + '" aria-label="' + t('searchHistory') + '"></label>' +
        '<div class="sidebar-history-scroll">' +
          '<div class="history-section-label">' + t('projects') + '</div>' + projectHtml +
          '<div class="history-section-label history-personal-label">' + t('personal') + '</div>' +
          '<div class="sidebar-history-list">' + personalHtml + '</div>' +
          '<div class="history-sentinel" id="history-sentinel"><span class="history-sentinel-dot"></span><span>' + t('loadMore') + '</span></div>' +
          '<div class="history-empty" hidden>' + t('noHistory') + '</div>' +
        '</div>' +
        '<div class="history-action-menu" hidden role="menu">' +
          '<button type="button" data-history-action="pin" role="menuitem">' + svg('pin', 11) + '<span>置顶 / 取消置顶</span></button>' +
          '<button type="button" data-history-action="rename" role="menuitem">' + svg('file', 11) + '<span>重命名</span></button>' +
          '<button type="button" data-history-action="archive" role="menuitem">' + svg('archive', 11) + '<span>归档</span></button>' +
          '<button type="button" data-history-action="delete" role="menuitem" class="is-danger">' + svg('trash', 11) + '<span>删除</span></button>' +
        '</div>' +
      '</section>' +
      '<div class="sidebar-foot">' +
        '<a class="runtime-mini" href="settings.html#runtime">' +
          '<span class="runtime-mini-icon">π</span>' +
          '<span><b>Pi Agent</b><small><i></i>' + t('running') + '</small></span>' +
          svg('chevron', 11) +
        '</a>' +
        '<a class="user-chip" href="settings.html">' +
          '<span class="avatar">陈</span>' +
          '<span class="user-chip-copy"><span class="user-chip-name">陈羽</span><br>' +
          '<span class="user-chip-plan">' + t('localWorkspace') + '</span></span>' +
        '</a>' +
      '</div>';
    host.innerHTML = html;
    wireSidebarHistory(host, state);
  }

  function wireSidebarHistory(host, state) {
    const scroller = host.querySelector('.sidebar-history-scroll');
    const search = host.querySelector('#history-search');
    const sentinel = host.querySelector('#history-sentinel');
    const empty = host.querySelector('.history-empty');
    const menu = host.querySelector('.history-action-menu');
    let visibleCount = renderPersonalPage(host, state, 0, 4);
    let selectedRow = null;

    function updateSentinel() {
      const done = visibleCount >= state.personal.length;
      sentinel.classList.toggle('is-done', done);
      sentinel.querySelector('span:last-child').textContent = t(done ? 'loadedAll' : 'loadMore');
    }
    updateSentinel();

    host.querySelectorAll('[data-project-toggle]').forEach(function (button) {
      button.addEventListener('click', function () {
        const group = button.closest('.history-project');
        const index = Array.from(host.querySelectorAll('.history-project')).indexOf(group);
        const open = !group.classList.contains('is-open');
        group.classList.toggle('is-open', open);
        button.setAttribute('aria-expanded', String(open));
        if (state.projects[index]) state.projects[index].open = open;
        saveHistoryState(state);
      });
    });

    function loadNextPage() {
      if (search && search.value.trim()) return;
      visibleCount = renderPersonalPage(host, state, visibleCount, 3);
      updateSentinel();
    }

    if ('IntersectionObserver' in window && sentinel && scroller) {
      const observer = new IntersectionObserver(function (entries) {
        if (entries.some(function (entry) { return entry.isIntersecting; })) loadNextPage();
      }, { root: scroller, rootMargin: '12px' });
      observer.observe(sentinel);
    } else if (scroller) {
      scroller.addEventListener('scroll', function () {
        if (scroller.scrollTop + scroller.clientHeight >= scroller.scrollHeight - 16) loadNextPage();
      });
    }

    function renderSearch(query) {
      const list = host.querySelector('.sidebar-history-list');
      list.innerHTML = '';
      if (!query) {
        visibleCount = renderPersonalPage(host, state, 0, Math.max(4, visibleCount));
        sentinel.hidden = false;
        updateSentinel();
        return;
      }
      const matches = state.personal.filter(function (item) {
        return (item.title + ' ' + item.meta).toLowerCase().indexOf(query) >= 0;
      });
      list.innerHTML = matches.map(personalRowHtml).join('');
      sentinel.hidden = true;
    }

    if (search) search.addEventListener('input', function () {
      const query = search.value.trim().toLowerCase();
      let projectMatches = 0;
      host.querySelectorAll('.history-project').forEach(function (group) {
        const ownMatch = group.dataset.historySearch.indexOf(query) >= 0;
        let childMatch = false;
        group.querySelectorAll('.history-item').forEach(function (row) {
          const match = !query || ownMatch || row.dataset.historySearch.indexOf(query) >= 0;
          row.closest('.history-row').hidden = !match;
          childMatch = childMatch || match;
        });
        const match = !query || ownMatch || childMatch;
        group.hidden = !match;
        if (query && match) group.classList.add('is-open');
        if (match) projectMatches += 1;
      });
      renderSearch(query);
      const personalMatches = query ? state.personal.filter(function (item) { return (item.title + ' ' + item.meta).toLowerCase().indexOf(query) >= 0; }).length : visibleCount;
      empty.hidden = projectMatches + personalMatches !== 0;
    });

    function findSession(id) {
      let result = null;
      state.projects.some(function (project) {
        const session = project.sessions.find(function (item) { return item.id === id; });
        if (session) { result = { session: session, collection: project.sessions }; return true; }
        return false;
      });
      if (result) return result;
      const session = state.personal.find(function (item) { return item.id === id; });
      return session ? { session: session, collection: state.personal } : null;
    }

    host.addEventListener('click', function (event) {
      const trigger = event.target.closest('[data-history-action="menu"]');
      if (trigger) {
        event.preventDefault(); event.stopPropagation();
        selectedRow = trigger.closest('.history-row');
        menu.hidden = false;
        menu.style.top = Math.min(selectedRow.offsetTop + selectedRow.offsetHeight, host.clientHeight - 132) + 'px';
        menu.querySelector('button').focus();
        return;
      }
      const action = event.target.closest('[data-history-action]:not([data-history-action="menu"])');
      if (!action || !selectedRow) return;
      const found = findSession(selectedRow.dataset.sessionId);
      if (!found) return;
      if (action.dataset.historyAction === 'pin') found.session.pinned = !found.session.pinned;
      if (action.dataset.historyAction === 'rename') {
        const next = window.prompt('重命名会话', found.session.title);
        if (next && next.trim()) found.session.title = next.trim();
      }
      if (action.dataset.historyAction === 'archive' || action.dataset.historyAction === 'delete') {
        const index = found.collection.indexOf(found.session);
        if (index >= 0) found.collection.splice(index, 1);
      }
      saveHistoryState(state);
      menu.hidden = true;
      renderSidebar();
      toast(action.dataset.historyAction === 'delete' ? '会话已删除' : action.dataset.historyAction === 'archive' ? '会话已归档到资源库' : '会话已更新');
    });

    document.addEventListener('click', function (event) {
      if (menu && !menu.hidden && !event.target.closest('.history-action-menu') && !event.target.closest('[data-history-action="menu"]')) menu.hidden = true;
    });
  }

  function addTranslations(locale, values) {
    if (!I18N[locale]) I18N[locale] = {};
    Object.assign(I18N[locale], values || {});
    applyI18n();
  }

  function applyI18n(root) {
    document.documentElement.lang = getLocale();
    (root || document).querySelectorAll('[data-i18n]').forEach(function (el) {
      el.textContent = t(el.dataset.i18n);
    });
    (root || document).querySelectorAll('[data-i18n-placeholder]').forEach(function (el) {
      el.setAttribute('placeholder', t(el.dataset.i18nPlaceholder));
    });
    (root || document).querySelectorAll('[data-i18n-aria-label]').forEach(function (el) {
      el.setAttribute('aria-label', t(el.dataset.i18nAriaLabel));
    });
    (root || document).querySelectorAll('[data-i18n-title]').forEach(function (el) {
      el.setAttribute('title', t(el.dataset.i18nTitle));
    });
  }

  function setLocale(locale) {
    if (!I18N[locale]) return;
    localStorage.setItem('anvil.locale', locale);
    renderSidebar();
    applyI18n();
    document.dispatchEvent(new CustomEvent('anvil:localechange', { detail: { locale: locale } }));
  }

  let lastDialogOpener = null;

  function dialogFocusable(dialog) {
    return Array.from(dialog.querySelectorAll('button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])')).filter(function (el) {
      return !el.hidden && el.getAttribute('aria-hidden') !== 'true';
    });
  }

  function openDialog(dialog, opener) {
    if (!dialog) return;
    lastDialogOpener = opener || document.activeElement;
    dialog.hidden = false;
    const focusable = dialogFocusable(dialog);
    setTimeout(function () { (focusable[0] || dialog.querySelector('.dialog')).focus(); }, 0);
  }

  function closeDialog(dialog) {
    if (!dialog) return;
    dialog.hidden = true;
    if (lastDialogOpener && document.contains(lastDialogOpener)) lastDialogOpener.focus();
    lastDialogOpener = null;
  }

  function trapDialogFocus(event, dialog) {
    if (event.key !== 'Tab') return;
    const focusable = dialogFocusable(dialog);
    if (!focusable.length) { event.preventDefault(); return; }
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
  }

  /* ---------- toast ---------- */
  function toast(message, kind) {
    let host = document.querySelector('.toast-host');
    if (!host) {
      host = document.createElement('div');
      host.className = 'toast-host';
      host.setAttribute('aria-live', 'polite');
      host.setAttribute('aria-atomic', 'true');
      document.body.appendChild(host);
    }
    const el = document.createElement('div');
    el.className = 'toast';
    el.innerHTML = svg(kind === 'warn' ? 'alert' : 'check', 14) + '<span>' + message + '</span>';
    host.appendChild(el);
    setTimeout(function () {
      el.style.transition = 'opacity .2s ease, transform .2s ease';
      el.style.opacity = '0';
      el.style.transform = 'translateY(6px)';
      setTimeout(function () { el.remove(); }, 220);
    }, 2200);
  }

  /* ---------- command palette ---------- */
  function paletteItems() {
    const items = [];
    PRIMARY_NAV.concat(COMPACT_NAV).forEach(function (item) {
      items.push({ text: t(item.textKey), sub: item.page === 'schedules' ? t('automation') : t('page'), href: item.href, icon: item.icon });
    });
    items.push(
      { text: t('newChat'), sub: t('action') + ' · ' + t('chat'), href: 'chat.html', icon: 'plus' },
      { text: t('newSchedule'), sub: t('action') + ' · ' + t('automation'), href: 'schedules.html', icon: 'clock' },
      { text: t('addMcp'), sub: t('action') + ' · MCP', href: 'mcp.html', icon: 'plug' },
      { text: t('manageRuntime'), sub: t('settings'), href: 'settings.html#runtime', icon: 'terminal' },
      { text: t('tokenAudit'), sub: t('settings'), href: 'settings.html#obs', icon: 'zap' }
    );
    return items;
  }

  function openPalette(initial) {
    let host = document.getElementById('cmd-palette');
    if (!host) {
      host = document.createElement('div');
      host.className = 'backdrop cmd-backdrop';
      host.id = 'cmd-palette';
      host.hidden = true;
      host.innerHTML =
        '<div class="dialog cmd-dialog" role="dialog" aria-modal="true" aria-label="快速跳转">' +
          '<div class="cmd-head">' + svg('search', 15) +
            '<input class="cmd-input" type="text" placeholder="' + t('quickSearch') + '" aria-label="' + t('quickSearch') + '">' +
            '<span class="kbd">ESC</span>' +
          '</div>' +
          '<div class="cmd-list" role="listbox" aria-label="跳转结果"></div>' +
          '<div class="cmd-foot"><span><span class="kbd">↑</span><span class="kbd">↓</span> 移动</span>' +
            '<span><span class="kbd">↵</span> 打开</span><span class="cmd-foot-spacer"></span>' +
            '<span><span class="kbd">⌘</span><span class="kbd">K</span> 呼出</span></div>' +
        '</div>';
      document.body.appendChild(host);
    }

    const input = host.querySelector('.cmd-input');
    const list = host.querySelector('.cmd-list');
    const all = paletteItems();
    let active = 0;

    function render() {
      const q = input.value.trim().toLowerCase();
      const rows = all.filter(function (it) {
        return !q || it.text.toLowerCase().indexOf(q) >= 0 || it.sub.toLowerCase().indexOf(q) >= 0;
      });
      active = 0;
      if (!rows.length) {
        list.innerHTML = '<div class="cmd-empty">' + t('noMatches') + '</div>';
        return;
      }
      list.innerHTML = rows.map(function (it, i) {
        return '<a class="cmd-item' + (i === 0 ? ' is-active' : '') + '" href="' + it.href + '" data-idx="' + i + '">' +
          svg(it.icon, 15) +
          '<span class="cmd-item-main"><span class="cmd-item-text">' + it.text + '</span>' +
          '<span class="cmd-item-sub">' + it.sub + '</span></span>' +
          '<span class="cmd-item-go">' + svg('chevron', 12) + '</span></a>';
      }).join('');
      wireRows(rows);
    }

    function wireRows(rows) {
      list.querySelectorAll('.cmd-item').forEach(function (el, i) {
        el.addEventListener('mouseenter', function () { setActive(i); });
        el.addEventListener('click', function () { host.hidden = true; });
      });
      list._rows = rows;
    }

    function setActive(i) {
      const els = list.querySelectorAll('.cmd-item');
      if (!els.length) return;
      active = (i + els.length) % els.length;
      els.forEach(function (el, n) { el.classList.toggle('is-active', n === active); });
      const el = els[active];
      if (el) {
        const top = el.offsetTop;
        const bottom = top + el.offsetHeight;
        if (top < list.scrollTop) list.scrollTop = top;
        else if (bottom > list.scrollTop + list.clientHeight) list.scrollTop = bottom - list.clientHeight;
      }
    }

    input.value = initial || '';
    render();
    host.hidden = false;
    input.oninput = render;
    input.onkeydown = function (e) {
      if (e.key === 'ArrowDown') { e.preventDefault(); setActive(active + 1); }
      else if (e.key === 'ArrowUp') { e.preventDefault(); setActive(active - 1); }
      else if (e.key === 'Enter') {
        const el = list.querySelector('.cmd-item.is-active');
        if (el) window.location.href = el.getAttribute('href');
      }
    };
    setTimeout(function () { input.focus(); }, 10);
  }

  /* ---------- tabs, dialogs, switches ---------- */
  function wire() {
    document.addEventListener('click', function (e) {
      const tab = e.target.closest('[data-tab]');
      if (tab) {
        const key = tab.dataset.tab;
        document.querySelectorAll('[data-tab]').forEach(function (t) {
          if (t.dataset.tabGroup === tab.dataset.tabGroup) t.classList.toggle('is-active', t === tab);
        });
        document.querySelectorAll('[data-pane]').forEach(function (p) {
          if (p.dataset.paneGroup === tab.dataset.tabGroup) p.classList.toggle('is-active', p.dataset.pane === key);
        });
        if (tab.dataset.tabGroup === 'rail') document.body.dataset.railTab = key;
      }

      const opener = e.target.closest('[data-dialog-open]');
      if (opener) {
        const d = document.getElementById(opener.dataset.dialogOpen);
        if (d) openDialog(d, opener);
        else if (opener.dataset.dialogOpen === 'search-dialog') openPalette('');
      }

      const closer = e.target.closest('[data-dialog-close]');
      if (closer) closeDialog(closer.closest('.backdrop'));

      if (e.target.classList && e.target.classList.contains('backdrop')) closeDialog(e.target);

      const sw = e.target.closest('.switch');
      if (sw) {
        const next = sw.getAttribute('aria-checked') !== 'true';
        sw.setAttribute('aria-checked', String(next));
        if (sw.dataset.toast) toast(sw.dataset.toast.replace('{state}', next ? '已开启' : '已关闭'));
      }

      const cb = e.target.closest('.checkbox');
      if (cb) cb.setAttribute('aria-checked', String(cb.getAttribute('aria-checked') !== 'true'));

      const copy = e.target.closest('[data-copy]');
      if (copy) {
        const text = copy.dataset.copy;
        if (navigator.clipboard) navigator.clipboard.writeText(text).catch(function () {});
        toast('已复制到剪贴板');
      }

      const toggle = e.target.closest('[data-toggle-class]');
      if (toggle) {
        const target = document.querySelector(toggle.dataset.toggleTarget);
        if (target) target.classList.toggle(toggle.dataset.toggleClass);
      }
    });

    document.addEventListener('keydown', function (e) {
      const openDialogs = Array.from(document.querySelectorAll('.backdrop:not([hidden])'));
      const activeDialog = openDialogs[openDialogs.length - 1];
      if (activeDialog) trapDialogFocus(e, activeDialog);
      if (e.key === 'Escape' && activeDialog) {
        closeDialog(activeDialog);
      }
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        openPalette('');
      }
    });
  }

  function hydrateIcons(root) {
    (root || document).querySelectorAll('[data-icon]').forEach(function (el) {
      el.outerHTML = svg(el.dataset.icon, Number(el.dataset.size || 16));
    });
  }

  window.Anvil = {
    svg: svg,
    toast: toast,
    icons: ICONS,
    hydrate: hydrateIcons,
    palette: openPalette,
    getLocale: getLocale,
    setLocale: setLocale,
    t: t,
    addTranslations: addTranslations,
    translate: applyI18n,
    getHistoryState: loadHistoryState,
    saveHistoryState: saveHistoryState,
    assignSessionToProject: assignSessionToProject,
    renderSidebar: renderSidebar,
    openDialog: openDialog,
    closeDialog: closeDialog
  };

  function boot() {
    document.documentElement.lang = getLocale();
    renderSidebar();
    applyI18n();
    hydrateIcons();
    wire();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }
})();
