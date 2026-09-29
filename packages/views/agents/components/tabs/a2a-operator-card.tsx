"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";
import {
  agentA2AOperatorConfigOptions,
  useDeleteAgentA2AOperatorIdentity,
  useUpdateAgentA2AOperatorIdentity,
  useUpdateAgentA2AProdForward,
} from "@multica/core/agent-a2a";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Switch } from "@multica/ui/components/ui/switch";
import { useT } from "../../../i18n";

const DECIMAL_ID = /^[1-9][0-9]{0,19}$/;

function formatTime(value: string | null | undefined): string {
  if (!value) return "";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

/**
 * Deployment-operator settings for an Agent's A2A endpoint: the DingTalk
 * digital employee identity (the same record as the Integrations DingTalk
 * identity), and how production A2A traffic for that employee reaches
 * pre-release. Renders nothing unless the server says the viewer is an
 * operator.
 */
export function A2AOperatorCard({ wsId, agentId }: { wsId: string; agentId: string }) {
  const { t } = useT("agents");
  const operatorQuery = useQuery(agentA2AOperatorConfigOptions(wsId, agentId));
  const updateIdentity = useUpdateAgentA2AOperatorIdentity(wsId, agentId);
  const deleteIdentity = useDeleteAgentA2AOperatorIdentity(wsId, agentId);
  const updateProdForward = useUpdateAgentA2AProdForward(wsId, agentId);

  const [uid, setUid] = useState("");
  const [orgId, setOrgId] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [organizationName, setOrganizationName] = useState("");
  const [deapAgentUuid, setDeapAgentUuid] = useState("");

  const config = operatorQuery.data;
  if (config?.operator !== true) {
    return null;
  }
  const identity = config.dwsIdentity;
  const prodForward = config.prodForward;
  const forwardTarget = config.forwardTarget;
  const identityValid = DECIMAL_ID.test(uid.trim()) && DECIMAL_ID.test(orgId.trim());

  const run = async (action: () => Promise<unknown>, success: string) => {
    try {
      await action();
      toast.success(success);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.tab_body.a2a.operator.save_failed));
    }
  };

  const saveIdentity = () =>
    run(async () => {
      await updateIdentity.mutateAsync({
        uid: uid.trim(),
        orgId: orgId.trim(),
        displayName: displayName.trim() || undefined,
        organizationName: organizationName.trim() || undefined,
        deapAgentUuid: deapAgentUuid.trim() || undefined,
      });
      setUid("");
      setOrgId("");
      setDisplayName("");
      setOrganizationName("");
      setDeapAgentUuid("");
    }, t(($) => $.tab_body.a2a.operator.identity_saved));

  return (
    <Card className="py-0 shadow-none">
      <CardContent className="divide-y px-0">
        <div className="px-4 py-4">
          <div className="flex items-center gap-2">
            <h3 className="text-body font-semibold">{t(($) => $.tab_body.a2a.operator.title)}</h3>
            <Badge variant="secondary">{t(($) => $.tab_body.a2a.operator.badge)}</Badge>
          </div>
          <p className="mt-1 text-caption text-muted-foreground">
            {t(($) => $.tab_body.a2a.operator.description)}
          </p>
        </div>

        <div className="space-y-3 px-4 py-4">
          <div>
            <h4 className="text-body font-medium">{t(($) => $.tab_body.a2a.operator.identity_title)}</h4>
            <p className="mt-1 text-caption text-muted-foreground">
              {t(($) => $.tab_body.a2a.operator.identity_description)}
            </p>
          </div>
          {identity ? (
            <div className="flex flex-wrap items-center justify-between gap-2 rounded-md bg-muted/30 px-3 py-2.5 text-caption">
              <span className="flex min-w-0 flex-wrap items-center gap-2">
                <span className="font-medium">{identity.displayName || identity.uid}</span>
                <span className="min-w-0 break-all font-mono text-muted-foreground">
                  uid={identity.uid} · orgId={identity.orgId}
                  {identity.deapAgentUuid ? ` · agentUuid=${identity.deapAgentUuid}` : ""}
                </span>
                <Badge variant={identity.a2aEnabled ? "default" : "secondary"}>
                  {identity.a2aEnabled
                    ? t(($) => $.tab_body.a2a.operator.identity_a2a_enabled)
                    : t(($) => $.tab_body.a2a.operator.identity_a2a_disabled)}
                </Badge>
              </span>
              <Button
                variant="outline"
                size="sm"
                onClick={() =>
                  void run(() => deleteIdentity.mutateAsync(), t(($) => $.tab_body.a2a.operator.identity_cleared))
                }
                disabled={deleteIdentity.isPending}
              >
                {t(($) => $.tab_body.a2a.operator.clear)}
              </Button>
            </div>
          ) : (
            <p className="text-caption text-muted-foreground">
              {t(($) => $.tab_body.a2a.operator.identity_empty)}
            </p>
          )}
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="a2a-operator-uid">UID</Label>
              <Input
                id="a2a-operator-uid"
                inputMode="numeric"
                value={uid}
                onChange={(event) => setUid(event.target.value)}
                className="font-mono"
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="a2a-operator-org">OrgID</Label>
              <Input
                id="a2a-operator-org"
                inputMode="numeric"
                value={orgId}
                onChange={(event) => setOrgId(event.target.value)}
                className="font-mono"
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="a2a-operator-name">{t(($) => $.tab_body.a2a.operator.display_name)}</Label>
              <Input id="a2a-operator-name" value={displayName} onChange={(event) => setDisplayName(event.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="a2a-operator-organization">
                {t(($) => $.tab_body.a2a.operator.organization_name)}
              </Label>
              <Input
                id="a2a-operator-organization"
                value={organizationName}
                onChange={(event) => setOrganizationName(event.target.value)}
              />
            </div>
            <div className="space-y-1.5 sm:col-span-2">
              <Label htmlFor="a2a-operator-deap">{t(($) => $.tab_body.a2a.operator.deap_agent_uuid)}</Label>
              <Input
                id="a2a-operator-deap"
                value={deapAgentUuid}
                onChange={(event) => setDeapAgentUuid(event.target.value)}
                className="font-mono"
              />
            </div>
          </div>
          <Button size="sm" onClick={() => void saveIdentity()} disabled={!identityValid || updateIdentity.isPending}>
            {updateIdentity.isPending && <Loader2 className="size-4 animate-spin" />}
            {t(($) => $.tab_body.a2a.operator.save_identity)}
          </Button>
        </div>

        {prodForward && (
          <div className="space-y-3 px-4 py-4">
            <div className="flex items-start justify-between gap-4">
              <div className="min-w-0">
                <h4 className="text-body font-medium">{t(($) => $.tab_body.a2a.operator.prod_forward_title)}</h4>
                <p className="mt-1 text-caption text-muted-foreground">
                  {t(($) => $.tab_body.a2a.operator.prod_forward_description)}
                </p>
              </div>
              <Switch
                checked={prodForward.accept}
                disabled={updateProdForward.isPending}
                aria-label={t(($) => $.tab_body.a2a.operator.prod_forward_title)}
                onCheckedChange={(accept) =>
                  void run(
                    () => updateProdForward.mutateAsync({ accept }),
                    t(($) => $.tab_body.a2a.operator.prod_forward_saved),
                  )
                }
              />
            </div>
            {!prodForward.accept ? (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.tab_body.a2a.operator.prod_forward_off)}
              </p>
            ) : prodForward.blockedReason ? (
              <div className="text-caption">
                <p className="text-muted-foreground">{t(($) => $.tab_body.a2a.operator.prod_forward_pending)}</p>
                <p className="mt-1 break-all">{prodForward.blockedReason}</p>
              </div>
            ) : (
              <>
                <ul className="space-y-2">
                  {prodForward.registrations.map((registration) => (
                    <li key={registration.registry} className="rounded-md bg-muted/30 px-3 py-2 text-caption">
                      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
                        <span className="min-w-0 break-all font-mono">{registration.registry}</span>
                        <span className="text-muted-foreground">
                          {registration.registeredAt && registration.current
                            ? t(($) => $.tab_body.a2a.operator.prod_forward_registered, {
                              time: formatTime(registration.registeredAt),
                            })
                            : registration.registeredAt
                              ? t(($) => $.tab_body.a2a.operator.prod_forward_stale)
                              : t(($) => $.tab_body.a2a.operator.prod_forward_not_registered)}
                        </span>
                      </div>
                      {registration.error && (
                        <p className="mt-1 break-all text-destructive">{registration.error}</p>
                      )}
                    </li>
                  ))}
                </ul>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={updateProdForward.isPending}
                  onClick={() =>
                    void run(
                      () => updateProdForward.mutateAsync({ accept: true }),
                      t(($) => $.tab_body.a2a.operator.prod_forward_reregistered),
                    )
                  }
                >
                  {updateProdForward.isPending && <Loader2 className="size-4 animate-spin" />}
                  {t(($) => $.tab_body.a2a.operator.reregister)}
                </Button>
              </>
            )}
          </div>
        )}

        {forwardTarget && (
          <div className="space-y-1 px-4 py-4">
            <h4 className="text-body font-medium">{t(($) => $.tab_body.a2a.operator.forward_target_title)}</h4>
            <p className="text-caption text-muted-foreground">
              {t(($) => $.tab_body.a2a.operator.forward_target_description, {
                agent: forwardTarget.agentName || forwardTarget.rpcUrl,
                time: formatTime(forwardTarget.registeredAt),
              })}
            </p>
            <p className="break-all font-mono text-caption">{forwardTarget.rpcUrl}</p>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
