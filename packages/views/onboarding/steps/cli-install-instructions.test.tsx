import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import { configStore } from "@multica/core/config";
import enCommon from "../../locales/en/common.json";
import enOnboarding from "../../locales/en/onboarding.json";
import { CliInstallInstructions } from "./cli-install-instructions";

const TEST_RESOURCES = { en: { common: enCommon, onboarding: enOnboarding } };

const ligatureClasses = [
  "[font-variant-ligatures:none]",
  "[font-feature-settings:'liga'_0]",
];

function resetConfigStore() {
  configStore.setState({
    daemonServerUrl: "",
    daemonAppUrl: "",
  });
}

describe("CliInstallInstructions", () => {
  it("disables font ligatures in CLI command code", () => {
    resetConfigStore();
    render(
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <CliInstallInstructions />
      </I18nProvider>,
    );

    expect(screen.getByText("multica setup")).toHaveClass(...ligatureClasses);
  });

  it("uses self-host setup when daemon URLs are configured", () => {
    resetConfigStore();
    configStore.getState().setDaemonConfig({
      daemonServerUrl: "https://api.example.com/",
      daemonAppUrl: "https://app.example.com/",
    });
    const { container } = render(
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <CliInstallInstructions />
      </I18nProvider>,
    );

    expect(container).toHaveTextContent(
      "multica setup self-host --server-url https://api.example.com --app-url https://app.example.com",
    );
    expect(container).not.toHaveTextContent("https://api.multica.ai");
  });
});
