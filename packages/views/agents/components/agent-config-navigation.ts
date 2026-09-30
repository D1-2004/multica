export type DetailSection =
  | "overview"
  | "work"
  | "inbound"
  | "memory"
  | "configuration";

export type ConfigGroupId =
  | "identity_goals"
  | "capabilities"
  | "connections"
  | "execution"
  | "management";

export type DetailTab =
  | "overview"
  | "work"
  | "inbound"
  | "memory"
  | "digital_employee"
  | "instructions"
  | "okr"
  | "skills"
  | "dsh"
  | "filesystem"
  | "mcp_config"
  | "composio_mcp"
  | "context_capabilities"
  | "integrations"
  | "mcp_access"
  | "a2a"
  | "general"
  | "runner"
  | "env"
  | "custom_args"
  | "runtime_config"
  | "export"
  | "publish"
  | "access"
  | "llm_trace";

export type AgentTabLabelKey =
  | "overview"
  | "work"
  | "inbound"
  | "memory"
  | "configuration"
  | "digital_employee"
  | "instructions"
  | "okr"
  | "skills"
  | "dsh"
  | "filesystem"
  | "mcp_config"
  | "composio_mcp"
  | "context_capabilities"
  | "integrations"
  | "mcp_access"
  | "a2a"
  | "general"
  | "runner"
  | "environment"
  | "custom_args"
  | "runtime_config"
  | "export"
  | "publish"
  | "access"
  | "llm_trace";

export interface AgentConfigItem {
  id: DetailTab;
  labelKey: AgentTabLabelKey;
}

export interface AgentConfigGroup {
  id: ConfigGroupId;
  labelKey: ConfigGroupId;
  items: readonly AgentConfigItem[];
}

export const AGENT_CONFIG_GROUPS: readonly AgentConfigGroup[] = [
  {
    id: "identity_goals",
    labelKey: "identity_goals",
    items: [
      { id: "digital_employee", labelKey: "digital_employee" },
      { id: "instructions", labelKey: "instructions" },
      { id: "okr", labelKey: "okr" },
    ],
  },
  {
    id: "capabilities",
    labelKey: "capabilities",
    items: [
      { id: "skills", labelKey: "skills" },
      { id: "mcp_config", labelKey: "mcp_config" },
      { id: "composio_mcp", labelKey: "composio_mcp" },
      { id: "context_capabilities", labelKey: "context_capabilities" },
    ],
  },
  {
    id: "connections",
    labelKey: "connections",
    items: [
      { id: "integrations", labelKey: "integrations" },
      { id: "mcp_access", labelKey: "mcp_access" },
      { id: "a2a", labelKey: "a2a" },
    ],
  },
  {
    id: "execution",
    labelKey: "execution",
    items: [
      { id: "general", labelKey: "general" },
      { id: "runner", labelKey: "runner" },
      { id: "dsh", labelKey: "dsh" },
      { id: "filesystem", labelKey: "filesystem" },
      { id: "env", labelKey: "environment" },
      { id: "custom_args", labelKey: "custom_args" },
      { id: "runtime_config", labelKey: "runtime_config" },
    ],
  },
  {
    id: "management",
    labelKey: "management",
    items: [
      { id: "export", labelKey: "export" },
      { id: "publish", labelKey: "publish" },
      { id: "access", labelKey: "access" },
      { id: "llm_trace", labelKey: "llm_trace" },
    ],
  },
];

const CONFIG_GROUP_BY_VIEW = new Map<DetailTab, ConfigGroupId>(
  AGENT_CONFIG_GROUPS.flatMap((group) =>
    group.items.map((item) => [item.id, group.id] as const),
  ),
);

const DETAIL_VIEWS = new Set<DetailTab>([
  "overview",
  "work",
  "inbound",
  "memory",
  ...CONFIG_GROUP_BY_VIEW.keys(),
]);

export function normalizeDetailView(value: string | null): DetailTab | null {
  if (value === "dsh_plugins" || value === "dsh_home") return "dsh";
  if (value === "import_export") return "publish";
  if (value === "identity") return "digital_employee";
  if (value !== null && DETAIL_VIEWS.has(value as DetailTab)) {
    return value as DetailTab;
  }
  return null;
}

export function groupForConfigView(view: DetailTab): ConfigGroupId | null {
  return CONFIG_GROUP_BY_VIEW.get(view) ?? null;
}

export function isConfigView(view: DetailTab): boolean {
  return CONFIG_GROUP_BY_VIEW.has(view);
}

export function sectionForView(view: DetailTab): DetailSection {
  if (
    view === "overview" ||
    view === "work" ||
    view === "inbound" ||
    view === "memory"
  ) {
    return view;
  }
  return "configuration";
}
