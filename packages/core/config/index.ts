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
  // When true, the login screen shows DingTalk as the only sign-in method.
  dingtalkOnly: boolean;
  daemonServerUrl: string;
  daemonAppUrl: string;
  // Self-host gate (#3433): when true, every "Create workspace" affordance
  // must be hidden. Defaults to false so unknown / older servers behave like
  // the managed-cloud case.
  workspaceCreationDisabled: boolean;
  // True once /api/config has resolved (on success OR failure). The login
  // screen gates its render on this so a DingTalk-only deployment doesn't
  // flash the email form before the config flips it to DingTalk-only.
  authConfigLoaded: boolean;
  setCdnConfig: (config: { cdnDomain: string; cdnSigned?: boolean }) => void;
  setAuthConfig: (config: {
    allowSignup: boolean;
    googleClientId?: string;
    dingtalkClientId?: string;
    dingtalkOnly?: boolean;
    workspaceCreationDisabled?: boolean;
  }) => void;
  setDaemonConfig: (config: {
    daemonServerUrl?: string;
    daemonAppUrl?: string;
  }) => void;
}

export const configStore = createStore<ConfigState>((set) => ({
  cdnDomain: "",
  cdnSigned: false,
  allowSignup: true,
  googleClientId: "",
  dingtalkClientId: "",
  dingtalkOnly: false,
  daemonServerUrl: "",
  daemonAppUrl: "",
  workspaceCreationDisabled: false,
  authConfigLoaded: false,
  setCdnConfig: ({ cdnDomain, cdnSigned = false }) => set({ cdnDomain, cdnSigned }),
  setAuthConfig: ({
    allowSignup,
    googleClientId = "",
    dingtalkClientId = "",
    dingtalkOnly = false,
    workspaceCreationDisabled = false,
  }) =>
    set({
      allowSignup,
      googleClientId,
      dingtalkClientId,
      dingtalkOnly,
      workspaceCreationDisabled,
      authConfigLoaded: true,
    }),
  setDaemonConfig: ({ daemonServerUrl = "", daemonAppUrl = "" }) =>
    set({ daemonServerUrl, daemonAppUrl }),
}));

export function useConfigStore(): ConfigState;
export function useConfigStore<T>(selector: (state: ConfigState) => T): T;
export function useConfigStore<T>(selector?: (state: ConfigState) => T) {
  return useStore(configStore, selector as (state: ConfigState) => T);
}
