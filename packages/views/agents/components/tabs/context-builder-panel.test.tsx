// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ContextNodeDetail, ContextNodeRef } from "@multica/core/context-capabilities";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import { ContextBuilderPanel, type ContextBuilderConnect } from "./context-builder-panel";

const mocks = vi.hoisted(() => ({
  getNode: vi.fn(),
  setPrompts: vi.fn(),
  setBinding: vi.fn(),
  setCredential: vi.fn(),
  deleteCredential: vi.fn(),
  setMcpConfig: vi.fn(),
  startConnection: vi.fn(),
  revokeGrants: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    getContextNode: mocks.getNode,
    setContextNodePrompts: mocks.setPrompts,
    setContextNodeBinding: mocks.setBinding,
    setContextNodeCredential: mocks.setCredential,
    deleteContextNodeCredential: mocks.deleteCredential,
    setContextNodeMcpConfig: mocks.setMcpConfig,
    startContextNodeConnection: mocks.startConnection,
    revokeContextNodeGrants: mocks.revokeGrants,
  },
  errorCode: () => undefined,
}));
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }));

const copy = enAgents.tab_body.context_builder;
const apps = enAgents.tab_body.connected_apps;

const sceneNode: ContextNodeRef = { orgId: "dingA", scopeType: "scene", scopeKey: "cid-group" };

function detailOf(overrides: Partial<ContextNodeDetail> = {}): ContextNodeDetail {
  return {
    scope: { type: "scene", orgId: "dingA", key: "cid-group", title: "Release crew" },
    scene: null,
    prompts: [
      { id: "p1", name: "Tone", order: 1, text: "Be brief.", enabled: true, updatedByName: "Ada", updatedAt: "" },
      { id: "p2", name: "Format", order: 2, text: "Use lists.", enabled: true, updatedByName: "Ada", updatedAt: "" },
    ],
    connectors: [
      {
        id: "conn-wiki",
        name: "Wiki",
        catalogSlug: "",
        authMode: "bearer",
        acceptsCredential: true,
        acceptsPat: false,
        oauthAvailable: false,
        installUrl: "",
        global: false,
        enabled: true,
        credential: { connected: false, account: "" },
      },
      {
        id: "conn-github",
        name: "GitHub",
        catalogSlug: "github",
        authMode: "oauth",
        acceptsCredential: true,
        acceptsPat: true,
        oauthAvailable: true,
        installUrl: "",
        global: false,
        enabled: false,
        credential: { connected: false, account: "" },
      },
    ],
    skills: [{ id: "skill-report", name: "Weekly report", description: "Writes reports", enabled: false }],
    mcpConfig: null,
    mcpConfigRedacted: false,
    canConnect: true,
    rights: null,
    effective: {
      prompts: [
        { name: "Tone", text: "Be formal.", layer: "org", overridden: true, overriddenBy: "scene" },
        { name: "Tone", text: "Be brief.", layer: "scene", overridden: false, overriddenBy: null },
      ],
      connectors: [{ id: "conn-docs", name: "Docs", layer: "global" }],
      skills: [],
      mcpServers: [{ name: "search", layer: "org", overridden: false, overriddenBy: null }],
    },
    ...overrides,
  };
}

function renderPanel({
  node = sceneNode,
  openApp = "",
  canEdit = true,
}: { node?: ContextNodeRef; openApp?: string; canEdit?: boolean } = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const connect: ContextBuilderConnect = {
    returnPath: (slug) => `/acme/agents/agent-1?app=${slug}`,
    navigate: vi.fn(),
    handOff: vi.fn(() => false),
  };
  const onOpenAppChange = vi.fn();
  const onDirtyChange = vi.fn();
  render(
    <I18nProvider locale="en" resources={{ en: { common: enCommon, agents: enAgents } }}>
      <QueryClientProvider client={client}>
        <ContextBuilderPanel
          wsId="ws-1"
          agentId="agent-1"
          node={node}
          canEdit={canEdit}
          connect={connect}
          openApp={openApp}
          onOpenAppChange={onOpenAppChange}
          onDirtyChange={onDirtyChange}
        />
      </QueryClientProvider>
    </I18nProvider>,
  );
  return { connect, onOpenAppChange, onDirtyChange };
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.getNode.mockResolvedValue(detailOf());
  mocks.setPrompts.mockImplementation(
    async (_ws: string, _agent: string, _node: ContextNodeRef, prompts: { name: string; order: number; text: string }[]) =>
      prompts.map((prompt, index) => ({ id: `s${index}`, updatedByName: "", updatedAt: "", ...prompt })),
  );
  mocks.setBinding.mockResolvedValue(undefined);
});

describe("ContextBuilderPanel prompts", () => {
  it("lists the level's prompt components in order", async () => {
    renderPanel();

    const tone = await screen.findByRole("listitem", { name: "Tone" });
    expect(within(tone).getByText("Be brief.")).toBeInTheDocument();
    expect(within(tone).getByText("1")).toBeInTheDocument();
    expect(within(screen.getByRole("listitem", { name: "Format" })).getByText("2")).toBeInTheDocument();
    expect(mocks.getNode).toHaveBeenCalledWith("ws-1", "agent-1", sceneNode);
  });

  it("adds, edits, reorders and deletes components, then saves the whole list", async () => {
    const user = userEvent.setup();
    const { onDirtyChange } = renderPanel();
    await screen.findByRole("listitem", { name: "Tone" });

    // Add.
    await user.click(screen.getByRole("button", { name: copy.prompt_add }));
    let dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByLabelText(copy.prompt_name), "Glossary");
    await user.type(within(dialog).getByLabelText(copy.prompt_text), "PR means pull request.");
    await user.click(within(dialog).getByRole("button", { name: copy.prompt_done }));
    expect(await screen.findByRole("listitem", { name: "Glossary" })).toBeInTheDocument();
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);

    // Edit.
    await user.click(screen.getByRole("button", { name: copy.prompt_edit.replace("{{name}}", "Tone") }));
    dialog = await screen.findByRole("dialog");
    const text = within(dialog).getByLabelText(copy.prompt_text);
    expect(text).toHaveValue("Be brief.");
    await user.clear(text);
    await user.type(text, "Be very brief.");
    await user.click(within(dialog).getByRole("button", { name: copy.prompt_done }));

    // Reorder: Glossary to the top.
    await user.click(screen.getByRole("button", { name: copy.prompt_move_up.replace("{{name}}", "Glossary") }));
    await user.click(screen.getByRole("button", { name: copy.prompt_move_up.replace("{{name}}", "Glossary") }));

    // Delete.
    await user.click(screen.getByRole("button", { name: copy.prompt_delete.replace("{{name}}", "Format") }));
    expect(screen.queryByRole("listitem", { name: "Format" })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: copy.save }));
    await waitFor(() =>
      expect(mocks.setPrompts).toHaveBeenCalledWith("ws-1", "agent-1", sceneNode, [
        { name: "Glossary", order: 1, text: "PR means pull request.", enabled: true },
        { name: "Tone", order: 2, text: "Be very brief.", enabled: true },
      ]),
    );
    await waitFor(() => expect(screen.queryByRole("button", { name: copy.save })).not.toBeInTheDocument());
    expect(onDirtyChange).toHaveBeenLastCalledWith(false);
  });

  it("refuses a duplicate name and an empty content", async () => {
    const user = userEvent.setup();
    renderPanel();
    await screen.findByRole("listitem", { name: "Tone" });

    await user.click(screen.getByRole("button", { name: copy.prompt_add }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByLabelText(copy.prompt_name), "Format");
    await user.type(within(dialog).getByLabelText(copy.prompt_text), "x");
    await user.click(within(dialog).getByRole("button", { name: copy.prompt_done }));
    expect(within(dialog).getByRole("alert")).toHaveTextContent(copy.prompt_name_duplicate);

    await user.clear(within(dialog).getByLabelText(copy.prompt_name));
    await user.type(within(dialog).getByLabelText(copy.prompt_name), "Other");
    await user.clear(within(dialog).getByLabelText(copy.prompt_text));
    await user.click(within(dialog).getByRole("button", { name: copy.prompt_done }));
    expect(within(dialog).getByRole("alert")).toHaveTextContent(copy.prompt_text_required);
    expect(mocks.setPrompts).not.toHaveBeenCalled();
  });

  it("refuses what the server would: control characters in a name, NUL in the content", async () => {
    const user = userEvent.setup();
    renderPanel();
    await screen.findByRole("listitem", { name: "Tone" });

    await user.click(screen.getByRole("button", { name: copy.prompt_add }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(copy.prompt_name), { target: { value: "Two\tparts" } });
    fireEvent.change(within(dialog).getByLabelText(copy.prompt_text), { target: { value: "x" } });
    await user.click(within(dialog).getByRole("button", { name: copy.prompt_done }));
    expect(within(dialog).getByRole("alert")).toHaveTextContent(copy.prompt_name_invalid);

    fireEvent.change(within(dialog).getByLabelText(copy.prompt_name), { target: { value: "Clean" } });
    fireEvent.change(within(dialog).getByLabelText(copy.prompt_text), { target: { value: "a\u0000b" } });
    expect(within(dialog).getByRole("alert")).toHaveTextContent(copy.prompt_text_invalid);
    expect(mocks.setPrompts).not.toHaveBeenCalled();
  });

  it("discards unsaved edits back to the stored list", async () => {
    const user = userEvent.setup();
    renderPanel();

    await user.click(
      await screen.findByRole("button", { name: copy.prompt_delete.replace("{{name}}", "Tone") }),
    );
    expect(screen.queryByRole("listitem", { name: "Tone" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: copy.discard }));
    expect(screen.getByRole("listitem", { name: "Tone" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: copy.save })).not.toBeInTheDocument();
  });

  it("is read-only without edit rights", async () => {
    renderPanel({ canEdit: false });

    expect(await screen.findByRole("listitem", { name: "Tone" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: copy.prompt_add })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: copy.prompt_edit.replace("{{name}}", "Tone") })).not.toBeInTheDocument();
    expect(screen.getByRole("switch", { name: copy.toggle_aria.replace("{{name}}", "Wiki") })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
  });

  it("switches a component off and saves it with the list", async () => {
    const user = userEvent.setup();
    renderPanel();

    await user.click(await screen.findByRole("switch", { name: copy.toggle_aria.replace("{{name}}", "Format") }));
    await user.click(screen.getByRole("button", { name: copy.save }));
    await waitFor(() =>
      expect(mocks.setPrompts).toHaveBeenCalledWith("ws-1", "agent-1", sceneNode, [
        { name: "Tone", order: 1, text: "Be brief.", enabled: true },
        { name: "Format", order: 2, text: "Use lists.", enabled: false },
      ]),
    );
  });
});

describe("ContextBuilderPanel rights", () => {
  const personNode: ContextNodeRef = { orgId: "dingA", scopeType: "person", scopeKey: "staff-1" };
  const personScope = { type: "person" as const, orgId: "dingA", key: "staff-1", title: "Ada" };

  it("follows the server's rights: a manager only views someone's personal level", async () => {
    mocks.getNode.mockResolvedValue(
      detailOf({
        scope: personScope,
        canConnect: false,
        rights: { toggle: false, connect: false, editPrompts: false, editMcp: false },
      }),
    );
    renderPanel({ node: personNode });

    expect(await screen.findByRole("listitem", { name: "Tone" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: copy.prompt_add })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: enAgents.tab_body.mcp_config.add_action })).not.toBeInTheDocument();
    expect(screen.getByRole("switch", { name: copy.toggle_aria.replace("{{name}}", "Wiki") })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
  });

  it("reads a personal level as its person's own when the server sends no rights", async () => {
    mocks.getNode.mockResolvedValue(detailOf({ scope: personScope, canConnect: false }));
    renderPanel({ node: personNode });

    expect(await screen.findByRole("listitem", { name: "Tone" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: copy.prompt_add })).not.toBeInTheDocument();
    expect(screen.getByRole("switch", { name: copy.toggle_aria.replace("{{name}}", "Wiki") })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
  });

  it("lets the person edit their own level", async () => {
    mocks.getNode.mockResolvedValue(detailOf({ scope: personScope, canConnect: true }));
    renderPanel({ node: personNode });

    expect(await screen.findByRole("button", { name: copy.prompt_add })).toBeInTheDocument();
    expect(screen.getByRole("switch", { name: copy.toggle_aria.replace("{{name}}", "Wiki") })).not.toHaveAttribute(
      "aria-disabled",
    );
  });

  it("switches a level's own MCP server off with the disabled flag", async () => {
    const config = { mcpServers: { docs: { url: "https://mcp.example/docs" } } };
    mocks.getNode.mockResolvedValue(detailOf({ mcpConfig: config }));
    mocks.setMcpConfig.mockImplementation(
      async (_ws: string, _agent: string, _node: ContextNodeRef, mcpConfig: unknown) => mcpConfig,
    );
    const user = userEvent.setup();
    renderPanel();

    await user.click(await screen.findByRole("switch", { name: copy.toggle_aria.replace("{{name}}", "docs") }));
    await waitFor(() =>
      expect(mocks.setMcpConfig).toHaveBeenCalledWith("ws-1", "agent-1", sceneNode, {
        mcpServers: { docs: { url: "https://mcp.example/docs", disabled: true } },
      }),
    );
  });
});

describe("ContextBuilderPanel effective preview", () => {
  it("shows each component's layer and the ones a nearer layer replaces", async () => {
    renderPanel();

    const preview = await screen.findByRole("region", { name: copy.effective_title });
    const formal = within(preview).getByText("Be formal.").closest("li")!;
    expect(within(formal).getByText(copy.layer_org)).toBeInTheDocument();
    expect(within(formal).getByText(copy.overridden)).toBeInTheDocument();
    const brief = within(preview).getByText("Be brief.").closest("li")!;
    expect(within(brief).getByText(copy.layer_scene)).toBeInTheDocument();
    expect(within(brief).queryByText(copy.overridden)).not.toBeInTheDocument();
    const docs = within(preview).getByText("Docs").closest("li")!;
    expect(within(docs).getByText(copy.layer_global)).toBeInTheDocument();
    expect(within(preview).getByText("search")).toBeInTheDocument();
  });
});

describe("ContextBuilderPanel capabilities", () => {
  it("switches offered connectors and skills on at this level", async () => {
    const user = userEvent.setup();
    renderPanel();

    expect(await screen.findByRole("switch", { name: copy.toggle_aria.replace("{{name}}", "Wiki") })).toBeChecked();
    await user.click(screen.getByRole("switch", { name: copy.toggle_aria.replace("{{name}}", "Weekly report") }));

    await waitFor(() =>
      expect(mocks.setBinding).toHaveBeenCalledWith("ws-1", "agent-1", sceneNode, {
        resourceType: "skill",
        resourceId: "skill-report",
        enabled: true,
      }),
    );
  });

  function grantAll() {
    const detail = detailOf();
    mocks.getNode.mockResolvedValue({
      ...detail,
      connectors: detail.connectors.map((connector) => ({ ...connector, global: true, enabled: false })),
    });
  }

  it("shows a granted connector as 通用能力 with a token action but no switch", async () => {
    grantAll();
    renderPanel();

    const wiki = await screen.findByRole("listitem", { name: "Wiki" });
    expect(within(wiki).getByText(enAgents.tab_body.connectors.status_enabled)).toBeInTheDocument();
    expect(within(wiki).queryByRole("switch")).not.toBeInTheDocument();
    expect(within(wiki).getByRole("button", { name: copy.token_set })).toBeInTheDocument();
  });

  it("shows a granted app's dialog without the level switch", async () => {
    grantAll();
    renderPanel({ openApp: "github" });

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(copy.common_note)).toBeInTheDocument();
    expect(within(dialog).queryByRole("switch")).not.toBeInTheDocument();
  });

  it("stores a level's token for an Aone FaaS connector", async () => {
    mocks.setCredential.mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderPanel();

    await user.click(await screen.findByRole("button", { name: copy.token_set }));
    await user.type(screen.getByLabelText(copy.token_label.replace("{{name}}", "Wiki")), "secret-token");
    await user.click(screen.getByRole("button", { name: enAgents.internal_mcp.catalog.save }));

    await waitFor(() =>
      expect(mocks.setCredential).toHaveBeenCalledWith("ws-1", "agent-1", sceneNode, {
        connectorId: "conn-wiki",
        bearer: "secret-token",
      }),
    );
  });

  it("connects an app account for the level and returns to its dialog", async () => {
    mocks.startConnection.mockResolvedValue("https://github.com/login/oauth/authorize?state=x");
    const user = userEvent.setup();
    const { connect } = renderPanel({ openApp: "github" });

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("switch", { name: copy.enable_scene })).not.toBeChecked();
    await user.click(within(dialog).getByRole("button", { name: apps.connect }));

    await waitFor(() =>
      expect(mocks.startConnection).toHaveBeenCalledWith("ws-1", "agent-1", sceneNode, {
        connectorId: "conn-github",
        returnTo: "/acme/agents/agent-1?app=github",
      }),
    );
    expect(connect.navigate).toHaveBeenCalledWith("https://github.com/login/oauth/authorize?state=x");
  });

  it("leaves a person's accounts to that person", async () => {
    mocks.getNode.mockResolvedValue(
      detailOf({ scope: { type: "person", orgId: "dingA", key: "staff-1", title: "Ada" }, canConnect: false }),
    );
    renderPanel({ node: { orgId: "dingA", scopeType: "person", scopeKey: "staff-1" }, openApp: "github" });

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("switch", { name: copy.enable_person })).toBeInTheDocument();
    expect(within(dialog).getByText(copy.owner_connects)).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: apps.connect })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: copy.token_set })).not.toBeInTheDocument();
  });

  it("locks custom MCP servers the workspace redacts", async () => {
    mocks.getNode.mockResolvedValue(detailOf({ mcpConfigRedacted: true }));
    renderPanel();

    expect(await screen.findByText(enAgents.tab_body.mcp_config.redacted_title)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: enAgents.tab_body.mcp_config.add_action })).not.toBeInTheDocument();
  });

  it("says when a level cannot be configured", async () => {
    mocks.getNode.mockResolvedValue(detailOf({ scope: null }));
    renderPanel();

    expect(await screen.findByText(copy.scope_unknown)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: copy.prompt_add })).not.toBeInTheDocument();
  });
});

describe("ContextBuilderPanel configure-page access", () => {
  const personNode: ContextNodeRef = { orgId: "dingA", scopeType: "person", scopeKey: "staff-1" };

  it("revokes a person's configure-page access after confirming", async () => {
    mocks.getNode.mockResolvedValue(
      detailOf({ scope: { type: "person", orgId: "dingA", key: "staff-1", title: "Ada" }, canConnect: false }),
    );
    mocks.revokeGrants.mockResolvedValue(2);
    const user = userEvent.setup();
    renderPanel({ node: personNode });

    const section = await screen.findByRole("region", { name: copy.access_title });
    await user.click(within(section).getByRole("button", { name: copy.access_revoke }));
    const confirm = await screen.findByRole("alertdialog");
    expect(within(confirm).getByText(copy.access_revoke_person)).toBeInTheDocument();
    await user.click(within(confirm).getByRole("button", { name: copy.access_revoke }));

    await waitFor(() => expect(mocks.revokeGrants).toHaveBeenCalledWith("ws-1", "agent-1", personNode));
  });

  it("offers no revoke without edit rights", async () => {
    renderPanel({ canEdit: false });
    expect(await screen.findByRole("listitem", { name: "Tone" })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: copy.access_title })).not.toBeInTheDocument();
  });

  it("offers no revoke at the enterprise level, which has no grants", async () => {
    mocks.getNode.mockResolvedValue(detailOf({ scope: { type: "org", orgId: "dingA", key: "dingA", title: "Acme" } }));
    renderPanel({ node: { orgId: "dingA", scopeType: "org", scopeKey: "dingA" } });
    expect(await screen.findByRole("listitem", { name: "Tone" })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: copy.access_title })).not.toBeInTheDocument();
  });
});
