"use client";

import { useEffect, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { QRCode } from "react-qr-code";
import { CheckCircle2, Circle, CircleHelp, Copy, QrCode, RefreshCw } from "lucide-react";
import { toast } from "sonner";
import type { Agent } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  agentA2AConfigOptions,
  agentA2AOperatorConfigOptions,
  useUpdateAgentA2AConfig,
  useUpdateAgentA2AOperatorIdentity,
} from "@multica/core/agent-a2a";
import {
  dingtalkAccountBindingsOptions,
  dingtalkNativeSubscriptionStatusOptions,
  useBeginDingTalkAccountBinding,
  useDeleteDingTalkAccountBinding,
  useSetDingTalkNativeDEAPLink,
  useSetDingTalkNativeSubscription,
} from "@multica/core/dingtalk-account-bindings";
import type { DingTalkNativeStream } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@multica/ui/components/ui/popover";
import { Switch } from "@multica/ui/components/ui/switch";
import { cn } from "@multica/ui/lib/utils";
import { InboundCoordinatorSetting } from "../agents/components/agent-message-settings";
import { SettingsSection } from "../settings/components/settings-layout";
import { useT } from "../i18n";

type BindingMode = "identity" | "message";

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback;
}

/**
 * 租户配置 of a Tag tenant's employee. Everything shared (instructions,
 * skills, connectors, runtime) is inherited from the Tag and edited on its
 * left; a tenant only configures who its digital employee is, how its
 * DingTalk events reach it, and how the inbound coordinator receives them.
 */
export function TagTenantConfig({
  agent,
  canEdit,
  onUpdate,
}: {
  agent: Agent;
  canEdit: boolean;
  onUpdate: (data: Record<string, unknown>) => Promise<void>;
}) {
  return (
    <div className="space-y-10">
      <EmployeeIdentitySection agent={agent} canEdit={canEdit} onUpdate={onUpdate} />
      <InboundCoordinatorSetting agent={agent} canEdit={canEdit} onUpdate={onUpdate} />
    </div>
  );
}

function EmployeeIdentitySection({
  agent,
  canEdit,
  onUpdate,
}: {
  agent: Agent;
  canEdit: boolean;
  onUpdate: (data: Record<string, unknown>) => Promise<void>;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const { data: listing } = useQuery(dingtalkAccountBindingsOptions(wsId));
  const binding = listing?.bindings.find((item) => item.agentId === agent.id) ?? null;
  const identityActive = binding?.dwsIdentity.status === "active";
  const routeActive = binding?.messageRoute.status === "active";
  const nativeOn = binding?.dwsIdentity.nativeSubscription === true;
  const [qrMode, setQrMode] = useState<BindingMode | null>(null);

  return (
    <SettingsSection title={t(($) => $.tag_tenant.identity_title)} description={t(($) => $.tag_tenant.identity_hint)}>
      <div className="space-y-6">
        <Layer step="1" title={t(($) => $.tag_tenant.issue_title)} hint={t(($) => $.tag_tenant.issue_hint)}>
          <IdentityIssuance
            agent={agent}
            canEdit={canEdit}
            identityActive={identityActive}
            organizationName={binding?.dwsIdentity.organizationName ?? ""}
            accountName={binding?.dwsIdentity.accountDisplayName ?? ""}
            onScan={() => setQrMode("identity")}
          />
        </Layer>
        <Layer step="2" title={t(($) => $.tag_tenant.perceive_title)} hint={t(($) => $.tag_tenant.perceive_hint)}>
          <EventPerception
            agent={agent}
            canEdit={canEdit}
            identityActive={identityActive}
            routeActive={routeActive}
            routeOrganization={binding?.messageRoute.organizationName ?? ""}
            nativeOn={nativeOn}
            onUpdate={onUpdate}
            onScanRoute={() => setQrMode("message")}
          />
        </Layer>
      </div>
      {qrMode ? <BindingQrDialog agent={agent} mode={qrMode} onClose={() => setQrMode(null)} /> : null}
    </SettingsSection>
  );
}

function Layer({ step, title, hint, children }: { step: string; title: string; hint: string; children: ReactNode }) {
  return (
    <section className="rounded-xl border">
      <header className="flex items-start gap-3 border-b px-4 py-3">
        <span className="mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-muted text-caption font-medium">
          {step}
        </span>
        <div className="min-w-0">
          <h3 className="text-body font-medium">{title}</h3>
          <p className="text-caption text-muted-foreground">{hint}</p>
        </div>
      </header>
      <div className="space-y-4 px-4 py-4">{children}</div>
    </section>
  );
}

function IdentityIssuance({
  agent,
  canEdit,
  identityActive,
  organizationName,
  accountName,
  onScan,
}: {
  agent: Agent;
  canEdit: boolean;
  identityActive: boolean;
  organizationName: string;
  accountName: string;
  onScan: () => void;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const { data: operator } = useQuery(agentA2AOperatorConfigOptions(wsId, agent.id));
  // The native status carries the DEAP supervisor link of this identity.
  const { data: nativeStatus } = useQuery({
    ...dingtalkNativeSubscriptionStatusOptions(wsId, agent.id, identityActive),
    refetchInterval: false,
  });
  const supervisorLink = nativeStatus?.deapLink ?? null;
  const removeBinding = useDeleteDingTalkAccountBinding(wsId);
  const [method, setMethod] = useState<"scan" | "fill">("scan");
  const [error, setError] = useState<string | null>(null);
  const filled = operator?.dwsIdentity ?? null;

  return (
    <>
      <div className="flex items-center gap-3 rounded-lg bg-muted/40 px-3 py-2.5">
        {identityActive ? (
          <CheckCircle2 className="h-4 w-4 shrink-0 text-emerald-600" aria-hidden="true" />
        ) : (
          <Circle className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        )}
        <div className="min-w-0 flex-1 text-body">
          {identityActive ? (
            <>
              <span className="font-medium">{accountName || filled?.displayName || filled?.uid}</span>
              <span className="text-muted-foreground"> · {organizationName || filled?.organizationName}</span>
              {filled ? (
                <div className="font-mono text-caption text-muted-foreground">
                  {t(($) => $.tag_tenant.identity_ids, { orgId: filled.orgId, uid: filled.uid })}
                </div>
              ) : null}
              {supervisorLink ? (
                <div className="font-mono text-caption text-muted-foreground">
                  {t(($) => $.tag_tenant.supervisor_link, {
                    supervisor: supervisorLink.supervisorUid,
                    deap: supervisorLink.deapAgentUuid,
                  })}
                </div>
              ) : null}
            </>
          ) : (
            <span className="text-muted-foreground">{t(($) => $.tag_tenant.identity_none)}</span>
          )}
        </div>
        {identityActive && canEdit ? (
          <Button
            variant="ghost"
            size="sm"
            disabled={removeBinding.isPending}
            onClick={() =>
              removeBinding.mutate(
                { agentId: agent.id, bindingMode: "identity" },
                { onError: (e) => setError(errorMessage(e, t(($) => $.tag_tenant.action_failed))) },
              )
            }
          >
            {t(($) => $.tag_tenant.identity_unbind)}
          </Button>
        ) : null}
      </div>

      <div role="radiogroup" aria-label={t(($) => $.tag_tenant.issue_title)} className="grid gap-2 sm:grid-cols-2">
        <MethodOption
          selected={method === "scan"}
          title={t(($) => $.tag_tenant.method_scan)}
          hint={t(($) => $.tag_tenant.method_scan_hint)}
          onSelect={() => setMethod("scan")}
        />
        <MethodOption
          selected={method === "fill"}
          title={t(($) => $.tag_tenant.method_fill)}
          hint={t(($) => $.tag_tenant.method_fill_hint)}
          onSelect={() => setMethod("fill")}
        />
      </div>

      {method === "scan" ? (
        <Button variant="outline" disabled={!canEdit} onClick={onScan}>
          <QrCode className="h-4 w-4" aria-hidden="true" />
          {identityActive ? t(($) => $.tag_tenant.scan_again) : t(($) => $.tag_tenant.scan_identity)}
        </Button>
      ) : operator?.operator === true ? (
        <DirectIdentityForm agent={agent} canEdit={canEdit} initialOrgId={filled?.orgId ?? ""} initialUid={filled?.uid ?? ""} />
      ) : (
        <p className="text-caption text-muted-foreground">{t(($) => $.tag_tenant.fill_operator_only)}</p>
      )}
      {error ? <p role="alert" className="text-caption text-destructive">{error}</p> : null}
    </>
  );
}

function MethodOption({
  selected,
  title,
  hint,
  onSelect,
}: {
  selected: boolean;
  title: string;
  hint: string;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      onClick={onSelect}
      className={cn(
        "rounded-lg border px-3 py-2.5 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        selected ? "border-foreground font-medium" : "hover:bg-muted/40",
      )}
    >
      <div className="text-body">{title}</div>
      <div className="text-caption font-normal text-muted-foreground">{hint}</div>
    </button>
  );
}

const DECIMAL_ID = /^[1-9][0-9]{0,19}$/;

// Local dws commands, run as the supervisor, that read the DEAP profile the
// cloud checks the identity against.
const DEAP_LIST_COMMAND = "dws dingtalk-tag manage list --jq '.data[] | {name, agentUuid}'";

function deapUserIdCommand(agentUuid: string): string {
  return `dws dingtalk-tag manage detail --agent-uuid ${agentUuid} --jq '.data.profile.userId'`;
}

/** A "?" next to a field label that opens how to find the value. */
function FieldTip({ label, children }: { label: string; children: ReactNode }) {
  return (
    <Popover>
      <PopoverTrigger
        render={
          <button
            type="button"
            aria-label={label}
            className="rounded-full text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <CircleHelp className="h-3.5 w-3.5" aria-hidden="true" />
          </button>
        }
      />
      <PopoverContent align="start" className="w-[26rem] max-w-[calc(100vw-2rem)] space-y-2 text-caption">
        {children}
      </PopoverContent>
    </Popover>
  );
}

/** One command line in a field tip, copyable as is. */
function TipCommand({ command }: { command: string }) {
  const { t } = useT("agents");
  return (
    <div className="flex items-start gap-1.5 rounded-md bg-muted px-2 py-1.5">
      <code className="min-w-0 flex-1 break-all font-mono text-caption">{command}</code>
      <button
        type="button"
        className="rounded p-0.5 hover:bg-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        aria-label={t(($) => $.tag_tenant.tip_copy)}
        onClick={() => {
          void navigator.clipboard?.writeText(command).then(
            () => toast.success(t(($) => $.tag_tenant.tip_copied)),
            () => undefined,
          );
        }}
      >
        <Copy className="h-3 w-3" aria-hidden="true" />
      </button>
    </div>
  );
}

/** 直接填写: the digital employee's userId within the organization's numeric
 * OrgId (not a corpId), as DEAP reports it, and its supervisor's uid. */
function DirectIdentityForm({
  agent,
  canEdit,
  initialOrgId,
  initialUid,
}: {
  agent: Agent;
  canEdit: boolean;
  initialOrgId: string;
  initialUid: string;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const saveIdentity = useUpdateAgentA2AOperatorIdentity(wsId, agent.id);
  const saveSupervisor = useSetDingTalkNativeDEAPLink(wsId);
  const [orgId, setOrgId] = useState(initialOrgId);
  const [uid, setUid] = useState(initialUid);
  const [supervisorUid, setSupervisorUid] = useState("");
  const [deapAgentUuid, setDeapAgentUuid] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [organizationName, setOrganizationName] = useState("");
  const [notice, setNotice] = useState<{ tone: "ok" | "warn" | "error"; text: string } | null>(null);

  const idsValid = DECIMAL_ID.test(orgId.trim()) && DECIMAL_ID.test(uid.trim());
  const supervisorValid = supervisorUid.trim() === "" || DECIMAL_ID.test(supervisorUid.trim());
  const wantsSupervisor = supervisorUid.trim() !== "";
  const supervisorComplete = !wantsSupervisor || deapAgentUuid.trim() !== "";
  const busy = saveIdentity.isPending || saveSupervisor.isPending;
  const canSubmit = canEdit && idsValid && supervisorValid && supervisorComplete && !busy;

  const submit = async () => {
    if (!canSubmit) return;
    setNotice(null);
    try {
      await saveIdentity.mutateAsync({
        uid: uid.trim(),
        orgId: orgId.trim(),
        displayName: displayName.trim() || undefined,
        organizationName: organizationName.trim() || undefined,
        deapAgentUuid: deapAgentUuid.trim() || undefined,
      });
    } catch (error) {
      setNotice({ tone: "error", text: errorMessage(error, t(($) => $.tag_tenant.action_failed)) });
      return;
    }
    if (!wantsSupervisor) {
      setNotice({ tone: "ok", text: t(($) => $.tag_tenant.fill_saved) });
      return;
    }
    try {
      await saveSupervisor.mutateAsync({
        agentId: agent.id,
        deapAgentUuid: deapAgentUuid.trim(),
        supervisorUid: supervisorUid.trim(),
      });
      setNotice({ tone: "ok", text: t(($) => $.tag_tenant.fill_saved_supervisor) });
    } catch (error) {
      setNotice({ tone: "error", text: errorMessage(error, t(($) => $.tag_tenant.action_failed)) });
    }
  };

  const field = (
    id: string,
    label: string,
    value: string,
    set: (v: string) => void,
    placeholder?: string,
    mono = true,
    tip?: ReactNode,
  ) => (
    <div className="grid gap-1.5">
      <div className="flex items-center gap-1">
        <Label htmlFor={id}>{label}</Label>
        {tip ? <FieldTip label={t(($) => $.tag_tenant.tip_label, { field: label })}>{tip}</FieldTip> : null}
      </div>
      <Input
        id={id}
        value={value}
        placeholder={placeholder}
        className={mono ? "font-mono" : undefined}
        disabled={!canEdit}
        onChange={(event) => set(event.target.value)}
      />
    </div>
  );

  return (
    <form
      className="space-y-3"
      onSubmit={(event) => {
        event.preventDefault();
        void submit();
      }}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        {field(`tag-fill-org-${agent.id}`, t(($) => $.tag_tenant.fill_org_id), orgId, setOrgId, "439446171", true, (
          <p>{t(($) => $.tag_tenant.tip_org)}</p>
        ))}
        {field(`tag-fill-deap-${agent.id}`, t(($) => $.tag_tenant.fill_deap_agent), deapAgentUuid, setDeapAgentUuid, undefined, true, (
          <>
            <p>{t(($) => $.tag_tenant.tip_deap)}</p>
            <TipCommand command={DEAP_LIST_COMMAND} />
            <p className="text-muted-foreground">{t(($) => $.tag_tenant.tip_profile)}</p>
          </>
        ))}
        {field(`tag-fill-uid-${agent.id}`, t(($) => $.tag_tenant.fill_uid), uid, setUid, "858957531", true, (
          <>
            <p>{t(($) => $.tag_tenant.tip_uid)}</p>
            <TipCommand command={DEAP_LIST_COMMAND} />
            <TipCommand
              command={deapUserIdCommand(deapAgentUuid.trim() || t(($) => $.tag_tenant.tip_deap_placeholder))}
            />
            <p>{t(($) => $.tag_tenant.tip_uid_check)}</p>
            <p className="text-muted-foreground">{t(($) => $.tag_tenant.tip_profile)}</p>
          </>
        ))}
        {field(`tag-fill-supervisor-${agent.id}`, t(($) => $.tag_tenant.fill_supervisor_uid), supervisorUid, setSupervisorUid, undefined, true, (
          <p>{t(($) => $.tag_tenant.tip_supervisor)}</p>
        ))}
        {field(`tag-fill-name-${agent.id}`, t(($) => $.tag_tenant.fill_display_name), displayName, setDisplayName, undefined, false)}
        {field(`tag-fill-orgname-${agent.id}`, t(($) => $.tag_tenant.fill_org_name), organizationName, setOrganizationName, undefined, false)}
      </div>
      <p className="text-caption text-muted-foreground">{t(($) => $.tag_tenant.fill_ids_note)}</p>
      {!supervisorComplete ? (
        <p className="text-caption text-amber-700 dark:text-amber-300">{t(($) => $.tag_tenant.fill_supervisor_needs_deap)}</p>
      ) : null}
      {notice ? (
        <p
          role={notice.tone === "error" ? "alert" : "status"}
          className={cn(
            "text-caption",
            notice.tone === "ok" && "text-emerald-700 dark:text-emerald-300",
            notice.tone === "warn" && "text-amber-700 dark:text-amber-300",
            notice.tone === "error" && "text-destructive",
          )}
        >
          {notice.text}
        </p>
      ) : null}
      <div className="flex justify-end">
        <Button type="submit" disabled={!canSubmit}>
          {t(($) => $.tag_tenant.fill_submit)}
        </Button>
      </div>
    </form>
  );
}

function EventPerception({
  agent,
  canEdit,
  identityActive,
  routeActive,
  routeOrganization,
  nativeOn,
  onUpdate,
  onScanRoute,
}: {
  agent: Agent;
  canEdit: boolean;
  identityActive: boolean;
  routeActive: boolean;
  routeOrganization: string;
  nativeOn: boolean;
  onUpdate: (data: Record<string, unknown>) => Promise<void>;
  onScanRoute: () => void;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const setNative = useSetDingTalkNativeSubscription(wsId);
  const removeBinding = useDeleteDingTalkAccountBinding(wsId);
  const { data: nativeStatus } = useQuery(dingtalkNativeSubscriptionStatusOptions(wsId, agent.id, nativeOn));
  const { data: a2a } = useQuery(agentA2AConfigOptions(wsId, agent.id));
  const updateA2A = useUpdateAgentA2AConfig(wsId, agent.id);
  const endpoint = a2a?.endpoint ?? null;
  const a2aOn = endpoint?.enabled === true;
  const [error, setError] = useState<string | null>(null);
  const fail = (e: unknown) => setError(errorMessage(e, t(($) => $.tag_tenant.action_failed)));

  const nativeBlockedReason = !identityActive
    ? t(($) => $.tag_tenant.native_needs_identity)
    : routeActive
      ? t(($) => $.tag_tenant.native_blocked_by_route)
      : null;

  return (
    <>
      <div role="list" className="space-y-2">
        <PerceptionOption
          active={nativeOn}
          title={t(($) => $.tag_tenant.mode_native)}
          hint={t(($) => $.tag_tenant.mode_native_hint)}
          status={
            nativeOn
              ? <NativeStreamStatus stream={nativeStatus?.stream ?? null} />
              : nativeBlockedReason ?? t(($) => $.tag_tenant.mode_off)
          }
          control={
            <Switch
              aria-label={t(($) => $.tag_tenant.mode_native)}
              checked={nativeOn}
              disabled={!canEdit || setNative.isPending || (!nativeOn && nativeBlockedReason !== null)}
              onCheckedChange={(checked) => {
                setError(null);
                void (async () => {
                  // Native events are answered through the Coordinator and
                  // managed DingTalk replies; turn them on first if needed.
                  if (checked === true && (agent.inbound_coordinator !== true || agent.dingtalk_response_enabled !== true)) {
                    try {
                      await onUpdate({ inbound_coordinator: true, dingtalk_response_enabled: true });
                    } catch (e) {
                      fail(e);
                      return;
                    }
                  }
                  setNative.mutate({ agentId: agent.id, enabled: checked === true }, { onError: fail });
                })();
              }}
            />
          }
        />
        <PerceptionOption
          active={routeActive}
          title={t(($) => $.tag_tenant.mode_backend)}
          hint={t(($) => $.tag_tenant.mode_backend_hint)}
          status={
            routeActive
              ? t(($) => $.tag_tenant.route_bound, { organization: routeOrganization })
              : nativeOn
                ? t(($) => $.tag_tenant.route_blocked_by_native)
                : t(($) => $.tag_tenant.mode_off)
          }
          control={
            routeActive ? (
              <Button
                variant="ghost"
                size="sm"
                disabled={!canEdit || removeBinding.isPending}
                onClick={() => {
                  setError(null);
                  removeBinding.mutate({ agentId: agent.id, bindingMode: "message" }, { onError: fail });
                }}
              >
                {t(($) => $.tag_tenant.route_unbind)}
              </Button>
            ) : (
              <Button variant="outline" size="sm" disabled={!canEdit || nativeOn} onClick={onScanRoute}>
                <QrCode className="h-3.5 w-3.5" aria-hidden="true" />
                {t(($) => $.tag_tenant.route_scan)}
              </Button>
            )
          }
        />
        <PerceptionOption
          active={a2aOn}
          title={t(($) => $.tag_tenant.mode_a2a)}
          hint={t(($) => $.tag_tenant.mode_a2a_hint)}
          status={
            endpoint === null
              ? t(($) => $.tag_tenant.a2a_unavailable)
              : a2aOn
                ? t(($) => $.tag_tenant.a2a_on, { id: endpoint.publicAgentId })
                : t(($) => $.tag_tenant.mode_off)
          }
          control={
            <Switch
              aria-label={t(($) => $.tag_tenant.mode_a2a)}
              checked={a2aOn}
              disabled={!canEdit || endpoint === null || updateA2A.isPending}
              onCheckedChange={(checked) => {
                if (!endpoint) return;
                setError(null);
                updateA2A.mutate(
                  {
                    enabled: checked === true,
                    cardName: endpoint.cardName,
                    cardDescription: endpoint.cardDescription,
                    cardVersion: endpoint.cardVersion,
                    cardSkills: endpoint.cardSkills,
                  },
                  { onError: fail },
                );
              }}
            />
          }
        />
      </div>
      {error ? <p role="alert" className="text-caption text-destructive">{error}</p> : null}
    </>
  );
}

function PerceptionOption({
  active,
  title,
  hint,
  status,
  control,
}: {
  active: boolean;
  title: string;
  hint: string;
  status: ReactNode;
  control: ReactNode;
}) {
  const { t } = useT("agents");
  return (
    <div role="listitem" className={cn("flex items-start gap-3 rounded-lg border px-3 py-3", active && "border-foreground/40 bg-muted/30")}>
      {active ? (
        <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-emerald-600" aria-hidden="true" />
      ) : (
        <Circle className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
      )}
      <div className="min-w-0 flex-1 space-y-0.5">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-body font-medium">{title}</span>
          {active ? (
            <span className="rounded-full bg-emerald-500/10 px-2 py-0.5 text-caption text-emerald-700 dark:text-emerald-300">
              {t(($) => $.tag_tenant.mode_active)}
            </span>
          ) : null}
        </div>
        <p className="text-caption text-muted-foreground">{hint}</p>
        <div className="text-caption">{status}</div>
      </div>
      <div className="shrink-0">{control}</div>
    </div>
  );
}

/** The native stream in words. While it keeps failing, the server's reason
 * is shown, translated when it is a known identity mismatch. */
function NativeStreamStatus({ stream }: { stream: DingTalkNativeStream | null }) {
  const { t } = useT("agents");
  const state = stream?.state ?? "unknown";
  const label =
    state === "connected"
      ? t(($) => $.tag_tenant.stream_connected)
      : state === "connecting"
        ? t(($) => $.tag_tenant.stream_connecting)
        : state === "disconnected"
          ? t(($) => $.tag_tenant.stream_disconnected)
          : state === "unavailable"
            ? t(($) => $.tag_tenant.stream_unavailable)
            : state === "off"
              ? t(($) => $.tag_tenant.stream_off)
              : t(($) => $.tag_tenant.stream_unknown);
  const failing = state !== "connected" && !!stream?.lastError;
  const deapMismatch = stream?.lastError?.includes("DEAP digital employee is not this identity") === true;
  return (
    <div className="space-y-0.5">
      <p className={cn(state === "connected" ? "text-emerald-700 dark:text-emerald-300" : failing && "text-destructive")}>
        {label}
        {failing && stream?.failures ? ` · ${t(($) => $.tag_tenant.stream_failures, { count: stream.failures })}` : null}
      </p>
      {failing ? (
        <p role="alert" className="text-destructive">
          {deapMismatch ? t(($) => $.tag_tenant.stream_deap_mismatch) : stream?.lastError}
        </p>
      ) : null}
    </div>
  );
}

/** Scan flow for both layers: identity issuance (binding_mode=identity) and
 * backend subscription (binding_mode=message). The binding list refreshes
 * when the scan completes, which closes the dialog. */
function BindingQrDialog({ agent, mode, onClose }: { agent: Agent; mode: BindingMode; onClose: () => void }) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const begin = useBeginDingTalkAccountBinding(wsId);
  const { data: listing } = useQuery({ ...dingtalkAccountBindingsOptions(wsId), refetchInterval: 3_000 });
  const binding = listing?.bindings.find((item) => item.agentId === agent.id) ?? null;
  const status = mode === "identity" ? binding?.dwsIdentity.status : binding?.messageRoute.status;
  const [attempt, setAttempt] = useState<{ qrCodeUrl: string; expiresAt: string } | null>(null);
  const [startedFrom] = useState(status);
  const [error, setError] = useState<string | null>(null);
  const [expired, setExpired] = useState(false);

  const start = () => {
    setError(null);
    setExpired(false);
    begin.mutate(
      { agentId: agent.id, bindingMode: mode },
      {
        onSuccess: (next) => {
          if (!next.qrCodeUrl || !next.expiresAt) setError(t(($) => $.tag_tenant.action_failed));
          else setAttempt({ qrCodeUrl: next.qrCodeUrl, expiresAt: next.expiresAt });
        },
        onError: (e) => setError(errorMessage(e, t(($) => $.tag_tenant.action_failed))),
      },
    );
  };

  // Start once when the dialog opens.
  useEffect(() => {
    start();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (!attempt) return;
    const remaining = Date.parse(attempt.expiresAt) - Date.now();
    if (!Number.isFinite(remaining) || remaining <= 0) {
      setExpired(true);
      return;
    }
    const timer = setTimeout(() => setExpired(true), remaining);
    return () => clearTimeout(timer);
  }, [attempt]);

  // A fresh active binding means the scan completed.
  useEffect(() => {
    if (attempt && status === "active" && startedFrom !== "active") onClose();
  }, [attempt, onClose, startedFrom, status]);

  return (
    <Dialog open onOpenChange={(open) => (!open ? onClose() : undefined)}>
      <DialogContent className="max-w-sm">
        <DialogHeader>
          <DialogTitle>
            {mode === "identity" ? t(($) => $.tag_tenant.qr_identity_title) : t(($) => $.tag_tenant.qr_route_title)}
          </DialogTitle>
          <DialogDescription>{t(($) => $.tag_tenant.qr_hint, { agent: agent.name })}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col items-center gap-3 py-2">
          {attempt && !expired ? (
            <div className="rounded-md border bg-white p-3">
              <QRCode value={attempt.qrCodeUrl} size={192} aria-label={t(($) => $.tag_tenant.qr_label)} />
            </div>
          ) : expired ? (
            <p className="text-caption text-muted-foreground">{t(($) => $.tag_tenant.qr_expired)}</p>
          ) : (
            <p className="text-caption text-muted-foreground">{t(($) => $.tag_tenant.qr_loading)}</p>
          )}
          {error ? <p role="alert" className="text-caption text-destructive">{error}</p> : null}
        </div>
        <DialogFooter>
          <Button variant="outline" size="sm" onClick={onClose}>
            {t(($) => $.tag_page.close)}
          </Button>
          {expired || error ? (
            <Button size="sm" disabled={begin.isPending} onClick={start}>
              <RefreshCw className="h-3 w-3" aria-hidden="true" />
              {t(($) => $.tag_tenant.qr_new)}
            </Button>
          ) : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
