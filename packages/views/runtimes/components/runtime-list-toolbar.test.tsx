// @vitest-environment jsdom

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enRuntimes from "../../locales/en/runtimes.json";
import { RuntimeListToolbar } from "./runtime-list-toolbar";

const TEST_RESOURCES = { en: { common: enCommon, runtimes: enRuntimes } };

function renderToolbar(scope: "mine" | "all") {
  const onScopeChange = vi.fn();
  const onSearchChange = vi.fn();
  render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <RuntimeListToolbar
        scope={scope}
        onScopeChange={onScopeChange}
        scopeCounts={{ mine: 2, all: 5 }}
        search=""
        onSearchChange={onSearchChange}
        ownerId={null}
        onOwnerChange={vi.fn()}
        ownerOptions={[
          { id: "user-1", name: "Ada", count: 2 },
          { id: "user-2", name: "Lin", count: 3 },
        ]}
        visibleCount={scope === "mine" ? 2 : 5}
      />
    </I18nProvider>,
  );
  return { onScopeChange, onSearchChange };
}

describe("RuntimeListToolbar", () => {
  it("offers search and the Mine / All ownership scopes", () => {
    const { onScopeChange, onSearchChange } = renderToolbar("mine");

    fireEvent.change(screen.getByRole("searchbox", { name: "Search runtimes…" }), {
      target: { value: "sandbox" },
    });
    fireEvent.click(screen.getByRole("button", { name: "All5" }));

    expect(onSearchChange).toHaveBeenCalledWith("sandbox");
    expect(onScopeChange).toHaveBeenCalledWith("all");
  });

  it("shows the owner picker only in the All scope", () => {
    const { rerender } = render(
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <RuntimeListToolbar
          scope="mine"
          onScopeChange={vi.fn()}
          scopeCounts={{ mine: 2, all: 5 }}
          search=""
          onSearchChange={vi.fn()}
          ownerId={null}
          onOwnerChange={vi.fn()}
          ownerOptions={[{ id: "user-1", name: "Ada", count: 2 }]}
          visibleCount={2}
        />
      </I18nProvider>,
    );

    expect(screen.queryByRole("combobox", { name: "Owner" })).not.toBeInTheDocument();

    rerender(
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <RuntimeListToolbar
          scope="all"
          onScopeChange={vi.fn()}
          scopeCounts={{ mine: 2, all: 5 }}
          search=""
          onSearchChange={vi.fn()}
          ownerId={null}
          onOwnerChange={vi.fn()}
          ownerOptions={[{ id: "user-1", name: "Ada", count: 2 }]}
          visibleCount={5}
        />
      </I18nProvider>,
    );

    expect(screen.getByRole("combobox", { name: "Owner" })).toHaveTextContent(
      "All owners",
    );
  });
});
