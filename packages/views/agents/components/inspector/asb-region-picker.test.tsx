import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../../../test/i18n";
import { ASBRegionPicker } from "./asb-region-picker";
const state = vi.hoisted(() => ({ error: false }));
vi.mock("@multica/core/runtimes", () => ({
  useASBRegions: () => ({
    data: state.error
      ? undefined
      : { available: true, regions: ["cn-hangzhou", "cn-new-region"] },
    isError: state.error,
    isPending: false,
    isSuccess: !state.error,
    refetch: vi.fn(),
  }),
}));
const agent = {
  id: "agent-1",
  workspace_id: "workspace-1",
  runtime_id: "runtime-1",
  runtime_config: { other: { enabled: true } },
} as unknown as Agent;
describe("ASB region picker", () => {
  afterEach(cleanup);
  beforeEach(() => {
    state.error = false;
  });
  it("saves only Hangzhou while preserving other configuration", async () => {
    const onSave = vi.fn(async () => {});
    renderWithI18n(<ASBRegionPicker agent={agent} canEdit onSave={onSave} />);
    expect(screen.queryByLabelText("cn-zhangjiakou")).toBeNull();
    fireEvent.click(screen.getByLabelText("cn-new-region"));
    fireEvent.click(screen.getByRole("button", { name: "Save regions" }));
    await waitFor(() =>
      expect(onSave).toHaveBeenCalledWith({
        runtime_config: {
          other: { enabled: true },
          asb_regions: ["cn-hangzhou"],
        },
      }),
    );
  });
  it("does not turn an empty manual selection into automatic placement", () => {
    renderWithI18n(
      <ASBRegionPicker
        agent={{ ...agent, runtime_config: { asb_regions: ["cn-hangzhou"] } }}
        canEdit
        onSave={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByLabelText("cn-hangzhou"));
    expect(screen.getByRole("button", { name: "Save regions" })).toBeDisabled();
  });
  it("preserves selection and blocks saving when discovery fails", () => {
    state.error = true;
    renderWithI18n(
      <ASBRegionPicker
        agent={{ ...agent, runtime_config: { asb_regions: ["cn-hangzhou"] } }}
        canEdit
        onSave={vi.fn()}
      />,
    );
    expect(screen.getByText(/Could not load ASB regions/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Save regions" })).toBeDisabled();
  });
});
