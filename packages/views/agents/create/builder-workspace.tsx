"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useDefaultLayout } from "react-resizable-panels";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import {
  ResizableHandle,
  ResizablePanel,
  ResizablePanelGroup,
} from "@multica/ui/components/ui/resizable";
import {
  applyDraftRuntimeChange,
  decodeBuilderInput,
  encodeBuilderInput,
  mergeBuilderDraft,
  parseBuilderDraft,
  parseBuilderPackageText,
  agentManifestSchemaOptions,
  stripBuilderDraft,
} from "@multica/core/agents";
import {
  runtimeDisplayLabel,
  runtimeModelsOptions,
} from "@multica/core/runtimes";
import type { AgentBuilderSessionSummary } from "@multica/core/types";
import { BuilderPackagePanel } from "./builder-package-panel";
import { RuntimePicker } from "../components/runtime-picker";
import { BuilderConversation } from "./builder-conversation";
import { useBuilderDraftSync } from "./use-builder-draft-sync";
import { useBuilderSession } from "./use-builder-session";
import { useCreateAgentForm } from "./use-create-agent-form";
import { useT } from "../../i18n";

/**
 * One creation conversation: the chat on the left, its live configuration on
 * the right.
 *
 * Mounted with `key={sessionId}` by the page, so switching conversations tears
 * this down and builds it again. That is deliberate — every piece of state here
 * (the draft, which assistant message has been applied, the composer) belongs
 * to one conversation, and a remount is the only reset that cannot forget a
 * field.
 */
export function BuilderWorkspace({
  sessionId,
  squadId,
  session,
  sessionSettled,
  fallbackRuntimeId,
  onDiscarded,
  onRuntimeLabel,
}: {
  sessionId: string;
  squadId: string | null;
  /** This conversation's row from the list, once it has arrived. */
  session: AgentBuilderSessionSummary | undefined;
  /** The list query answered — either data or an error. */
  sessionSettled: boolean;
  /** Runtime this conversation was just started on. A conversation only joins
   *  the list once it has a message, so for the first turn this is the only
   *  truthful answer to "where does it run". */
  fallbackRuntimeId: string;
  /** The conversation no longer exists; the page decides where to go. */
  onDiscarded: () => void;
  /** Reports the runtime this conversation settled on, for the shell's chip. */
  onRuntimeLabel: (label: string | null) => void;
}) {
  const { t } = useT("agents");
  const { defaultLayout, onLayoutChanged } = useDefaultLayout({
    id: "multica_agent_builder_layout",
  });

  // Resuming: the conversation already runs somewhere, and only the server
  // knows where. Until it answers, the form seeds no runtime at all — falling
  // back to the first usable one would put the picker on a runtime that
  // executes nothing (MUL-5163).
  const runtimeSeed = useMemo(
    () => ({
      ready: sessionSettled,
      runtimeId: session?.runtime_id || fallbackRuntimeId,
    }),
    [fallbackRuntimeId, session?.runtime_id, sessionSettled],
  );
  const form = useCreateAgentForm({ runtimeSeed });
  const { draft, setDraft, selectedRuntime } = form;

  const draftSync = useBuilderDraftSync({
    sessionId,
    stored: session?.draft,
    storedSettled: sessionSettled,
    runtimeId: session?.runtime_id ?? "",
    draft,
    setDraft,
  });

  const packageSchema = useQuery(agentManifestSchemaOptions());

  const builderModelsQuery = useQuery(
    runtimeModelsOptions(
      selectedRuntime?.status === "online" ? selectedRuntime.id : null,
    ),
  );
  // `null` means discovery is not available yet (or failed), while `[]` is
  // an authoritative catalog with no selectable models. In both cases the
  // builder may preserve the user's current value but cannot invent one.
  const builderModelCatalog = useMemo(
    () =>
      builderModelsQuery.isSuccess
        ? builderModelsQuery.data.supported
          ? builderModelsQuery.data.models
          : []
        : null,
    [builderModelsQuery.data, builderModelsQuery.isSuccess],
  );
  const validBuilderModelIds = useMemo(
    () =>
      builderModelCatalog === null
        ? null
        : new Set(builderModelCatalog.map((model) => model.id)),
    [builderModelCatalog],
  );

  // Read through a ref so the encoder handed to the session hook stays current
  // without making the hook depend on every catalog query.
  const encodeContext = useRef({
    draft,
    workspaceSkills: form.workspaceSkills,
    members: form.members,
    selectedRuntime,
    builderModelCatalog,
    packageSchema: packageSchema.data,
  });
  encodeContext.current = {
    draft,
    workspaceSkills: form.workspaceSkills,
    members: form.members,
    selectedRuntime,
    builderModelCatalog,
    packageSchema: packageSchema.data,
  };

  const builder = useBuilderSession({
    sessionId,
    encodeInput: (text) => {
      const context = encodeContext.current;
      return encodeBuilderInput(
        text,
        context.draft,
        context.workspaceSkills,
        context.members,
        context.selectedRuntime,
        context.builderModelCatalog,
        context.packageSchema,
      );
    },
  });

  const skillIdSet = useMemo(
    () => new Set(form.workspaceSkills.map((skill) => skill.id)),
    [form.workspaceSkills],
  );
  const memberIdSet = useMemo(
    () => new Set(form.members.map((member) => member.user_id)),
    [form.members],
  );

  // Realtime chat updates can mutate the cached messages array in place. Use
  // the latest structured message's scalar identity/content as effect inputs
  // so a draft is still applied when the array reference itself is unchanged.
  const latestDraftMessage = [...builder.messages]
    .reverse()
    .find(
      (message) =>
        message.role === "assistant" && (parseBuilderPackageText(message.content) !== null || parseBuilderDraft(message.content)),
    );
  const latestDraftMessageId = latestDraftMessage?.id;
  const latestDraftMessageContent = latestDraftMessage?.content;

  const { restored, markApplied } = draftSync;
  const appliedRef = useRef<string | null>(null);
  appliedRef.current = draftSync.appliedMessageId;

  useEffect(() => {
    // Gated on the restore: merging a reply into the form before the stored
    // configuration lands would be overwritten a tick later, and the merge
    // would have been computed against an empty draft.
    if (!restored || builder.pending) return;
    if (
      !latestDraftMessageId ||
      !latestDraftMessageContent ||
      latestDraftMessageId === appliedRef.current
    ) {
      return;
    }
    const payload = parseBuilderDraft(latestDraftMessageContent);
    const packageText = parseBuilderPackageText(latestDraftMessageContent);
    if (!payload && packageText === null) return;
    markApplied(latestDraftMessageId);
    setDraft((current) => ({
      ...(payload ? mergeBuilderDraft(current, payload, skillIdSet, memberIdSet, validBuilderModelIds) : current),
      packageText: packageText ?? "",
    }));
  }, [
    builder.pending,
    latestDraftMessageContent,
    latestDraftMessageId,
    markApplied,
    memberIdSet,
    restored,
    setDraft,
    skillIdSet,
    validBuilderModelIds,
  ]);

  const displayMessages = useMemo(
    () =>
      builder.messages.map((message) => ({
        ...message,
        content:
          message.role === "user"
            ? decodeBuilderInput(message.content)
            : stripBuilderDraft(message.content),
      })),
    [builder.messages],
  );

  // True once the draft's runtime is known to match the carrier's. Until then
  // no selection may reach the server: RuntimePicker seeds an empty selection by
  // calling onSelect itself, and in this pane onSelect rebinds the live
  // conversation — so honouring that seed would move the conversation to
  // whatever runtime sorts first, silently when idle and as "stop the current
  // reply before switching runtime" when not.
  const runtimeKnown = sessionSettled && draft.runtimeId.length > 0;

  useEffect(() => {
    onRuntimeLabel(selectedRuntime ? runtimeDisplayLabel(selectedRuntime) : null);
  }, [onRuntimeLabel, selectedRuntime]);

  const handleRuntimeSelect = async (runtimeId: string) => {
    if (!runtimeKnown) return;
    if (!runtimeId || runtimeId === draft.runtimeId) return;
    const bound = await builder.switchRuntime(runtimeId);
    if (!bound) return;
    // Model ids are per-runtime; clear it — with the per-model thinking /
    // speed overrides — so the new runtime resolves its own defaults instead
    // of keeping values it may not serve.
    setDraft((current) => applyDraftRuntimeChange(current, bound));
  };

  // No beforeunload guard here on purpose: the conversation lives on the server
  // and the configuration autosaves, so there is nothing a reload can lose. The
  // manual route still warns because its form has no server-side home.

  const [confirmingDiscard, setConfirmingDiscard] = useState(false);
  // A conversation that no longer exists cannot be shown, edited or resumed;
  // leave rather than render an editable shell over nothing.
  const { missing } = builder;
  useEffect(() => {
    if (missing) onDiscarded();
  }, [missing, onDiscarded]);

  const discard = async () => {
    if (!(await builder.destroy())) return;
    setConfirmingDiscard(false);
    onDiscarded();
  };

  return (
    <>
      {/* The group lives here, not on the route, so its two panels are its
          only children — the same shape the chat page uses. A group whose
          children alternate between one and two panels (setup vs conversation)
          cannot keep a single persisted layout straight, and a panel arriving
          through a child component's fragment is not a child it can measure. */}
      <ResizablePanelGroup
        orientation="horizontal"
        className="min-h-0 flex-1"
        defaultLayout={defaultLayout}
        onLayoutChanged={onLayoutChanged}
      >
        <ResizablePanel id="conversation" minSize="30%">
          <BuilderConversation
            sessionId={sessionId}
            messages={displayMessages}
            loading={builder.messagesLoading}
            pendingTask={builder.pendingTask}
            runtimeOnline={selectedRuntime?.status === "online"}
            onSend={builder.send}
            onStop={() => void builder.stop()}
            restoreDraftRequest={builder.restoreDraftRequest}
            onRestoreDraftApplied={builder.handleRestoreDraftApplied}
            error={builder.error}
          />
        </ResizablePanel>
        <ResizableHandle />
        <ResizablePanel
          id="config"
          defaultSize={420}
          minSize={340}
          groupResizeBehavior="preserve-pixel-size"
        >
          <BuilderPackagePanel
            content={draft.packageText ?? ""}
            onChange={(packageText) => setDraft((current) => ({ ...current, packageText }))}
            runtimeId={selectedRuntime?.id ?? null}
            runtimeProvider={selectedRuntime?.provider ?? null}
            runtimeControl={<RuntimePicker runtimes={form.runtimes} runtimesLoading={form.runtimesLoading} members={form.members} currentUserId={form.currentUserId} selectedRuntimeId={draft.runtimeId} disabled={builder.pending || builder.switchingRuntime || !runtimeKnown} onSelect={(runtimeId) => void handleRuntimeSelect(runtimeId)} />}
            squadId={squadId}
            pending={builder.pending || builder.switchingRuntime || !draftSync.restored}
            onCreated={() => builder.archiveAfterCreate()}
            onDiscard={() => setConfirmingDiscard(true)}
            discarding={builder.closing}
          />
        </ResizablePanel>
      </ResizablePanelGroup>

      <AlertDialog
        open={confirmingDiscard}
        onOpenChange={(open) => {
          if (!open && !builder.closing) setConfirmingDiscard(false);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.creation_studio.drafts.discard_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.creation_studio.drafts.discard_confirm)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={builder.closing}>
              {t(($) => $.creation_studio.drafts.discard_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={(event) => {
                // Keep the dialog up while the delete is in flight; it closes
                // from `discard` only once the server has actually accepted.
                event.preventDefault();
                void discard();
              }}
              disabled={builder.closing}
            >
              {t(($) => $.creation_studio.drafts.discard)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
