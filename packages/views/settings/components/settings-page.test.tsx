import { fireEvent, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { SidebarProvider, useSidebar } from "@multica/ui/components/ui/sidebar";
import { renderWithI18n } from "../../test/i18n";

// This file tests the settings SHELL — the chrome around the tabs — so every
// tab panel is stubbed out. Their contents have their own test files.
const stub = vi.hoisted(
  () => (name: string) => () => ({ [name]: () => <div>{name}</div> }),
);
vi.mock("@multica/core/global-models", () => ({useDeveloperCapabilities:()=>({data:{developer:false}})}));
vi.mock("./developer-tab", stub("DeveloperTab"));
vi.mock("./account-tab", stub("AccountTab"));
vi.mock("./preferences-tab", stub("PreferencesTab"));
vi.mock("./chat-tab", stub("ChatTab"));
vi.mock("./issue-tab", stub("IssueTab"));
vi.mock("./tokens-tab", stub("TokensTab"));
vi.mock("./mcp-connections-tab", stub("MCPConnectionsTab"));
vi.mock("./local-runner-tab", stub("LocalRunnerTab"));
vi.mock("./workspace-tab", stub("WorkspaceTab"));
vi.mock("./members-tab", stub("MembersTab"));
vi.mock("./repositories-tab", stub("RepositoriesTab"));
vi.mock("./repository-settings", stub("RepositorySettings"));
vi.mock("./integrations-tab", stub("IntegrationsTab"));
vi.mock("./labs-tab", stub("LabsTab"));
vi.mock("./notifications-tab", stub("NotificationsTab"));
vi.mock("./labels-tab", stub("LabelsTab"));
vi.mock("./properties-tab", stub("PropertiesTab"));
vi.mock("./quick-actions-tab", stub("QuickActionsTab"));
vi.mock("./hosted-sites-tab", stub("HostedSitesTab"));
vi.mock("./keyboard-shortcuts-tab", stub("KeyboardShortcutsTab"));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ name: "Acme" }),
}));

vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({ role: "member" }),
}));

const replace = vi.fn();
vi.mock("../../navigation", () => ({
  useNavigation: () => ({
    searchParams: new URLSearchParams(),
    pathname: "/acme/settings",
    replace,
  }),
}));

// Compact by default: that is the width where the nav is a sheet and this
// trigger is the only way to reach it.
const layout = { compact: true };
vi.mock("@multica/ui/hooks/use-mobile", () => ({
  useIsMobile: () => layout.compact,
  useIsCompact: () => layout.compact,
}));

import { SettingsPage } from "./settings-page";

function NavStateProbe() {
  const { openMobile } = useSidebar();
  return <div data-testid="nav-open">{String(openMobile)}</div>;
}

function trigger() {
  return screen.getByRole("button", { name: "Toggle Sidebar" });
}

beforeEach(() => {
  vi.stubGlobal("localStorage", {getItem:()=>null,setItem:()=>{},removeItem:()=>{}});
  layout.compact = true;
  replace.mockClear();
});

describe("SettingsPage nav trigger", () => {
  it("opens the nav from settings at compact widths", () => {
    // Settings builds its own chrome instead of a PageHeader, so without this
    // control a touch user who lands here has no way back to the nav at all —
    // the keyboard shortcut is not an answer on a tablet.
    renderWithI18n(
      <SidebarProvider>
        <NavStateProbe />
        <SettingsPage />
      </SidebarProvider>,
    );

    expect(screen.getByTestId("nav-open").textContent).toBe("false");

    fireEvent.click(trigger());

    expect(screen.getByTestId("nav-open").textContent).toBe("true");
  });

  it("hides the trigger only where the nav is a permanent column", () => {
    // The nav is in-flow from `xl` up, so the control is CSS-gated rather than
    // unmounted — jsdom applies no stylesheet, hence the class assertion.
    renderWithI18n(
      <SidebarProvider>
        <SettingsPage />
      </SidebarProvider>,
    );

    expect(trigger().className).toContain("xl:hidden");
  });

  it("still renders standalone, without a sidebar around it", () => {
    // Desktop mounts settings inside its own shell; the trigger has to no-op
    // rather than throw when there is no SidebarProvider above it.
    renderWithI18n(<SettingsPage />);

    expect(
      screen.queryByRole("button", { name: "Toggle Sidebar" }),
    ).not.toBeInTheDocument();
    expect(screen.getByText("Settings")).toBeInTheDocument();
  });
});

describe("SettingsPage MCP connections tab", () => {
  it("exposes MCP connections as its own account tab", () => {
    renderWithI18n(<SettingsPage />);

    expect(
      screen.getByRole("tab", { name: "MCP Connections" }),
    ).toBeInTheDocument();
  });
});

describe("SettingsPage My Computer tab", () => {
  it("exposes connected computers under My Account", () => {
    renderWithI18n(<SettingsPage />);

    expect(
      screen.getByRole("tab", { name: "My Computer" }),
    ).toBeInTheDocument();
  });
});

describe("SettingsPage hosted websites tab", () => {
  it("exposes user-owned hosted websites in the workspace settings group", () => {
    renderWithI18n(<SettingsPage />);

    expect(screen.getByRole("tab", { name: "Websites" })).toBeInTheDocument();
  });
});


describe("SettingsPage repository navigation", () => {
  it("provides one repository entry without provider or protocol tabs", () => {
    renderWithI18n(<SettingsPage />);
    expect(screen.getAllByRole("tab", { name: "Repositories" })).toHaveLength(1);
    expect(screen.queryByRole("tab", { name: "Git" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "GitHub" })).not.toBeInTheDocument();
  });
});
