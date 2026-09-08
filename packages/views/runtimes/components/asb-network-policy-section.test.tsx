// @vitest-environment jsdom
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import enRuntimes from "../../locales/en/runtimes.json";
const query = vi.hoisted(() => ({ isPending: false, isError: false, refetch: vi.fn(), data: { available: true, defaultTargets: ["mcp.dingtalk.com"], customTargets: ["old.example"], effectiveTargets: [] } }));
const update = vi.hoisted(() => ({ isPending: false, mutateAsync: vi.fn() }));
const toastError = vi.hoisted(() => vi.fn());
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("@multica/core/runtimes", () => ({ useASBNetworkPolicy: () => query, useUpdateASBNetworkPolicy: () => update }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: toastError } }));
import { ASBNetworkPolicySection } from "./asb-network-policy-section";
function show() { return render(<I18nProvider locale="en" resources={{ en: { runtimes: enRuntimes } }}><ASBNetworkPolicySection runtimeId="asb-1" /></I18nProvider>); }
describe("ASB network allowlist", () => {
 beforeEach(() => { vi.clearAllMocks(); query.isError = false; query.data.available = true; update.mutateAsync.mockResolvedValue({ available: true }); });
 it("shows default services separately and saves only user additions", async () => {
  show(); expect(screen.getByText("mcp.dingtalk.com")).toBeInTheDocument();
  fireEvent.change(screen.getByLabelText("Additional destinations"), { target: { value: "new.example\n other.example" } });
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(update.mutateAsync).toHaveBeenCalledWith(["new.example", "other.example"]));
 });
 it("keeps the draft when saving fails", async () => {
  update.mutateAsync.mockRejectedValue(new Error("Wildcards are not allowed")); show();
  fireEvent.change(screen.getByLabelText("Additional destinations"), { target: { value: "*.example.com" } });
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(toastError).toHaveBeenCalledWith("Wildcards are not allowed"));
  expect(screen.getByLabelText("Additional destinations")).toHaveValue("*.example.com");
 });
 it("does not offer an empty editable list after a malformed response", () => {
  query.data.available = false; show(); expect(screen.queryByRole("textbox")).not.toBeInTheDocument(); expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
 });
});
