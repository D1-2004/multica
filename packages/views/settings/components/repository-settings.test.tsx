import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enSettings from "../../locales/en/settings.json";
import { RepositorySettings } from "./repository-settings";

const navigation = vi.hoisted(() => ({
  pathname: "/acme/settings",
  searchParams: new URLSearchParams(),
  replace: vi.fn(),
}));

vi.mock("../../navigation", () => ({ useNavigation: () => navigation }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("./repositories-tab", () => ({ RepositoriesTab: () => <div>Repository picker</div> }));
vi.mock("./repository-connections", () => ({ RepositoryConnections: () => <div>Identity management</div> }));
vi.mock("./github-collaboration-settings", () => ({ GitHubCollaborationSettings: () => <div>Collaboration preferences</div> }));

function renderSettings() {
  return render(
    <I18nProvider locale="en" resources={{ en: { settings: enSettings } }}>
      <RepositorySettings />
    </I18nProvider>,
  );
}

describe("RepositorySettings", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    navigation.searchParams = new URLSearchParams("tab=repositories");
  });

  it.each(["git", "github"])("opens access identities for an external %s bookmark", (tab) => {
    navigation.searchParams = new URLSearchParams({ tab, github_connected: "1" });
    renderSettings();
    expect(screen.getByRole("tab", { name: "Access identities" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("Identity management")).toBeVisible();
    expect(screen.queryByText("Repository picker")).not.toBeInTheDocument();
  });

  it("defaults to repository selection and uses canonical URLs when changing sections", async () => {
    const user = userEvent.setup();
    renderSettings();
    expect(screen.getByText("Repository picker")).toBeVisible();
    await user.click(screen.getByRole("tab", { name: "Access identities" }));
    expect(navigation.replace).toHaveBeenCalledWith("/acme/settings?tab=repositories&section=connections");
    await user.click(screen.getByRole("tab", { name: "Collaboration settings" }));
    expect(navigation.replace).toHaveBeenCalledWith("/acme/settings?tab=repositories&section=collaboration");
  });

  it("opens the section selected by a direct import link", () => {
    navigation.searchParams = new URLSearchParams("tab=repositories&section=connections");
    renderSettings();
    expect(screen.getByText("Identity management")).toBeVisible();
    expect(screen.queryByText("Collaboration preferences")).not.toBeInTheDocument();
  });
});
