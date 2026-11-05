# Chat Workflow Completion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete the local-first chat prototype so project history, responsive artifacts, scenario states, internationalization, sharing, accessibility, and verification form coherent workflows.

**Architecture:** Keep the existing dependency-free HTML/CSS/JavaScript architecture. Shared sidebar state and translations remain in `assets/studio.js`; chat-specific state, scenarios, and controls remain in `chat.html`; presentation remains in the shared stylesheets.

**Tech Stack:** HTML5, CSS, browser JavaScript, localStorage, shell validation.

**Spec:** User-approved findings from the preceding product review in this conversation.

## Global Constraints

- Simplified Chinese remains the default locale, with English available.
- Preserve the existing Anvil visual system and local-first personal-client positioning.
- THIL content decisions remain separate from tool permission grants.
- External sharing must be opt-in and disclose upload behavior.
- Keep `data-od-id` and other inspection hooks.

## Review Focus

- Narrow screens must expose artifacts through a usable drawer, not a hidden rail.
- History pagination must append DOM rows per page rather than hiding all pre-rendered rows.
- Project assignment must persist and update both header and sidebar grouping.
- Locale changes must update static and dynamically generated system copy.
- Dialog keyboard focus must remain trapped and return to the opener.

---

### Task 1: Responsive artifact drawer and turn-linked outputs

**Files:** Modify `chat.html`, `assets/studio.css`, `assets/pages.css`, `tests/validate-agent-ui.sh`.

- [ ] Add failing assertions for rail scrim, mobile drawer, artifact IDs, selected artifact filtering, and return-to-turn action.
- [ ] Run validation and confirm failure.
- [ ] Implement responsive drawer and per-turn artifact selection.
- [ ] Run validation and JavaScript parsing.

### Task 2: Persistent projects and true incremental history

**Files:** Modify `assets/studio.js`, `assets/studio.css`, `chat.html`, `tests/validate-agent-ui.sh`.

- [ ] Add failing assertions for history persistence API, incremental DOM rendering, project assignment, and history actions.
- [ ] Run validation and confirm failure.
- [ ] Implement localStorage-backed project/session state, append-on-scroll pagination, search, project assignment, pin/rename/archive/delete actions, and persisted expansion.
- [ ] Run validation and JavaScript parsing.

### Task 3: Distinct session scenarios, local export, and complete system i18n

**Files:** Modify `chat.html`, `assets/studio.js`, `assets/pages.css`, `tests/validate-agent-ui.sh`.

- [ ] Add failing assertions for scenario routing, failure/fallback/auth/context/stopped states, local export, cloud-share disclosure, and translation registration.
- [ ] Run validation and confirm failure.
- [ ] Implement scenario templates/routing, local snapshot export UI, explicit cloud-share disclosure, and translated static/dynamic system copy.
- [ ] Run validation and JavaScript parsing.

### Task 4: Accessibility and browser verification

**Files:** Modify `assets/studio.js`, `chat.html`, `tests/validate-agent-ui.sh`; update `chat-project-preview.png`.

- [ ] Add failing assertions for focus restoration, focus trapping, live regions, and dialog labels.
- [ ] Run validation and confirm failure.
- [ ] Implement dialog focus management and accessible status announcements.
- [ ] Run full structural validation, JavaScript parsing, and browser interaction checks at desktop and narrow viewport sizes.
