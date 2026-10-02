import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent, AgentRuntime } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { AgentDetailInspector } from "./agent-detail-inspector";

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: () => ({ data: undefined, isSuccess: false }),
}));

// The picker is the only way agent labels ever reached the agent settings
// form. Mocking it means the assertion below fails loudly if the row is
// re-added, instead of passing because a real picker rendered nothing.
vi.mock("../../labels/resource-label-picker", () => ({
  ResourceLabelPicker: () => <div data-testid="resource-label-picker" />,
}));

vi.mock("../../common/avatar-upload-control", () => ({
  AvatarUploadControl: () => <div data-testid="avatar-upload" />,
}));

vi.mock("./inspector/model-picker", () => ({
  ModelPicker: () => <div data-testid="model-picker" />,
}));

vi.mock("./inspector/runtime-picker", () => ({
  RuntimePicker: () => <div data-testid="runtime-picker" />,
}));

vi.mock("./inspector/thinking-prop-row", () => ({
  ThinkingSettingField: () => <div data-testid="thinking-field" />,
}));

vi.mock("./inspector/service-tier-setting-field", () => ({
  ServiceTierSettingField: () => <div data-testid="service-tier-field" />,
}));

vi.mock("./integrations/github-identity-binding", () => ({
  GitHubIdentityBindingCard: () => (
    <section aria-label="GitHub sandbox identity" />
  ),
}));

const agent = {
  id: "agent-1",
  workspace_id: "workspace-1",
  name: "Lambda",
  description: "Test agent",
  runtime_id: "runtime-1",
} as Agent;

const fcRuntime = {
  id: "runtime-1",
  runtime_mode: "cloud",
  provider: "pi",
  metadata: { kind: "fc-e2b" },
} as AgentRuntime;

describe("AgentDetailInspector labels", () => {
  afterEach(cleanup);
  // Agent labels were removed from the product (MUL-5600). Label Settings no
  // longer manages an agent catalog, so an attach-only picker here would be a
  // dead end pointing at a catalog the user cannot populate.
  it("does not offer a label picker", () => {
    renderWithI18n(
      <AgentDetailInspector
        agent={agent}
        runtime={null}
        runtimes={[]}
        members={[]}
        currentUserId="user-1"
        canEdit
        onUpdate={vi.fn(async () => {})}
      />,
    );

    // Sanity check: runtime configuration actually rendered.
    expect(screen.getByTestId("runtime-picker")).toBeInTheDocument();

    expect(screen.queryByTestId("resource-label-picker")).toBeNull();
    expect(screen.queryByText("Labels")).toBeNull();
  });

  it("keeps Local Runner out of runtime configuration", () => {
    renderWithI18n(
      <AgentDetailInspector
        agent={agent}
        runtime={null}
        runtimes={[]}
        members={[]}
        currentUserId="user-1"
        canEdit
        onUpdate={vi.fn(async () => {})}
      />,
    );

    expect(screen.queryByText("Local Runner")).not.toBeInTheDocument();
    expect(screen.queryByTestId("runner-picker")).not.toBeInTheDocument();
    expect(screen.queryByRole("switch", { name: "Event trigger" })).toBeNull();
  });

  it("places GitHub sandbox identity with runtime configuration", () => {
    renderWithI18n(
      <AgentDetailInspector
        agent={agent}
        runtime={null}
        runtimes={[]}
        members={[]}
        currentUserId="user-1"
        canEdit
        onUpdate={vi.fn(async () => {})}
      />,
    );

    expect(
      screen.getByRole("region", { name: "GitHub sandbox identity" }),
    ).toBeInTheDocument();
  });

  it("keeps scene memory flags off the settings form", () => {
    renderWithI18n(
      <AgentDetailInspector
        agent={agent}
        runtime={null}
        runtimes={[]}
        members={[]}
        currentUserId="user-1"
        canEdit
        onUpdate={vi.fn(async () => {})}
      />,
    );

    expect(screen.queryByLabelText("Write")).toBeNull();
    expect(screen.queryByLabelText("Recall")).toBeNull();
    expect(screen.queryByLabelText("Show list")).toBeNull();
    expect(screen.queryByRole("switch", { name: "Sandbox reuse" })).toBeNull();
  });

  it("turns sandbox reuse off from beside concurrency", async () => {
    const user = userEvent.setup();
    const onUpdate = vi.fn(async () => {});
    renderWithI18n(
      <AgentDetailInspector
        agent={agent}
        runtime={fcRuntime}
        runtimes={[]}
        members={[]}
        currentUserId="user-1"
        canEdit
        onUpdate={onUpdate}
      />,
    );

    const toggle = screen.getByRole("switch", { name: "Sandbox reuse" });
    expect(toggle).toBeChecked();
    await user.click(toggle);
    expect(onUpdate).toHaveBeenCalledWith("agent-1", {
      sandbox_connection_reuse: false,
    });
  });
});
