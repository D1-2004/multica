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
  DingTalkMessageRouteOutcome,
  DingTalkProcessingSurface,
} from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  dingtalkAccountBindingsOptions,
  useBeginDingTalkAccountBinding,
  useDeleteDingTalkAccountBinding,
  useUpdateDingTalkAccountBindingSurface,
} from "@multica/core/dingtalk-account-bindings";
import { Button } from "@multica/ui/components/ui/button";
import { Avatar, AvatarFallback, AvatarImage } from "@multica/ui/components/ui/avatar";
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
import { useT } from "../../../i18n";

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback;
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

  const renderExpandableSummary = (summary: string) => (
    <div>
      <button
        type="button"
        className="flex items-center gap-1 text-left hover:text-foreground"
        aria-expanded={expanded}
        onClick={() => setExpanded((open) => !open)}
      >
        {expanded ? (
          <ChevronDown className="size-3 shrink-0" />
        ) : (
          <ChevronRight className="size-3 shrink-0" />
        )}
        <span>{summary}</span>
      </button>
      {expanded ? (
        <ul className="mt-2 space-y-2 pl-4">
          {outcome.conversations.map((conversation) => (
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
        renderExpandableSummary(subscriptionSummary)
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
        return renderExpandableSummary(summary);
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
  const messageRouteReconciliationState = bindingMode === "message" &&
    (currentBinding?.messageRoute.status === "unbound" ||
      currentBinding?.messageRoute.status === "bound_to_other_agent" ||
      currentBinding?.messageRoute.status === "inconsistent" ||
      currentBinding?.messageRoute.status === "router_unavailable");
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
      setActionError(errorMessage(error, beginFailed));
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
            <Button
              variant="destructive"
              size="sm"
              onClick={() => {
                if (!rejectUnauthorizedOperation()) setConfirmOpen(true);
              }}
              className="shrink-0"
              disabled={permissionLoading || deleteBinding.isPending || updateBindingSurface.isPending}
            >
              <Trash2 className="h-3 w-3" />
              {t(($) => $.tab_body.integrations.dingtalk_account_unbind)}
            </Button>
          </div>
        ) : (
          <div className="space-y-3">
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
              disabled={permissionLoading || beginBinding.isPending}
            >
              {retryMessageBinding ? <RefreshCw className="h-3 w-3" /> : null}
              {beginBinding.isPending ? startingLabel :
                retryMessageBinding
                  ? t(($) => $.tab_body.integrations.dingtalk_account_new_qr)
                  : connectLabel}
            </Button>
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
