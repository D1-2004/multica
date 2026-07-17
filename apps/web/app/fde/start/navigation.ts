type NavigatorSnapshot = Pick<Navigator, "maxTouchPoints" | "platform" | "userAgent">;

type BrowserNavigation = {
  location: Pick<Location, "assign" | "replace">;
  open: Window["open"];
};

type NativeOpenLink = (options: {
  url: string;
  enableShare?: boolean;
}) => Promise<unknown>;

type NativeOpenLinkLoader = () => Promise<NativeOpenLink>;
type NativeClosePage = (options: Record<string, never>) => Promise<unknown>;
type NativeClosePageLoader = () => Promise<NativeClosePage>;

async function loadNativeOpenLink(): Promise<NativeOpenLink> {
  await import("dingtalk-jsapi/entry/mobile");
  const { default: openLink } = await import(
    "dingtalk-jsapi/api/biz/util/openLink"
  );
  return openLink;
}

async function loadNativeClosePage(): Promise<NativeClosePage> {
  await import("dingtalk-jsapi/entry/mobile");
  const { default: closePage } = await import(
    "dingtalk-jsapi/api/biz/navigation/close"
  );
  return closePage;
}

export function isIOSDingTalk(
  navigatorSnapshot: NavigatorSnapshot = navigator,
): boolean {
  const { maxTouchPoints, platform, userAgent } = navigatorSnapshot;
  const isDingTalk = /DingTalk/i.test(userAgent);
  const isIOSDevice = /iPad|iPhone|iPod/i.test(userAgent)
    || (platform === "MacIntel" && maxTouchPoints > 1);
  return isDingTalk && isIOSDevice;
}

export async function openDingTalkInstallPage(
  url: string,
  browser: BrowserNavigation = window,
  navigatorSnapshot: NavigatorSnapshot = navigator,
  nativeOpenLinkLoader: NativeOpenLinkLoader = loadNativeOpenLink,
): Promise<void> {
  if (isIOSDingTalk(navigatorSnapshot)) {
    try {
      const nativeOpenLink = await nativeOpenLinkLoader();
      await nativeOpenLink({ url, enableShare: false });
    } catch {
      // Older iOS clients may not expose the native API. The current-WebView
      // fallback still completes registration, though DingTalk can show an
      // intermediate text-link confirmation in that compatibility path.
      browser.location.assign(url);
    }
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

export async function closeDingTalkPage(
  browser: Pick<Window, "close" | "history"> = window,
  nativeClosePageLoader: NativeClosePageLoader = loadNativeClosePage,
): Promise<void> {
  try {
    const nativeClosePage = await nativeClosePageLoader();
    await nativeClosePage({});
  } catch {
    if (browser.history.length > 1) {
      browser.history.back();
    } else {
      browser.close();
    }
  }
}
