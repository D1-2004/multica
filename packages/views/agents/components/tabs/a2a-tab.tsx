"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, Copy, KeyRound, Loader2, RotateCw, Trash2 } from "lucide-react";
import { toast } from "sonner";
import type { Agent } from "@multica/core/types";
import {
  agentA2AConfigOptions,
  useCreateAgentA2AClient,
  useCreateAgentA2ACredential,
  useDeleteAgentA2ACredential,
  useUpdateAgentA2AClient,
  useUpdateAgentA2AConfig,
  type AgentA2AClient,
} from "@multica/core/agent-a2a";
import { useWorkspaceId } from "@multica/core/hooks";
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
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Switch } from "@multica/ui/components/ui/switch";
import { copyText } from "@multica/ui/lib/clipboard";
import { useT } from "../../../i18n";
import { A2AOperatorCard } from "./a2a-operator-card";

const A2A_CLIENT_NAME = "A2A Client";
const DEFAULT_SCOPES = ["send", "read", "list", "cancel"] as const;
const DEFAULT_EXPIRY_DAYS = 90;
const EMPTY_CLIENTS: AgentA2AClient[] = [];

interface GeneratedCredential {
  clientId: string;
  credentialId: string;
  token: string;
}

function credentialExpiry(): string {
  return new Date(
    Date.now() + DEFAULT_EXPIRY_DAYS * 24 * 60 * 60 * 1000,
  ).toISOString();
}

function findA2AClient(clients: AgentA2AClient[]): AgentA2AClient | null {
  return (
    clients.find(
      (client) =>
        client.status === "active" &&
        client.name.toLowerCase() === A2A_CLIENT_NAME.toLowerCase(),
    ) ?? null
  );
}

export function A2ATab({ agent }: { agent: Agent }) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const configQuery = useQuery({
    ...agentA2AConfigOptions(wsId, agent.id),
    enabled: !!wsId && !!agent.id,
  });
  const updateConfig = useUpdateAgentA2AConfig(wsId, agent.id);
  const createClient = useCreateAgentA2AClient(wsId, agent.id);
  const updateClient = useUpdateAgentA2AClient(wsId, agent.id);
  const createCredential = useCreateAgentA2ACredential(wsId, agent.id);
  const deleteCredential = useDeleteAgentA2ACredential(wsId, agent.id);

  const endpoint = configQuery.data?.endpoint ?? null;
  const clients = configQuery.data?.clients ?? EMPTY_CLIENTS;
  const a2aClient = useMemo(() => findA2AClient(clients), [clients]);
  const activeCredentials = useMemo(
    () =>
      a2aClient?.credentials.filter(
        (credential) => credential.status === "active",
      ) ?? [],
    [a2aClient],
  );
  const [generatedCredential, setGeneratedCredential] =
    useState<GeneratedCredential | null>(null);
  const [copiedValue, setCopiedValue] = useState<"rpc" | "card" | "token" | null>(
    null,
  );
  const [generating, setGenerating] = useState(false);
  const [revokeOpen, setRevokeOpen] = useState(false);

  const credentialExists =
    generatedCredential !== null || activeCredentials.length > 0;
  const endpointReady =
    endpoint?.enabled === true &&
    endpoint.rpcUrl.trim() !== "" &&
    endpoint.cardUrl.trim() !== "";
  const busy =
    generating ||
    updateConfig.isPending ||
    createClient.isPending ||
    updateClient.isPending ||
    createCredential.isPending ||
    deleteCredential.isPending;

  const copy = async (target: "rpc" | "card" | "token", value: string) => {
    if (await copyText(value)) {
      setCopiedValue(target);
      toast.success(t(($) => $.tab_body.a2a.copied));
      setTimeout(() => setCopiedValue(null), 2000);
    } else {
      toast.error(t(($) => $.tab_body.a2a.copy_failed));
    }
  };

  const revokeCredentialIds = async (
    client: AgentA2AClient,
    includeGenerated: boolean,
  ) => {
    const credentialIds = new Set(
      client.credentials
        .filter((credential) => credential.status === "active")
        .map((credential) => credential.id),
    );
    if (includeGenerated && generatedCredential?.clientId === client.id) {
      credentialIds.add(generatedCredential.credentialId);
    }
    for (const credentialId of credentialIds) {
      await deleteCredential.mutateAsync({
        clientId: client.id,
        credentialId,
      });
    }
  };

  const handleToggle = async (enabled: boolean) => {
    if (!endpoint) return;
    try {
      await updateConfig.mutateAsync({
        enabled,
        cardName: endpoint.cardName,
        cardDescription: endpoint.cardDescription,
        cardVersion: endpoint.cardVersion,
        cardSkills: endpoint.cardSkills,
      });
      toast.success(
        enabled
          ? t(($) => $.tab_body.a2a.remote_access_enabled)
          : t(($) => $.tab_body.a2a.remote_access_disabled),
      );
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.tab_body.a2a.config_save_failed),
      );
    }
  };

  const handleGenerateCredential = async () => {
    if (!endpointReady) return;
    setGenerating(true);
    setCopiedValue(null);
    try {
      let client = a2aClient;
      if (client) {
        await revokeCredentialIds(client, true);
      } else {
        client = await createClient.mutateAsync({
          name: A2A_CLIENT_NAME,
          scopes: [...DEFAULT_SCOPES],
        });
      }

      let token = "";
      const credential = await createCredential.mutateAsync({
        clientId: client.id,
        data: { expiresAt: credentialExpiry() },
        onToken: (value) => {
          token = value;
        },
      });
      if (!token) {
        throw new Error(t(($) => $.tab_body.a2a.credential_create_failed));
      }
      setGeneratedCredential({
        clientId: client.id,
        credentialId: credential.id,
        token,
      });
      toast.success(t(($) => $.tab_body.a2a.credential_created));
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.tab_body.a2a.credential_create_failed),
      );
    } finally {
      setGenerating(false);
    }
  };

  const handleScopeChange = async (
    scope: (typeof DEFAULT_SCOPES)[number],
    enabled: boolean,
  ) => {
    if (!a2aClient) return;
    const next = DEFAULT_SCOPES.filter((candidate) =>
      candidate === scope ? enabled : a2aClient.scopes.includes(candidate),
    );
    if (next.length === 0) return;
    try {
      await updateClient.mutateAsync({
        clientId: a2aClient.id,
        data: { scopes: [...next] },
      });
      toast.success(t(($) => $.tab_body.a2a.permissions_saved));
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.tab_body.a2a.permissions_save_failed),
      );
    }
  };

  const handleLimitChange = async (
    field: "rateLimitPerMinute" | "maxConcurrentTasks",
    rawValue: string,
  ) => {
    if (!a2aClient) return;
    const trimmed = rawValue.trim();
    const value = trimmed === "" ? null : Number(trimmed);
    if (value !== null && (!Number.isInteger(value) || value <= 0)) {
      toast.error(t(($) => $.tab_body.a2a.limit_invalid));
      return;
    }
    if (a2aClient[field] === value) return;
    try {
      await updateClient.mutateAsync({
        clientId: a2aClient.id,
        data: { [field]: value },
      });
      toast.success(t(($) => $.tab_body.a2a.permissions_saved));
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.tab_body.a2a.permissions_save_failed),
      );
    }
  };

  const handleRevokeCredential = async () => {
    const client =
      a2aClient ??
      clients.find(
        (candidate) => candidate.id === generatedCredential?.clientId,
      ) ??
      null;
    if (!client) return;
    try {
      await revokeCredentialIds(client, true);
      setGeneratedCredential(null);
      setCopiedValue(null);
      setRevokeOpen(false);
      toast.success(t(($) => $.tab_body.a2a.credential_revoked));
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.tab_body.a2a.credential_revoke_failed),
      );
    }
  };

  if (configQuery.isLoading) {
    return (
      <p className="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="size-4 animate-spin" />
        {t(($) => $.tab_body.a2a.loading)}
      </p>
    );
  }

  if (configQuery.isError || !endpoint) {
    return (
      <div className="space-y-3">
        <p className="text-sm text-destructive">
          {t(($) => $.tab_body.a2a.load_failed)}
        </p>
        <Button variant="outline" size="sm" onClick={() => configQuery.refetch()}>
          {t(($) => $.tab_body.a2a.retry)}
        </Button>
      </div>
    );
  }

  return (
    <div className="space-y-5">
      <p className="text-sm leading-6 text-muted-foreground">
        {t(($) => $.tab_body.a2a.a2a_only_intro)}
      </p>

      <Card className="py-0 shadow-none">
        <CardContent className="divide-y px-0">
          <div className="flex items-center justify-between gap-4 px-4 py-4">
            <div>
              <div className="flex items-center gap-2">
                <h3 className="text-sm font-semibold">
                  {t(($) => $.tab_body.a2a.enable_label)}
                </h3>
                <Badge variant={endpoint.enabled ? "default" : "secondary"}>
                  {endpoint.enabled
                    ? t(($) => $.tab_body.a2a.enabled)
                    : t(($) => $.tab_body.a2a.disabled)}
                </Badge>
              </div>
              <p className="mt-1 text-xs text-muted-foreground">
                {t(($) => $.tab_body.a2a.enable_hint)}
              </p>
            </div>
            <Switch
              checked={endpoint.enabled}
              onCheckedChange={(checked) => void handleToggle(checked)}
              disabled={updateConfig.isPending}
              aria-label={t(($) => $.tab_body.a2a.enable_label)}
            />
          </div>

          {endpoint.enabled && (
            <div className="space-y-4 px-4 py-4">
              <EndpointField
                label={t(($) => $.tab_body.a2a.rpc_url)}
                value={endpoint.rpcUrl}
                copied={copiedValue === "rpc"}
                copyLabel={t(($) => $.tab_body.a2a.copy_rpc_url)}
                onCopy={() => copy("rpc", endpoint.rpcUrl)}
              />
              <EndpointField
                label={t(($) => $.tab_body.a2a.card_url)}
                value={endpoint.cardUrl}
                copied={copiedValue === "card"}
                copyLabel={t(($) => $.tab_body.a2a.copy_card_url)}
                onCopy={() => copy("card", endpoint.cardUrl)}
              />
            </div>
          )}
        </CardContent>
      </Card>

      {endpoint.enabled && (
        <Card className="py-0 shadow-none">
          <CardContent className="space-y-4 px-4 py-4">
            <div>
              <div className="flex items-center gap-2">
                <h3 className="text-sm font-semibold">
                  {t(($) => $.tab_body.a2a.a2a_api_key_title)}
                </h3>
                <Badge variant={credentialExists ? "default" : "secondary"}>
                  {credentialExists
                    ? t(($) => $.tab_body.a2a.link_active)
                    : t(($) => $.tab_body.a2a.link_not_created)}
                </Badge>
              </div>
              <p className="mt-1 text-xs leading-5 text-muted-foreground">
                {t(($) => $.tab_body.a2a.a2a_api_key_description)}
              </p>
            </div>

            {generatedCredential ? (
              <EndpointField
                label="API Token"
                value={generatedCredential.token}
                copied={copiedValue === "token"}
                copyLabel={t(($) => $.tab_body.a2a.copy_secret)}
                onCopy={() => copy("token", generatedCredential.token)}
              />
            ) : credentialExists ? (
              <p className="rounded-md bg-muted/30 px-3 py-2.5 text-xs text-muted-foreground">
                {t(($) => $.tab_body.a2a.existing_token_description, {
                  prefix: activeCredentials[0]?.tokenPrefix ?? "mca2a_••••",
                })}
              </p>
            ) : null}

            {generatedCredential && (
              <p className="text-xs text-muted-foreground">
                {t(($) => $.tab_body.a2a.secret_warning)}
              </p>
            )}

            {a2aClient && (
              <div className="space-y-3">
                <Label>{t(($) => $.tab_body.a2a.permissions)}</Label>
                <div className="grid gap-2 sm:grid-cols-2">
                  {DEFAULT_SCOPES.map((scope) => (
                    <label key={scope} className="flex items-center gap-2 text-sm">
                      <Checkbox
                        checked={a2aClient.scopes.includes(scope)}
                        disabled={busy}
                        onCheckedChange={(checked) =>
                          void handleScopeChange(scope, checked === true)
                        }
                      />
                      {t(($) => $.tab_body.a2a.scope_labels[scope])}
                    </label>
                  ))}
                </div>
                <div className="grid gap-3 sm:grid-cols-2">
                  <div className="space-y-1.5">
                    <Label htmlFor="a2a-rate-limit">
                      {t(($) => $.tab_body.a2a.rate_limit)}
                    </Label>
                    <Input
                      id="a2a-rate-limit"
                      type="number"
                      min={1}
                      step={1}
                      disabled={busy}
                      defaultValue={a2aClient.rateLimitPerMinute ?? ""}
                      placeholder={t(($) => $.tab_body.a2a.unlimited)}
                      onBlur={(event) =>
                        void handleLimitChange("rateLimitPerMinute", event.target.value)
                      }
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label htmlFor="a2a-concurrency-limit">
                      {t(($) => $.tab_body.a2a.concurrency_limit)}
                    </Label>
                    <Input
                      id="a2a-concurrency-limit"
                      type="number"
                      min={1}
                      step={1}
                      disabled={busy}
                      defaultValue={a2aClient.maxConcurrentTasks ?? ""}
                      placeholder={t(($) => $.tab_body.a2a.unlimited)}
                      onBlur={(event) =>
                        void handleLimitChange("maxConcurrentTasks", event.target.value)
                      }
                    />
                  </div>
                </div>
              </div>
            )}

            <div className="flex flex-wrap gap-2">
              <Button
                size="sm"
                onClick={handleGenerateCredential}
                disabled={!endpointReady || busy}
              >
                {generating ? (
                  <Loader2 className="size-4 animate-spin" />
                ) : credentialExists ? (
                  <RotateCw className="size-4" />
                ) : (
                  <KeyRound className="size-4" />
                )}
                {credentialExists
                  ? t(($) => $.tab_body.a2a.regenerate_api_key)
                  : t(($) => $.tab_body.a2a.generate_api_key)}
              </Button>
              {credentialExists && (
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => setRevokeOpen(true)}
                  disabled={busy}
                >
                  <Trash2 className="size-4 text-destructive" />
                  {t(($) => $.tab_body.a2a.revoke)}
                </Button>
              )}
            </div>
          </CardContent>
        </Card>
      )}

      <A2AOperatorCard wsId={wsId} agentId={agent.id} />

      <AlertDialog open={revokeOpen} onOpenChange={setRevokeOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.tab_body.a2a.revoke_credential_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.tab_body.a2a.revoke_credential_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t(($) => $.tab_body.a2a.cancel)}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={handleRevokeCredential}>
              {t(($) => $.tab_body.a2a.revoke)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function EndpointField({
  label,
  value,
  copied,
  copyLabel,
  onCopy,
}: {
  label: string;
  value: string;
  copied: boolean;
  copyLabel: string;
  onCopy: () => void;
}) {
  return (
    <div className="space-y-1.5">
      <Label>{label}</Label>
      <div className="flex min-w-0 gap-2">
        <Input
          readOnly
          value={value}
          className="min-w-0 bg-muted/30 font-mono text-xs"
        />
        <Button
          variant="outline"
          size="icon"
          onClick={onCopy}
          aria-label={copyLabel}
        >
          {copied ? <Check className="size-4" /> : <Copy className="size-4" />}
        </Button>
      </div>
    </div>
  );
}
