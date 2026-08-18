"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { useAuthStore } from "@multica/core/auth";
import {
  completeOnboarding,
  type OnboardingStep,
} from "@multica/core/onboarding";
import { setCurrentWorkspace } from "@multica/core/platform";
import { workspaceListOptions } from "@multica/core/workspace/queries";
import type { Workspace } from "@multica/core/types";
import { StepShell } from "./components/step-shell";
import { StepWorkspace } from "./steps/step-workspace";
import { useT } from "../i18n";

interface OnboardingFlowProps {
  onComplete: (
    workspace?: Workspace,
    destination?: OnboardingDestination,
  ) => void;
  mode?: OnboardingMode;
  onCancel?: () => void;
  runtimeInstructions?: React.ReactNode;
  onRuntimeRefresh?: () => void | Promise<void>;
  runtimesPending?: boolean;
}

/**
 * The internal FDE distribution deliberately keeps onboarding to one step:
 * create or select a workspace, then enter the product. Runtime installation
 * and Mika setup remain workspace/settings actions. The broader upstream
 * component contract is retained so web and desktop callers can share the
 * merged package without carrying a second onboarding implementation.
 */
export function OnboardingFlow({
  onComplete,
  mode = "first_run",
  onCancel,
}: OnboardingFlowProps) {
  const { t } = useT("onboarding");
  const user = useAuthStore((state) => state.user);
  if (!user) {
    throw new Error("OnboardingFlow requires an authenticated user");
  }

  const isNewWorkspace = mode === "new_workspace";
  const [busy, setBusy] = useState(false);
  const { data: workspaces = [] } = useQuery(workspaceListOptions());
  const existing = isNewWorkspace ? null : (workspaces[0] ?? null);

  const handleWorkspaceCreated = async (workspace: Workspace) => {
    try {
      await completeOnboarding("runtime_skipped", workspace.id);
    } catch (error) {
      toast.error(
        error instanceof Error ? error.message : t(($) => $.errors.skip_failed),
      );
      return;
    }

    if (!isNewWorkspace) {
      setCurrentWorkspace(workspace.slug, workspace.id);
    }
    onComplete(workspace, undefined);
  };

  return (
    <StepShell
      currentStep="workspace"
      onBack={isNewWorkspace ? onCancel : undefined}
      backDisabled={busy}
    >
      <StepWorkspace
        existing={existing}
        onCreated={handleWorkspaceCreated}
        onBusyChange={setBusy}
      />
    </StepShell>
  );
}

export type OnboardingMode = "first_run" | "new_workspace";

export type OnboardingDestination =
  { kind: "issue"; issueId: string } | { kind: "chat"; sessionId: string };

export type { OnboardingStep };
