// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen } from "@testing-library/react";
import { renderWithI18n } from "../../test/i18n";
import { AgentConfigNav } from "./agent-config-nav";
import { AGENT_CONFIG_GROUPS } from "./agent-config-navigation";

describe("AgentConfigNav", () => {
  it("shows only the active desktop group and selects a clear default", () => {
    const onSelect = vi.fn();
    renderWithI18n(
      <AgentConfigNav
        groups={AGENT_CONFIG_GROUPS}
        activeView="digital_employee"
        onSelect={onSelect}
      />,
    );

    expect(
      screen.getByRole("button", { name: /^Identity & Goals/i }),
    ).toHaveAttribute("aria-expanded", "true");
    expect(
      screen.getByRole("button", { name: /^Capabilities/i }),
    ).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("tab", { name: "Skills" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /^Capabilities/i }));
    expect(onSelect).toHaveBeenCalledWith("skills");
  });

  it("uses two compact selectors on narrow screens", () => {
    renderWithI18n(
      <AgentConfigNav
        groups={AGENT_CONFIG_GROUPS}
        activeView="digital_employee"
        onSelect={vi.fn()}
      />,
    );

    expect(screen.getAllByRole("combobox")).toHaveLength(2);
    expect(
      screen.getByRole("combobox", { name: "Configuration group" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("combobox", { name: "Configuration page" }),
    ).toBeInTheDocument();
  });
});
