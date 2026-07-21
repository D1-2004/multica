"use client";

import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ChevronRight, ExternalLink, RefreshCw, Trash2 } from "lucide-react";
// Named import, NOT default — same electron-vite CJS interop constraint
// documented in lark-tab.tsx.
import { QRCode } from "react-qr-code";
import { cn } from "@multica/ui/lib/utils";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Switch } from "@multica/ui/components/ui/switch";
import { Card, CardContent } from "@multica/ui/components/ui/card";
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
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@multica/ui/components/ui/dialog";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions } from "@multica/core/workspace/queries";
import { useActorName } from "@multica/core/workspace/hooks";
import { dingtalkInstallationsOptions, dingtalkKeys } from "@multica/core/dingtalk";
import { api, ApiError } from "@multica/core/api";
import type {
  DingTalkInstallation,
  DingTalkInstallCapabilities,
  DingTalkInstallStatusResponse,
  DingTalkTransportMode,
} from "@multica/core/types";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";

// The DingTalk developer console where installed apps are managed
// (credentials, permissions, release). The scan-to-create flow does not
// return a console deep link per app, so we link to the console home.
/** Deep link into the dev console's enterprise internal-app list — the
 * page the scan-created bot app lives on. A per-app detail link needs the
 * console's numeric appId, which the device flow does not return, so the
 * list page is the closest stable target. */
const DINGTALK_DEV_CONSOLE = "https://open-dev.dingtalk.com/fe/app#/corp/app";

function isDingTalkInstallationApproving(installation: DingTalkInstallation): boolean {
  return installation.registration_status === "APPROVING";
}

// DingTalkTab is the workspace settings panel for DingTalk enterprise bot
// installations, created through the scan-to-create device flow
// ("一键创建钉钉应用"). Listing is member-visible; the disconnect action
// is admin-only (the backend enforces it; the UI hides the button for
// non-admins to match).
//
// Adding a new installation flows through the Agent detail page: the
// install path is per-agent (each Multica Agent gets exactly one app —
// see the (workspace_id, agent_id, channel_type) UNIQUE), so the
// "Bind your first agent" copy in the empty state hints users at the
// right entry point. Mirrors LarkTab.
export function DingTalkTab() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const user = useAuthStore((s) => s.user);

  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const canManage =
    currentMember?.role === "owner" || currentMember?.role === "admin";

  const { data, isLoading } = useQuery({
    ...dingtalkInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  const installations = data?.installations ?? [];
  const configured = data?.configured === true;

  const [disconnectTarget, setDisconnectTarget] = useState<string | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);
  const [retryingRouter, setRetryingRouter] = useState<string | null>(null);

  async function handleDisconnect() {
    if (!disconnectTarget || disconnecting) return;
    setDisconnecting(true);
    try {
      await api.deleteDingTalkInstallation(wsId, disconnectTarget);
      await qc.invalidateQueries({ queryKey: dingtalkKeys.installations(wsId) });
      toast.success(t(($) => $.dingtalk.toast_disconnected));
      setDisconnectTarget(null);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.dingtalk.toast_disconnect_failed));
    } finally {
      setDisconnecting(false);
    }
  }

  async function handleRetryRouter(installationId: string) {
    if (retryingRouter) return;
    setRetryingRouter(installationId);
    try {
      await api.retryDingTalkRouterRegistration(wsId, installationId);
      await qc.invalidateQueries({ queryKey: dingtalkKeys.installations(wsId) });
      toast.success(t(($) => $.dingtalk.toast_router_retry_succeeded));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.dingtalk.toast_router_retry_failed));
    } finally {
      setRetryingRouter(null);
    }
  }

  return (
    <div className="space-y-8">
      <section className="space-y-1">
        <p className="text-sm text-muted-foreground">
          {t(($) => $.dingtalk.page_description)}
        </p>
      </section>

      {!configured ? (
        <Card>
          <CardContent className="space-y-2">
            <p className="text-sm font-medium">{t(($) => $.dingtalk.not_enabled_title)}</p>
            <p className="text-xs text-muted-foreground">
              {t(($) => $.dingtalk.not_enabled_description_prefix)}{" "}
              <code className="rounded bg-muted px-1 py-0.5 text-[10px]">
                MULTICA_DINGTALK_SECRET_KEY
              </code>{" "}
              {t(($) => $.dingtalk.not_enabled_description_suffix)}{" "}
              {t(($) => $.dingtalk.not_enabled_self_host_hint)}
            </p>
          </CardContent>
        </Card>
      ) : (
        <section className="space-y-3">
          <h2 className="text-sm font-semibold">{t(($) => $.dingtalk.connected_bots)}</h2>
          {isLoading ? (
            <Card>
              <CardContent>
                <p className="text-sm text-muted-foreground">{t(($) => $.dingtalk.loading)}</p>
              </CardContent>
            </Card>
          ) : installations.length === 0 ? (
            <Card>
              <CardContent className="space-y-2">
                <p className="text-sm font-medium">{t(($) => $.dingtalk.empty_title)}</p>
                <p className="text-xs text-muted-foreground">
                  {t(($) => $.dingtalk.empty_description_prefix)}{" "}
                  <strong>{t(($) => $.dingtalk.empty_description_cta)}</strong>{" "}
                  {t(($) => $.dingtalk.empty_description_suffix)}
                </p>
              </CardContent>
            </Card>
          ) : (
            <Card>
              <CardContent className="divide-y">
                {installations.map((inst) => (
                  <InstallationRow
                    key={inst.id}
                    installation={inst}
                    canManage={canManage}
                    retryingRouter={retryingRouter === inst.id}
                    onRetryRouter={() => void handleRetryRouter(inst.id)}
                    onDisconnect={() => setDisconnectTarget(inst.id)}
                  />
                ))}
              </CardContent>
            </Card>
          )}
        </section>
      )}

      <AlertDialog
        open={!!disconnectTarget}
        onOpenChange={(v) => {
          if (!v && !disconnecting) setDisconnectTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.dingtalk.disconnect_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.dingtalk.disconnect_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>
              {t(($) => $.dingtalk.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleDisconnect} disabled={disconnecting}>
              {disconnecting
                ? t(($) => $.dingtalk.disconnecting)
                : t(($) => $.dingtalk.disconnect)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function InstallationRow({
  installation,
  canManage,
  retryingRouter,
  onRetryRouter,
  onDisconnect,
}: {
  installation: DingTalkInstallation;
  canManage: boolean;
  retryingRouter: boolean;
  onRetryRouter: () => void;
  onDisconnect: () => void;
}) {
  const { t } = useT("settings");
  // The bot is bound 1:1 to a Multica Agent. Render the Multica agent's
  // identity here rather than the raw client_id — that means nothing to
  // product users.
  const { getAgentName } = useActorName();
  const isActive = installation.status === "active";
  const isApproving = isDingTalkInstallationApproving(installation);
  const agentName = getAgentName(installation.agent_id);
  return (
    <div className="flex items-start justify-between gap-4 py-3 first:pt-0 last:pb-0">
      <div className="flex items-start gap-3">
        <ActorAvatar
          actorType="agent"
          actorId={installation.agent_id}
          size="lg"
          enableHoverCard
          profileLink
        />
        <div className="space-y-1">
          <p className="text-sm font-medium">
            {agentName}
            {!isActive && (
              <span className="ml-2 rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
                {t(($) => $.dingtalk.revoked_badge)}
              </span>
            )}
          </p>
          <p className="text-[10px] text-muted-foreground">
            {t(($) => $.dingtalk.installed_at_label, {
              when: new Date(installation.installed_at).toLocaleString(),
            })}
          </p>
          {isApproving && (
            <p className="text-[10px] text-amber-700 dark:text-amber-400">
              {t(($) => $.dingtalk.approving_hint)}
            </p>
          )}
          {installation.transport_mode === "HTTP_CALLBACK" && (
            <p className="text-[10px] text-muted-foreground">
              {installation.router_status === "registered"
                ? t(($) => $.dingtalk.router_registered)
                : t(($) => $.dingtalk.router_registration_failed)}
            </p>
          )}
        </div>
      </div>
      {canManage && isActive && (
        <div className="flex items-center gap-2">
          {installation.transport_mode === "HTTP_CALLBACK" &&
            installation.router_status !== "registered" && (
              <Button
                variant="outline"
                size="sm"
                onClick={onRetryRouter}
                disabled={retryingRouter}
              >
                <RefreshCw className={cn("h-3 w-3", retryingRouter && "animate-spin")} />
                {t(($) => $.dingtalk.router_retry)}
              </Button>
            )}
          <Button variant="outline" size="sm" onClick={onDisconnect}>
            <Trash2 className="h-3 w-3" />
            {t(($) => $.dingtalk.disconnect)}
          </Button>
        </div>
      )}
    </div>
  );
}

// DingTalkAgentBindButton is the per-agent CTA we expose from the agent
// detail page. Visibility rules mirror LarkAgentBindButton:
//   1. Non-owner/admin viewers see nothing — the backend gates install /
//      status / disconnect on those roles.
//   2. If this agent ALREADY has an active installation, owner/admins see
//      the connected badge regardless of install_supported (which only
//      governs NEW scan-installs).
//   3. Otherwise the Bind CTA shows only when install_supported is true.
export function DingTalkAgentBindButton({
  agentId,
  agentName,
  className,
  onShowConnectedDetails,
}: {
  agentId: string;
  agentName?: string;
  className?: string;
  /** When set, the connected state renders as a compact read-only status
   * row that invokes this callback on click instead of the full badge —
   * same contract as LarkAgentBindButton. */
  onShowConnectedDetails?: () => void;
}) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const user = useAuthStore((s) => s.user);
  const [dialogOpen, setDialogOpen] = useState(false);

  const { data: listing } = useQuery({
    ...dingtalkInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  // `configured` (at-rest key present) gates the CTA; `install_supported`
  // only governs whether the scan-to-create device flow is wired. The
  // manual-credential path works whenever configured, so the button shows
  // even when the scan flow is down — the dialog opens straight into the
  // manual form in that case.
  const configured = listing?.configured === true;
  const installSupported = listing?.install_supported === true;

  const { data: members = [] } = useQuery({
    ...memberListOptions(wsId),
    enabled: !!wsId,
  });
  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const canManage =
    currentMember?.role === "owner" || currentMember?.role === "admin";

  if (!canManage) return null;

  const existing = listing?.installations.find(
    (inst) => inst.agent_id === agentId && inst.status === "active",
  );
  if (existing) {
    return onShowConnectedDetails ? (
      <DingTalkAgentBotStatusRow
        onClick={onShowConnectedDetails}
        installation={existing}
        className={className}
      />
    ) : (
      <DingTalkAgentBotConnectedBadge installation={existing} className={className} />
    );
  }

  if (!configured) return null;

  return (
    <>
      <Button
        variant="outline"
        size="sm"
        className={className}
        onClick={() => setDialogOpen(true)}
        disabled={!agentId}
        title={
          agentName
            ? t(($) => $.dingtalk.bind_button_title, { agent: agentName })
            : undefined
        }
        data-testid="dingtalk-agent-bind"
      >
        <ExternalLink className="h-3 w-3" />
        {t(($) => $.dingtalk.bind_button)}
      </Button>
      {dialogOpen && (
        <DingTalkInstallDialog
          wsId={wsId}
          agentId={agentId}
          agentName={agentName}
          installSupported={installSupported}
          capabilities={listing?.capabilities}
          onClose={() => setDialogOpen(false)}
        />
      )}
    </>
  );
}

// DingTalkAgentBotStatusRow is the compact, read-only connected
// affordance the agent inspector renders instead of the full badge —
// a single full-width button deep-linking into the Integrations tab.
function DingTalkAgentBotStatusRow({
  onClick,
  installation,
  className,
}: {
  onClick: () => void;
  installation: DingTalkInstallation;
  className?: string;
}) {
  const { t } = useT("settings");
  const isApproving = isDingTalkInstallationApproving(installation);
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
        className,
      )}
      data-testid="dingtalk-agent-bot-status"
    >
      <span
        className={cn(
          "inline-block h-1.5 w-1.5 shrink-0 rounded-full",
          isApproving ? "bg-amber-500" : "bg-emerald-500",
        )}
      />
      <span className="truncate">
        {isApproving
          ? t(($) => $.dingtalk.agent_bot_approving_label)
          : t(($) => $.dingtalk.agent_bot_connected_label)}
      </span>
      <ChevronRight className="ml-auto h-3.5 w-3.5 shrink-0" />
    </button>
  );
}

// DingTalkAgentBotConnectedBadge is the full "already connected"
// affordance the Integrations tab renders in place of the Bind button:
// green-dot status + Disconnect, and a secondary link to the DingTalk
// developer console (the scan-to-create flow yields no per-app deep
// link, so the console home is the closest management surface).
function DingTalkAgentBotConnectedBadge({
  installation,
  className,
}: {
  installation: DingTalkInstallation;
  className?: string;
}) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const isApproving = isDingTalkInstallationApproving(installation);

  const [confirmOpen, setConfirmOpen] = useState(false);
  const [disconnecting, setDisconnecting] = useState(false);

  async function handleDisconnect() {
    if (disconnecting) return;
    setDisconnecting(true);
    try {
      await api.deleteDingTalkInstallation(wsId, installation.id);
      await qc.invalidateQueries({ queryKey: dingtalkKeys.installations(wsId) });
      toast.success(t(($) => $.dingtalk.toast_disconnected));
      setConfirmOpen(false);
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t(($) => $.dingtalk.toast_disconnect_failed),
      );
    } finally {
      setDisconnecting(false);
    }
  }

  return (
    <div
      className={cn("space-y-2", className)}
      data-testid="dingtalk-agent-bot-connected"
    >
      <div className="flex items-center justify-between gap-3">
        <span className="inline-flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
          <span
            className={cn(
              "inline-block h-1.5 w-1.5 shrink-0 rounded-full",
              isApproving ? "bg-amber-500" : "bg-emerald-500",
            )}
          />
          <span className="truncate">
            {isApproving
              ? t(($) => $.dingtalk.agent_bot_approving_label)
              : t(($) => $.dingtalk.agent_bot_connected_label)}
          </span>
        </span>
        <Button
          variant="destructive"
          size="sm"
          onClick={() => setConfirmOpen(true)}
          disabled={disconnecting}
          title={t(($) => $.dingtalk.agent_bot_disconnect_tooltip)}
          aria-label={t(($) => $.dingtalk.disconnect)}
          data-testid="dingtalk-agent-bot-disconnect"
        >
          <Trash2 className="h-3 w-3" />
          {disconnecting
            ? t(($) => $.dingtalk.disconnecting)
            : t(($) => $.dingtalk.disconnect)}
        </Button>
      </div>

      <a
        href={DINGTALK_DEV_CONSOLE}
        target="_blank"
        rel="noopener noreferrer"
        className="inline-flex items-center gap-1 text-xs text-muted-foreground underline-offset-2 transition-colors hover:text-foreground hover:underline"
        title={t(($) => $.dingtalk.agent_bot_manage_tooltip)}
      >
        <ExternalLink className="h-3 w-3" />
        {t(($) => $.dingtalk.agent_bot_manage_link)}
      </a>

      <AlertDialog
        open={confirmOpen}
        onOpenChange={(v) => {
          if (!v && !disconnecting) setConfirmOpen(false);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.dingtalk.disconnect_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.dingtalk.disconnect_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>
              {t(($) => $.dingtalk.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDisconnect}
              disabled={disconnecting}
            >
              {disconnecting
                ? t(($) => $.dingtalk.disconnecting)
                : t(($) => $.dingtalk.disconnect)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// DingTalkInstallDialog walks the user through binding an agent to a
// DingTalk app. It offers two paths:
//
//   scan   — the device flow: POST /dingtalk/install/begin → render QR →
//            poll /dingtalk/install/{sessionId}/status until success/error.
//   manual — the fallback: the operator creates the app themselves in the
//            DingTalk developer console and pastes its AppKey/AppSecret,
//            which POST /dingtalk/install/manual persists directly. This
//            keeps binding possible when the scan flow is broken or the
//            device-flow transport is not wired (installSupported=false).
//
// When installSupported is false the dialog opens straight into the manual
// form and never touches the (unavailable) begin endpoint.
//
// The scan path re-fetches a fresh session on each "retry" rather than
// reusing a stale device_code — DingTalk's device_code is single-use
// and time-boxed. Session/polling state handling mirrors
// LarkInstallDialog (see that file for the StrictMode closedRef note).
function DingTalkInstallDialog({
  wsId,
  agentId,
  agentName,
  installSupported,
  capabilities,
  onClose,
}: {
  wsId: string;
  agentId: string;
  agentName?: string;
  /** Whether the scan-to-create device flow is wired. When false the
   * dialog starts in — and stays on — the manual form. */
  installSupported: boolean;
  capabilities?: DingTalkInstallCapabilities;
  onClose: () => void;
}) {
  const { t } = useT("settings");
  const qc = useQueryClient();

  // Which install path is on screen. Default to scan when it is available,
  // otherwise the manual form is the only option.
  const [mode, setMode] = useState<"scan" | "manual">(
    installSupported ? "scan" : "manual",
  );

  const [session, setSession] = useState<null | {
    sessionId: string;
    qrCodeURL: string;
    expiresInSeconds: number;
    pollIntervalSeconds: number;
  }>(null);
  const [status, setStatus] = useState<DingTalkInstallStatusResponse["status"]>("pending");
  const [errorReason, setErrorReason] = useState<string | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [beginning, setBeginning] = useState(false);
  const closedRef = useRef(false);

  // Transport and allowUnbound are chosen before the first registration
  // request. Once a QR exists, changing either option replaces that session
  // with a fresh device code. Default off = the standard bind-first bot.
  const [allowUnbound, setAllowUnbound] = useState(false);
  const httpCallbackAvailable = capabilities?.http_callback.available === true;
  const [transportMode, setTransportMode] = useState<DingTalkTransportMode>("STREAM");

  // Manual-form state.
  const [clientId, setClientId] = useState("");
  const [clientSecret, setClientSecret] = useState("");
  const [robotCode, setRobotCode] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [manualError, setManualError] = useState<string | null>(null);

  async function beginSession(
    allow = allowUnbound,
    transport: DingTalkTransportMode = transportMode,
  ) {
    setBeginning(true);
    setStatus("pending");
    setErrorReason(null);
    setErrorMessage(null);
    setSession(null);
    try {
      const res = await api.beginDingTalkInstall(wsId, agentId, allow, transport);
      if (closedRef.current) return;
      setSession({
        sessionId: res.session_id,
        qrCodeURL: res.qr_code_url,
        expiresInSeconds: res.expires_in_seconds,
        pollIntervalSeconds: res.poll_interval_seconds,
      });
    } catch (e) {
      if (closedRef.current) return;
      setStatus("error");
      setErrorReason("internal_error");
      setErrorMessage(e instanceof Error ? e.message : String(e));
    } finally {
      setBeginning(false);
    }
  }

  // Switch to the manual form. Nothing to begin — the operator supplies
  // the credentials directly.
  function switchToManual() {
    setMode("manual");
    setManualError(null);
  }

  // Switch (back) to the scan configuration. The user still explicitly
  // starts registration after choosing the transport.
  function switchToScan() {
    setMode("scan");
  }

  async function submitManual() {
    const key = clientId.trim();
    const secret = clientSecret.trim();
    const robot = robotCode.trim();
    if (!key || !secret || !robot) {
      setManualError(t(($) => $.dingtalk.install_manual_missing_fields));
      return;
    }
    setSubmitting(true);
    setManualError(null);
    try {
      await api.manualInstallDingTalk(wsId, agentId, {
        clientId: key,
        clientSecret: secret,
        robotCode: robot,
        allowUnbound,
      });
      if (closedRef.current) return;
      await qc.invalidateQueries({ queryKey: dingtalkKeys.installations(wsId) });
      toast.success(t(($) => $.dingtalk.install_success_toast));
      onClose();
    } catch (e) {
      if (closedRef.current) return;
      setManualError(
        e instanceof Error ? e.message : t(($) => $.dingtalk.install_manual_error_generic),
      );
    } finally {
      setSubmitting(false);
    }
  }

  useEffect(() => {
    closedRef.current = false;
    return () => {
      closedRef.current = true;
    };
  }, []);

  useEffect(() => {
    if (mode !== "scan" || !session || status !== "pending") return;
    const intervalMs = Math.max(2000, session.pollIntervalSeconds * 1000);
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;

    const poll = async () => {
      if (cancelled) return;
      try {
        const res = await api.getDingTalkInstallStatus(wsId, session.sessionId);
        if (cancelled) return;
        setStatus(res.status);
        if (res.status === "success") {
          await qc.invalidateQueries({ queryKey: dingtalkKeys.installations(wsId) });
          toast.success(t(($) => $.dingtalk.install_success_toast));
          setTimeout(() => {
            if (!cancelled) onClose();
          }, 800);
          return;
        }
        if (res.status === "approving") {
          await qc.invalidateQueries({ queryKey: dingtalkKeys.installations(wsId) });
          toast.message(t(($) => $.dingtalk.install_approving_toast));
          return;
        }
        if (res.status === "error") {
          setErrorReason(res.error_reason ?? "internal_error");
          setErrorMessage(res.error_message ?? null);
          return;
        }
        timer = setTimeout(poll, intervalMs);
      } catch (e) {
        if (cancelled) return;
        // Terminal HTTP states must NOT be retried — same rationale as
        // LarkInstallDialog: 404 = session lost, 403/401 = permission
        // gone; anything else is transient and re-polls.
        if (e instanceof ApiError) {
          if (e.status === 404) {
            setStatus("error");
            setErrorReason("session_lost");
            setErrorMessage(e.message);
            return;
          }
          if (e.status === 403 || e.status === 401) {
            setStatus("error");
            setErrorReason("forbidden");
            setErrorMessage(e.message);
            return;
          }
        }
        timer = setTimeout(poll, intervalMs);
        toast.message(t(($) => $.dingtalk.install_poll_retry), {
          description: e instanceof Error ? e.message : String(e),
        });
      }
    };

    timer = setTimeout(poll, intervalMs);
    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session?.sessionId, status, mode]);

  return (
    <Dialog
      open
      onOpenChange={(o) => {
        if (!o) onClose();
      }}
    >
      <DialogContent className="max-w-sm">
        <DialogHeader>
          <DialogTitle>{t(($) => $.dingtalk.install_dialog_title)}</DialogTitle>
          <DialogDescription>
            {agentName
              ? t(($) => $.dingtalk.install_dialog_description_for_agent, { agent: agentName })
              : t(($) => $.dingtalk.install_dialog_description)}
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col items-center gap-4 py-2">
          {mode === "scan" ? (
            <>
              {(status === "pending" || !session) && (
                <>
                  <div className="grid w-full grid-cols-2 gap-2" data-testid="dingtalk-transport-mode">
                    {(["STREAM", "HTTP_CALLBACK"] as const).map((transport) => {
                      const disabled = transport === "HTTP_CALLBACK" && !httpCallbackAvailable;
                      const selected = transportMode === transport;
                      return (
                        <Button
                          key={transport}
                          type="button"
                          size="sm"
                          variant={selected ? "default" : "outline"}
                          aria-pressed={selected}
                          disabled={beginning || disabled}
                          title={
                            disabled
                              ? t(($) => $.dingtalk.install_transport_http_unavailable)
                              : undefined
                          }
                          onClick={() => {
                            if (selected) return;
                            setTransportMode(transport);
                            if (session) void beginSession(allowUnbound, transport);
                          }}
                        >
                          {transport === "STREAM"
                            ? t(($) => $.dingtalk.install_transport_stream)
                            : t(($) => $.dingtalk.install_transport_http)}
                        </Button>
                      );
                    })}
                  </div>
                  <div className="flex w-full items-start gap-3 rounded-md border p-3">
                    <Switch
                      id="dingtalk-allow-unbound"
                      checked={allowUnbound}
                      disabled={beginning}
                      onCheckedChange={(checked) => {
                        setAllowUnbound(checked);
                        if (session) void beginSession(checked);
                      }}
                    />
                    <label htmlFor="dingtalk-allow-unbound" className="flex-1 cursor-pointer">
                      <span className="text-sm font-medium">
                        {t(($) => $.dingtalk.install_allow_unbound_label)}
                      </span>
                      <span className="mt-0.5 block text-xs text-muted-foreground">
                        {t(($) => $.dingtalk.install_allow_unbound_hint)}
                      </span>
                    </label>
                  </div>
                </>
              )}

              {beginning && !session && (
                <p className="text-sm text-muted-foreground">{t(($) => $.dingtalk.install_starting)}</p>
              )}

              {session && status === "pending" && (
                <>
                  <div className="rounded-md border bg-white p-3">
                    <QRCode value={session.qrCodeURL} size={192} />
                  </div>
                  <p className="text-center text-xs text-muted-foreground">
                    {t(($) => $.dingtalk.install_scan_hint)}
                  </p>
                  <a
                    href={session.qrCodeURL}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-xs underline text-muted-foreground"
                  >
                    {t(($) => $.dingtalk.install_open_link_fallback)}
                  </a>
                </>
              )}

              {status === "success" && (
                <p className="text-sm font-medium">{t(($) => $.dingtalk.install_success)}</p>
              )}

              {status === "approving" && (
                <div className="space-y-2 text-center" data-testid="dingtalk-install-approving">
                  <p className="text-sm font-medium text-amber-700 dark:text-amber-400">
                    {t(($) => $.dingtalk.install_approving)}
                  </p>
                  <p className="text-xs text-muted-foreground">
                    {t(($) => $.dingtalk.install_approving_hint)}
                  </p>
                </div>
              )}

              {status === "error" && (
                <div className="space-y-2 text-center">
                  <p className="text-sm font-medium text-destructive">
                    {(() => {
                      switch (errorReason) {
                        case "expired":
                          return t(($) => $.dingtalk.install_error_expired);
                        case "install_failed":
                          return t(($) => $.dingtalk.install_error_install_failed);
                        case "dingtalk_protocol_error":
                          return t(($) => $.dingtalk.install_error_protocol);
                        case "credentials_check_failed":
                          return t(($) => $.dingtalk.install_error_credentials);
                        case "installation_conflict":
                          return t(($) => $.dingtalk.install_error_conflict);
                        case "superseded":
                          return t(($) => $.dingtalk.install_error_superseded);
                        case "session_lost":
                          return t(($) => $.dingtalk.install_error_session_lost);
                        case "forbidden":
                          return t(($) => $.dingtalk.install_error_forbidden);
                        default:
                          return t(($) => $.dingtalk.install_error_generic);
                      }
                    })()}
                  </p>
                  {errorMessage && (
                    <p className="text-[10px] text-muted-foreground break-all">
                      {errorMessage}
                    </p>
                  )}
                </div>
              )}

              {status !== "success" && status !== "approving" && (
                <button
                  type="button"
                  onClick={switchToManual}
                  className="text-xs underline text-muted-foreground hover:text-foreground"
                  data-testid="dingtalk-install-manual-link"
                >
                  {t(($) => $.dingtalk.install_manual_link)}
                </button>
              )}
            </>
          ) : (
            <div className="w-full space-y-4" data-testid="dingtalk-install-manual-form">
              <p className="text-xs text-muted-foreground">
                {t(($) => $.dingtalk.install_manual_description)}
              </p>
              <a
                href={DINGTALK_DEV_CONSOLE}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-1 text-xs text-muted-foreground underline-offset-2 transition-colors hover:text-foreground hover:underline"
              >
                <ExternalLink className="h-3 w-3" />
                {t(($) => $.dingtalk.install_manual_console_link)}
              </a>
              <div className="space-y-1.5">
                <Label htmlFor="dingtalk-manual-appkey">
                  {t(($) => $.dingtalk.install_manual_appkey_label)}
                </Label>
                <Input
                  id="dingtalk-manual-appkey"
                  value={clientId}
                  onChange={(e) => setClientId(e.target.value)}
                  placeholder={t(($) => $.dingtalk.install_manual_appkey_placeholder)}
                  disabled={submitting}
                  autoComplete="off"
                  spellCheck={false}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="dingtalk-manual-appsecret">
                  {t(($) => $.dingtalk.install_manual_appsecret_label)}
                </Label>
                <Input
                  id="dingtalk-manual-appsecret"
                  type="password"
                  value={clientSecret}
                  onChange={(e) => setClientSecret(e.target.value)}
                  placeholder={t(($) => $.dingtalk.install_manual_appsecret_placeholder)}
                  disabled={submitting}
                  autoComplete="off"
                  spellCheck={false}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="dingtalk-manual-robot-code">
                  {t(($) => $.dingtalk.install_manual_robot_code_label)}
                </Label>
                <Input
                  id="dingtalk-manual-robot-code"
                  value={robotCode}
                  onChange={(e) => setRobotCode(e.target.value)}
                  placeholder={t(($) => $.dingtalk.install_manual_robot_code_placeholder)}
                  disabled={submitting}
                  autoComplete="off"
                  spellCheck={false}
                />
              </div>
              <div className="flex w-full items-start gap-3 rounded-md border p-3">
                <Switch
                  id="dingtalk-manual-allow-unbound"
                  checked={allowUnbound}
                  disabled={submitting}
                  onCheckedChange={setAllowUnbound}
                />
                <label
                  htmlFor="dingtalk-manual-allow-unbound"
                  className="flex-1 cursor-pointer"
                >
                  <span className="text-sm font-medium">
                    {t(($) => $.dingtalk.install_allow_unbound_label)}
                  </span>
                  <span className="mt-0.5 block text-xs text-muted-foreground">
                    {t(($) => $.dingtalk.install_allow_unbound_hint)}
                  </span>
                </label>
              </div>
              {manualError && (
                <p className="text-xs text-destructive" role="alert">
                  {manualError}
                </p>
              )}
              {installSupported && (
                <button
                  type="button"
                  onClick={switchToScan}
                  className="text-xs underline text-muted-foreground hover:text-foreground"
                >
                  {t(($) => $.dingtalk.install_manual_back_to_scan)}
                </button>
              )}
            </div>
          )}
        </div>

        <DialogFooter>
          {mode === "manual" ? (
            <>
              <Button variant="outline" size="sm" onClick={onClose} disabled={submitting}>
                {t(($) => $.dingtalk.install_close)}
              </Button>
              <Button
                size="sm"
                onClick={() => void submitManual()}
                disabled={submitting}
                data-testid="dingtalk-install-manual-submit"
              >
                {submitting
                  ? t(($) => $.dingtalk.install_manual_submitting)
                  : t(($) => $.dingtalk.install_manual_submit)}
              </Button>
            </>
          ) : status === "error" ? (
            <>
              <Button variant="outline" size="sm" onClick={onClose}>
                {t(($) => $.dingtalk.install_close)}
              </Button>
              <Button size="sm" onClick={() => beginSession()} disabled={beginning}>
                <RefreshCw className="h-3 w-3" />
                {t(($) => $.dingtalk.install_retry)}
              </Button>
            </>
          ) : !session ? (
            <>
              <Button variant="outline" size="sm" onClick={onClose} disabled={beginning}>
                {t(($) => $.dingtalk.install_close)}
              </Button>
              <Button
                size="sm"
                onClick={() => void beginSession()}
                disabled={beginning}
                data-testid="dingtalk-install-start"
              >
                {beginning
                  ? t(($) => $.dingtalk.install_starting)
                  : t(($) => $.dingtalk.install_start)}
              </Button>
            </>
          ) : (
            <Button variant="outline" size="sm" onClick={onClose}>
              {t(($) => $.dingtalk.install_close)}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
