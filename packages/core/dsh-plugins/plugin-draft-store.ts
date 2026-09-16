import { useState } from "react";
import { createStore, useStore } from "zustand";
import type { AgentDshPlugin, UpdateAgentDshPluginConfig } from "./types";

type ConfigChanges = Record<string, UpdateAgentDshPluginConfig>;
interface PluginDraft {
  draft: AgentDshPlugin[] | null;
  base: AgentDshPlugin[] | null;
  configChanges: ConfigChanges;
  setDraft: (draft: AgentDshPlugin[] | null, base?: AgentDshPlugin[]) => void;
  setConfigChanges: (changes: ConfigChanges | ((current: ConfigChanges) => ConfigChanges)) => void;
}

// Scoped to the mounted employee editor. Private settings stay in memory and
// are discarded on navigation; they are never written to browser persistence.
export function useDshPluginDraftStore() {
  const [store] = useState(() => createStore<PluginDraft>((set) => ({
    draft: null,
    base: null,
    configChanges: {},
    setDraft: (draft, base) => set(state => ({ draft, base: draft === null ? null : state.base ?? base ?? [] })),
    setConfigChanges: (changes) => set((state) => ({
      configChanges: typeof changes === "function" ? changes(state.configChanges) : changes,
    })),
  })));
  return useStore(store);
}
