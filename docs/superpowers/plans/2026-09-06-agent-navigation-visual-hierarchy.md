# Agent Navigation Visual Hierarchy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the global sidebar scannable by four stable domains, emphasize Agents as the primary Agent entry, and clearly distinguish configuration group headings from clickable tabs.

**Architecture:** Keep all routes and navigation behavior unchanged. Replace the two broad sidebar registries with four static domain registries rendered by the existing semantic link components, then refine the existing configuration directory through typography, spacing, and persistent selected states only.

**Tech Stack:** React 19, TypeScript, Tailwind semantic tokens, i18next, Vitest, Testing Library.

---

### Task 1: Organize the global sidebar into four domains

**Files:**
- Modify: `packages/views/layout/app-sidebar.test.tsx`
- Modify: `packages/views/layout/app-sidebar.tsx`
- Modify: `packages/views/locales/en/layout.json`
- Modify: `packages/views/locales/zh-Hans/layout.json`
- Modify: `packages/views/locales/ja/layout.json`
- Modify: `packages/views/locales/ko/layout.json`

- [x] **Step 1: Write the failing domain and emphasis test**

Add assertions that the rendered sidebar includes the four domain labels in order and exposes a stable marker for the primary Agents link:

```tsx
it("organizes workspace destinations into four visible domains", () => {
  const { container } = render(<AppSidebar />);
  expect(screen.getAllByTestId("sidebar-domain").map((node) => node.textContent)).toEqual([
    "Collaboration",
    "Agent",
    "Runtime",
    "Configuration",
  ]);
  expect(container.querySelector('button[data-href="/acme/agents"] [data-primary-agent-entry]')).not.toBeNull();
});
```

- [x] **Step 2: Run the sidebar test and verify RED**

Run: `pnpm --filter @multica/views test -- app-sidebar.test.tsx`

Expected: FAIL because only the generic Workspace label exists and the Agents item has no primary-entry marker.

- [x] **Step 3: Implement the four static registries and emphasized Agents row**

Define the registries as module constants:

```ts
const collaborationNav = [
  { key: "issues", labelKey: "issues" },
  { key: "projects", labelKey: "projects" },
  { key: "autopilots", labelKey: "autopilots" },
  { key: "usage", labelKey: "usage" },
  { key: "sites", labelKey: "sites" },
] as const;
const agentNav = [
  { key: "agents", labelKey: "agents", primary: true },
  { key: "squads", labelKey: "squads" },
  { key: "skills", labelKey: "skills" },
] as const;
const runtimeNav = [
  { key: "runtimes", labelKey: "runtimes" },
  { key: "runners", labelKey: "runners" },
] as const;
const configurationNav = [{ key: "settings", labelKey: "settings" }] as const;
```

Render every registry with a non-interactive `SidebarGroupLabel` carrying `data-testid="sidebar-domain"`. Preserve `AppLink`, active-route detection, focus behavior, hover feedback, and direct visibility. On the Agents row, render an icon wrapper with `data-primary-agent-entry`, `bg-brand/10`, and `text-brand`; use `font-semibold text-foreground` for the row without weakening its active state.

Add localized group labels under `sidebar`:

```json
{
  "collaboration_group": "Collaboration",
  "agent_group": "Agent",
  "runtime_group": "Runtime",
  "configuration_group": "Configuration"
}
```

Chinese uses `协作`、`Agent`、`运行时`、`配置`; Japanese uses `コラボレーション`、`Agent`、`ランタイム`、`設定`; Korean uses `협업`、`Agent`、`런타임`、`설정`.

- [x] **Step 4: Run the sidebar test and verify GREEN**

Run: `pnpm --filter @multica/views test -- app-sidebar.test.tsx`

Expected: PASS with all sidebar tests green.

### Task 2: Separate configuration headings from tabs

**Files:**
- Modify: `packages/views/agents/components/agent-config-nav.test.tsx`
- Modify: `packages/views/agents/components/agent-config-nav.tsx`

- [x] **Step 1: Write the failing hierarchy test**

Expose semantic test markers and assert their hierarchy classes:

```tsx
it("visually separates group headings from configuration tabs", () => {
  renderWithI18n(
    <AgentConfigNav groups={AGENT_CONFIG_GROUPS} activeView="digital_employee" onSelect={vi.fn()} />,
  );
  expect(screen.getByText("Identity & Goals")).toHaveClass("text-micro", "font-semibold");
  expect(screen.getByRole("tab", { name: "Digital Employee" })).toHaveClass("text-body", "min-h-10");
});
```

- [x] **Step 2: Run the configuration-nav test and verify RED**

Run: `pnpm --filter @multica/views test -- agent-config-nav.test.tsx`

Expected: FAIL because both headings and tabs still use `text-caption` and tabs use `min-h-9`.

- [x] **Step 3: Implement the approved hierarchy**

Use `text-micro font-semibold` for static headings and `text-body min-h-10` for tabs. Increase group separation to `mb-6`, keep group-internal spacing at `space-y-1`, and retain the selected background, left marker, hover contrast, truncation, `aria-selected`, and `focus-visible:ring-2`. Keep the narrow-screen two-select layout unchanged.

- [x] **Step 4: Run focused tests and verify GREEN**

Run: `pnpm --filter @multica/views test -- app-sidebar.test.tsx agent-config-nav.test.tsx`

Expected: PASS with both suites green.

### Task 3: Verify code and visual behavior

**Files:**
- Verify: `packages/views/layout/app-sidebar.tsx`
- Verify: `packages/views/agents/components/agent-config-nav.tsx`

- [x] **Step 1: Run focused type and lint checks**

Run: `pnpm --filter @multica/views typecheck`

Expected: exit code 0.

Run: `pnpm --filter @multica/views lint`

Expected: no new errors in modified files.

- [ ] **Step 2: Inspect the local page at desktop width**

Open the existing local application and verify that the global sidebar shows `协作 / Agent / 运行时 / 配置`, Agents is visually primary without adding a badge, all destinations remain directly visible, configuration group labels read as labels rather than tabs, selected tabs remain selected on hover, and no horizontal overflow appears.

- [ ] **Step 3: Inspect the local page at narrow width**

Verify that the global sidebar remains usable in its sheet and that the configuration area still uses the existing two selectors instead of rendering the desktop directory.
