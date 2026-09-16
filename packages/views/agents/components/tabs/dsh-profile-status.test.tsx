// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import { dshProfileKeys } from "@multica/core/agents";
import enAgents from "../../../locales/en/agents.json";
import { DshProfileStatus } from "./dsh-profile-status";

const calls = vi.hoisted(() => ({ get: vi.fn(), prepare: vi.fn(), retry: vi.fn() }));
vi.mock("@multica/core/api", () => ({ api: {
  getDSHProfile: (...args: unknown[]) => calls.get(...args),
  prepareDSHProfile: (...args: unknown[]) => calls.prepare(...args),
  retryDSHProfileBuild: (...args: unknown[]) => calls.retry(...args),
} }));
const pending = { state: "pending_host", current: false, desiredRevision: "7", appliedRevision: "4", builds: [] };
const clients: QueryClient[] = [];
beforeEach(() => { vi.resetAllMocks(); calls.get.mockResolvedValue(pending); });
afterEach(() => { cleanup(); clients.forEach((client) => client.clear()); clients.length = 0; });
function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  clients.push(client);
  render(<I18nProvider locale="en" resources={{ en: { agents: enAgents } }}>
    <QueryClientProvider client={client}><DshProfileStatus workspaceId="workspace" agentId="employee" /></QueryClientProvider>
  </I18nProvider>);
  return client;
}

it("distinguishes desired and last confirmed revisions without starting a host", async () => {
  const client = show();
  await screen.findByText("Plugin dependencies are ready. Applying the configuration; active tasks finish first.");
  expect(screen.getByText("7")).toBeTruthy();
  expect(screen.getByText("4")).toBeTruthy();
  expect(calls.prepare).not.toHaveBeenCalled();
  expect(client.getQueryData(dshProfileKeys.detail("other", "employee"))).toBeUndefined();
});

it("shows the failed plugin and does not present it as still preparing", async () => {
  calls.get.mockResolvedValue({ ...pending, state: "build_failed", builds: [{ packageName: "fixture", version: "1.0.0", state: "failed" }] });
  show();
  await screen.findByText("fixture · 1.0.0");
  expect(screen.getByText("Failed")).toBeTruthy();
  expect(screen.queryByText("Preparing plugin dependencies. The configuration is not active yet.")).toBeNull();
  expect(calls.prepare).not.toHaveBeenCalled();
});

it("prepares changed configuration only on explicit action and then reads its receipt", async () => {
  calls.get.mockResolvedValue({ ...pending, state: "configuration_changed" });
  calls.prepare.mockImplementation(async () => { calls.get.mockResolvedValue(pending); return pending; });
  show();
  const button = await screen.findByRole("button", { name: "Prepare configuration" });
  expect(calls.prepare).not.toHaveBeenCalled();
  await userEvent.click(button);
  await screen.findByText("Plugin dependencies are ready. Applying the configuration; active tasks finish first.");
  expect(calls.prepare).toHaveBeenCalledExactlyOnceWith("workspace", "employee");
});

it("does not show cached applied status when the status request fails", async () => {
  calls.get.mockRejectedValue(new Error("unavailable"));
  show();
  await screen.findByText("Configuration status is unavailable. Refresh to check again.");
  expect(screen.queryByText("The running DSH host has confirmed this configuration.")).toBeNull();
});

it("retries only an explicitly selected cleaned attempt and refreshes after an unknown receipt", async () => {
  calls.get.mockResolvedValue({ ...pending, state: "build_failed", builds: [{ id: "attempt-a", canRetry: true, packageName: "fixture", version: "1.0.0", state: "failed" }] });
  calls.retry.mockImplementation(async () => {
    calls.get.mockResolvedValue({ ...pending, state: "waiting_for_builds", builds: [{ id: "attempt-b", canRetry: false, packageName: "fixture", version: "1.0.0", state: "queued" }] });
    throw new Error("receipt lost");
  });
  show();
  const button = await screen.findByRole("button", { name: "Retry build" });
  expect(calls.retry).not.toHaveBeenCalled();
  await userEvent.click(button);
  await screen.findByRole("alert");
  await screen.findByText("Preparing plugin dependencies. The configuration is not active yet.");
  expect(calls.retry).toHaveBeenCalledExactlyOnceWith("workspace", "employee", "7", "attempt-a");
  expect(screen.queryByRole("button", { name: "Retry build" })).toBeNull();
});

it.each([false, undefined])("does not offer retry before cleanup is confirmed (%s)", async (canRetry) => {
  calls.get.mockResolvedValue({ ...pending, state: "build_failed", builds: [{ id: "attempt-a", canRetry, packageName: "fixture", version: "1.0.0", state: "failed" }] });
  show();
  await screen.findByText("fixture · 1.0.0");
  expect(screen.queryByRole("button", { name: "Retry build" })).toBeNull();
  expect(calls.retry).not.toHaveBeenCalled();
});


it("ends startup waiting and lets the user retry the failed revision", async () => {
  calls.get.mockResolvedValue({ ...pending, state: "apply_failed" });
  calls.prepare.mockImplementation(async () => { calls.get.mockResolvedValue(pending); return pending; });
  show();
  await screen.findByText("The plugins were built, but DSH could not start. Check the plugin configuration and retry.");
  await userEvent.click(await screen.findByRole("button", { name: "Retry startup" }));
  await screen.findByText("Plugin dependencies are ready. Applying the configuration; active tasks finish first.");
  expect(calls.prepare).toHaveBeenCalledExactlyOnceWith("workspace", "employee");
});
