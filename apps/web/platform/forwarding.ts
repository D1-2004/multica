import type { StorageAdapter } from "@multica/core/types";

/** The gateway owns target authorization; the browser only derives its namespace. */
export function browserForwarding() {
  if (typeof window === "undefined") return null;
  const match = /^\/forward\/([a-z][a-z0-9-]{0,31})\/dingtalk\/configure\/?$/.exec(
    window.location.pathname,
  );
  const target = match?.[1];
  return target ? {
    basePath: `/forward/${target}`,
    namespace: `mf_${target}_`,
    csrfCookieName: `mf_${target}_multica_csrf`,
  } : null;
}

export function forwardStorageKey(key: string): string {
  return `${browserForwarding()?.namespace ?? ""}${key}`;
}

/** Capture the namespace at boot so logout/cleanup cannot touch another session. */
export function createForwardStorage(namespace: string): StorageAdapter {
  return {
    getItem(key) {
      try {
        return window.localStorage.getItem(`${namespace}${key}`);
      } catch {
        return null;
      }
    },
    setItem(key, value) {
      try {
        window.localStorage.setItem(`${namespace}${key}`, value);
      } catch {
        // Storage may be unavailable in WebViews.
      }
    },
    removeItem(key) {
      try {
        window.localStorage.removeItem(`${namespace}${key}`);
      } catch {
        // Storage may be unavailable in WebViews.
      }
    },
  };
}
