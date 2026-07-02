import { createStore } from "zustand/vanilla";
import { useStore } from "zustand";

interface ConfigState {
  cdnDomain: string;
  // True when cdnDomain serves private content via time-bounded signed URLs
  // (CloudFront signing enabled server-side). Renderers must not treat a raw
  // storage URL on that domain as a loadable media source (MUL-3254).
  cdnSigned: boolean;
  allowSignup: boolean;
  googleClientId: string;
  dingtalkClientId: string;
  // Sign-in allowlist from LOGIN_PROVIDERS (e.g. ["dingtalk","lark"]). The
  // login screen must only offer the listed entry points; the unlisted login
  // routes are closed server-side. Empty means unrestricted.
  loginProviders: string[];
  larkClientId: string;
  daemonServerUrl: string;
  daemonAppUrl: string;
  // Self-host gate (#3433): when true, every "Create workspace" affordance
  // must be hidden. Defaults to false so unknown / older servers behave like
  // the managed-cloud case.
  workspaceCreationDisabled: boolean;
  featureFlags: Record<string, boolean>;
  // True once /api/config has resolved (on success OR failure). The login
  // screen gates its render on this so a provider-locked deployment doesn't
  // flash the email form before the config applies the lock.
  authConfigLoaded: boolean;
  setCdnConfig: (config: { cdnDomain: string; cdnSigned?: boolean }) => void;
  setAuthConfig: (config: {
    allowSignup: boolean;
    googleClientId?: string;
    dingtalkClientId?: string;
    loginProviders?: string[];
    larkClientId?: string;
    workspaceCreationDisabled?: boolean;
  }) => void;
  setDaemonConfig: (config: {
    daemonServerUrl?: string;
    daemonAppUrl?: string;
  }) => void;
  setFeatureFlags: (flags?: Record<string, boolean>) => void;
}

export const configStore = createStore<ConfigState>((set) => ({
  cdnDomain: "",
  cdnSigned: false,
  allowSignup: true,
  googleClientId: "",
  dingtalkClientId: "",
  loginProviders: [],
  larkClientId: "",
  daemonServerUrl: "",
  daemonAppUrl: "",
  workspaceCreationDisabled: false,
  featureFlags: {},
  authConfigLoaded: false,
  setCdnConfig: ({ cdnDomain, cdnSigned = false }) => set({ cdnDomain, cdnSigned }),
  setAuthConfig: ({
    allowSignup,
    googleClientId = "",
    dingtalkClientId = "",
    loginProviders = [],
    larkClientId = "",
    workspaceCreationDisabled = false,
  }) =>
    set({
      allowSignup,
      googleClientId,
      dingtalkClientId,
      loginProviders,
      larkClientId,
      workspaceCreationDisabled,
      authConfigLoaded: true,
    }),
  setDaemonConfig: ({ daemonServerUrl = "", daemonAppUrl = "" }) =>
    set({ daemonServerUrl, daemonAppUrl }),
  setFeatureFlags: (flags = {}) => set({ featureFlags: { ...flags } }),
}));

// isLoginProviderAllowed reports whether a sign-in entry point may be
// offered. An empty allowlist (older servers, or no LOGIN_PROVIDERS set)
// allows everything. Provider names: "email" | "google" | "dingtalk" | "lark".
export function isLoginProviderAllowed(
  providers: string[],
  name: string,
): boolean {
  return providers.length === 0 || providers.includes(name);
}

export function useConfigStore(): ConfigState;
export function useConfigStore<T>(selector: (state: ConfigState) => T): T;
export function useConfigStore<T>(selector?: (state: ConfigState) => T) {
  return useStore(configStore, selector as (state: ConfigState) => T);
}

export function featureFlagEnabled(
  flags: Readonly<Record<string, boolean>> | undefined,
  key: string,
  defaultValue = false,
): boolean {
  return flags?.[key] ?? defaultValue;
}

export function useFeatureEnabled(key: string, defaultValue = false): boolean {
  return useConfigStore((state) =>
    featureFlagEnabled(state.featureFlags, key, defaultValue),
  );
}
