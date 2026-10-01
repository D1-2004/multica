"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, Link2, RefreshCw, ShieldCheck, Trash2 } from "lucide-react";
import { QRCode } from "react-qr-code";
import { mid2Url } from "@ali/ding-mediaid";
import { toast } from "sonner";
import type {
  BeginDingTalkAccountBindingResponse,
  DingTalkAccountBindingOutcome,
  DingTalkAccountBindingsResponse,
  DingTalkBindingMode,
  DingTalkConversationSummary,
  DingTalkManualMessageScope,
  DingTalkMessageRouteOutcome,
  DingTalkProcessingSurface,
  DingTalkNativeStream,
  DingTalkNativeDEAPLink,
} from "@multica/core/types";
import { errorCode } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  dingtalkAccountBindingsOptions,
  reusableDingTalkIdentitiesOptions,
  useReuseDingTalkIdentity,
  useBeginDingTalkAccountBinding,
  useBindDingTalkMessageRouteManually,
  useDeleteDingTalkAccountBinding,
  useSetDingTalkNativeSubscription,
  useSetDingTalkNativeDEAPLink,
  useRemoveDingTalkNativeDEAPLink,
  dingtalkNativeSubscriptionStatusOptions,
  useUpdateDingTalkAccountBindingSurface,
} from "@multica/core/dingtalk-account-bindings";
import { Button } from "@multica/ui/components/ui/button";
import { Avatar, AvatarFallback, AvatarImage } from "@multica/ui/components/ui/avatar";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  NativeSelect,
  NativeSelectOption,
} from "@multica/ui/components/ui/native-select";
import { Switch } from "@multica/ui/components/ui/switch";
import { Tooltip, TooltipContent, TooltipTrigger } from "@multica/ui/components/ui/tooltip";
import {
  Popover,
  PopoverContent,
  PopoverDescription,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { RadioGroup, RadioGroupItem } from "@multica/ui/components/ui/radio-group";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
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
import { useT, useTimeAgo } from "../../../i18n";

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback;
}

// DingTalk account (user) ids are positive decimal integers.
const DINGTALK_DECIMAL_ID = /^[1-9][0-9]{0,19}$/;
// An organization's corpId, the Router's tenant id; the numeric OrgID is not
// accepted by the Router's subscription create.
const DINGTALK_CORP_ID = /^ding[0-9A-Za-z]{8,64}$/;

// Localizes the stable `code` the native-subscription and manual-binding
// endpoints attach to failures; unknown codes fall back to the server text.
function useNativeSubscriptionErrorMessage() {
  const { t } = useT("agents");
  return (error: unknown, fallback: string): string => {
    switch (errorCode(error)) {
      case "agent_binding_forbidden":
        return t(($) => $.tab_body.integrations.dingtalk_account_permission_denied);
      case "native_subscription_requires_identity":
        return t(($) => $.tab_body.integrations.dingtalk_identity_native_subscription_requires_identity);
      case "native_subscription_conflicts_with_message_binding":
        return t(($) => $.tab_body.integrations.dingtalk_identity_native_subscription_blocked);
      case "native_subscription_requires_managed_response":
        return t(($) => $.tab_body.integrations.dingtalk_identity_native_subscription_requires_managed_response);
      case "native_subscription_account_in_use":
        return t(($) => $.tab_body.integrations.dingtalk_identity_native_subscription_account_in_use);
      case "native_subscription_unavailable":
        return t(($) => $.tab_body.integrations.dingtalk_identity_native_subscription_unavailable);
      case "message_binding_conflicts_with_native_subscription":
        return t(($) => $.tab_body.integrations.dingtalk_account_native_subscription_blocked);
      case "operator_only":
        return t(($) => $.tab_body.integrations.dingtalk_account_manual_operator_only);
      case "binding_already_active":
        return t(($) => $.tab_body.integrations.dingtalk_account_manual_already_active);
      case "invalid_identity":
        return t(($) => $.tab_body.integrations.dingtalk_account_manual_invalid_identity);
      case "invalid_message_scope":
        return t(($) => $.tab_body.integrations.dingtalk_account_manual_invalid_scope);
      case "subscription_verify_failed":
        return t(($) => $.tab_body.integrations.dingtalk_account_manual_verify_failed);
      default:
        return errorMessage(error, fallback);
    }
  };
}

function displayName(outcome: DingTalkAccountBindingOutcome, fallback: string): string {
  return outcome.accountDisplayName?.trim() || fallback;
}

function conversationAvatarUrls(
  avatarMediaId?: string | null,
  avatarUrl?: string | null,
): string[] {
  const candidates: string[] = [];
  const mediaId = avatarMediaId?.trim();
  if (mediaId) {
    try {
      const mediaUrl = mid2Url(mediaId, { imageSize: "thumb" })?.trim();
      if (mediaUrl) candidates.push(mediaUrl);
    } catch {
      // Fall through to the URL snapshot when the media ID cannot be decoded.
    }
  }
  const snapshotUrl = avatarUrl?.trim();
  if (snapshotUrl && !candidates.includes(snapshotUrl)) {
    candidates.push(snapshotUrl);
  }
  return candidates;
}

function DingTalkConversationAvatar({
  conversation,
}: {
  conversation: DingTalkConversationSummary;
}) {
  const { avatarMediaId, avatarUrl: snapshotAvatarUrl } = conversation;
  const avatarUrls = useMemo(
    () => conversationAvatarUrls(avatarMediaId, snapshotAvatarUrl),
    [avatarMediaId, snapshotAvatarUrl],
  );
  const [avatarIndex, setAvatarIndex] = useState(0);

  useEffect(() => setAvatarIndex(0), [avatarUrls]);

  const currentAvatarUrl = avatarUrls[avatarIndex];
  return (
    <Avatar size="sm">
      {currentAvatarUrl ? (
        <AvatarImage
          src={currentAvatarUrl}
          alt={conversation.name}
          onLoadingStatusChange={(status) => {
            if (status === "error") {
              setAvatarIndex((index) => Math.min(index + 1, avatarUrls.length));
            }
          }}
        />
      ) : null}
      <AvatarFallback>
        {Array.from(conversation.name.trim())[0] || "?"}
      </AvatarFallback>
    </Avatar>
  );
}

function DingTalkMessageScopeSummary({
  outcome,
}: {
  outcome: DingTalkMessageRouteOutcome;
}) {
  const { t } = useT("agents");
  const [expanded, setExpanded] = useState(false);
  const [emojiExpanded, setEmojiExpanded] = useState(false);

  const renderExpandableSummary = (
    summary: string,
    conversations: DingTalkMessageRouteOutcome["conversations"],
    open: boolean,
    onToggle: () => void,
  ) => (
    <div>
      <button
        type="button"
        className="flex items-center gap-1 text-left hover:text-foreground"
        aria-expanded={open}
        onClick={onToggle}
      >
        {open ? (
          <ChevronDown className="size-3 shrink-0" />
        ) : (
          <ChevronRight className="size-3 shrink-0" />
        )}
        <span>{summary}</span>
      </button>
      {open ? (
        <ul className="mt-2 space-y-2 pl-4">
          {conversations.map((conversation) => (
            <li key={conversation.cid} className="flex items-center gap-2">
              <DingTalkConversationAvatar conversation={conversation} />
              <span className="leading-relaxed text-foreground">
                {conversation.name}
              </span>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );

  const subscriptionSummary = (() => {
    if (outcome.messageScopeVersion !== 2 || !outcome.subscription) {
      return undefined;
    }
    const { directCids, groupCids } = outcome.subscription;
    const directAll = directCids.includes("*");
    const groupAll = groupCids.includes("*");
    const directCount = directAll ? 0 : directCids.length;
    const groupCount = groupAll ? 0 : groupCids.length;
    if (directAll && groupAll) {
      return t(($) => $.tab_body.integrations.dingtalk_account_scope_all);
    }
    if (directAll) {
      return groupCount === 0
        ? t(($) => $.tab_body.integrations.dingtalk_account_scope_direct_all)
        : t(($) => $.tab_body.integrations.dingtalk_account_scope_direct_all_group_custom, {
            count: groupCount,
          });
    }
    if (groupAll) {
      return directCount === 0
        ? t(($) => $.tab_body.integrations.dingtalk_account_scope_group_all)
        : t(($) => $.tab_body.integrations.dingtalk_account_scope_direct_custom_group_all, {
            count: directCount,
          });
    }
    if (directCount === 0 && groupCount === 0) {
      return undefined;
    }
    if (directCount === 0) {
      return t(($) => $.tab_body.integrations.dingtalk_account_scope_group_custom, {
        count: groupCount,
      });
    }
    if (groupCount === 0) {
      return t(($) => $.tab_body.integrations.dingtalk_account_scope_direct_custom, {
        count: directCount,
      });
    }
    return t(($) => $.tab_body.integrations.dingtalk_account_scope_direct_custom_group_custom, {
      count: directCount + groupCount,
      directCount,
      groupCount,
    });
  })();

  const messageScopeSummary = (() => {
    if (subscriptionSummary !== undefined) {
      return outcome.conversations.length > 0 ? (
        renderExpandableSummary(subscriptionSummary, outcome.conversations, expanded, () =>
          setExpanded((open) => !open),
        )
      ) : (
        <p>{subscriptionSummary}</p>
      );
    }
    switch (outcome.messageScope) {
      case "all":
        return <p>{t(($) => $.tab_body.integrations.dingtalk_account_scope_all)}</p>;
      case "custom": {
        const summary = t(
          ($) => $.tab_body.integrations.dingtalk_account_scope_custom,
          { count: outcome.conversations.length },
        );
        return renderExpandableSummary(summary, outcome.conversations, expanded, () =>
          setExpanded((open) => !open),
        );
      }
      case "direct_only":
      default:
        return (
          <p>
            {t(($) => $.tab_body.integrations.dingtalk_account_scope_direct_only)}
          </p>
        );
    }
  })();

  return (
    <div className="space-y-1">
      {messageScopeSummary}
      {outcome.calendarStartEnabled ? (
        <p>{t(($) => $.tab_body.integrations.dingtalk_account_scope_calendar_start)}</p>
      ) : null}
      {outcome.enabledDomains.includes("approval") ? (
        <p>{t(($) => $.tab_body.integrations.dingtalk_account_scope_approval)}</p>
      ) : null}
      {outcome.emojiConversations.length > 0
        ? renderExpandableSummary(
            t(($) => $.tab_body.integrations.dingtalk_account_scope_emoji, {
              count: outcome.emojiConversations.length,
            }),
            outcome.emojiConversations,
            emojiExpanded,
            () => setEmojiExpanded((open) => !open),
          )
        : null}
    </div>
  );
}

export function DingTalkRunModePicker({
  value,
  disabled,
  readOnly,
  onReadOnlyClick,
  onConfirm,
}: {
  value: DingTalkProcessingSurface;
  disabled?: boolean;
  readOnly?: boolean;
  onReadOnlyClick?: () => void;
  onConfirm: (surfaceType: DingTalkProcessingSurface) => Promise<boolean>;
}) {
  const { t } = useT("agents");
  const [open, setOpen] = useState(false);
  const [draftValue, setDraftValue] = useState<DingTalkProcessingSurface>(value);

  function surfaceLabel(surfaceType: DingTalkProcessingSurface): string {
    switch (surfaceType) {
      case "issue":
        return t(($) => $.tab_body.integrations.dingtalk_account_surface_issue);
      case "chat":
        return t(($) => $.tab_body.integrations.dingtalk_account_surface_chat);
      case "auto":
        return t(($) => $.tab_body.integrations.dingtalk_account_surface_auto);
    }
  }

  function surfaceDescription(surfaceType: DingTalkProcessingSurface): string {
    switch (surfaceType) {
      case "issue":
        return t(($) => $.tab_body.integrations.dingtalk_account_surface_issue_description);
      case "chat":
        return t(($) => $.tab_body.integrations.dingtalk_account_surface_chat_description);
      case "auto":
        return t(($) => $.tab_body.integrations.dingtalk_account_surface_auto_description);
    }
  }

  function handleOpenChange(nextOpen: boolean) {
    if (nextOpen && readOnly) {
      onReadOnlyClick?.();
      return;
    }
    if (nextOpen) setDraftValue(value);
    setOpen(nextOpen);
  }

  async function commit() {
    if (await onConfirm(draftValue)) setOpen(false);
  }

  return (
    <Popover open={open} onOpenChange={handleOpenChange}>
      <PopoverTrigger
        render={(
          <Button
            type="button"
            size="xs"
            variant="outline"
            aria-label={`${t(($) => $.tab_body.integrations.dingtalk_account_run_mode)}: ${surfaceLabel(value)}`}
            disabled={disabled}
          >
            {surfaceLabel(value)}
            <ChevronDown className="size-3 text-muted-foreground" />
          </Button>
        )}
      />
      <PopoverContent align="start" className="w-80 gap-0 p-0">
        <PopoverHeader className="border-b px-4 py-3">
          <PopoverTitle>
            {t(($) => $.tab_body.integrations.dingtalk_account_run_mode)}
          </PopoverTitle>
          <PopoverDescription className="text-caption leading-relaxed">
            {t(($) => $.tab_body.integrations.dingtalk_account_surface_picker_description)}
          </PopoverDescription>
        </PopoverHeader>
        <RadioGroup
          value={draftValue}
          onValueChange={(nextValue) => setDraftValue(nextValue)}
          aria-label={t(($) => $.tab_body.integrations.dingtalk_account_run_mode)}
          className="gap-1 p-2"
        >
          {(["issue", "chat", "auto"] as const).map((surfaceType) => {
            const selected = draftValue === surfaceType;
            return (
              <label
                key={surfaceType}
                className={`flex cursor-pointer items-start gap-3 rounded-md border px-3 py-2.5 transition-colors ${selected
                  ? "border-primary/40 bg-primary/5"
                  : "border-transparent hover:bg-muted/60"}`}
              >
                <RadioGroupItem
                  value={surfaceType}
                  aria-label={surfaceLabel(surfaceType)}
                  className="mt-0.5"
                />
                <span className="min-w-0 space-y-1">
                  <span className="block text-body font-medium text-foreground">
                    {surfaceLabel(surfaceType)}
                  </span>
                  <span className="block text-caption leading-relaxed text-muted-foreground">
                    {surfaceDescription(surfaceType)}
                  </span>
                </span>
              </label>
            );
          })}
        </RadioGroup>
        <div className="flex items-center justify-end gap-2 border-t px-3 py-2.5">
          <Button
            type="button"
            size="sm"
            variant="outline"
            disabled={disabled}
            onClick={() => handleOpenChange(false)}
          >
            {t(($) => $.tab_body.integrations.dingtalk_account_cancel)}
          </Button>
          <Button
            type="button"
            size="sm"
            disabled={disabled || draftValue === value}
            onClick={() => void commit()}
          >
            {t(($) => $.tab_body.integrations.dingtalk_account_surface_confirm)}
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  );
}

export function DingTalkAccountBindingCard({
  agentId,
  agentName,
  bindingMode = "message",
  canOperate,
  permissionLoading,
}: {
  agentId: string;
  agentName: string;
  bindingMode?: DingTalkBindingMode;
  canOperate: boolean;
  permissionLoading: boolean;
}) {
  const wsId = useWorkspaceId();
  const { data, isPending } = useQuery({
    ...dingtalkAccountBindingsOptions(wsId),
    enabled: !!wsId,
  });

  return (
    <DingTalkBindingModeCard
      agentId={agentId}
      agentName={agentName}
      bindingMode={bindingMode}
      data={data}
      listingPending={isPending}
      canOperate={canOperate}
      permissionLoading={permissionLoading}
    />
  );
}

function ReusableExecutionIdentity({ agentId }: { agentId: string }) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const candidates = useQuery(reusableDingTalkIdentitiesOptions(wsId, agentId));
  const reuse = useReuseDingTalkIdentity(wsId);
  const [selected, setSelected] = useState("");
  const [error, setError] = useState<string | null>(null);
  const identities = candidates.data ?? [];
  const selectedIdentity = identities.find((identity) => identity.sourceAgentId === selected);

  async function applyIdentity() {
    if (!selectedIdentity || reuse.isPending) return;
    setError(null);
    try {
      await reuse.mutateAsync({ agentId, sourceAgentId: selectedIdentity.sourceAgentId });
    } catch (cause) {
      setError(errorMessage(cause, t(($) => $.tab_body.integrations.dingtalk_identity_reuse_failed)));
      void candidates.refetch();
    }
  }

  if (candidates.isPending) return <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.integrations.dingtalk_account_loading)}</p>;
  if (candidates.isError) return (
    <div className="space-y-2">
      <p className="text-caption text-muted-foreground" role="alert">{t(($) => $.tab_body.integrations.dingtalk_identity_reuse_load_failed)}</p>
      <Button variant="outline" size="sm" onClick={() => void candidates.refetch()} disabled={candidates.isFetching}>{t(($) => $.tab_body.integrations.dingtalk_identity_reuse_retry)}</Button>
    </div>
  );
  if (!identities.length) return <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.integrations.dingtalk_identity_reuse_empty)}</p>;
  return (
    <div className="space-y-2" data-testid="reusable-execution-identity">
      <label className="block text-caption font-medium" htmlFor={`reuse-identity-${agentId}`}>
        {t(($) => $.tab_body.integrations.dingtalk_identity_reuse_select)}
      </label>
      <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.integrations.dingtalk_identity_reuse_hint)}</p>
      <div className="flex flex-wrap items-center gap-2">
        <select id={`reuse-identity-${agentId}`} className="h-9 min-w-0 max-w-full rounded-md border bg-background px-3 text-sm" value={selected} onChange={(event) => setSelected(event.target.value)} disabled={reuse.isPending}>
          <option value="">{t(($) => $.tab_body.integrations.dingtalk_identity_reuse_placeholder)}</option>
          {identities.map((identity) => <option key={identity.sourceAgentId} value={identity.sourceAgentId}>
            {identity.accountDisplayName} · {identity.organizationName} ({identity.sourceAgentName})
          </option>)}
        </select>
        <Button size="sm" onClick={() => void applyIdentity()} disabled={!selectedIdentity || reuse.isPending}>
          {t(($) => $.tab_body.integrations.dingtalk_identity_reuse_apply)}
        </Button>
      </div>
      {error ? <p className="text-caption text-destructive" role="alert">{error}</p> : null}
    </div>
  );
}

// The native subscription's event stream (WebSocket) as a status light: green
// connected, amber connecting, red disconnected, grey when no stream can run
// or the state is unknown. Details are on hover and focus.
function NativeStreamIndicator({ stream }: { stream: DingTalkNativeStream | undefined }) {
  const { t } = useT("agents");
  const timeAgo = useTimeAgo();
  const state = stream?.state ?? "unknown";
  let label: string;
  let dot: string;
  switch (state) {
    case "connected":
      label = t(($) => $.tab_body.integrations.dingtalk_identity_native_stream_connected);
      dot = "bg-success";
      break;
    case "connecting":
      label = t(($) => $.tab_body.integrations.dingtalk_identity_native_stream_connecting);
      dot = "animate-pulse bg-warning";
      break;
    case "disconnected":
      label = t(($) => $.tab_body.integrations.dingtalk_identity_native_stream_disconnected);
      dot = "bg-destructive";
      break;
    case "unavailable":
      label = t(($) => $.tab_body.integrations.dingtalk_identity_native_stream_unavailable);
      dot = "bg-muted-foreground/40";
      break;
    default:
      label = t(($) => $.tab_body.integrations.dingtalk_identity_native_stream_unknown);
      dot = "bg-muted-foreground/40";
  }
  const details = [label];
  if (state === "connected") {
    details.push(stream?.lastEventAt
      ? t(($) => $.tab_body.integrations.dingtalk_identity_native_stream_last_event, {
        when: timeAgo(stream.lastEventAt),
      })
      : t(($) => $.tab_body.integrations.dingtalk_identity_native_stream_no_event));
  } else if (stream?.lastConnectedAt) {
    details.push(t(($) => $.tab_body.integrations.dingtalk_identity_native_stream_last_connected, {
      when: timeAgo(stream.lastConnectedAt),
    }));
  }
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <span
            role="img"
            tabIndex={0}
            // Tooltips are not announced: the accessible name carries the
            // details too.
            aria-label={details.join(" · ")}
            data-testid="dingtalk-native-stream"
            data-state={state}
            className="inline-flex size-4 shrink-0 items-center justify-center rounded-full outline-none focus-visible:ring-2 focus-visible:ring-ring"
          />
        }
      >
        <span aria-hidden className={`size-2 rounded-full ${dot}`} />
      </TooltipTrigger>
      <TooltipContent className="flex-col items-start">
        {details.map((line) => <p key={line}>{line}</p>)}
      </TooltipContent>
    </Tooltip>
  );
}

const DEAP_AGENT_UUID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;

// A digital employee's native event credential is issued by DEAP through its
// supervisor. Everyone sees the link; only deployment operators edit it.
function NativeDEAPLink({ agentId, link, editable }: {
  agentId: string;
  link: DingTalkNativeDEAPLink | null;
  editable: boolean;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const setLink = useSetDingTalkNativeDEAPLink(wsId);
  const removeLink = useRemoveDingTalkNativeDEAPLink(wsId);
  const [employee, setEmployee] = useState(link?.deapAgentUuid ?? "");
  const [supervisor, setSupervisor] = useState(link?.supervisorUid ?? "");
  const [error, setError] = useState<string | null>(null);
  const trimmedEmployee = employee.trim();
  const trimmedSupervisor = supervisor.trim();
  const employeeInvalid = trimmedEmployee !== "" && !DEAP_AGENT_UUID.test(trimmedEmployee);
  const supervisorInvalid = trimmedSupervisor !== "" && !DINGTALK_DECIMAL_ID.test(trimmedSupervisor);
  const valid = DEAP_AGENT_UUID.test(trimmedEmployee) && DINGTALK_DECIMAL_ID.test(trimmedSupervisor);
  const pending = setLink.isPending || removeLink.isPending;
  const unchanged = link !== null && trimmedEmployee === link.deapAgentUuid && trimmedSupervisor === link.supervisorUid;
  const employeeFieldId = `dingtalk-deap-employee-${agentId}`;
  const supervisorFieldId = `dingtalk-deap-supervisor-${agentId}`;

  function failure(cause: unknown): string {
    switch (errorCode(cause)) {
      case "operator_only":
        return t(($) => $.tab_body.integrations.dingtalk_identity_deap_link_operator_only);
      case "invalid_deap_link":
        return t(($) => $.tab_body.integrations.dingtalk_identity_deap_link_invalid);
      case "native_subscription_requires_identity":
        return t(($) => $.tab_body.integrations.dingtalk_identity_native_subscription_requires_identity);
      default:
        return t(($) => $.tab_body.integrations.dingtalk_identity_deap_link_failed);
    }
  }

  async function save() {
    if (!valid || pending) return;
    setError(null);
    try {
      await setLink.mutateAsync({ agentId, deapAgentUuid: trimmedEmployee, supervisorUid: trimmedSupervisor });
    } catch (cause) {
      setError(failure(cause));
    }
  }

  async function remove() {
    if (pending) return;
    setError(null);
    try {
      await removeLink.mutateAsync(agentId);
    } catch (cause) {
      setError(failure(cause));
    }
  }

  return (
    <div className="space-y-3 rounded-md bg-muted/30 p-3" data-testid="dingtalk-native-deap-link">
      <div className="space-y-1">
        <h4 className="text-caption font-medium">
          {t(($) => $.tab_body.integrations.dingtalk_identity_deap_link_title)}
        </h4>
        <p className="text-caption leading-relaxed text-muted-foreground">
          {t(($) => $.tab_body.integrations.dingtalk_identity_deap_link_description)}
        </p>
      </div>
      {editable ? (
        <>
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="min-w-0 space-y-1.5">
              <Label htmlFor={employeeFieldId} className="text-caption">
                {t(($) => $.tab_body.integrations.dingtalk_identity_deap_link_employee)}
              </Label>
              <Input
                id={employeeFieldId}
                autoComplete="off"
                spellCheck={false}
                value={employee}
                onChange={(event) => setEmployee(event.target.value)}
                disabled={pending}
                aria-invalid={employeeInvalid || undefined}
                aria-describedby={employeeInvalid ? `${employeeFieldId}-error` : undefined}
                className="font-mono"
              />
              {employeeInvalid ? (
                <p id={`${employeeFieldId}-error`} className="text-caption text-destructive">
                  {t(($) => $.tab_body.integrations.dingtalk_identity_deap_link_invalid_employee)}
                </p>
              ) : null}
            </div>
            <div className="min-w-0 space-y-1.5">
              <Label htmlFor={supervisorFieldId} className="text-caption">
                {t(($) => $.tab_body.integrations.dingtalk_identity_deap_link_supervisor)}
              </Label>
              <Input
                id={supervisorFieldId}
                inputMode="numeric"
                autoComplete="off"
                value={supervisor}
                onChange={(event) => setSupervisor(event.target.value)}
                disabled={pending}
                aria-invalid={supervisorInvalid || undefined}
                aria-describedby={supervisorInvalid ? `${supervisorFieldId}-error` : undefined}
                className="font-mono"
              />
              {supervisorInvalid ? (
                <p id={`${supervisorFieldId}-error`} className="text-caption text-destructive">
                  {t(($) => $.tab_body.integrations.dingtalk_account_manual_invalid_id)}
                </p>
              ) : null}
            </div>
          </div>
          <div className="flex flex-wrap items-center justify-end gap-2">
            {link ? (
              <Button variant="outline" size="sm" onClick={() => void remove()} disabled={pending}>
                {t(($) => $.tab_body.integrations.dingtalk_identity_deap_link_remove)}
              </Button>
            ) : null}
            <Button size="sm" onClick={() => void save()} disabled={!valid || unchanged || pending}>
              {setLink.isPending
                ? t(($) => $.tab_body.integrations.dingtalk_identity_deap_link_saving)
                : t(($) => $.tab_body.integrations.dingtalk_identity_deap_link_save)}
            </Button>
          </div>
        </>
      ) : (
        <div className="space-y-1 text-caption text-muted-foreground">
          <p className="break-all">
            {link
              ? t(($) => $.tab_body.integrations.dingtalk_identity_deap_link_current, {
                employee: link.deapAgentUuid,
                supervisor: link.supervisorUid,
              })
              : t(($) => $.tab_body.integrations.dingtalk_identity_deap_link_none)}
          </p>
          <p>{t(($) => $.tab_body.integrations.dingtalk_identity_deap_link_operator_only)}</p>
        </div>
      )}
      {error ? (
        <p className="text-caption text-destructive" role="alert">
          {error}
        </p>
      ) : null}
    </div>
  );
}

// Operator-only form that binds the digital-employee message route by DingTalk
// corpId/UID instead of the QR scan. The server re-checks operator access.
function ManualMessageBinding({
  agentId,
  disabled,
  rejectUnauthorizedOperation,
}: {
  agentId: string;
  disabled: boolean;
  rejectUnauthorizedOperation: () => boolean;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const bindManually = useBindDingTalkMessageRouteManually(wsId);
  const localizeError = useNativeSubscriptionErrorMessage();
  const [corpId, setCorpId] = useState("");
  const [uid, setUid] = useState("");
  const [messageScope, setMessageScope] = useState<DingTalkManualMessageScope>("all");
  const [error, setError] = useState<string | null>(null);

  const trimmedCorpId = corpId.trim();
  const trimmedUid = uid.trim();
  const corpIdInvalid = trimmedCorpId !== "" && !DINGTALK_CORP_ID.test(trimmedCorpId);
  const uidInvalid = trimmedUid !== "" && !DINGTALK_DECIMAL_ID.test(trimmedUid);
  const identityValid = DINGTALK_CORP_ID.test(trimmedCorpId) && DINGTALK_DECIMAL_ID.test(trimmedUid);
  const controlsDisabled = disabled || bindManually.isPending;
  const corpIdFieldId = `dingtalk-manual-corp-${agentId}`;
  const uidFieldId = `dingtalk-manual-uid-${agentId}`;
  const scopeFieldId = `dingtalk-manual-scope-${agentId}`;
  const invalidId = t(($) => $.tab_body.integrations.dingtalk_account_manual_invalid_id);

  async function submit() {
    if (!identityValid || controlsDisabled) return;
    if (rejectUnauthorizedOperation()) return;
    setError(null);
    try {
      await bindManually.mutateAsync({
        agentId,
        corpId: trimmedCorpId,
        uid: trimmedUid,
        messageScope,
      });
      setCorpId("");
      setUid("");
      setMessageScope("all");
    } catch (cause) {
      setError(localizeError(
        cause,
        t(($) => $.tab_body.integrations.dingtalk_account_manual_failed),
      ));
    }
  }

  return (
    <div
      className="space-y-3 rounded-md bg-muted/30 p-3"
      data-testid="dingtalk-manual-message-binding"
    >
      <div className="space-y-1">
        <h4 className="text-caption font-medium">
          {t(($) => $.tab_body.integrations.dingtalk_account_manual_title)}
        </h4>
        <p className="text-caption leading-relaxed text-muted-foreground">
          {t(($) => $.tab_body.integrations.dingtalk_account_manual_description)}
        </p>
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="min-w-0 space-y-1.5">
          <Label htmlFor={corpIdFieldId} className="text-caption">
            {t(($) => $.tab_body.integrations.dingtalk_account_manual_corp_id)}
          </Label>
          <Input
            id={corpIdFieldId}
            autoComplete="off"
            spellCheck={false}
            placeholder="ding…"
            value={corpId}
            onChange={(event) => setCorpId(event.target.value)}
            disabled={controlsDisabled}
            aria-invalid={corpIdInvalid || undefined}
            aria-describedby={corpIdInvalid ? `${corpIdFieldId}-error` : undefined}
            className="font-mono"
          />
          {corpIdInvalid ? (
            <p id={`${corpIdFieldId}-error`} className="text-caption text-destructive">
              {t(($) => $.tab_body.integrations.dingtalk_account_manual_invalid_corp_id)}
            </p>
          ) : null}
        </div>
        <div className="min-w-0 space-y-1.5">
          <Label htmlFor={uidFieldId} className="text-caption">
            {t(($) => $.tab_body.integrations.dingtalk_account_manual_uid)}
          </Label>
          <Input
            id={uidFieldId}
            inputMode="numeric"
            autoComplete="off"
            value={uid}
            onChange={(event) => setUid(event.target.value)}
            disabled={controlsDisabled}
            aria-invalid={uidInvalid || undefined}
            aria-describedby={uidInvalid ? `${uidFieldId}-error` : undefined}
            className="font-mono"
          />
          {uidInvalid ? (
            <p id={`${uidFieldId}-error`} className="text-caption text-destructive">
              {invalidId}
            </p>
          ) : null}
        </div>
      </div>
      <div className="flex flex-wrap items-end gap-3">
        <div className="min-w-0 space-y-1.5">
          <Label htmlFor={scopeFieldId} className="text-caption">
            {t(($) => $.tab_body.integrations.dingtalk_account_manual_scope)}
          </Label>
          <NativeSelect
            id={scopeFieldId}
            value={messageScope}
            onChange={(event) =>
              setMessageScope(event.target.value === "direct_only" ? "direct_only" : "all")}
            disabled={controlsDisabled}
          >
            <NativeSelectOption value="all">
              {t(($) => $.tab_body.integrations.dingtalk_account_manual_scope_all)}
            </NativeSelectOption>
            <NativeSelectOption value="direct_only">
              {t(($) => $.tab_body.integrations.dingtalk_account_manual_scope_direct_only)}
            </NativeSelectOption>
          </NativeSelect>
        </div>
        <Button
          type="button"
          onClick={() => void submit()}
          disabled={controlsDisabled || !identityValid}
        >
          {bindManually.isPending
            ? t(($) => $.tab_body.integrations.dingtalk_account_manual_submitting)
            : t(($) => $.tab_body.integrations.dingtalk_account_manual_submit)}
        </Button>
      </div>
      {error ? <p className="text-caption text-destructive" role="alert">{error}</p> : null}
    </div>
  );
}

function DingTalkBindingModeCard({
  agentId,
  agentName,
  bindingMode,
  data,
  listingPending,
  canOperate,
  permissionLoading,
}: {
  agentId: string;
  agentName: string;
  bindingMode: DingTalkBindingMode;
  data?: DingTalkAccountBindingsResponse;
  listingPending: boolean;
  canOperate: boolean;
  permissionLoading: boolean;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const beginBinding = useBeginDingTalkAccountBinding(wsId);
  const deleteBinding = useDeleteDingTalkAccountBinding(wsId);
  const updateBindingSurface = useUpdateDingTalkAccountBindingSurface(wsId);
  const setNativeSubscription = useSetDingTalkNativeSubscription(wsId);
  const localizeError = useNativeSubscriptionErrorMessage();
  const [attempt, setAttempt] = useState<BeginDingTalkAccountBindingResponse | null>(null);
  const [expired, setExpired] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);

  const currentBinding = useMemo(
    () => data?.bindings.find((binding) => binding.agentId === agentId) ?? null,
    [agentId, data?.bindings],
  );
  const messageRouteActive = currentBinding?.messageRoute.status === "active";
  const messageRoutePending = currentBinding?.messageRoute.status === "pending";
  // Identity-only aggregate rows also contain status=unbound. Only an
  // actual message projection has a scope version (or legacy boundAt).
  // messageScope itself is not evidence: the API parser defaults it.
  const hasMessageProjection = currentBinding?.messageRoute.messageScopeVersion !== undefined ||
    Boolean(currentBinding?.messageRoute.boundAt);
  const messageRouteNeedsReconciliation =
    (currentBinding?.messageRoute.status === "unbound" && hasMessageProjection) ||
    currentBinding?.messageRoute.status === "bound_to_other_agent" ||
    currentBinding?.messageRoute.status === "inconsistent" ||
    currentBinding?.messageRoute.status === "router_unavailable";
  const messageRouteReconciliationState = bindingMode === "message" &&
    messageRouteNeedsReconciliation;
  // Native subscription and the digital-employee message binding are mutually
  // exclusive. The message card counts as bound whenever it offers Unbind.
  const nativeSubscriptionOn = currentBinding?.dwsIdentity.nativeSubscription === true;
  const messageBindingPresent = messageRouteActive || messageRouteNeedsReconciliation;
  const nativeSubscriptionBlocked = messageBindingPresent && !nativeSubscriptionOn;
  const messageBindingBlocked = bindingMode === "message" && nativeSubscriptionOn;
  const showManualBinding = bindingMode === "message" && data?.manualBindingAllowed === true;
  const messageBindingFailed = bindingMode === "message" &&
    currentBinding?.messageRoute.status === "failed";
  const retryMessageBinding = bindingMode === "message" &&
    (messageRoutePending || messageBindingFailed);
  const identityActive = currentBinding?.dwsIdentity.status === "active";
  const connected = bindingMode === "message"
    ? messageRouteActive
    : identityActive;
  const canUnbind = bindingMode === "message"
    ? messageRouteActive || messageRouteReconciliationState
    : identityActive;
  const showBoundAccount = connected || messageRouteReconciliationState;
  const accountOutcome = bindingMode === "message"
    ? currentBinding?.messageRoute
    : currentBinding?.dwsIdentity;
  const showNativeSubscription = bindingMode === "identity" && identityActive;
  const nativeSubscriptionDisabled = permissionLoading || setNativeSubscription.isPending ||
    deleteBinding.isPending || nativeSubscriptionBlocked;
  const nativeSubscriptionSwitchId = `dingtalk-native-subscription-${agentId}`;
  const nativeSubscriptionLabelId = `${nativeSubscriptionSwitchId}-label`;
  const nativeSubscriptionHintId = `${nativeSubscriptionSwitchId}-hint`;
  const nativeSubscriptionBlockedId = `${nativeSubscriptionSwitchId}-blocked`;
  const nativeStreamWatched = showNativeSubscription && nativeSubscriptionOn;
  // Read for a bound identity (its DEAP link shows before native is on);
  // polled only while the stream is watched.
  const nativeStatus = useQuery(
    dingtalkNativeSubscriptionStatusOptions(wsId, agentId, showNativeSubscription, nativeStreamWatched),
  );
  const nativeStream = nativeStreamWatched ? nativeStatus.data?.stream : undefined;

  const title = bindingMode === "message"
    ? t(($) => $.tab_body.integrations.dingtalk_account_title)
    : t(($) => $.tab_body.integrations.dingtalk_identity_title);
  const description = bindingMode === "message"
    ? t(($) => $.tab_body.integrations.dingtalk_account_description)
    : t(($) => $.tab_body.integrations.dingtalk_identity_description);
  const connectLabel = bindingMode === "message"
    ? t(($) => $.tab_body.integrations.dingtalk_account_connect)
    : t(($) => $.tab_body.integrations.dingtalk_identity_connect);
  const startingLabel = bindingMode === "message"
    ? t(($) => $.tab_body.integrations.dingtalk_account_starting)
    : t(($) => $.tab_body.integrations.dingtalk_identity_starting);
  const beginFailed = bindingMode === "message"
    ? t(($) => $.tab_body.integrations.dingtalk_account_begin_failed)
    : t(($) => $.tab_body.integrations.dingtalk_identity_begin_failed);
  const fallbackName = bindingMode === "message"
    ? t(($) => $.tab_body.integrations.dingtalk_account_fallback_name)
    : t(($) => $.tab_body.integrations.dingtalk_identity_fallback_name);
  const permissionDenied = t(
    ($) => $.tab_body.integrations.dingtalk_account_permission_denied,
  );

  function rejectUnauthorizedOperation(): boolean {
    if (permissionLoading || canOperate) return false;
    toast.error(permissionDenied);
    return true;
  }

  function statusLabel(status: string): string {
    switch (status) {
      case "active":
        return t(($) => $.tab_body.integrations.dingtalk_account_status_active);
      case "pending":
        return t(($) => $.tab_body.integrations.dingtalk_account_status_pending);
      case "failed":
        return t(($) => $.tab_body.integrations.dingtalk_account_status_failed);
      case "skipped":
        return t(($) => $.tab_body.integrations.dingtalk_account_status_skipped);
      case "bound_to_other_agent":
        return t(($) => $.tab_body.integrations.dingtalk_account_status_bound_to_other_agent);
      case "inconsistent":
        return t(($) => $.tab_body.integrations.dingtalk_account_status_inconsistent);
      case "router_unavailable":
        return t(($) => $.tab_body.integrations.dingtalk_account_status_router_unavailable);
      default:
        return t(($) => $.tab_body.integrations.dingtalk_account_status_unbound);
    }
  }

  useEffect(() => {
    if (!attempt) {
      setExpired(false);
      return;
    }
    const expiresAt = Date.parse(attempt.expiresAt);
    const remaining = expiresAt - Date.now();
    if (!Number.isFinite(expiresAt) || remaining <= 0) {
      setExpired(true);
      return;
    }
    setExpired(false);
    const timer = setTimeout(() => setExpired(true), remaining);
    return () => clearTimeout(timer);
  }, [attempt]);

  useEffect(() => {
    if (!connected) return;
    setAttempt(null);
    setExpired(false);
  }, [connected]);

  async function startBinding() {
    if (rejectUnauthorizedOperation()) return;
    setActionError(null);
    try {
      const nextAttempt = await beginBinding.mutateAsync({ agentId, bindingMode });
      if (!nextAttempt.bindingId || !nextAttempt.qrCodeUrl || !nextAttempt.expiresAt) {
        setActionError(beginFailed);
        return;
      }
      setAttempt(nextAttempt);
    } catch (error) {
      setActionError(
        errorCode(error) === "message_binding_conflicts_with_native_subscription"
          ? t(($) => $.tab_body.integrations.dingtalk_account_native_subscription_blocked)
          : errorMessage(error, beginFailed),
      );
    }
  }

  async function toggleNativeSubscription(enabled: boolean) {
    if (rejectUnauthorizedOperation()) return;
    if (enabled && nativeSubscriptionBlocked) return;
    setActionError(null);
    try {
      await setNativeSubscription.mutateAsync({ agentId, enabled });
    } catch (error) {
      setActionError(localizeError(
        error,
        t(($) => $.tab_body.integrations.dingtalk_identity_native_subscription_failed),
      ));
    }
  }

  async function unbind() {
    if (!canUnbind) return;
    if (rejectUnauthorizedOperation()) return;
    setActionError(null);
    try {
      await deleteBinding.mutateAsync({ agentId, bindingMode });
      setConfirmOpen(false);
    } catch (error) {
      const fallback = bindingMode === "message"
        ? t(($) => $.tab_body.integrations.dingtalk_account_unbind_failed)
        : t(($) => $.tab_body.integrations.dingtalk_identity_unbind_failed);
      setActionError(errorMessage(error, fallback));
    }
  }

  async function updateSurface(surfaceType: DingTalkProcessingSurface): Promise<boolean> {
    if (rejectUnauthorizedOperation()) return false;
    if (bindingMode !== "message" || currentBinding?.messageRoute.surfaceType === surfaceType) {
      return true;
    }
    setActionError(null);
    try {
      await updateBindingSurface.mutateAsync({ agentId, surfaceType });
      return true;
    } catch (error) {
      setActionError(errorMessage(
        error,
        t(($) => $.tab_body.integrations.dingtalk_account_surface_update_failed),
      ));
      return false;
    }
  }

  return (
    <section
      className="rounded-lg border"
      data-testid={bindingMode === "message" ? "dingtalk-account-binding-card" : "dingtalk-identity-binding-card"}
    >
      <div className="flex items-start gap-3 p-4">
        <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border bg-muted/40 text-muted-foreground">
          {bindingMode === "message" ? <Link2 className="h-4 w-4" /> : <ShieldCheck className="h-4 w-4" />}
        </span>
        <div className="min-w-0 flex-1 space-y-1">
          <h3 className="text-body font-medium">{title}</h3>
          <p className="text-caption leading-relaxed text-muted-foreground">{description}</p>
        </div>
      </div>

      <div className="border-t px-4 py-3">
        {listingPending ? (
          <p className="text-caption text-muted-foreground">
            {t(($) => $.tab_body.integrations.dingtalk_account_loading)}
          </p>
        ) : data?.configured !== true ? (
          <p className="text-caption text-muted-foreground">
            {t(($) => $.tab_body.integrations.dingtalk_account_not_configured)}
          </p>
        ) : showBoundAccount && accountOutcome ? (
          <div className="space-y-3">
            <div
              className="flex items-start justify-between gap-3"
              data-testid={bindingMode === "message"
                ? "dingtalk-account-binding-active-row"
                : "dingtalk-identity-binding-active-row"}
            >
              <div className="flex min-w-0 items-start gap-3">
                <Avatar>
                  {accountOutcome.accountAvatarUrl ? (
                    <AvatarImage src={accountOutcome.accountAvatarUrl} alt={displayName(accountOutcome, fallbackName)} />
                  ) : null}
                  <AvatarFallback>{displayName(accountOutcome, fallbackName).slice(0, 1)}</AvatarFallback>
                </Avatar>
                <div className="min-w-0">
                  <p className="truncate text-body font-medium">{displayName(accountOutcome, fallbackName)}</p>
                  <div className="mt-1 text-caption text-muted-foreground">
                    {bindingMode === "message" && currentBinding?.messageRoute.status === "active" ? (
                      <DingTalkMessageScopeSummary outcome={currentBinding.messageRoute} />
                    ) : bindingMode === "message" &&
                      (currentBinding?.messageRoute.status === "bound_to_other_agent" ||
                        currentBinding?.messageRoute.status === "inconsistent") ? (
                      <p className="text-destructive" role="alert">
                        {t(($) => $.tab_body.integrations.dingtalk_account_binding_invalid_warning)}
                      </p>
                    ) : bindingMode === "message" && currentBinding?.messageRoute.status === "router_unavailable" ? (
                      <p className="text-amber-700 dark:text-amber-400" role="status">
                        {t(($) => $.tab_body.integrations.dingtalk_account_router_unavailable_warning)}
                      </p>
                    ) : bindingMode === "message" && currentBinding ? (
                      <p className="text-destructive" role="alert">
                        {t(($) => $.tab_body.integrations.dingtalk_account_unbound_warning)}
                      </p>
                    ) : (
                      <p>{t(($) => $.tab_body.integrations.dingtalk_identity_connected)}</p>
                    )}
                  </div>
                  {accountOutcome.organizationName?.trim() ? (
                    <p className="mt-1 text-caption text-muted-foreground">
                      {t(($) => $.tab_body.integrations.dingtalk_account_organization)}: {accountOutcome.organizationName}
                    </p>
                  ) : null}
                  {bindingMode === "message" && messageRouteActive && currentBinding?.messageRoute.surfaceType ? (
                    <div className="mt-2 flex flex-wrap items-center gap-2">
                      <span className="text-caption text-muted-foreground">
                        {t(($) => $.tab_body.integrations.dingtalk_account_run_mode)}
                      </span>
                      <DingTalkRunModePicker
                        value={currentBinding.messageRoute.surfaceType}
                        disabled={permissionLoading || updateBindingSurface.isPending}
                        readOnly={!canOperate}
                        onReadOnlyClick={rejectUnauthorizedOperation}
                        onConfirm={updateSurface}
                      />
                    </div>
                  ) : null}
                </div>
              </div>
              <div className="flex shrink-0 flex-wrap items-center justify-end gap-x-4 gap-y-2">
                {showNativeSubscription ? (
                  <div className="flex items-center gap-2">
                    <Label
                      id={nativeSubscriptionLabelId}
                      htmlFor={nativeSubscriptionSwitchId}
                      className={`text-caption ${nativeSubscriptionDisabled ? "text-muted-foreground" : ""}`}
                    >
                      {t(($) => $.tab_body.integrations.dingtalk_identity_native_subscription)}
                    </Label>
                    <Switch
                      id={nativeSubscriptionSwitchId}
                      size="sm"
                      checked={nativeSubscriptionOn}
                      disabled={nativeSubscriptionDisabled}
                      aria-labelledby={nativeSubscriptionLabelId}
                      aria-describedby={nativeSubscriptionBlocked
                        ? `${nativeSubscriptionHintId} ${nativeSubscriptionBlockedId}`
                        : nativeSubscriptionHintId}
                      aria-busy={setNativeSubscription.isPending || undefined}
                      onCheckedChange={(enabled) => void toggleNativeSubscription(enabled)}
                    />
                    {nativeStreamWatched ? <NativeStreamIndicator stream={nativeStream} /> : null}
                  </div>
                ) : null}
                <Button
                  variant="destructive"
                  size="sm"
                  onClick={() => {
                    if (!rejectUnauthorizedOperation()) setConfirmOpen(true);
                  }}
                  className="shrink-0"
                  disabled={permissionLoading || deleteBinding.isPending || updateBindingSurface.isPending ||
                    setNativeSubscription.isPending}
                >
                  <Trash2 className="h-3 w-3" />
                  {t(($) => $.tab_body.integrations.dingtalk_account_unbind)}
                </Button>
              </div>
            </div>
            {showNativeSubscription ? (
              <div className="space-y-1 text-caption leading-relaxed text-muted-foreground">
                <p id={nativeSubscriptionHintId}>
                  {t(($) => $.tab_body.integrations.dingtalk_identity_native_subscription_hint)}
                </p>
                {nativeSubscriptionBlocked ? (
                  <p id={nativeSubscriptionBlockedId} className="text-foreground">
                    {t(($) => $.tab_body.integrations.dingtalk_identity_native_subscription_blocked)}
                  </p>
                ) : null}
                {nativeStream?.state === "disconnected" && nativeStream.lastError ? (
                  <p className="break-words text-destructive" role="status">
                    {t(($) => $.tab_body.integrations.dingtalk_identity_native_stream_error, {
                      failures: nativeStream.failures,
                      error: nativeStream.lastError,
                    })}
                  </p>
                ) : null}
              </div>
            ) : null}
            {showNativeSubscription && nativeStatus.data ? (
              <NativeDEAPLink
                key={`${nativeStatus.data.deapLink?.deapAgentUuid ?? ""}:${nativeStatus.data.deapLink?.supervisorUid ?? ""}`}
                agentId={agentId}
                link={nativeStatus.data.deapLink}
                editable={nativeStatus.data.deapLinkEditable}
              />
            ) : null}
          </div>
        ) : (
          <div className="space-y-3">
            {bindingMode === "identity" && canOperate && !permissionLoading ? <ReusableExecutionIdentity key={agentId} agentId={agentId} /> : null}
            {messageBindingBlocked ? (
              <p className="text-caption text-foreground" role="note">
                {t(($) => $.tab_body.integrations.dingtalk_account_native_subscription_blocked)}
              </p>
            ) : null}
            {bindingMode === "message" && messageRoutePending ? (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.tab_body.integrations.dingtalk_account_pending_restart)}
              </p>
            ) : null}
            {messageBindingFailed && currentBinding ? (
              <div className="space-y-1 text-caption text-muted-foreground">
                <p>
                  {t(($) => $.tab_body.integrations.dingtalk_account_message_route)}: {" "}
                  {statusLabel(currentBinding.messageRoute.status)}
                </p>
                {currentBinding.messageRoute.error?.message ? (
                  <p className="text-destructive" role="alert">
                    {currentBinding.messageRoute.error.message}
                  </p>
                ) : null}
              </div>
            ) : null}
            <Button
              variant="outline"
              size="sm"
              onClick={() => void startBinding()}
              disabled={permissionLoading || beginBinding.isPending || messageBindingBlocked}
            >
              {retryMessageBinding ? <RefreshCw className="h-3 w-3" /> : null}
              {beginBinding.isPending ? startingLabel :
                retryMessageBinding
                  ? t(($) => $.tab_body.integrations.dingtalk_account_new_qr)
                  : connectLabel}
            </Button>
            {showManualBinding ? (
              <ManualMessageBinding
                agentId={agentId}
                disabled={permissionLoading || messageBindingBlocked}
                rejectUnauthorizedOperation={rejectUnauthorizedOperation}
              />
            ) : null}
          </div>
        )}

        {actionError ? <p className="mt-3 text-caption text-destructive" role="alert">{actionError}</p> : null}
      </div>

      {attempt ? (
        <Dialog open onOpenChange={(open) => { if (!open) setAttempt(null); }}>
          <DialogContent className="max-w-sm">
            <DialogHeader>
              <DialogTitle>
                {bindingMode === "message"
                  ? t(($) => $.tab_body.integrations.dingtalk_account_dialog_title)
                  : t(($) => $.tab_body.integrations.dingtalk_identity_dialog_title)}
              </DialogTitle>
              <DialogDescription>
                {bindingMode === "message"
                  ? t(($) => $.tab_body.integrations.dingtalk_account_dialog_description, { agent: agentName })
                  : t(($) => $.tab_body.integrations.dingtalk_identity_dialog_description, { agent: agentName })}
              </DialogDescription>
            </DialogHeader>
            <div className="flex flex-col items-center gap-4 py-2">
              {expired ? (
                <div className="space-y-2 text-center">
                  <p className="text-body font-medium">
                    {t(($) => $.tab_body.integrations.dingtalk_account_expired_title)}
                  </p>
                  <p className="text-caption text-muted-foreground">
                    {t(($) => $.tab_body.integrations.dingtalk_account_expired_description)}
                  </p>
                </div>
              ) : (
                <>
                  <div className="rounded-md border bg-white p-3">
                    <QRCode
                      value={attempt.qrCodeUrl}
                      size={192}
                      aria-label={bindingMode === "message"
                        ? t(($) => $.tab_body.integrations.dingtalk_account_qr_label)
                        : t(($) => $.tab_body.integrations.dingtalk_identity_qr_label)}
                    />
                  </div>
                  <p className="text-center text-caption text-muted-foreground">
                    {bindingMode === "message"
                      ? t(($) => $.tab_body.integrations.dingtalk_account_scan_hint)
                      : t(($) => $.tab_body.integrations.dingtalk_identity_scan_hint)}
                  </p>
                </>
              )}
            </div>
            <DialogFooter>
              <Button variant="outline" size="sm" onClick={() => setAttempt(null)}>
                {t(($) => $.tab_body.integrations.dingtalk_account_close)}
              </Button>
              {expired ? (
                <Button size="sm" onClick={() => void startBinding()} disabled={permissionLoading || beginBinding.isPending}>
                  <RefreshCw className="h-3 w-3" />
                  {t(($) => $.tab_body.integrations.dingtalk_account_new_qr)}
                </Button>
              ) : null}
            </DialogFooter>
          </DialogContent>
        </Dialog>
      ) : null}

      <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {bindingMode === "message"
                ? t(($) => $.tab_body.integrations.dingtalk_account_confirm_title)
                : t(($) => $.tab_body.integrations.dingtalk_identity_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {bindingMode === "message"
                ? t(($) => $.tab_body.integrations.dingtalk_account_confirm_description)
                : t(($) => $.tab_body.integrations.dingtalk_identity_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteBinding.isPending}>
              {t(($) => $.tab_body.integrations.dingtalk_account_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => void unbind()}
              disabled={deleteBinding.isPending}
            >
              {deleteBinding.isPending
                ? t(($) => $.tab_body.integrations.dingtalk_account_unbinding)
                : t(($) => $.tab_body.integrations.dingtalk_account_unbind)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  );
}
