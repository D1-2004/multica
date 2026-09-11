import { useState } from "react";
import { beforeEach, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import { paths } from "@multica/core/paths";
import { NavigationProvider } from "../../navigation";
import enAgents from "../../locales/en/agents.json";
import { BuilderPackagePanel } from "./builder-package-panel";
import { ZIPPublishTab } from "../components/tabs/zip-publish-tab";

const mocked = vi.hoisted(() => ({ prepare:vi.fn(), create:vi.fn(), previewZIP:vi.fn(), confirm:vi.fn(), push:vi.fn(), archive:vi.fn() }));
vi.mock("@multica/core/api", () => ({ api: {
  prepareAgentPackage: mocked.prepare, createAgentFromPackage: mocked.create,
  previewAgentPackagePublication: mocked.previewZIP, syncAgentSource: mocked.confirm,
  getAgentSchemaUrl: () => "https://api.example.test/api/agent-schema",
} }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("@multica/core/paths", async (original) => ({ ...await original<object>(), useWorkspacePaths: () => paths.workspace("acme") }));
vi.mock("sonner", () => ({ toast: { success:vi.fn(), warning:vi.fn(), error:vi.fn() } }));

const content = '{"manifest":{"name":"Builder","configuration":{"persona":"Full"}},"files":{"AGENTS.md":"Full instructions"}}';
const requirements = { secrets:[], deferred_bindings:[], runtime_provider:"" };
function Builder() {
  const [value, setValue] = useState(content);
  return <BuilderPackagePanel content={value} onChange={setValue} runtimeId="runtime-1" runtimeProvider="codex" runtimeControl={null} squadId={null} pending={false} onCreated={mocked.archive} onDiscard={() => {}} discarding={false} />;
}
function mount(zip = false) {
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions:{ queries:{retry:false}, mutations:{retry:false} } })}>
    <I18nProvider locale="en" resources={{ en:{ agents:enAgents } }}>
      <NavigationProvider value={{ push:mocked.push, replace:vi.fn(), back:vi.fn(), pathname:"/acme/agents/new/ai", searchParams:new URLSearchParams(), getShareableUrl:(path) => path }}>
        {zip ? <ZIPPublishTab agentId="agent-1" canEdit hasGitSource={false} /> : <Builder />}
      </NavigationProvider>
    </I18nProvider>
  </QueryClientProvider>);
}
beforeEach(() => {
  vi.clearAllMocks();
  mocked.prepare.mockResolvedValue({ preview_id:"preview-1", package_hash:"hash", name:"Builder", definition:{ configuration:{persona:"Full"} }, instructions:"Full instructions", skills:[], requirements, warnings:[] });
  mocked.create.mockResolvedValue({ agent:{id:"agent-1"}, warnings:[] });
  mocked.previewZIP.mockResolvedValue({ preview_id:"zip-preview", resolved_sha:"hash", requirements, configuration_changes:[{path:"instructions", status:"modified", before:"old", after:"updated"}], warnings:[] });
  mocked.confirm.mockResolvedValue({ source:{agent_id:"agent-1", synced_commit_sha:"hash"}, changed:true, warnings:[] });
  mocked.archive.mockResolvedValue(undefined);
});

it("validates the full Builder package and confirms only the reviewed snapshot", async () => {
  mount();
  expect(screen.getByRole("button", {name:enAgents.creation_studio.create_and_open})).toBeDisabled();
  fireEvent.click(screen.getByRole("button", {name:"Validate and preview"}));
  await screen.findByText("Full instructions");
  expect(mocked.prepare).toHaveBeenCalledWith("workspace-1", content);
  expect(mocked.create).not.toHaveBeenCalled();
  fireEvent.change(screen.getByLabelText("Manifest and files"), {target:{value:content + " "}});
  expect(screen.getByRole("button", {name:enAgents.creation_studio.create_and_open})).toBeDisabled();
  fireEvent.click(screen.getByRole("button", {name:"Validate and preview"}));
  await waitFor(() => expect(screen.getByRole("button", {name:enAgents.creation_studio.create_and_open})).toBeEnabled());
  fireEvent.click(screen.getByRole("button", {name:enAgents.creation_studio.create_and_open}));
  await waitFor(() => expect(mocked.push).toHaveBeenCalledWith("/acme/agents/agent-1"));
  expect(mocked.create).toHaveBeenCalledWith("workspace-1", {preview_id:"preview-1", runtime_id:"runtime-1", secrets:{}, deferred_bindings:[]});
});

it("keeps complete schema diagnostics and the current schema link visible", async () => {
  mocked.prepare.mockRejectedValue(Object.assign(new Error("manifest schema validation failed: expected string"), { body:{validation:{errors:[{instanceLocation:"/configuration/persona", error:"expected string"}, {instanceLocation:"/skills/29", error:"missing property enabled"}]}, schema_url:"/api/agent-schema"} }));
  mount();
  fireEvent.click(screen.getByRole("button", {name:"Validate and preview"}));
  const alert = await screen.findByRole("alert");
  expect(alert).toHaveTextContent("/configuration/persona");
  expect(alert).toHaveTextContent("/skills/29");
  expect(alert).toHaveTextContent("missing property enabled");
  expect(screen.getByRole("link", {name:"Download the current JSON Schema"})).toHaveAttribute("href", "https://api.example.test/api/agent-schema");
  expect(screen.getByRole("button", {name:enAgents.creation_studio.create_and_open})).toBeDisabled();
});

it("does not allow another create while the committed Agent's Builder is being archived", async () => {
  let finishArchive!: () => void;
  mocked.archive.mockImplementation(() => new Promise<void>((resolve) => { finishArchive = resolve; }));
  mount();
  fireEvent.click(screen.getByRole("button", {name:"Validate and preview"}));
  await waitFor(() => expect(screen.getByRole("button", {name:enAgents.creation_studio.create_and_open})).toBeEnabled());
  fireEvent.click(screen.getByRole("button", {name:enAgents.creation_studio.create_and_open}));
  await waitFor(() => expect(mocked.archive).toHaveBeenCalled());
  expect(screen.getByRole("button", {name:enAgents.creation_studio.create_and_open})).toBeDisabled();
  finishArchive();
  await waitFor(() => expect(mocked.push).toHaveBeenCalled());
  expect(mocked.create).toHaveBeenCalledTimes(1);
});

it("uploads an existing Agent ZIP, invalidates changed files and requires confirmation", async () => {
  mount(true);
  const first = new File(["zip"], "agent.zip", {type:"application/zip"});
  fireEvent.change(screen.getByLabelText("Agent ZIP package"), {target:{files:[first]}});
  fireEvent.click(screen.getByRole("button", {name:"Preview changes"}));
  await screen.findByText("updated");
  expect(mocked.previewZIP).toHaveBeenCalledWith("agent-1", first);
  expect(mocked.confirm).not.toHaveBeenCalled();
  fireEvent.change(screen.getByLabelText("Agent ZIP package"), {target:{files:[new File(["zip2"], "new.zip")]}});
  expect(screen.queryByRole("button", {name:"Confirm publication"})).toBeNull();
  fireEvent.click(screen.getByRole("button", {name:"Preview changes"}));
  await screen.findByRole("button", {name:"Confirm publication"});
  fireEvent.click(screen.getByRole("button", {name:"Confirm publication"}));
  await waitFor(() => expect(mocked.confirm).toHaveBeenCalledWith("agent-1", "zip-preview", {secrets:{}, deferred_bindings:[]}));
});
