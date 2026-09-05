// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen } from "@testing-library/react";
import { renderWithI18n } from "../../test/i18n";
import { AgentConfigNav } from "./agent-config-nav";
import { AGENT_CONFIG_GROUPS } from "./agent-config-navigation";

describe("AgentConfigNav", () => {
  it("keeps every desktop configuration group and page visible", () => {
    const onSelect = vi.fn();
    renderWithI18n(
      <AgentConfigNav
        groups={AGENT_CONFIG_GROUPS}
        activeView="digital_employee"
        onSelect={onSelect}
      />,
    );

    for (const name of [
      "Identity & Goals",
      "Capabilities",
      "Connections",
      "Execution",
      "Management",
    ]) {
      expect(screen.getAllByText(name).length).toBeGreaterThan(0);
    }
    expect(screen.queryByRole("button", { name: /^Capabilities/i })).not.toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Skills" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Access" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: "Skills" }));
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
