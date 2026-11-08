/*
 * U07 页面审计器（唯一实现，两个执行者共用）。
 *
 * 为什么只有一份：Playwright 预检和 built-Wails 门禁必须用同一套判定。两份实现
 * 一定会分叉，而分叉之后"预检过了、门禁不过"或反过来，都会让人开始怀疑检查本身，
 * 最后就变成"关掉检查"。
 *
 * 执行方式：
 *   - Playwright：(await page.evaluate(source)) → window.__u07audit.audit(opts)
 *   - 门禁（Go/CDP）：Runtime.evaluate 同样注入源码，再调用 __u07audit.audit(opts)
 *
 * 判定口径（每条都对应 v1-gates 里的一行，不做"看起来还行"的猜测）：
 *   - unnamed-control   交互元素没有可访问名称（aria-label/labelledby/文本/label/alt/title）
 *   - low-contrast      文本与有效背景的 WCAG 对比度不足（AA：正文 4.5，大字 3.0）
 *   - no-visible-focus  聚焦后没有任何可见变化（outline/box-shadow/边框颜色/背景色）
 *   - clipped-control   交互元素横向超出视口
 *   - unreachable       滚到视野内仍不可见/尺寸为 0/被 pointer-events 关掉的可操作控件
 *   - overlap           两个交互元素实质重叠（>4×4 CSS px）
 *   - h-scroll          页面出现横向滚动（布局在窄窗口下撑破）
 *   - truncated-label   交互元素的文案被 ellipsis/hidden 截断
 *   - direct-color      元素内联样式里出现直接颜色值（主题必须走语义 token）
 */
(function (global) {
  "use strict";

  var INTERACTIVE = [
    "button",
    "a[href]",
    'select',
    'input:not([type="hidden"])',
    "textarea",
    '[tabindex]:not([tabindex="-1"])',
    '[role="button"]',
    '[role="tab"]',
    '[role="link"]',
    '[role="checkbox"]',
    '[role="switch"]',
    '[contenteditable="true"]',
  ].join(",");

  function norm(s) {
    return String(s == null ? "" : s)
      .replace(/[\s\u00a0]+/g, " ")
      .trim();
  }

  function isVisible(el, style) {
    var st = style || getComputedStyle(el);
    if (st.visibility === "hidden" || st.display === "none") return false;
    if (parseFloat(st.opacity || "1") === 0) return false;
    var r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  }

  function describe(el) {
    var tag = el.tagName.toLowerCase();
    var id = el.id ? "#" + el.id : "";
    var cls = (el.className && typeof el.className === "string")
      ? "." + norm(el.className).split(" ").slice(0, 2).join(".")
      : "";
    var text = norm(el.innerText || el.textContent);
    if (text.length > 40) text = text.slice(0, 40) + "…";
    return tag + id + cls + (text ? "「" + text + "」" : "");
  }

  // ---------------------------------------------------------------- 可访问名称

  // 可访问名称里不包含 aria-hidden 的内容（ARIA 规定）：一个只剩装饰图标的
  // 按钮就是"没有名字"，不能因为图标字符是文本就算它有了名字。
  function textWithoutAriaHidden(el) {
    var out = "";
    for (var i = 0; i < el.childNodes.length; i++) {
      var n = el.childNodes[i];
      if (n.nodeType === 3) {
        out += n.nodeValue;
        continue;
      }
      if (n.nodeType !== 1) continue;
      if (n.getAttribute("aria-hidden") === "true") continue;
      out += textWithoutAriaHidden(n);
    }
    return out;
  }

  function accessibleName(el) {
    var aria = el.getAttribute("aria-label");
    if (aria && norm(aria)) return norm(aria);

    var labelledby = el.getAttribute("aria-labelledby");
    if (labelledby) {
      var fromIds = labelledby
        .split(/\s+/)
        .map(function (id) {
          var t = document.getElementById(id);
          return t ? norm(t.innerText || t.textContent) : "";
        })
        .filter(Boolean)
        .join(" ");
      if (norm(fromIds)) return norm(fromIds);
    }

    if (el.tagName === "IMG") {
      var alt = el.getAttribute("alt");
      if (alt !== null && norm(alt)) return norm(alt);
    }

    var title = el.getAttribute("title");
    if (title && norm(title)) return norm(title);

    var text = norm(textWithoutAriaHidden(el));
    if (text) return text;

    if (el.labels && el.labels.length) {
      for (var i = 0; i < el.labels.length; i++) {
        var lt = norm(el.labels[i].innerText || el.labels[i].textContent);
        if (lt) return lt;
      }
    }

    if (el.tagName === "INPUT" && (el.type === "submit" || el.type === "button")) {
      if (norm(el.value)) return norm(el.value);
    }

    return "";
  }

  // ---------------------------------------------------------------- 对比度

  function parseColor(str) {
    if (!str) return null;
    var hex = /^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$/.exec(str.trim());
    if (hex) {
      var h = hex[1];
      var to = function (s) {
        return parseInt(s, 16);
      };
      if (h.length === 3) {
        return { r: to(h[0] + h[0]), g: to(h[1] + h[1]), b: to(h[2] + h[2]), a: 1 };
      }
      return {
        r: to(h.slice(0, 2)),
        g: to(h.slice(2, 4)),
        b: to(h.slice(4, 6)),
        a: h.length === 8 ? to(h.slice(6, 8)) / 255 : 1,
      };
    }
    var m = /^rgba?\(([^)]+)\)$/.exec(str.trim());
    if (!m) return null;
    var parts = m[1].split(/[,\s/]+/).filter(Boolean);
    if (parts.length < 3) return null;
    var c = {
      r: parseFloat(parts[0]),
      g: parseFloat(parts[1]),
      b: parseFloat(parts[2]),
      a: parts.length > 3 ? parseFloat(parts[3]) : 1,
    };
    if (isNaN(c.r) || isNaN(c.g) || isNaN(c.b)) return null;
    if (isNaN(c.a)) c.a = 1;
    return c;
  }

  function channel(v) {
    v = v / 255;
    return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4);
  }

  function luminance(c) {
    return 0.2126 * channel(c.r) + 0.7152 * channel(c.g) + 0.0722 * channel(c.b);
  }

  function blend(fg, bg) {
    var a = fg.a;
    return {
      r: fg.r * a + bg.r * (1 - a),
      g: fg.g * a + bg.g * (1 - a),
      b: fg.b * a + bg.b * (1 - a),
      a: 1,
    };
  }

  function contrastRatio(fg, bg) {
    var l1 = luminance(blend(fg, bg));
    var l2 = luminance(bg);
    var hi = Math.max(l1, l2);
    var lo = Math.min(l1, l2);
    return (hi + 0.05) / (lo + 0.05);
  }

  // 有效背景：向上找第一个不透明背景色；透明的一律叠到它的父级上。
  function effectiveBackground(el) {
    var node = el;
    var stack = [];
    while (node && node.nodeType === 1) {
      var bg = parseColor(getComputedStyle(node).backgroundColor);
      if (bg && bg.a > 0) {
        stack.push(bg);
        if (bg.a >= 1) break;
      }
      node = node.parentElement;
    }
    var base = { r: 255, g: 255, b: 255, a: 1 };
    if (stack.length && stack[stack.length - 1].a >= 1) {
      base = stack.pop();
    }
    while (stack.length) {
      base = blend(stack.pop(), base);
    }
    return base;
  }

  function ownText(el) {
    var out = [];
    for (var i = 0; i < el.childNodes.length; i++) {
      var n = el.childNodes[i];
      if (n.nodeType === 3) out.push(n.nodeValue);
    }
    return norm(out.join(" "));
  }

  function textSize(el, st) {
    var px = parseFloat(st.fontSize || "16");
    var weight = parseInt(st.fontWeight || "400", 10) || 400;
    var large = px >= 24 || (px >= 18.66 && weight >= 700);
    return { px: px, weight: weight, large: large };
  }

  // ---------------------------------------------------------------- 主题 token

  function themeTokens() {
    var cs = getComputedStyle(document.documentElement);
    var names = [
      "background",
      "surface",
      "surface-raised",
      "foreground",
      "muted",
      "border",
      "accent",
      "accent-surface",
      "success",
      "warning",
      "danger",
      "focus",
    ];
    var out = {};
    for (var i = 0; i < names.length; i++) {
      out[names[i]] = norm(cs.getPropertyValue("--anvil-" + names[i]));
    }
    return out;
  }

  // token 自身的对比度：前景 token 必须能在它实际会配的背景上看清。
  function tokenContrast() {
    var tokens = themeTokens();
    var pairs = [
      ["foreground", "background"],
      ["foreground", "surface"],
      ["foreground", "surface-raised"],
      ["muted", "background"],
      ["muted", "surface"],
      ["muted", "surface-raised"],
      ["accent", "background"],
      ["accent", "surface-raised"],
      ["success", "background"],
      ["warning", "background"],
      ["danger", "background"],
      ["focus", "background"],
    ];
    var out = [];
    for (var i = 0; i < pairs.length; i++) {
      var fg = parseColor(tokens[pairs[i][0]]);
      var bg = parseColor(tokens[pairs[i][1]]);
      if (!fg || !bg) {
        out.push({ pair: pairs[i].join("/"), ratio: null, reason: "无法解析 token 颜色" });
        continue;
      }
      out.push({ pair: pairs[i].join("/"), ratio: Math.round(contrastRatio(fg, bg) * 100) / 100 });
    }
    return out;
  }

  // ---------------------------------------------------------------- 布局

  function scrollIntoViewAndCheck(el) {
    try {
      el.scrollIntoView({ block: "center", inline: "center" });
    } catch (e) {
      /* 老引擎会抛；下面按几何判定 */
    }
    var r = el.getBoundingClientRect();
    var vw = window.innerWidth;
    var vh = window.innerHeight;
    var intersect =
      r.right > 1 && r.bottom > 1 && r.left < vw - 1 && r.top < vh - 1;
    return { intersect: intersect, rect: r };
  }

  // ---------------------------------------------------------------- 主入口

  function audit(opts) {
    opts = opts || {};
    var contrastCheck = opts.contrast !== false;
    var layoutCheck = opts.layout !== false;
    var findings = [];
    var facts = {
      locale: document.documentElement.lang || "",
      theme: document.documentElement.dataset.theme || "",
      themeTokens: themeTokens(),
      tokenContrast: tokenContrast(),
      viewport: { w: window.innerWidth, h: window.innerHeight, dpr: window.devicePixelRatio },
      document: {
        scrollWidth: document.documentElement.scrollWidth,
        clientWidth: document.documentElement.clientWidth,
        scrollHeight: document.documentElement.scrollHeight,
      },
      controls: 0,
      states: [],
      headings: [],
    };

    facts.headings = Array.prototype.slice
      .call(document.querySelectorAll("h1,h2,h3"))
      .map(function (e) {
        return norm(e.innerText || e.textContent);
      })
      .filter(Boolean)
      .slice(0, 20);

    // 状态词：把页面上真实出现的状态标签收进来（loading/empty/error/success/审批风险…），
    // 它们是"这些状态被渲染过"的凭据，而不是我口头上的覆盖声明。
    facts.states = Array.prototype.slice
      .call(document.querySelectorAll('[data-state], [data-status], [role="alert"], [role="status"]'))
      .map(function (e) {
        return norm(e.getAttribute("data-state") || e.getAttribute("data-status") || e.innerText);
      })
      .filter(Boolean)
      .slice(0, 40);

    var controls = Array.prototype.slice.call(document.querySelectorAll(INTERACTIVE)).filter(function (el) {
      return isVisible(el);
    });
    facts.controls = controls.length;

    for (var i = 0; i < controls.length; i++) {
      var el = controls[i];
      var st = getComputedStyle(el);
      var name = accessibleName(el);
      if (!name) {
        findings.push({ rule: "unnamed-control", target: describe(el), detail: "没有可访问名称" });
      }

      if (layoutCheck) {
        var r = el.getBoundingClientRect();
        if (r.left < -0.5 || r.right > window.innerWidth + 0.5) {
          findings.push({
            rule: "clipped-control",
            target: describe(el),
            detail: "横向超出视口 [" + Math.round(r.left) + "," + Math.round(r.right) + "] vw=" + window.innerWidth,
          });
        }
        if (st.pointerEvents === "none") {
          findings.push({ rule: "unreachable", target: describe(el), detail: "pointer-events:none" });
        }
        if (st.textOverflow === "ellipsis" && el.scrollWidth > el.clientWidth + 1) {
          findings.push({
            rule: "truncated-label",
            target: describe(el),
            detail: "文案被截断 " + el.clientWidth + "<" + el.scrollWidth,
          });
        }
      }
    }

    if (layoutCheck) {
      // 横向滚动：布局在窄窗口里撑破（page 级缺陷，定位到具体元素不方便，给数值即可）。
      if (document.documentElement.scrollWidth > document.documentElement.clientWidth + 1) {
        findings.push({
          rule: "h-scroll",
          target: "document",
          detail: "scrollWidth=" + document.documentElement.scrollWidth + " clientWidth=" + document.documentElement.clientWidth,
        });
      }

      // 交互元素两两重叠（同一屏内、实质覆盖）。
      for (var a = 0; a < controls.length; a++) {
        for (var b = a + 1; b < controls.length; b++) {
          var ra = controls[a].getBoundingClientRect();
          var rb = controls[b].getBoundingClientRect();
          var w = Math.min(ra.right, rb.right) - Math.max(ra.left, rb.left);
          var h = Math.min(ra.bottom, rb.bottom) - Math.max(ra.top, rb.top);
          if (w > 4 && h > 4) {
            findings.push({
              rule: "overlap",
              target: describe(controls[a]) + " × " + describe(controls[b]),
              detail: Math.round(w) + "×" + Math.round(h) + " CSS px 重叠",
            });
          }
        }
      }

      // 可达性：滚到视野内后仍不可见，或尺寸为 0 的可操作控件。
      for (var k = 0; k < controls.length; k++) {
        var chk = scrollIntoViewAndCheck(controls[k]);
        if (!chk.intersect) {
          findings.push({
            rule: "unreachable",
            target: describe(controls[k]),
            detail: "滚入视野后仍不在视口内",
          });
        }
      }
    }

    if (contrastCheck) {
      // 文本对比度：只看真正承载文字的元素（自己的文本节点非空）。
      var textEls = Array.prototype.slice.call(document.querySelectorAll("body *")).filter(function (el) {
        if (el.closest("[aria-hidden='true']")) return false;
        if (!ownText(el)) return false;
        return isVisible(el);
      });
      for (var t = 0; t < textEls.length; t++) {
        var tel = textEls[t];
        var tst = getComputedStyle(tel);
        var fg = parseColor(tst.color);
        if (!fg) continue;
        var bg = effectiveBackground(tel);
        var ratio = contrastRatio(fg, bg);
        var size = textSize(tel, tst);
        var need = size.large ? 3.0 : 4.5;
        if (ratio + 0.005 < need) {
          findings.push({
            rule: "low-contrast",
            target: describe(tel),
            detail:
              "ratio=" + (Math.round(ratio * 100) / 100) + " < " + need +
              " (" + tst.color + " on rgb(" + Math.round(bg.r) + "," + Math.round(bg.g) + "," + Math.round(bg.b) + "))",
          });
        }
      }
    }

    // 焦点可见性**不在这里判**：程序化 focus() 不会命中 :focus-visible，
    // 用它判定会把所有"只在键盘聚焦时画焦点环"的控件判成缺陷（实测侧栏 8 个
    // 入口全被误报）。焦点必须由真实键盘 Tab 驱动，见 tabFocusStep()。

    return {
      findings: findings,
      facts: facts,
      summary: {
        total: findings.length,
        byRule: findings.reduce(function (acc, x) {
          acc[x.rule] = (acc[x.rule] || 0) + 1;
          return acc;
        }, {}),
      },
    };
  }

  // ---------------------------------------------------------------- 单点测量

  // 一个元素的样式快照（hover/active/focus/disabled 各自的对比度与可见性证据）。
  function controlSnapshot(selectorOrNull) {
    var el = selectorOrNull ? document.querySelector(selectorOrNull) : document.activeElement;
    return snapshotOf(el);
  }

  // 坐标处的元素快照（hover/active 探测用）。
  //
  // elementFromPoint 命中的往往是按钮里的图标（<svg>）或文字 span：样式在交互元素
  // 本身，不在图标上。所以先上溯到最近的交互元素，再取样式——否则会得到"悬停没有
  // 任何变化"的假结论（U07 实测踩过）。
  function controlSnapshotAt(el) {
    if (!el) return null;
    var interactive = el.closest ? el.closest(INTERACTIVE) : null;
    return snapshotOf(interactive || el);
  }

  function snapshotOf(el) {
    if (!el) return null;
    var st = getComputedStyle(el);
    var fg = parseColor(st.color);
    var bg = effectiveBackground(el);
    var r = el.getBoundingClientRect();
    return {
      target: describe(el),
      color: st.color,
      background: st.backgroundColor,
      effectiveBg: "rgb(" + Math.round(bg.r) + "," + Math.round(bg.g) + "," + Math.round(bg.b) + ")",
      ratio: fg ? Math.round(contrastRatio(fg, bg) * 100) / 100 : null,
      outline: st.outlineStyle + " " + st.outlineWidth + " " + st.outlineColor,
      boxShadow: st.boxShadow,
      opacity: st.opacity,
      cursor: st.cursor,
      rect: { x: Math.round(r.left), y: Math.round(r.top), w: Math.round(r.width), h: Math.round(r.height) },
      text: norm(el.innerText || el.textContent).slice(0, 60),
    };
  }

  /**
   * 读取"当前键盘焦点"的可见性证据。
   *
   * 必须配合真实 Tab 键使用（Playwright: page.keyboard.press("Tab")；
   * 门禁: CDP Input.dispatchKeyEvent）。程序化 focus() 不会让 Chromium
   * 进入键盘模态，:focus-visible 不会命中——这一点是本检查可信的前提。
   */
  function tabFocusStep() {
    var el = document.activeElement;
    if (!el || el === document.body) return null;
    var st = getComputedStyle(el);
    var outlineWidth = parseFloat(st.outlineWidth || "0");
    var focusVisible = typeof el.matches === "function" ? el.matches(":focus-visible") : false;
    return {
      target: describe(el),
      tag: el.tagName.toLowerCase(),
      focusVisible: focusVisible,
      outline: st.outlineStyle + " " + st.outlineWidth + " " + st.outlineColor,
      boxShadow: st.boxShadow,
      borderColor: st.borderColor,
      backgroundColor: st.backgroundColor,
      // 可见焦点环：outline 或 box-shadow 至少一个被真的画出来。
      ringVisible: (st.outlineStyle !== "none" && outlineWidth > 0) || st.boxShadow !== "none",
    };
  }

  var api = {
    audit: audit,
    tabFocusStep: tabFocusStep,
    accessibleName: accessibleName,
    contrastRatio: function (a, b) {
      var fg = parseColor(a);
      var bg = parseColor(b);
      if (!fg || !bg) return null;
      return Math.round(contrastRatio(fg, bg) * 100) / 100;
    },
    themeTokens: themeTokens,
    tokenContrast: tokenContrast,
    controlSnapshot: controlSnapshot,
    controlSnapshotAt: controlSnapshotAt,
    INTERACTIVE: INTERACTIVE,
  };

  global.__u07audit = api;
  return api;
})(typeof globalThis !== "undefined" ? globalThis : this);
