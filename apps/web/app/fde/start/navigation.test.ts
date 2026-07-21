import { describe, expect, it, vi } from "vitest";
import { closeDingTalkPage, isIOSDingTalk, openDingTalkInstallPage } from "./navigation";

const installURL = "https://open-dev.dingtalk.com/fe/app-registration?user_code=abc";

function navigatorSnapshot(
  userAgent: string,
  platform = "",
  maxTouchPoints = 0,
) {
  return { userAgent, platform, maxTouchPoints };
}

function browserNavigation() {
  return {
    location: {
      assign: vi.fn(),
      replace: vi.fn(),
    },
    open: vi.fn(),
  };
}

describe("FDE DingTalk navigation", () => {
  it("opens iPhone DingTalk registration through the native openLink API", async () => {
    const browser = browserNavigation();
    const mobileNavigator = navigatorSnapshot(
      "Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AliApp(DingTalk/7.6.50)",
      "iPhone",
      5,
    );
    const nativeOpenLink = vi.fn().mockResolvedValue({});
    const loadNativeOpenLink = vi.fn().mockResolvedValue(nativeOpenLink);

    await openDingTalkInstallPage(
      installURL,
      browser,
      mobileNavigator,
      loadNativeOpenLink,
    );

    expect(loadNativeOpenLink).toHaveBeenCalledOnce();
    expect(nativeOpenLink).toHaveBeenCalledWith({
      url: installURL,
      enableShare: false,
    });
    expect(browser.location.assign).not.toHaveBeenCalled();
    expect(browser.open).not.toHaveBeenCalled();
  });

  it("falls back to the current WebView when native openLink is unavailable", async () => {
    const browser = browserNavigation();
    const mobileNavigator = navigatorSnapshot(
      "Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AliApp(DingTalk/7.6.50)",
      "iPhone",
      5,
    );
    const loadNativeOpenLink = vi.fn().mockRejectedValue(new Error("unsupported"));

    await openDingTalkInstallPage(
      installURL,
      browser,
      mobileNavigator,
      loadNativeOpenLink,
    );

    expect(browser.location.assign).toHaveBeenCalledWith(installURL);
    expect(browser.open).not.toHaveBeenCalled();
  });

  it("recognizes iPadOS desktop-style user agents as iOS", () => {
    expect(
      isIOSDingTalk(
        navigatorSnapshot(
          "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15) AliApp(DingTalk/7.6.50)",
          "MacIntel",
          5,
        ),
      ),
    ).toBe(true);
  });

  it("preserves the working Android DingTalk new-window flow", async () => {
    const browser = browserNavigation();
    const mobileNavigator = navigatorSnapshot(
      "Mozilla/5.0 (Linux; Android 15) AliApp(DingTalk/7.6.50)",
      "Linux armv8l",
      5,
    );
    const loadNativeOpenLink = vi.fn();

    await openDingTalkInstallPage(
      installURL,
      browser,
      mobileNavigator,
      loadNativeOpenLink,
    );

    expect(browser.open).toHaveBeenCalledWith(
      installURL,
      "_blank",
      "noopener,noreferrer",
    );
    expect(browser.location.assign).not.toHaveBeenCalled();
    expect(loadNativeOpenLink).not.toHaveBeenCalled();
  });

  it("closes the onboarding WebView through the native DingTalk API", async () => {
    const browser = {
      close: vi.fn(),
      history: { length: 2, back: vi.fn() },
    };
    const nativeClosePage = vi.fn().mockResolvedValue({});
    const loadNativeClosePage = vi.fn().mockResolvedValue(nativeClosePage);

    await closeDingTalkPage(browser as unknown as Pick<Window, "close" | "history">, loadNativeClosePage);

    expect(nativeClosePage).toHaveBeenCalledWith({});
    expect(browser.history.back).not.toHaveBeenCalled();
    expect(browser.close).not.toHaveBeenCalled();
  });

  it("falls back to browser history when the native close API is unavailable", async () => {
    const browser = {
      close: vi.fn(),
      history: { length: 2, back: vi.fn() },
    };
    const loadNativeClosePage = vi.fn().mockRejectedValue(new Error("unsupported"));

    await closeDingTalkPage(browser as unknown as Pick<Window, "close" | "history">, loadNativeClosePage);

    expect(browser.history.back).toHaveBeenCalledOnce();
    expect(browser.close).not.toHaveBeenCalled();
  });
});
