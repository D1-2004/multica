"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, Link2, RefreshCw, ShieldCheck, Trash2 } from "lucide-react";
import { QRCode } from "react-qr-code";
import { mid2Url } from "@ali/ding-mediaid";
import type {
  BeginDingTalkAccountBindingResponse,
  DingTalkAccountBindingOutcome,
  DingTalkAccountBindingsResponse,
  DingTalkBindingMode,
  DingTalkConversationSummary,
  DingTalkMessageRouteOutcome,
} from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  dingtalkAccountBindingsOptions,
  useBeginDingTalkAccountBinding,
  useDeleteDingTalkAccountBinding,
} from "@multica/core/dingtalk-account-bindings";
import { Button } from "@multica/ui/components/ui/button";
import { Avatar, AvatarFallback, AvatarImage } from "@multica/ui/components/ui/avatar";
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

  switch (outcome.messageScope) {
    case "all":
      return <p>{t(($) => $.tab_body.integrations.dingtalk_account_scope_all)}</p>;
    case "custom": {
      const summary = t(
        ($) => $.tab_body.integrations.dingtalk_account_scope_custom,
        { count: outcome.conversations.length },
      );
      return (
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
    }
    case "direct_only":
    default:
      return (
        <p>
          {t(($) => $.tab_body.integrations.dingtalk_account_scope_direct_only)}
        </p>
      );
  }
}

export function DingTalkAccountBindingCard({
  agentId,
  agentName,
  bindingMode = "message",
}: {
  agentId: string;
  agentName: string;
  bindingMode?: DingTalkBindingMode;
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
    />
  );
}

function DingTalkBindingModeCard({
  agentId,
  agentName,
  bindingMode,
  data,
  listingPending,
}: {
  agentId: string;
  agentName: string;
  bindingMode: DingTalkBindingMode;
  data?: DingTalkAccountBindingsResponse;
  listingPending: boolean;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const beginBinding = useBeginDingTalkAccountBinding(wsId);
  const deleteBinding = useDeleteDingTalkAccountBinding(wsId);
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
  const messageBindingFailed = bindingMode === "message" &&
    currentBinding?.messageRoute.status === "failed";
  const retryMessageBinding = bindingMode === "message" &&
    (messageRoutePending || messageBindingFailed);
  const identityActive = currentBinding?.dwsIdentity.status === "active";
  const connected = bindingMode === "message"
    ? messageRouteActive
    : identityActive;
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
    if (!connected) return;
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
          <h3 className="text-sm font-medium">{title}</h3>
          <p className="text-xs leading-relaxed text-muted-foreground">{description}</p>
        </div>
      </div>

      <div className="border-t px-4 py-3">
        {listingPending ? (
          <p className="text-xs text-muted-foreground">
            {t(($) => $.tab_body.integrations.dingtalk_account_loading)}
          </p>
        ) : data?.configured !== true ? (
          <p className="text-xs text-muted-foreground">
            {t(($) => $.tab_body.integrations.dingtalk_account_not_configured)}
          </p>
        ) : connected && accountOutcome ? (
          <div className="flex items-center justify-between gap-3">
            <div className="flex min-w-0 items-center gap-3">
              <Avatar>
                {accountOutcome.accountAvatarUrl ? (
                  <AvatarImage src={accountOutcome.accountAvatarUrl} alt={displayName(accountOutcome, fallbackName)} />
                ) : null}
                <AvatarFallback>{displayName(accountOutcome, fallbackName).slice(0, 1)}</AvatarFallback>
              </Avatar>
              <div className="min-w-0">
                <p className="truncate text-sm font-medium">{displayName(accountOutcome, fallbackName)}</p>
                <div className="mt-1 text-xs text-muted-foreground">
                  {bindingMode === "message" && currentBinding ? (
                    <DingTalkMessageScopeSummary outcome={currentBinding.messageRoute} />
                  ) : (
                    <p>{t(($) => $.tab_body.integrations.dingtalk_identity_connected)}</p>
                  )}
                </div>
                {accountOutcome.organizationName?.trim() ? (
                  <p className="mt-1 text-xs text-muted-foreground">
                    {t(($) => $.tab_body.integrations.dingtalk_account_organization)}: {accountOutcome.organizationName}
                  </p>
                ) : null}
              </div>
            </div>
            <Button
              variant="destructive"
              size="sm"
              onClick={() => setConfirmOpen(true)}
              disabled={deleteBinding.isPending}
            >
              <Trash2 className="h-3 w-3" />
              {t(($) => $.tab_body.integrations.dingtalk_account_unbind)}
            </Button>
          </div>
        ) : (
          <div className="space-y-3">
            {bindingMode === "message" && messageRoutePending ? (
              <p className="text-xs text-muted-foreground">
                {t(($) => $.tab_body.integrations.dingtalk_account_pending_restart)}
              </p>
            ) : null}
            {messageBindingFailed && currentBinding ? (
              <div className="space-y-1 text-xs text-muted-foreground">
                <p>
                  {t(($) => $.tab_body.integrations.dingtalk_account_message_route)}: {" "}
                  {statusLabel(currentBinding.messageRoute.status)}
                </p>
              </div>
            ) : null}
            <Button
              variant="outline"
              size="sm"
              onClick={() => void startBinding()}
              disabled={beginBinding.isPending}
            >
              {retryMessageBinding ? <RefreshCw className="h-3 w-3" /> : null}
              {beginBinding.isPending ? startingLabel :
                retryMessageBinding
                  ? t(($) => $.tab_body.integrations.dingtalk_account_new_qr)
                  : connectLabel}
            </Button>
          </div>
        )}

        {actionError ? <p className="mt-3 text-xs text-destructive" role="alert">{actionError}</p> : null}
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
                  <p className="text-sm font-medium">
                    {t(($) => $.tab_body.integrations.dingtalk_account_expired_title)}
                  </p>
                  <p className="text-xs text-muted-foreground">
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
                  <p className="text-center text-xs text-muted-foreground">
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
                <Button size="sm" onClick={() => void startBinding()} disabled={beginBinding.isPending}>
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
