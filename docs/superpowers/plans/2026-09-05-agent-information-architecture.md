# Agent Information Architecture Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Simplify the workspace sidebar and agent detail navigation, with a dedicated Digital Employee tab that never mixes employee identity with bot integrations.

**Architecture:** Keep the existing agent APIs and field components, but replace the accumulated capability/settings tab arrays with one data-driven configuration registry. Compose the new Digital Employee page from focused profile, voice, identity, and message-behavior sections; split bot connections and inbound MCP access into distinct pages. Keep every leaf view URL-addressable and retain feature/permission gates.

**Tech Stack:** React 19, TypeScript, TanStack Query, Base UI/shadcn primitives, Vitest, Testing Library, i18next JSON resources.

---

## File map

**Create**

- `packages/views/agents/components/agent-config-navigation.ts` — config group registry and pure view/group helpers.
- `packages/views/agents/components/agent-config-nav.tsx` — desktop accordion and narrow-screen two-level selectors.
- `packages/views/agents/components/agent-profile-settings.tsx` — avatar, name, and description settings extracted from the general inspector.
- `packages/views/agents/components/agent-voice-settings.tsx` — persona, reply tone, templates, and instruction extraction.
- `packages/views/agents/components/agent-message-settings.tsx` — chat resume, inbound Coordinator, and task-finished loop switches.
- `packages/views/agents/components/tabs/digital-employee-tab.tsx` — employee page composition and employee identity authorization.
- `packages/views/agents/components/tabs/mcp-access-tab.tsx` — external clients calling the agent through MCP.
- `packages/views/agents/components/agent-config-navigation.test.ts` — registry mapping and legacy view normalization.
- `packages/views/agents/components/tabs/digital-employee-tab.test.tsx` — employee/robot boundary tests.

**Modify**

- `packages/core/paths/paths.ts` and `packages/core/paths/paths.test.ts` — workspace integration-settings deep link.
- `packages/core/paths/consistency.test.ts` and `packages/core/paths/route-icons.test.ts` — register the new parameterless path helper as a settings alias.
- `packages/views/layout/app-sidebar.tsx` and `app-sidebar.test.tsx` — collapsible Agents & Squads and Extensions groups.
- `packages/views/agents/components/agent-overview-pane.tsx` and `.test.tsx` — five top tabs, config registry, grouped navigation, new leaf pages, and identity deep-link migration.
- `packages/views/agents/components/agent-detail-inspector.tsx` and `.labels.test.tsx` — retain runtime/model/execution fields, add GitHub sandbox identity, remove profile/message behavior.
- `packages/views/agents/components/tabs/instructions-tab.tsx` and `.test.tsx` — System Prompt only.
- `packages/views/agents/components/tabs/integrations-tab.tsx` and `.test.tsx` — bot integrations and dispatch configuration only.
- `packages/views/locales/{en,zh-Hans,ja,ko}/agents.json` — group and leaf labels plus Digital Employee section copy.
- `packages/views/locales/{en,zh-Hans,ja,ko}/layout.json` — global sidebar group and connection labels.

**Delete after callers move**

- `packages/views/agents/components/tabs/identity-tab.tsx`
- `packages/views/agents/components/tabs/identity-tab.test.tsx`

## Task 1: Group the global workspace sidebar

**Files:**

- Modify: `packages/core/paths/paths.ts`
- Modify: `packages/core/paths/paths.test.ts`
- Modify: `packages/core/paths/consistency.test.ts`
- Modify: `packages/core/paths/route-icons.test.ts`
- Modify: `packages/views/layout/app-sidebar.tsx`
- Modify: `packages/views/layout/app-sidebar.test.tsx`
- Modify: `packages/views/locales/en/layout.json`
- Modify: `packages/views/locales/zh-Hans/layout.json`
- Modify: `packages/views/locales/ja/layout.json`
- Modify: `packages/views/locales/ko/layout.json`

- [ ] **Step 1: Add failing path and sidebar tests**

Add the path assertion:

```ts
expect(ws.settingsIntegrations()).toBe("/acme/settings?tab=integrations");
```

Add sidebar assertions that the direct workspace section contains Tasks, Projects, Autopilot, Analytics, and Websites; the expanded Agents & Squads group contains Agents, Squads, Runtimes, and Local Runner; Extensions contains Skills and Connections; Settings remains standalone. Assert each group trigger exposes `aria-expanded`.

- [ ] **Step 2: Run the focused tests and observe failure**

Run:

```bash
pnpm --filter @multica/views exec vitest run layout/app-sidebar.test.tsx
pnpm --filter @multica/core exec vitest run paths/paths.test.ts paths/consistency.test.ts paths/route-icons.test.ts
```

Expected: missing `settingsIntegrations`, missing group triggers, and old flat-navigation assertions fail.

- [ ] **Step 3: Add the settings deep link and collapsible groups**

Add the path helper:

```ts
settingsIntegrations: () => `${ws}/settings?tab=integrations`,
```

Treat it as a settings alias in route coverage tests. Replace the flat agent/runtime/skill arrays with:

```ts
const workspaceNav = ["issues", "projects", "autopilots", "usage", "sites"];
const agentNav = ["agents", "squads", "runtimes", "runners"];
const extensionNav = ["skills", "settingsIntegrations"];
```

Render Agents & Squads and Extensions through controlled `Collapsible` sections. The active route's group starts open, group labels use `CollapsibleTrigger`, and selected child rows retain their active appearance during hover. Render Settings as a standalone final row.

- [ ] **Step 4: Run focused tests**

Run the two commands from Step 2. Expected: PASS.

## Task 2: Introduce one agent configuration registry

**Files:**

- Create: `packages/views/agents/components/agent-config-navigation.ts`
- Create: `packages/views/agents/components/agent-config-navigation.test.ts`
- Create: `packages/views/agents/components/agent-config-nav.tsx`
- Modify: `packages/views/agents/components/agent-overview-pane.tsx`
- Modify: `packages/views/agents/components/agent-overview-pane.test.tsx`

- [ ] **Step 1: Write failing registry and top-navigation tests**

Cover the following contract:

```ts
expect(normalizeDetailView("identity")).toBe("digital_employee");
expect(sectionForView("digital_employee")).toBe("configuration");
expect(groupForConfigView("mcp_config")).toBe("capabilities");
expect(groupForConfigView("integrations")).toBe("connections");
expect(groupForConfigView("general")).toBe("execution");
expect(groupForConfigView("llm_trace")).toBe("management");
```

Update pane tests to expect exactly Overview, Work, Conversations, Memory, and Configuration as top-level tabs. Opening Configuration must select Digital Employee when no config view is active.

- [ ] **Step 2: Run the tests and observe failure**

```bash
pnpm --filter @multica/views exec vitest run agents/components/agent-config-navigation.test.ts agents/components/agent-overview-pane.test.tsx
```

Expected: the registry module is absent and the pane still renders Capabilities/Settings.

- [ ] **Step 3: Implement the registry**

Define these stable IDs:

```ts
export type DetailSection = "overview" | "work" | "inbound" | "memory" | "configuration";
export type ConfigGroupId = "identity_goals" | "capabilities" | "connections" | "execution" | "management";
export type DetailTab =
  | "overview" | "work" | "inbound" | "memory"
  | "digital_employee" | "instructions" | "okr"
  | "skills" | "mcp_config" | "composio_mcp"
  | "integrations" | "mcp_access" | "a2a"
  | "general" | "runner" | "env" | "custom_args" | "runtime_config"
  | "access" | "llm_trace";
```

Create one `AGENT_CONFIG_GROUPS` constant in the accepted order. Implement `normalizeDetailView`, `isDetailTab`, `sectionForView`, and `groupForConfigView` from that single registry.

- [ ] **Step 4: Implement grouped desktop and mobile config navigation**

Desktop: group triggers select the group's first visible tab and only the active group panel is open. Mobile: render two labeled `Select` controls, one for the group and one for the active group's visible leaf tabs. Both call the pane's existing guarded `requestView` callback.

- [ ] **Step 5: Wire the pane and preserve feature gates**

Replace `CAPABILITY_TABS`, `SETTINGS_TABS`, and their ID sets with filtered registry groups. Keep current gates for MCP support, Composio ownership, bot deployment support, owner-only MCP access/A2A, editable environment/runner, OpenClaw routing, and cloud LLM Trace. Map the legacy `view=identity` URL to `view=digital_employee` with `navigation.replace`.

- [ ] **Step 6: Run focused tests**

Run the command from Step 2. Expected: PASS.

## Task 3: Build the Digital Employee page without duplicating settings logic

**Files:**

- Create: `packages/views/agents/components/agent-profile-settings.tsx`
- Create: `packages/views/agents/components/agent-voice-settings.tsx`
- Create: `packages/views/agents/components/agent-message-settings.tsx`
- Create: `packages/views/agents/components/tabs/digital-employee-tab.tsx`
- Create: `packages/views/agents/components/tabs/digital-employee-tab.test.tsx`
- Modify: `packages/views/agents/components/agent-detail-inspector.tsx`
- Modify: `packages/views/agents/components/tabs/instructions-tab.tsx`
- Delete: `packages/views/agents/components/tabs/identity-tab.tsx`
- Delete: `packages/views/agents/components/tabs/identity-tab.test.tsx`

- [ ] **Step 1: Write the failing Digital Employee boundary test**

Render `DigitalEmployeeTab` and assert:

```ts
expect(screen.getByText("Employee profile")).toBeInTheDocument();
expect(screen.getByRole("region", { name: /Enterprise digital employee/i })).toBeInTheDocument();
expect(screen.getByRole("region", { name: /Alibaba employee identity/i })).toBeInTheDocument();
expect(screen.getByLabelText("Persona")).toBeInTheDocument();
expect(screen.getByRole("switch", { name: /Inbound judge/i })).toBeInTheDocument();
expect(screen.queryByText("Lark")).not.toBeInTheDocument();
expect(screen.queryByText("GitHub sandbox identity")).not.toBeInTheDocument();
```

Also update `InstructionsTab` tests to assert persona/reply-tone controls are absent while System Prompt remains editable.

- [ ] **Step 2: Run the tests and observe failure**

```bash
pnpm --filter @multica/views exec vitest run agents/components/tabs/digital-employee-tab.test.tsx agents/components/tabs/instructions-tab.test.tsx
```

Expected: the page module is absent and Instructions still owns voice fields.

- [ ] **Step 3: Extract focused sections**

Move the existing implementations without changing save semantics:

- `AgentProfileSettings`: existing profile `useAutoSave`, avatar, name, description.
- `AgentVoiceSettings`: persona/reply tone local drafts, template buttons, max-length validation, extract-from-instructions action, explicit Save.
- `AgentMessageSettings`: chat-session resume, inbound Coordinator, and task-finished loop optimistic switches with rollback on failure.

Each section receives `agent`, `canEdit`, and the narrow callback it needs. `AgentVoiceSettings` reports dirty state to the parent.

- [ ] **Step 4: Compose DigitalEmployeeTab**

Render four `SettingsSection`s in this order:

```tsx
<AgentProfileSettings />
<EmployeeIdentitySettings
  agent={agent}
  runtime={runtime}
  canManage={canManageIdentity}
  canOperateDingTalkBinding={canOperateDingTalkBinding}
  dingTalkBindingPermissionLoading={dingTalkBindingPermissionLoading}
/>
<AgentVoiceSettings />
<AgentMessageSettings />
```

Define `EmployeeIdentitySettings` locally in `digital-employee-tab.tsx`. It renders both DingTalk binding modes (message account and DWS execution identity) plus the ASB-only Alibaba employee identity. It does not render GitHub identity or any bot bind button.

- [ ] **Step 5: Reduce old owners**

Remove profile/message sections from `AgentDetailInspector`. Remove persona/reply tone and extraction UI from `InstructionsTab`, leaving only system/workspace instructions and its dirty/save behavior. Delete the obsolete Identity tab and test after the pane no longer imports them.

- [ ] **Step 6: Run focused tests**

Run the command from Step 2 plus:

```bash
pnpm --filter @multica/views exec vitest run agents/components/agent-detail-inspector.labels.test.tsx
```

Expected: PASS.

## Task 4: Separate robots, inbound MCP, and runtime identity

**Files:**

- Create: `packages/views/agents/components/tabs/mcp-access-tab.tsx`
- Modify: `packages/views/agents/components/tabs/integrations-tab.tsx`
- Modify: `packages/views/agents/components/tabs/integrations-tab.test.tsx`
- Modify: `packages/views/agents/components/agent-detail-inspector.tsx`
- Modify: `packages/views/agents/components/agent-overview-pane.tsx`
- Modify: `packages/views/agents/components/agent-overview-pane.test.tsx`

- [ ] **Step 1: Add failing separation tests**

Assert `IntegrationsTab` renders DingTalk Bot, Lark, Slack, WeCom, and dispatch configuration but does not render `DingTalkAccountBindingCard`, `AgentMCPLinkCard`, enterprise identity, or GitHub identity. Assert `AgentMCPAccessTab` renders only `AgentMCPLinkCard`. Assert runtime settings render `GitHubIdentityBindingCard`.

- [ ] **Step 2: Run and observe failure**

```bash
pnpm --filter @multica/views exec vitest run agents/components/tabs/integrations-tab.test.tsx agents/components/agent-overview-pane.test.tsx agents/components/agent-detail-inspector.labels.test.tsx
```

Expected: Integrations still contains employee/MCP cards and runtime settings lack GitHub identity.

- [ ] **Step 3: Split the pages**

Remove the two mixed cards and related props from `IntegrationsTab`. Implement:

```tsx
export function AgentMCPAccessTab({ agent }: { agent: Agent }) {
  return <AgentMCPLinkCard agent={agent} />;
}
```

Add GitHub sandbox identity after the runtime execution settings card, preserving owner/admin authorization. Wire `mcp_access` in the pane.

- [ ] **Step 4: Run focused tests**

Run the command from Step 2. Expected: PASS.

## Task 5: Add navigation copy in every locale

**Files:**

- Modify: `packages/views/locales/en/agents.json`
- Modify: `packages/views/locales/zh-Hans/agents.json`
- Modify: `packages/views/locales/ja/agents.json`
- Modify: `packages/views/locales/ko/agents.json`
- Modify: `packages/views/locales/en/layout.json`
- Modify: `packages/views/locales/zh-Hans/layout.json`
- Modify: `packages/views/locales/ja/layout.json`
- Modify: `packages/views/locales/ko/layout.json`

- [ ] **Step 1: Add failing translation-shape assertions**

Extend the pane/sidebar tests to render all 4 locales and verify every new top label, group label, and leaf label resolves to visible text rather than an i18n key.

- [ ] **Step 2: Run and observe failure**

```bash
pnpm --filter @multica/views exec vitest run agents/components/agent-overview-pane.test.tsx layout/app-sidebar.test.tsx
```

Expected: new label keys are missing.

- [ ] **Step 3: Add localized labels**

Add keys for Conversations, Configuration, Digital Employee, Identity & Goals, Capabilities, Connections, Execution, Management, Robot Connections, MCP Access, and the Digital Employee section descriptions. Add sidebar keys for Agents & Squads, Extensions, and Connections. Preserve `Skills`, MCP, A2A, LLM Trace, and product terminology per the repository glossary.

- [ ] **Step 4: Run focused tests**

Run the command from Step 2. Expected: PASS.

## Task 6: Verify navigation, types, and UI behavior

**Files:**

- Modify if failures reveal regressions: only files already listed above.

- [ ] **Step 1: Run all agent and sidebar tests**

```bash
pnpm --filter @multica/views exec vitest run agents/components layout/app-sidebar.test.tsx
```

Expected: PASS.

- [ ] **Step 2: Run TypeScript checks**

```bash
pnpm typecheck
```

Expected: all workspace typechecks pass.

- [ ] **Step 3: Run lint for touched packages**

```bash
pnpm --filter @multica/core lint
pnpm --filter @multica/views lint
```

Expected: PASS.

- [ ] **Step 4: Start the local app and visually inspect desktop and narrow widths**

Verify:

- global sidebar groups expand and preserve active child styling;
- exactly five agent top tabs are present;
- Configuration opens Digital Employee by default;
- each config group exposes only its distinct leaf pages;
- Digital Employee and Robot Connections never duplicate the employee card;
- direct `?view=identity` rewrites to `?view=digital_employee`;
- narrow screens show two selectors without horizontal tab overflow.

- [ ] **Step 5: Review the final diff and commit**

Run:

```bash
git diff --check
git status --short
git diff --stat origin/feat/scene-memory-optimize...HEAD
```

Stage only the plan and implementation files. Commit with a structured Chinese conventional message describing sidebar grouping, agent configuration registry, Digital Employee separation, deep-link migration, and tests. Then print:

```bash
git log -1 --pretty=%B
```
