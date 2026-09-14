// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import { dshHomeKeys } from "@multica/core/agents";
import enAgents from "../../../locales/en/agents.json";
import enCommon from "../../../locales/en/common.json";
import { DshHomeTab } from "./dsh-home-tab";

const calls = vi.hoisted(() => ({ get: vi.fn(), ensure: vi.fn(), entry: vi.fn() }));
vi.mock("@multica/core/api", () => ({ api: {
  getDSHHome: (...args: unknown[]) => calls.get(...args),
  ensureDSHHome: (...args: unknown[]) => calls.ensure(...args),
  issueDSHNativeEntry: (...args: unknown[]) => calls.entry(...args),
} }));
const missing = { provisioned: false, state: "unprovisioned", step: 0, generation: 0, sandboxId: "" };
const ready = { provisioned: true, state: "offline", step: 6, generation: 0, sandboxId: "" };
const clients: QueryClient[] = [];
beforeEach(() => { vi.resetAllMocks(); calls.get.mockResolvedValue(missing); });
afterEach(() => { cleanup(); clients.forEach((c) => c.clear()); clients.length = 0; });

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  clients.push(client);
  render(<I18nProvider locale="en" resources={{ en: { agents: enAgents, common: enCommon } }}>
    <QueryClientProvider client={client}><DshHomeTab workspaceId="ws" agentId="agent" /></QueryClientProvider>
  </I18nProvider>);
  return client;
}

it("only provisions on an explicit action and distinguishes storage from the host", async () => {
  calls.ensure.mockImplementation(async () => { calls.get.mockResolvedValue(ready); return ready; });
  show();
  const button = await screen.findByRole("button", { name: "Prepare Home" });
  await waitFor(() => expect(button).not.toBeDisabled());
  expect(calls.ensure).not.toHaveBeenCalled();
  await userEvent.click(button);
  await screen.findByText("Storage is ready");
  expect(screen.getByText("The host starts when you open DSH or run a task.")).toBeTruthy();
  expect(calls.ensure).toHaveBeenCalledTimes(1);
});

it("reads durable status after an interrupted creation without repeating the write", async () => {
  calls.ensure.mockImplementation(async () => { calls.get.mockResolvedValue(ready); throw new Error("connection lost"); });
  show();
  const button = await screen.findByRole("button", { name: "Prepare Home" });
  await waitFor(() => expect(button).not.toBeDisabled());
  await userEvent.click(button);
  await screen.findByText("Storage is ready");
  expect(screen.queryByRole("alert")).toBeNull();
  expect(calls.ensure).toHaveBeenCalledTimes(1);
});

it("disables provisioning if readiness cannot be parsed", async () => {
  calls.get.mockResolvedValue(null);
  show();
  await screen.findByText("Home status is unavailable. Refresh to check again.");
  expect(screen.getByRole("button", { name: "Prepare Home" })).toBeDisabled();
  expect(calls.ensure).not.toHaveBeenCalled();
});

it("scopes employee Home cache by workspace and employee", () => {
  const client = show();
  client.setQueryData(dshHomeKeys.detail("other-workspace", "agent"), ready);
  expect(client.getQueryData(dshHomeKeys.detail("ws", "agent"))).toBeUndefined();
  expect(dshHomeKeys.detail("ws", "other-agent")).not.toEqual(dshHomeKeys.detail("ws", "agent"));
});

it("opens an explicitly prepared entry without caching its credential", async () => {
  calls.get.mockResolvedValue(ready);
  const entryUrl = "https://33124-sbx-fixture.fc.test/_multica/open#entry=dnge_" + "A".repeat(43);
  calls.entry.mockResolvedValue({ accessId: "access", entryUrl, expiresAt: new Date(Date.now() + 60000).toISOString() });
  const client = show();
  const button = await screen.findByRole("button", { name: "Prepare DSH" });
  await waitFor(() => expect(button).not.toBeDisabled());
  expect(calls.entry).not.toHaveBeenCalled();
  await userEvent.click(button);
  const link = await screen.findByRole("link", { name: "Enter DSH" });
  expect(link).toHaveAttribute("href", entryUrl);
  expect(link).toHaveAttribute("rel", "noopener noreferrer");
  expect(link).toHaveAttribute("referrerpolicy", "no-referrer");
  expect(calls.entry).toHaveBeenCalledWith("ws", "agent");
  expect(JSON.stringify(client.getMutationCache().getAll().map((m) => m.state.data))).not.toContain("dnge_");
  await userEvent.click(link);
  await screen.findByRole("button", { name: "Prepare DSH" });
  expect(screen.queryByRole("link", { name: "Enter DSH" })).toBeNull();
});

it("clears an expired entry and requires another explicit preparation", async () => {
  calls.get.mockResolvedValue(ready);
  calls.entry.mockImplementation(async () => ({ accessId: "access", entryUrl: "https://33124-sbx-fixture.fc.test/_multica/open#entry=dnge_" + "A".repeat(43), expiresAt: new Date(Date.now() + 300).toISOString() }));
  show();
  const button = await screen.findByRole("button", { name: "Prepare DSH" });
  await waitFor(() => expect(button).not.toBeDisabled());
  await userEvent.click(button);
  await screen.findByRole("link", { name: "Enter DSH" });
  await screen.findByRole("button", { name: "Prepare DSH" });
  expect(screen.queryByRole("link", { name: "Enter DSH" })).toBeNull();
  expect(calls.entry).toHaveBeenCalledTimes(1);
});

it.each([null, { accessId: "expired", entryUrl: "secret", expiresAt: "2000-01-01T00:00:00Z" }])("does not navigate or retry an unconfirmed entry", async (entry) => {
  calls.get.mockResolvedValue(ready); calls.entry.mockResolvedValue(entry);
  show();
  const button = await screen.findByRole("button", { name: "Prepare DSH" });
  await waitFor(() => expect(button).not.toBeDisabled());
  await userEvent.click(button);
  await screen.findByRole("alert");
  expect(screen.queryByRole("link", { name: "Enter DSH" })).toBeNull();
  expect(calls.entry).toHaveBeenCalledTimes(1);
});

it("does not show a delayed entry after switching employees", async () => {
  calls.get.mockResolvedValue(ready);
  let complete!: (value: unknown) => void;
  calls.entry.mockReturnValue(new Promise((resolve) => { complete = resolve; }));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  clients.push(client);
  const page = (agentId: string) => <I18nProvider locale="en" resources={{ en: { agents: enAgents, common: enCommon } }}>
    <QueryClientProvider client={client}><DshHomeTab workspaceId="ws" agentId={agentId} /></QueryClientProvider>
  </I18nProvider>;
  const view = render(page("first"));
  const button = await screen.findByRole("button", { name: "Prepare DSH" });
  await waitFor(() => expect(button).not.toBeDisabled());
  await userEvent.click(button);
  view.rerender(page("second"));
  await act(async () => complete({ accessId: "first-access", entryUrl: "https://33124-sbx-first.fc.test/_multica/open#entry=dnge_" + "A".repeat(43), expiresAt: new Date(Date.now() + 60000).toISOString() }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Prepare DSH" })).not.toBeDisabled());
  expect(screen.queryByRole("link", { name: "Enter DSH" })).toBeNull();
  expect(calls.entry).toHaveBeenCalledExactlyOnceWith("ws", "first");
});
