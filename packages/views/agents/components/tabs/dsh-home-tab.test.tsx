// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import { dshHomeKeys } from "@multica/core/agents";
import enAgents from "../../../locales/en/agents.json";
import enCommon from "../../../locales/en/common.json";
import { DshHomeTab } from "./dsh-home-tab";

const calls = vi.hoisted(() => ({ get: vi.fn(), ensure: vi.fn() }));
vi.mock("@multica/core/api", () => ({ api: {
  getDSHHome: (...args: unknown[]) => calls.get(...args),
  ensureDSHHome: (...args: unknown[]) => calls.ensure(...args),
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
  expect(screen.getByText("The host will start when a task needs it.")).toBeTruthy();
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
