type NavigatorSnapshot = Pick<Navigator, "maxTouchPoints" | "platform" | "userAgent">;

type BrowserNavigation = {
  location: Pick<Location, "assign" | "replace">;
  open: Window["open"];
};

export function isIOSDingTalk(
  navigatorSnapshot: NavigatorSnapshot = navigator,
): boolean {
  const { maxTouchPoints, platform, userAgent } = navigatorSnapshot;
  const isDingTalk = /DingTalk/i.test(userAgent);
  const isIOSDevice = /iPad|iPhone|iPod/i.test(userAgent)
    || (platform === "MacIntel" && maxTouchPoints > 1);
  return isDingTalk && isIOSDevice;
}

export function openDingTalkInstallPage(
  url: string,
  browser: BrowserNavigation = window,
  navigatorSnapshot: NavigatorSnapshot = navigator,
): void {
  if (isIOSDingTalk(navigatorSnapshot)) {
    // iOS DingTalk blocks async _blank popups and may reopen the target in a
    // desktop-style web context. Staying in the current WebView preserves the
    // DingTalk mobile context and allows its app-registration handoff to run.
    browser.location.assign(url);
    return;
  }
  browser.open(url, "_blank", "noopener,noreferrer");
}

export function replaceCurrentPage(
  url: string,
  browser: BrowserNavigation = window,
): void {
  browser.location.replace(url);
}
