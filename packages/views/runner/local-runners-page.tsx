"use client";

import { LocalRunnerTab } from "../settings/components/local-runner-tab";

/** Workspace entry point for account-owned runners available to its agents. */
export function LocalRunnersPage() {
  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto w-full max-w-5xl p-4 sm:p-6 md:p-8">
        <LocalRunnerTab />
      </div>
    </div>
  );
}
