"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";
import {
  agentA2AConfigOptions,
  agentA2AOperatorConfigOptions,
  useDeleteAgentA2AOperatorForward,
  useDeleteAgentA2AOperatorIdentity,
  useUpdateAgentA2AOperatorForward,
  useUpdateAgentA2AOperatorIdentity,
  type AgentA2AClient,
} from "@multica/core/agent-a2a";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { NativeSelect, NativeSelectOption } from "@multica/ui/components/ui/native-select";
import { useT } from "../../../i18n";

const DECIMAL_ID = /^[1-9][0-9]{0,19}$/;
const EMPTY_CLIENTS: AgentA2AClient[] = [];

/**
 * Deployment-operator settings for an Agent's A2A endpoint: the DEAP digital
 * employee identity A2A tasks run as, and a forward of inbound A2A traffic to
 * another environment. Renders nothing unless the server says the viewer is an
 * operator.
 */
export function A2AOperatorCard({ wsId, agentId }: { wsId: string; agentId: string }) {
  const { t } = useT("agents");
  const operatorQuery = useQuery(agentA2AOperatorConfigOptions(wsId, agentId));
  const a2aConfigQuery = useQuery(agentA2AConfigOptions(wsId, agentId));
  const updateIdentity = useUpdateAgentA2AOperatorIdentity(wsId, agentId);
  const deleteIdentity = useDeleteAgentA2AOperatorIdentity(wsId, agentId);
  const updateForward = useUpdateAgentA2AOperatorForward(wsId, agentId);
  const deleteForward = useDeleteAgentA2AOperatorForward(wsId, agentId);

  const [uid, setUid] = useState("");
  const [orgId, setOrgId] = useState("");
  const [deapAgentUuid, setDeapAgentUuid] = useState("");
  const [rpcUrl, setRpcUrl] = useState("");
  const [forwardToken, setForwardToken] = useState("");
  const [sourceClientId, setSourceClientId] = useState("");

  const config = operatorQuery.data;
  if (config?.operator !== true) {
    return null;
  }
  const identity = config.dwsIdentity;
  const forward = config.forward;
  const activeClients = (a2aConfigQuery.data?.clients ?? EMPTY_CLIENTS).filter(
    (client) => client.status === "active",
  );
  // Only one source client may be forwarded. A choice counts only while that
  // client is still active; otherwise fall back to the current binding, then
  // to the Agent's only active client.
  const isActiveClient = (id: string | undefined) =>
    !!id && activeClients.some((client) => client.id === id);
  const selectedClientId = isActiveClient(sourceClientId)
    ? sourceClientId
    : isActiveClient(forward?.sourceClientId)
      ? forward!.sourceClientId
      : activeClients.length === 1
        ? activeClients[0]!.id
        : "";
  const clientName = (id: string) => activeClients.find((client) => client.id === id)?.name ?? id;
  const identityValid = DECIMAL_ID.test(uid.trim()) && DECIMAL_ID.test(orgId.trim());
  const forwardEnabled = config.forwardAllowedOrigins.length > 0;
  const forwardValid =
    rpcUrl.trim().startsWith("https://") && forwardToken.trim().length > 0 && selectedClientId !== "";

  const saveIdentity = async () => {
    try {
      await updateIdentity.mutateAsync({
        uid: uid.trim(),
        orgId: orgId.trim(),
        deapAgentUuid: deapAgentUuid.trim() || undefined,
      });
      setUid("");
      setOrgId("");
      setDeapAgentUuid("");
      toast.success(t(($) => $.tab_body.a2a.operator.identity_saved));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.tab_body.a2a.operator.save_failed));
    }
  };

  const clearIdentity = async () => {
    try {
      await deleteIdentity.mutateAsync();
      toast.success(t(($) => $.tab_body.a2a.operator.identity_cleared));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.tab_body.a2a.operator.save_failed));
    }
  };

  const saveForward = async () => {
    try {
      await updateForward.mutateAsync({
        rpcUrl: rpcUrl.trim(),
        token: forwardToken.trim(),
        sourceClientId: selectedClientId,
      });
      setRpcUrl("");
      setForwardToken("");
      toast.success(t(($) => $.tab_body.a2a.operator.forward_saved));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.tab_body.a2a.operator.save_failed));
    }
  };

  const clearForward = async () => {
    try {
      await deleteForward.mutateAsync();
      toast.success(t(($) => $.tab_body.a2a.operator.forward_cleared));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.tab_body.a2a.operator.save_failed));
    }
  };

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
              <span className="min-w-0 break-all font-mono">
                uid={identity.uid} · orgId={identity.orgId}
                {identity.deapAgentUuid ? ` · agentUuid=${identity.deapAgentUuid}` : ""}
              </span>
              <Button
                variant="outline"
                size="sm"
                onClick={() => void clearIdentity()}
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
          <div className="grid gap-3 sm:grid-cols-3">
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
              <Label htmlFor="a2a-operator-deap">
                {t(($) => $.tab_body.a2a.operator.deap_agent_uuid)}
              </Label>
              <Input
                id="a2a-operator-deap"
                value={deapAgentUuid}
                onChange={(event) => setDeapAgentUuid(event.target.value)}
                className="font-mono"
              />
            </div>
          </div>
          <Button
            size="sm"
            onClick={() => void saveIdentity()}
            disabled={!identityValid || updateIdentity.isPending}
          >
            {updateIdentity.isPending && <Loader2 className="size-4 animate-spin" />}
            {t(($) => $.tab_body.a2a.operator.save_identity)}
          </Button>
        </div>

        <div className="space-y-3 px-4 py-4">
          <div>
            <h4 className="text-body font-medium">{t(($) => $.tab_body.a2a.operator.forward_title)}</h4>
            <p className="mt-1 text-caption text-muted-foreground">
              {forwardEnabled
                ? t(($) => $.tab_body.a2a.operator.forward_description, {
                  origins: config.forwardAllowedOrigins.join(", "),
                })
                : t(($) => $.tab_body.a2a.operator.forward_disabled)}
            </p>
          </div>
          {forward && (
            <div className="flex flex-wrap items-center justify-between gap-2 rounded-md bg-muted/30 px-3 py-2.5 text-caption">
              <span className="flex min-w-0 items-center gap-2">
                <Badge variant={forward.active ? "default" : "secondary"}>
                  {forward.active
                    ? t(($) => $.tab_body.a2a.operator.forward_active)
                    : t(($) => $.tab_body.a2a.operator.forward_inactive)}
                </Badge>
                <span className="min-w-0 break-all font-mono">{forward.rpcUrl}</span>
                {forward.sourceClientId && (
                  <span className="text-muted-foreground">
                    ← {clientName(forward.sourceClientId)}
                  </span>
                )}
              </span>
              <Button
                variant="outline"
                size="sm"
                onClick={() => void clearForward()}
                disabled={deleteForward.isPending}
              >
                {t(($) => $.tab_body.a2a.operator.clear)}
              </Button>
            </div>
          )}
          {forwardEnabled && (
            <>
              <div className="space-y-1.5">
                <Label htmlFor="a2a-operator-forward-client">
                  {t(($) => $.tab_body.a2a.operator.source_client)}
                </Label>
                {activeClients.length === 0 ? (
                  <p className="text-caption text-muted-foreground">
                    {t(($) => $.tab_body.a2a.operator.no_client)}
                  </p>
                ) : activeClients.length === 1 ? (
                  <p id="a2a-operator-forward-client" className="text-body">
                    {activeClients[0]!.name}
                  </p>
                ) : (
                  <NativeSelect
                    id="a2a-operator-forward-client"
                    value={selectedClientId}
                    onChange={(event) => setSourceClientId(event.target.value)}
                  >
                    <NativeSelectOption value="" disabled>
                      {t(($) => $.tab_body.a2a.operator.source_client_placeholder)}
                    </NativeSelectOption>
                    {activeClients.map((client) => (
                      <NativeSelectOption key={client.id} value={client.id}>
                        {client.name}
                      </NativeSelectOption>
                    ))}
                  </NativeSelect>
                )}
              </div>
              <div className="grid gap-3 sm:grid-cols-2">
                <div className="space-y-1.5">
                  <Label htmlFor="a2a-operator-forward-url">
                    {t(($) => $.tab_body.a2a.operator.forward_url)}
                  </Label>
                  <Input
                    id="a2a-operator-forward-url"
                    value={rpcUrl}
                    placeholder="https://…/api/a2a/agents/{id}/v1"
                    onChange={(event) => setRpcUrl(event.target.value)}
                    className="font-mono"
                  />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="a2a-operator-forward-token">
                    {t(($) => $.tab_body.a2a.operator.forward_token)}
                  </Label>
                  <Input
                    id="a2a-operator-forward-token"
                    type="password"
                    autoComplete="off"
                    value={forwardToken}
                    onChange={(event) => setForwardToken(event.target.value)}
                    className="font-mono"
                  />
                </div>
              </div>
              <Button
                size="sm"
                onClick={() => void saveForward()}
                disabled={!forwardValid || updateForward.isPending}
              >
                {updateForward.isPending && <Loader2 className="size-4 animate-spin" />}
                {t(($) => $.tab_body.a2a.operator.save_forward)}
              </Button>
            </>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
