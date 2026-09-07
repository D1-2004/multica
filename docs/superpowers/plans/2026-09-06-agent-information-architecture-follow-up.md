# Agent Information Architecture Follow-up Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make agents immediately discoverable, simplify the detail and configuration navigation, and show digital-employee behavior only when its inbound coordinator dependency is enabled.

**Architecture:** Keep all route and server field identifiers stable except for removing `inbound` from valid detail views. Render the global and configuration navigation from existing registries without collapsible state. Split digital-employee message controls by dependency so `inbound_coordinator` is the page-level gate while `chat_session_resume` remains independent.

**Tech Stack:** React 19, TypeScript, Tailwind semantic tokens, i18next, Vitest, Testing Library, Next.js shared views.

---

### Task 1: Flatten the global sidebar and replace technical copy

**Files:**
- Modify: `packages/views/layout/app-sidebar.tsx`
- Modify: `packages/views/layout/app-sidebar.test.tsx`
- Modify: `packages/views/locales/en/layout.json`
- Modify: `packages/views/locales/zh-Hans/layout.json`
- Modify: `packages/views/locales/ja/layout.json`
- Modify: `packages/views/locales/ko/layout.json`

- [ ] **Step 1: Write failing sidebar tests**

Replace the grouping expectations with direct-link expectations:

```tsx
it("shows agent tools as direct workspace links", () => {
  const { container } = render(<AppSidebar />);
  expect(screen.queryByRole("button", { name: /Agents & Squads/i })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /Extensions/i })).not.toBeInTheDocument();
  for (const href of ["/acme/agents", "/acme/squads", "/acme/runtimes", "/acme/runners", "/acme/skills"]) {
    expect(container.querySelector(`button[data-href="${href}"]`)).not.toBeNull();
  }
  expect(screen.getByText("My Computer")).toBeInTheDocument();
  expect(container.querySelector('button[data-href="/acme/settings?tab=integrations"]')).toBeNull();
});
```

- [ ] **Step 2: Verify the sidebar test fails**

Run: `pnpm --filter @multica/views test -- app-sidebar.test.tsx`

Expected: FAIL because the collapsible group buttons and Connections link still exist and Local Runner is still visible.

- [ ] **Step 3: Render one direct navigation list**

Delete `CollapsibleNavGroup`, the group-open state/effects, and `settingsIntegrations` from the sidebar nav types. Replace `agentNav` and `extensionNav` with one registry:

```ts
const agentToolsNav = [
  { key: "agents", labelKey: "agents" },
  { key: "squads", labelKey: "squads" },
  { key: "runtimes", labelKey: "runtimes" },
  { key: "runners", labelKey: "runners" },
  { key: "skills", labelKey: "skills" },
] as const;
```

Render each entry as a normal `SidebarMenuButton` in the existing workspace navigation surface. Change the four locale values for `nav.runners` to plain-language equivalents; English uses `My Computer`, Chinese uses `我的电脑`.

- [ ] **Step 4: Verify the sidebar tests pass**

Run: `pnpm --filter @multica/views test -- app-sidebar.test.tsx`

Expected: PASS.

### Task 2: Remove Conversations from agent details

**Files:**
- Modify: `packages/views/agents/components/agent-config-navigation.ts`
- Modify: `packages/views/agents/components/agent-config-navigation.test.ts`
- Modify: `packages/views/agents/components/agent-overview-pane.tsx`
- Modify: `packages/views/agents/components/agent-overview-pane.test.tsx`

- [ ] **Step 1: Write failing navigation tests**

```ts
it("rejects the removed inbound detail view", () => {
  expect(normalizeDetailView("inbound")).toBeNull();
});
```

```tsx
it("shows four plain-language destinations", () => {
  renderPane([makeRuntime("claude")]);
  expect(screen.getAllByRole("tab").map((tab) => tab.textContent)).toEqual([
    "Overview", "Work", "Memory", "Configuration",
  ]);
});
```

- [ ] **Step 2: Verify tests fail for the existing fifth tab**

Run: `pnpm --filter @multica/views test -- agent-config-navigation.test.ts agent-overview-pane.test.tsx`

Expected: FAIL because `inbound` is still a valid view and Conversations still renders.

- [ ] **Step 3: Remove the route from the detail registry and pane**

Remove `inbound` from `DetailSection`, `DetailTab`, `DETAIL_VIEWS`, `TOP_TABS`, `visibleViews`, section routing, overflow conditions, and the `CoordinatorSessionsTab` render/import. An invalid `?view=inbound` then follows the existing overview fallback.

- [ ] **Step 4: Verify agent-detail tests pass**

Run: `pnpm --filter @multica/views test -- agent-config-navigation.test.ts agent-overview-pane.test.tsx`

Expected: PASS.

### Task 3: Keep the complete configuration directory expanded

**Files:**
- Modify: `packages/views/agents/components/agent-config-nav.tsx`
- Modify: `packages/views/agents/components/agent-config-nav.test.tsx`
- Modify: `packages/views/agents/components/agent-overview-pane.test.tsx`

- [ ] **Step 1: Write a failing all-groups-visible test**

```tsx
it("keeps every desktop configuration group and page visible", () => {
  renderWithI18n(<AgentConfigNav groups={AGENT_CONFIG_GROUPS} activeView="digital_employee" onSelect={vi.fn()} />);
  for (const name of ["Identity & Goals", "Capabilities", "Connections", "Execution", "Management"]) {
    expect(screen.getByText(name)).toBeInTheDocument();
  }
  expect(screen.getByRole("tab", { name: "Skills" })).toBeInTheDocument();
  expect(screen.getByRole("tab", { name: "Access" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /^Capabilities/i })).not.toBeInTheDocument();
});
```

- [ ] **Step 2: Verify the test fails because inactive groups are collapsed**

Run: `pnpm --filter @multica/views test -- agent-config-nav.test.tsx`

Expected: FAIL because Skills and Access are absent.

- [ ] **Step 3: Replace accordion controls with static grouped navigation**

Render each group label as non-interactive text and render every visible item below it. Use a quiet surface and persistent selected treatment:

```tsx
<aside className="hidden w-56 shrink-0 overflow-y-auto bg-surface-subtle px-3 py-4 md:block">
  {groups.map((group) => (
    <section key={group.id} className="mb-5 last:mb-0">
      <h3 className="px-2 text-caption font-medium text-muted-foreground">
        {t(($) => $.tabs[group.labelKey])}
      </h3>
      <div role="tablist" aria-label={t(($) => $.tabs[group.labelKey])} className="mt-1 space-y-0.5">
        {group.items.map((item) => (
          <button
            key={item.id}
            type="button"
            role="tab"
            aria-selected={item.id === activeView}
            onClick={() => onSelect(item.id)}
          >
            {t(($) => $.tabs[item.labelKey])}
          </button>
        ))}
      </div>
    </section>
  ))}
</aside>
```

Keep the two compact selectors on narrow screens. Remove the chevron import and group-click behavior. Update overview-pane tests to locate configuration tabs directly instead of clicking a group.

- [ ] **Step 4: Verify configuration navigation tests pass**

Run: `pnpm --filter @multica/views test -- agent-config-nav.test.tsx agent-overview-pane.test.tsx`

Expected: PASS.

### Task 4: Gate digital-employee personality behind inbound judgment

**Files:**
- Modify: `packages/views/agents/components/agent-message-settings.tsx`
- Modify: `packages/views/agents/components/tabs/digital-employee-tab.tsx`
- Modify: `packages/views/agents/components/tabs/digital-employee-tab.test.tsx`
- Modify: `packages/views/locales/en/agents.json`
- Modify: `packages/views/locales/zh-Hans/agents.json`
- Modify: `packages/views/locales/ja/agents.json`
- Modify: `packages/views/locales/ko/agents.json`

- [ ] **Step 1: Write failing dependency tests**

Cover both states:

```tsx
function renderDigitalEmployee(overrides: Partial<Agent>) {
  renderWithI18n(
    <DigitalEmployeeTab
      agent={{ ...agent, ...overrides }}
      runtime={asbRuntime}
      members={[]}
      currentUserId="user-1"
      canEdit
      canOperateDingTalkBinding
      dingTalkBindingPermissionLoading={false}
      onUpdate={vi.fn(async () => {})}
    />,
  );
}

it("hides coordinator-only behavior until inbound judgment is enabled", () => {
  renderDigitalEmployee({ inbound_coordinator: false, chat_session_resume: true });
  expect(screen.getByLabelText("Judge before sandbox")).toBeInTheDocument();
  expect(screen.getByLabelText("Resume the previous conversation")).toBeInTheDocument();
  expect(screen.queryByLabelText("Persona")).not.toBeInTheDocument();
  expect(screen.queryByLabelText("Judge again when work finishes")).not.toBeInTheDocument();
  expect(screen.getByText(/only take effect after inbound judgment/i)).toBeInTheDocument();
});

it("shows personality and follow-up when inbound judgment is enabled", () => {
  renderDigitalEmployee({ inbound_coordinator: true });
  expect(screen.getByLabelText("Persona")).toBeInTheDocument();
  expect(screen.getByLabelText("Judge again when work finishes")).toBeInTheDocument();
});
```

- [ ] **Step 2: Verify the tests fail against the current flat message section**

Run: `pnpm --filter @multica/views test -- digital-employee-tab.test.tsx`

Expected: FAIL because personality always renders and the inbound switch is below it.

- [ ] **Step 3: Split controls by dependency and reorder the page**

Export focused controls from `agent-message-settings.tsx`:

```tsx
interface AgentMessageSettingProps {
  agent: Agent;
  canEdit: boolean;
  onUpdate: (data: Record<string, unknown>) => Promise<void>;
}

export function InboundCoordinatorSetting({ agent, canEdit, onUpdate }: AgentMessageSettingProps) {
  const { t } = useT("agents");
  return (
    <BooleanSetting
      agentId={agent.id}
      label={t(($) => $.inspector.prop_inbound_coordinator)}
      description={t(($) => $.inspector.prop_inbound_coordinator_hint)}
      enabled={agent.inbound_coordinator === true}
      canEdit={canEdit}
      onSave={(next) => onUpdate({ inbound_coordinator: next })}
    />
  );
}

export function ConversationResumeSetting({ agent, canEdit, onUpdate }: AgentMessageSettingProps) {
  const { t } = useT("agents");
  return (
    <BooleanSetting
      agentId={agent.id}
      label={t(($) => $.inspector.prop_chat_session_resume)}
      description={t(($) => $.inspector.prop_chat_session_resume_hint)}
      enabled={agent.chat_session_resume === true}
      canEdit={canEdit}
      onSave={(next) => onUpdate({ chat_session_resume: next })}
    />
  );
}

export function CoordinatorFollowUpSetting({ agent, canEdit, onUpdate }: AgentMessageSettingProps) {
  const { t } = useT("agents");
  return (
    <BooleanSetting
      agentId={agent.id}
      label={t(($) => $.inspector.prop_task_finished_loop)}
      description={t(($) => $.inspector.prop_task_finished_loop_hint)}
      enabled={agent.task_finished_loop_enabled === true}
      canEdit={canEdit}
      onSave={(next) => onUpdate({ task_finished_loop_enabled: next })}
    />
  );
}
```

In `DigitalEmployeeTab`, render `InboundCoordinatorSetting` first. Always render profile, identities, and `ConversationResumeSetting`. When `agent.inbound_coordinator === true`, render `AgentVoiceSettings` and `CoordinatorFollowUpSetting`; otherwise render the localized dependency explanation. Keep switch save rollback behavior unchanged.

- [ ] **Step 4: Verify digital-employee tests pass**

Run: `pnpm --filter @multica/views test -- digital-employee-tab.test.tsx`

Expected: PASS.

### Task 5: Finish plain-language copy and visual consistency

**Files:**
- Modify: `packages/views/locales/en/agents.json`
- Modify: `packages/views/locales/zh-Hans/agents.json`
- Modify: `packages/views/locales/ja/agents.json`
- Modify: `packages/views/locales/ko/agents.json`
- Modify: `packages/views/locales/en/settings.json`
- Modify: `packages/views/locales/zh-Hans/settings.json`
- Modify: `packages/views/locales/ja/settings.json`
- Modify: `packages/views/locales/ko/settings.json`
- Modify: affected locale and label tests under `packages/views/`

- [ ] **Step 1: Add failing locale assertions**

Assert that Chinese navigation and execution copy uses `我的电脑` / `使用我的电脑`, the workspace settings destination uses `应用集成`, and no modified user-facing label contains `Runner`.

- [ ] **Step 2: Verify locale assertions fail**

Run: `pnpm --filter @multica/views test -- locales agent-detail-inspector.labels.test.tsx`

Expected: FAIL on current Local Runner and Connections terminology.

- [ ] **Step 3: Update all four locales without changing internal identifiers**

Use outcome-oriented copy. Chinese examples:

```json
{
  "runners": "我的电脑",
  "execution_title": "使用我的电脑",
  "none": "不使用已连接的电脑",
  "empty_title": "还没有连接电脑",
  "empty_description": "连接后，智能体可以在你授权的电脑上使用本地文件和工具完成任务。"
}
```

Keep `runner`, `local_runner`, API fields, and routes unchanged.

- [ ] **Step 4: Run locale, label, and UI guideline checks**

Run: `pnpm --filter @multica/views test -- locales agent-detail-inspector.labels.test.tsx`

Expected: PASS. Review changed TSX for semantic buttons/links, labels, focus-visible treatment, selected-hover persistence, reduced motion, and long-text handling.

### Task 6: Verify, commit, push, deploy, and smoke test pre-release

**Files:**
- Modify: `docs/superpowers/plans/2026-09-06-agent-information-architecture-follow-up.md`

- [ ] **Step 1: Run focused and package verification**

Run:

```bash
pnpm --filter @multica/views test -- app-sidebar.test.tsx agent-config-navigation.test.ts agent-config-nav.test.tsx agent-overview-pane.test.tsx digital-employee-tab.test.tsx agent-detail-inspector.labels.test.tsx
pnpm --filter @multica/views typecheck
pnpm --filter @multica/views lint
git diff --check
```

Expected: all focused tests and typecheck pass; lint has no new errors.

- [ ] **Step 2: Commit implementation with a structured message**

Stage only the plan and product files. Preserve unrelated `.omx/` and `.superpowers/` files. Use a multi-line conventional commit describing navigation, digital-employee gating, copy, visual treatment, and tests.

- [ ] **Step 3: Rebase onto the current pre-release release branch if needed and push**

Run `git fetch origin`, identify the current `origin/releases/*_r_release_342160_dt-fde-multica-code`, rebase when the remote moved, rerun focused verification after conflict resolution, then push `codex/agent-information-architecture`.

- [ ] **Step 4: Deploy through the Aone pre-release pipeline**

Run `make deploy` with proxy variables unset. Monitor code merge, build, artifact scan, pre-release deployment, and integration test until all succeed and the pipeline waits at the manual pre-release verification gate.

- [ ] **Step 5: Smoke test the deployed page**

Verify the supplied agent detail URL returns HTTP 200 and its rendered resources contain the four top-level detail labels, direct sidebar labels, all five configuration groups, plain-language computer label, and inbound dependency copy.
