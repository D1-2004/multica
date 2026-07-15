"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link2, RefreshCw, Trash2 } from "lucide-react";
import { QRCode } from "react-qr-code";
import type {
  BeginDingTalkAccountBindingResponse,
  DingTalkAccountBindingOutcome,
} from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  dingtalkAccountBindingsOptions,
  useBeginDingTalkAccountBinding,
  useDeleteDingTalkAccountBinding,
} from "@multica/core/dingtalk-account-bindings";
import { Button } from "@multica/ui/components/ui/button";
import {
  Avatar,
  AvatarFallback,
  AvatarImage,
} from "@multica/ui/components/ui/avatar";
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
  const name = outcome.accountDisplayName?.trim();
  return name || fallback;
}

export function DingTalkAccountBindingCard({
  agentId,
  agentName,
}: {
  agentId: string;
  agentName: string;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const { data, isPending: listingPending } = useQuery({
    ...dingtalkAccountBindingsOptions(wsId),
    enabled: !!wsId,
  });
  const beginBinding = useBeginDingTalkAccountBinding(wsId);
  const deleteBinding = useDeleteDingTalkAccountBinding(wsId);
  const [attempt, setAttempt] =
    useState<BeginDingTalkAccountBindingResponse | null>(null);
  const [expired, setExpired] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);

  const currentBinding = useMemo(
    () =>
      data?.bindings.find(
        (binding) => binding.agentId === agentId,
      ) ?? null,
    [agentId, data?.bindings],
  );
  const dwsIdentityActive = currentBinding?.dwsIdentity.status === "active";
  const messageRouteActive = currentBinding?.messageRoute.status === "active";
  const hasConnectedBinding = dwsIdentityActive || messageRouteActive;
  const pendingBinding = currentBinding?.messageRoute.status === "pending";
  const accountOutcome = dwsIdentityActive
    ? currentBinding?.dwsIdentity
    : currentBinding?.messageRoute;

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
    if (!dwsIdentityActive) return;
    setAttempt(null);
    setExpired(false);
  }, [dwsIdentityActive]);

  async function startBinding() {
    setActionError(null);
    try {
      const nextAttempt = await beginBinding.mutateAsync(agentId);
      if (
        !nextAttempt.installationId ||
        !nextAttempt.qrCodeUrl ||
        !nextAttempt.expiresAt
      ) {
        setActionError(
          t(($) => $.tab_body.integrations.dingtalk_account_begin_failed),
        );
        return;
      }
      setAttempt(nextAttempt);
    } catch (error) {
      setActionError(
        errorMessage(
          error,
          t(($) => $.tab_body.integrations.dingtalk_account_begin_failed),
        ),
      );
    }
  }

  async function unbind() {
    if (!currentBinding || !hasConnectedBinding) return;
    setActionError(null);
    try {
      await deleteBinding.mutateAsync(currentBinding.id);
      setConfirmOpen(false);
    } catch (error) {
      setActionError(
        errorMessage(
          error,
          t(($) => $.tab_body.integrations.dingtalk_account_unbind_failed),
        ),
      );
    }
  }

  const beginLabel = pendingBinding
    ? t(($) => $.tab_body.integrations.dingtalk_account_new_qr)
    : t(($) => $.tab_body.integrations.dingtalk_account_connect);

  return (
    <section className="rounded-lg border" data-testid="dingtalk-account-binding-card">
      <div className="flex items-start gap-3 p-4">
        <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border bg-muted/40 text-muted-foreground">
          <Link2 className="h-4 w-4" />
        </span>
        <div className="min-w-0 flex-1 space-y-1">
          <h3 className="text-sm font-medium">
            {t(($) => $.tab_body.integrations.dingtalk_account_title)}
          </h3>
          <p className="text-xs leading-relaxed text-muted-foreground">
            {t(($) => $.tab_body.integrations.dingtalk_account_description)}
          </p>
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
        ) : currentBinding && hasConnectedBinding && accountOutcome ? (
          <div className="space-y-3">
            <div className="flex items-center justify-between gap-3">
              <div className="flex min-w-0 items-center gap-3">
                <Avatar>
                  {accountOutcome.accountAvatarUrl ? (
                    <AvatarImage
                      src={accountOutcome.accountAvatarUrl}
                      alt={displayName(
                        accountOutcome,
                        t(($) => $.tab_body.integrations.dingtalk_account_fallback_name),
                      )}
                    />
                  ) : null}
                  <AvatarFallback>
                    {displayName(
                      accountOutcome,
                      t(($) => $.tab_body.integrations.dingtalk_account_fallback_name),
                    ).slice(0, 1)}
                  </AvatarFallback>
                </Avatar>
                <div className="min-w-0">
                  <p className="truncate text-sm font-medium">
                    {displayName(
                      accountOutcome,
                      t(($) => $.tab_body.integrations.dingtalk_account_fallback_name),
                    )}
                  </p>
                  <div className="mt-1 space-y-1 text-xs text-muted-foreground">
                    <p>
                      {t(($) => $.tab_body.integrations.dingtalk_account_dws_identity)}: {" "}
                      {dwsIdentityActive
                        ? t(($) => $.tab_body.integrations.dingtalk_account_status_active)
                        : t(($) => $.tab_body.integrations.dingtalk_account_status_unbound)}
                    </p>
                    <p>
                      {t(($) => $.tab_body.integrations.dingtalk_account_message_route)}: {" "}
                      {messageRouteActive
                        ? t(($) => $.tab_body.integrations.dingtalk_account_status_active)
                        : currentBinding.messageRoute.status === "pending"
                          ? t(($) => $.tab_body.integrations.dingtalk_account_status_pending)
                          : t(($) => $.tab_body.integrations.dingtalk_account_status_unbound)}
                    </p>
                  </div>
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
          </div>
        ) : (
          <div className="space-y-3">
            {pendingBinding ? (
              <p className="text-xs text-muted-foreground">
                {t(($) => $.tab_body.integrations.dingtalk_account_pending_restart)}
              </p>
            ) : null}
            <Button
              variant="outline"
              size="sm"
              onClick={() => void startBinding()}
              disabled={beginBinding.isPending}
            >
              {pendingBinding ? <RefreshCw className="h-3 w-3" /> : null}
              {beginBinding.isPending
                ? t(($) => $.tab_body.integrations.dingtalk_account_starting)
                : beginLabel}
            </Button>
          </div>
        )}

        {actionError ? (
          <p className="mt-3 text-xs text-destructive" role="alert">
            {actionError}
          </p>
        ) : null}
      </div>

      {attempt ? (
        <Dialog
          open
          onOpenChange={(open) => {
            if (!open) setAttempt(null);
          }}
        >
          <DialogContent className="max-w-sm">
            <DialogHeader>
              <DialogTitle>
                {t(($) => $.tab_body.integrations.dingtalk_account_dialog_title)}
              </DialogTitle>
              <DialogDescription>
                {t(
                  ($) => $.tab_body.integrations.dingtalk_account_dialog_description,
                  { agent: agentName },
                )}
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
                      aria-label="DingTalk account QR code"
                    />
                  </div>
                  <p className="text-center text-xs text-muted-foreground">
                    {t(($) => $.tab_body.integrations.dingtalk_account_scan_hint)}
                  </p>
                </>
              )}
            </div>
            <DialogFooter>
              <Button variant="outline" size="sm" onClick={() => setAttempt(null)}>
                {t(($) => $.tab_body.integrations.dingtalk_account_close)}
              </Button>
              {expired ? (
                <Button
                  size="sm"
                  onClick={() => void startBinding()}
                  disabled={beginBinding.isPending}
                >
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
              {t(($) => $.tab_body.integrations.dingtalk_account_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.tab_body.integrations.dingtalk_account_confirm_description)}
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
