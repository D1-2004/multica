"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Check,
  Copy,
  Download,
  KeyRound,
  Loader2,
  Plus,
  Trash2,
} from "lucide-react";
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
import { Alert, AlertDescription } from "@multica/ui/components/ui/alert";
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Switch } from "@multica/ui/components/ui/switch";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { copyText } from "@multica/ui/lib/clipboard";
import { useT } from "../../../i18n";
import {
  buildA2ACurlExample,
  buildA2ALocalDebugBundle,
  buildCodingAgentMCPBundle,
  buildMulticaA2AExport,
  serializeJson,
} from "./a2a-export";

// The first inbound slice implements SendMessage and GetTask only. Keep newly
// minted clients honest about the operations this server actually exposes.
const DEFAULT_SCOPES = ["send", "read"] as const;
const CREDENTIAL_EXPIRIES = ["30", "90", "365", "never"] as const;

interface CardFormState {
  enabled: boolean;
  cardName: string;
  cardDescription: string;
  cardVersion: string;
}

interface SecretState {
  clientName: string;
  token: string;
  rpcUrl: string;
  agentCardUrl: string;
  protocolVersion: string;
  mcpUrl: string;
  publicAgentId: string;
}

function buildSecretDebugBundle(secret: SecretState | null) {
  if (
    !secret ||
    !secret.rpcUrl.trim() ||
    !secret.agentCardUrl.trim() ||
    !secret.protocolVersion.trim()
  ) {
    return null;
  }
  return buildA2ALocalDebugBundle({
    rpcUrl: secret.rpcUrl,
    agentCardUrl: secret.agentCardUrl,
    token: secret.token,
    protocolVersion: secret.protocolVersion,
    preferredBinding: "JSONRPC",
  });
}

function buildSecretMCPBundle(secret: SecretState | null) {
  if (!secret?.mcpUrl.trim() || !secret.publicAgentId.trim()) return null;
  try {
    return buildCodingAgentMCPBundle({
      mcpUrl: secret.mcpUrl,
      token: secret.token,
      publicAgentId: secret.publicAgentId,
    });
  } catch {
    return null;
  }
}

function formatDate(value: string | null): string {
  return value ? new Date(value).toLocaleString() : "—";
}

function credentialExpiry(option: string): string | null {
  if (option === "never") return null;
  return new Date(
    Date.now() + Number(option) * 24 * 60 * 60 * 1000,
  ).toISOString();
}

function downloadJson(filename: string, value: unknown) {
  const blob = new Blob([serializeJson(value)], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.click();
  URL.revokeObjectURL(url);
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
  const agentCard = configQuery.data?.agentCard ?? null;
  const clients = configQuery.data?.clients ?? [];
  const initialForm = useMemo<CardFormState>(
    () => ({
      enabled: endpoint?.enabled === true,
      cardName: endpoint?.cardName ?? agent.name,
      cardDescription: endpoint?.cardDescription ?? agent.description ?? "",
      cardVersion: endpoint?.cardVersion ?? "1.0.0",
    }),
    [
      agent.description,
      agent.name,
      endpoint?.cardDescription,
      endpoint?.cardName,
      endpoint?.cardVersion,
      endpoint?.enabled,
    ],
  );
  const [form, setForm] = useState<CardFormState>(initialForm);
  const [clientDialogOpen, setClientDialogOpen] = useState(false);
  const [clientName, setClientName] = useState("");
  const [credentialClient, setCredentialClient] =
    useState<AgentA2AClient | null>(null);
  const [credentialExpiryOption, setCredentialExpiryOption] = useState("90");
  const [quickConnecting, setQuickConnecting] = useState(false);
  const [secret, setSecret] = useState<SecretState | null>(null);
  const [revokeClient, setRevokeClient] = useState<AgentA2AClient | null>(null);
  const [revokeCredential, setRevokeCredential] = useState<{
    client: AgentA2AClient;
    credentialId: string;
  } | null>(null);

  useEffect(() => {
    setForm(initialForm);
  }, [initialForm]);

  const dirty =
    form.enabled !== initialForm.enabled ||
    form.cardName !== initialForm.cardName ||
    form.cardDescription !== initialForm.cardDescription ||
    form.cardVersion !== initialForm.cardVersion;

  const cardJson = useMemo(
    () => (agentCard ? serializeJson(agentCard) : ""),
    [agentCard],
  );
  const hasPublicCardUrl = (endpoint?.cardUrl.trim() ?? "") !== "";
  const hasPublicRpcUrl = (endpoint?.rpcUrl.trim() ?? "") !== "";
  const canExportConnection =
    endpoint?.enabled === true && hasPublicCardUrl && hasPublicRpcUrl;
  const connectionPreset = useMemo(
    () => {
      if (!endpoint || !canExportConnection) return null;
      return buildMulticaA2AExport({
        rpcUrl: endpoint.rpcUrl,
        agentCardUrl: endpoint.cardUrl,
        protocolVersion: endpoint.protocolVersion,
        preferredBinding: "JSONRPC",
      });
    },
    [canExportConnection, endpoint],
  );
  const connectionPresetJson = useMemo(
    () => (connectionPreset ? serializeJson(connectionPreset) : ""),
    [connectionPreset],
  );
  const curlExample = useMemo(
    () =>
      endpoint && canExportConnection
        ? buildA2ACurlExample({
            rpcUrl: endpoint.rpcUrl,
            protocolVersion: endpoint.protocolVersion,
          })
        : "",
    [canExportConnection, endpoint],
  );

  const handleCopy = async (value: string) => {
    if (await copyText(value)) {
      toast.success(t(($) => $.tab_body.a2a.copied));
    } else {
      toast.error(t(($) => $.tab_body.a2a.copy_failed));
    }
  };

  const handleSaveConfig = async () => {
    try {
      await updateConfig.mutateAsync({
        enabled: form.enabled,
        cardName: form.cardName.trim(),
        cardDescription: form.cardDescription.trim(),
        cardVersion: form.cardVersion.trim(),
        cardSkills: endpoint?.cardSkills ?? [],
      });
      toast.success(t(($) => $.tab_body.a2a.config_saved));
    } catch (error) {
      setForm(initialForm);
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.tab_body.a2a.config_save_failed),
      );
    }
  };

  const handleCreateClient = async () => {
    try {
      await createClient.mutateAsync({
        name: clientName.trim(),
        scopes: [...DEFAULT_SCOPES],
      });
      setClientName("");
      setClientDialogOpen(false);
      toast.success(t(($) => $.tab_body.a2a.client_created));
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.tab_body.a2a.client_create_failed),
      );
    }
  };

  const handleCreateCredential = async () => {
    if (!credentialClient || !endpoint || !canExportConnection) return;
    const selectedClient = credentialClient;
    const persistedEndpoint = endpoint;
    setSecret(null);
    try {
      await createCredential.mutateAsync({
        clientId: selectedClient.id,
        data: { expiresAt: credentialExpiry(credentialExpiryOption) },
        onToken: (token) =>
          setSecret({
            clientName: selectedClient.name,
            token,
            rpcUrl: persistedEndpoint.rpcUrl,
            agentCardUrl: persistedEndpoint.cardUrl,
            protocolVersion: persistedEndpoint.protocolVersion,
            mcpUrl: persistedEndpoint.mcpUrl ?? "",
            publicAgentId: persistedEndpoint.publicAgentId,
          }),
      });
      setCredentialClient(null);
      setCredentialExpiryOption("90");
      toast.success(t(($) => $.tab_body.a2a.credential_created));
    } catch (error) {
      setSecret(null);
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.tab_body.a2a.credential_create_failed),
      );
    }
  };

  const handleQuickConnect = async () => {
    if (!endpoint || !canExportConnection || !endpoint.mcpUrl?.trim()) return;
    setQuickConnecting(true);
    setSecret(null);
    try {
      let client = clients.find(
        (candidate) =>
          candidate.status === "active" &&
          candidate.name.toLowerCase() === "local coding agent",
      );
      client ??= await createClient.mutateAsync({
        name: "Local Coding Agent",
        scopes: [...DEFAULT_SCOPES],
      });
      const selectedClient = client;
      await createCredential.mutateAsync({
        clientId: selectedClient.id,
        data: { expiresAt: credentialExpiry("90") },
        onToken: (token) =>
          setSecret({
            clientName: selectedClient.name,
            token,
            rpcUrl: endpoint.rpcUrl,
            agentCardUrl: endpoint.cardUrl,
            protocolVersion: endpoint.protocolVersion,
            mcpUrl: endpoint.mcpUrl ?? "",
            publicAgentId: endpoint.publicAgentId,
          }),
      });
    } catch (error) {
      setSecret(null);
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.tab_body.a2a.credential_create_failed),
      );
    } finally {
      setQuickConnecting(false);
    }
  };

  const handleRevokeClient = async () => {
    if (!revokeClient) return;
    try {
      await updateClient.mutateAsync({
        clientId: revokeClient.id,
        data: { status: "revoked" },
      });
      toast.success(t(($) => $.tab_body.a2a.client_revoked));
      setRevokeClient(null);
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.tab_body.a2a.client_revoke_failed),
      );
    }
  };

  const handleRevokeCredential = async () => {
    if (!revokeCredential) return;
    try {
      await deleteCredential.mutateAsync({
        clientId: revokeCredential.client.id,
        credentialId: revokeCredential.credentialId,
      });
      toast.success(t(($) => $.tab_body.a2a.credential_revoked));
      setRevokeCredential(null);
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

  if (configQuery.isError) {
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
    <div className="space-y-8">
      <p className="text-xs leading-5 text-muted-foreground">
        {t(($) => $.tab_body.a2a.intro)}
      </p>

      <Alert>
        <AlertDescription>
          {t(($) => $.tab_body.a2a.capability_warning)}
        </AlertDescription>
      </Alert>

      <section className="space-y-3">
        <div className="flex items-end justify-between gap-4">
          <div>
            <h3 className="text-sm font-semibold">
              {t(($) => $.tab_body.a2a.endpoint_title)}
            </h3>
            <p className="mt-1 text-xs text-muted-foreground">
              {t(($) => $.tab_body.a2a.endpoint_description)}
            </p>
          </div>
          <Badge variant={endpoint?.enabled === true ? "default" : "secondary"}>
            {endpoint?.enabled === true
              ? t(($) => $.tab_body.a2a.enabled)
              : t(($) => $.tab_body.a2a.disabled)}
          </Badge>
        </div>
        <Card className="py-0 shadow-none">
          <CardContent className="divide-y px-0">
            <div className="flex items-center justify-between gap-4 px-4 py-3.5">
              <div>
                <Label htmlFor="agent-a2a-enabled">
                  {t(($) => $.tab_body.a2a.enable_label)}
                </Label>
                <p className="mt-1 text-xs text-muted-foreground">
                  {t(($) => $.tab_body.a2a.enable_hint)}
                </p>
              </div>
              <Switch
                id="agent-a2a-enabled"
                checked={form.enabled}
                onCheckedChange={(checked) =>
                  setForm((current) => ({ ...current, enabled: checked }))
                }
                aria-label={t(($) => $.tab_body.a2a.enable_label)}
              />
            </div>
            {hasPublicRpcUrl && (
              <div className="space-y-2 bg-primary/[0.03] px-4 py-4">
                <div>
                  <Label>{t(($) => $.tab_body.a2a.rpc_url)}</Label>
                  {endpoint?.enabled === true && (
                    <p className="mt-1 text-xs text-muted-foreground">
                      {t(($) => $.tab_body.a2a.call_description)}
                    </p>
                  )}
                </div>
                <div className="flex min-w-0 gap-2">
                  <Input
                    readOnly
                    value={endpoint?.rpcUrl ?? ""}
                    className="min-w-0 bg-background font-mono text-xs"
                  />
                  <Button
                    variant="default"
                    size="icon"
                    onClick={() => handleCopy(endpoint?.rpcUrl ?? "")}
                    aria-label={t(($) => $.tab_body.a2a.copy_rpc_url)}
                  >
                    <Copy className="size-4" />
                  </Button>
                </div>
              </div>
            )}
            <div className="grid gap-4 px-4 py-4 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label htmlFor="agent-a2a-name">
                  {t(($) => $.tab_body.a2a.card_name)}
                </Label>
                <Input
                  id="agent-a2a-name"
                  value={form.cardName}
                  onChange={(event) =>
                    setForm((current) => ({
                      ...current,
                      cardName: event.target.value,
                    }))
                  }
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="agent-a2a-version">
                  {t(($) => $.tab_body.a2a.card_version)}
                </Label>
                <Input
                  id="agent-a2a-version"
                  value={form.cardVersion}
                  onChange={(event) =>
                    setForm((current) => ({
                      ...current,
                      cardVersion: event.target.value,
                    }))
                  }
                />
              </div>
              <div className="space-y-1.5 sm:col-span-2">
                <Label htmlFor="agent-a2a-description">
                  {t(($) => $.tab_body.a2a.card_description)}
                </Label>
                <Textarea
                  id="agent-a2a-description"
                  value={form.cardDescription}
                  onChange={(event) =>
                    setForm((current) => ({
                      ...current,
                      cardDescription: event.target.value,
                    }))
                  }
                  rows={3}
                />
              </div>
            </div>
            <div className="flex justify-end px-4 py-3">
              <Button
                size="sm"
                onClick={handleSaveConfig}
                disabled={
                  !dirty ||
                  !form.cardName.trim() ||
                  !form.cardVersion.trim() ||
                  updateConfig.isPending
                }
              >
                {updateConfig.isPending && (
                  <Loader2 className="size-4 animate-spin" />
                )}
                {t(($) => $.tab_body.a2a.save_config)}
              </Button>
            </div>
          </CardContent>
        </Card>
      </section>

      {endpoint && (
        <section className="space-y-3">
          <div>
            <h3 className="text-sm font-semibold">
              {t(($) => $.tab_body.a2a.discovery_title)}
            </h3>
            <p className="mt-1 text-xs text-muted-foreground">
              {t(($) => $.tab_body.a2a.discovery_description)}
            </p>
          </div>
          {!canExportConnection && (
            <Alert>
              <AlertDescription>
                {t(($) => $.tab_body.a2a.export_unavailable)}
              </AlertDescription>
            </Alert>
          )}
          {hasPublicCardUrl && (
            <>
              {endpoint.enabled === true && (
                <Alert>
                  <AlertDescription>
                    {t(($) => $.tab_body.a2a.public_card_notice)}
                  </AlertDescription>
                </Alert>
              )}
              <Card className="py-0 shadow-none">
                <CardContent className="space-y-5 px-4 py-4">
                  <div className="space-y-1.5">
                    <Label>{t(($) => $.tab_body.a2a.card_url)}</Label>
                    <div className="flex min-w-0 gap-2">
                      <Input
                        readOnly
                        value={endpoint.cardUrl}
                        className="min-w-0 font-mono text-xs"
                      />
                      <Button
                        variant="outline"
                        size="icon"
                        onClick={() => handleCopy(endpoint.cardUrl)}
                        aria-label={t(($) => $.tab_body.a2a.copy_card_url)}
                      >
                        <Copy className="size-4" />
                      </Button>
                    </div>
                  </div>

                  {agentCard && (
                    <div className="space-y-2">
                      <div className="flex flex-wrap items-center justify-between gap-2">
                        <Label>{t(($) => $.tab_body.a2a.agent_card)}</Label>
                        <div className="flex gap-2">
                          <Button
                            variant="outline"
                            size="sm"
                            onClick={() => handleCopy(cardJson)}
                          >
                            <Copy className="size-4" />
                            {t(($) => $.tab_body.a2a.copy)}
                          </Button>
                          <Button
                            variant="outline"
                            size="sm"
                            onClick={() =>
                              downloadJson("agent-card.json", agentCard)
                            }
                          >
                            <Download className="size-4" />
                            {t(($) => $.tab_body.a2a.download)}
                          </Button>
                        </div>
                      </div>
                      <pre className="max-h-80 overflow-auto rounded-md border bg-muted/40 p-3 text-xs leading-5">
                        {cardJson}
                      </pre>
                    </div>
                  )}
                </CardContent>
              </Card>
            </>
          )}
        </section>
      )}

      {endpoint && canExportConnection && connectionPreset && (
        <section className="space-y-3">
          <div>
            <h3 className="text-sm font-semibold">
              {t(($) => $.tab_body.a2a.export_title)}
            </h3>
            <p className="mt-1 text-xs text-muted-foreground">
              {t(($) => $.tab_body.a2a.export_description)}
            </p>
          </div>
          <Card className="py-0 shadow-none">
            <CardContent className="space-y-5 px-4 py-4">
              <div className="space-y-2">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div>
                    <Label>{t(($) => $.tab_body.a2a.preset_file)}</Label>
                    <p className="mt-1 text-xs text-muted-foreground">
                      {t(($) => $.tab_body.a2a.nonstandard_notice)}
                    </p>
                  </div>
                  <div className="flex gap-2">
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => handleCopy(connectionPresetJson)}
                    >
                      <Copy className="size-4" />
                      {t(($) => $.tab_body.a2a.copy)}
                    </Button>
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() =>
                        downloadJson("multica-a2a.json", connectionPreset)
                      }
                    >
                      <Download className="size-4" />
                      {t(($) => $.tab_body.a2a.download)}
                    </Button>
                  </div>
                </div>
                <pre className="overflow-auto rounded-md border bg-muted/40 p-3 text-xs leading-5">
                  {connectionPresetJson}
                </pre>
              </div>
              <div className="space-y-2">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div>
                    <Label>{t(($) => $.tab_body.a2a.curl_title)}</Label>
                    <p className="mt-1 text-xs text-muted-foreground">
                      {t(($) => $.tab_body.a2a.curl_description)}
                    </p>
                  </div>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() =>
                      handleCopy(
                        buildA2ACurlExample({
                          rpcUrl: endpoint.rpcUrl,
                          protocolVersion: endpoint.protocolVersion,
                        }),
                      )
                    }
                  >
                    <Copy className="size-4" />
                    {t(($) => $.tab_body.a2a.copy)}
                  </Button>
                </div>
                <pre className="overflow-auto rounded-md border bg-muted/40 p-3 text-xs leading-5">
                  {curlExample}
                </pre>
              </div>
            </CardContent>
          </Card>
        </section>
      )}

      {endpoint && (
        <section className="space-y-3">
          <Card className="py-0 shadow-none">
            <CardContent className="flex flex-wrap items-center justify-between gap-4 px-4 py-4">
              <div>
                <h3 className="text-sm font-semibold">
                  {t(($) => $.tab_body.a2a.quick_connect_title)}
                </h3>
                <p className="mt-1 text-xs text-muted-foreground">
                  {t(($) => $.tab_body.a2a.quick_connect_description)}
                </p>
              </div>
              <Button
                onClick={handleQuickConnect}
                disabled={
                  !canExportConnection ||
                  !endpoint.mcpUrl?.trim() ||
                  quickConnecting
                }
              >
                {quickConnecting ? (
                  <Loader2 className="size-4 animate-spin" />
                ) : (
                  <KeyRound className="size-4" />
                )}
                {t(($) => $.tab_body.a2a.quick_connect_action)}
              </Button>
            </CardContent>
          </Card>
        </section>
      )}

      {endpoint && (
        <section className="space-y-3">
          <div className="flex items-end justify-between gap-4">
            <div>
              <h3 className="text-sm font-semibold">
                {t(($) => $.tab_body.a2a.clients_title)}
              </h3>
              <p className="mt-1 text-xs text-muted-foreground">
                {t(($) => $.tab_body.a2a.clients_description)}
              </p>
            </div>
            <Button size="sm" onClick={() => setClientDialogOpen(true)}>
              <Plus className="size-4" />
              {t(($) => $.tab_body.a2a.create_client)}
            </Button>
          </div>
          {clients.length === 0 ? (
            <Card className="shadow-none">
              <CardContent className="text-sm text-muted-foreground">
                {t(($) => $.tab_body.a2a.clients_empty)}
              </CardContent>
            </Card>
          ) : (
            <div className="space-y-3">
              {clients.map((client) => (
                <ClientCard
                  key={client.id}
                  client={client}
                  canCreateCredential={canExportConnection}
                  onCreateCredential={() => setCredentialClient(client)}
                  onRevokeClient={() => setRevokeClient(client)}
                  onRevokeCredential={(credentialId) =>
                    setRevokeCredential({ client, credentialId })
                  }
                />
              ))}
            </div>
          )}
        </section>
      )}

      <Dialog
        open={clientDialogOpen}
        onOpenChange={(open) => {
          setClientDialogOpen(open);
          if (!open) setClientName("");
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t(($) => $.tab_body.a2a.client_dialog_title)}</DialogTitle>
            <DialogDescription>
              {t(($) => $.tab_body.a2a.client_dialog_description)}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-1.5">
            <Label htmlFor="a2a-client-name">
              {t(($) => $.tab_body.a2a.client_name)}
            </Label>
            <Input
              id="a2a-client-name"
              value={clientName}
              onChange={(event) => setClientName(event.target.value)}
              placeholder={t(($) => $.tab_body.a2a.client_name_placeholder)}
            />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setClientDialogOpen(false)}>
              {t(($) => $.tab_body.a2a.cancel)}
            </Button>
            <Button
              onClick={handleCreateClient}
              disabled={!clientName.trim() || createClient.isPending}
            >
              {createClient.isPending && <Loader2 className="size-4 animate-spin" />}
              {t(($) => $.tab_body.a2a.create)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={credentialClient !== null}
        onOpenChange={(open) => {
          if (!open) {
            setCredentialClient(null);
            setCredentialExpiryOption("90");
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {t(($) => $.tab_body.a2a.credential_dialog_title)}
            </DialogTitle>
            <DialogDescription>
              {t(($) => $.tab_body.a2a.credential_dialog_description, {
                client: credentialClient?.name ?? "",
              })}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-1.5">
            <Label>{t(($) => $.tab_body.a2a.expires)}</Label>
            <Select
              value={credentialExpiryOption}
              onValueChange={(value) => {
                if (value) setCredentialExpiryOption(value);
              }}
            >
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {CREDENTIAL_EXPIRIES.map((option) => (
                  <SelectItem key={option} value={option}>
                    {option === "never"
                      ? t(($) => $.tab_body.a2a.expiry_never)
                      : t(($) => $.tab_body.a2a.expiry_days, {
                          count: Number(option),
                        })}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCredentialClient(null)}>
              {t(($) => $.tab_body.a2a.cancel)}
            </Button>
            <Button
              onClick={handleCreateCredential}
              disabled={!canExportConnection || createCredential.isPending}
            >
              {createCredential.isPending && (
                <Loader2 className="size-4 animate-spin" />
              )}
              {t(($) => $.tab_body.a2a.create)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <SecretDialog secret={secret} onClose={() => setSecret(null)} />

      <AlertDialog
        open={revokeClient !== null}
        onOpenChange={(open) => {
          if (!open) setRevokeClient(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.tab_body.a2a.revoke_client_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.tab_body.a2a.revoke_client_description, {
                client: revokeClient?.name ?? "",
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t(($) => $.tab_body.a2a.cancel)}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={handleRevokeClient}
            >
              {t(($) => $.tab_body.a2a.revoke)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={revokeCredential !== null}
        onOpenChange={(open) => {
          if (!open) setRevokeCredential(null);
        }}
      >
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
            <AlertDialogAction
              variant="destructive"
              onClick={handleRevokeCredential}
            >
              {t(($) => $.tab_body.a2a.revoke)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function ClientCard({
  client,
  canCreateCredential,
  onCreateCredential,
  onRevokeClient,
  onRevokeCredential,
}: {
  client: AgentA2AClient;
  canCreateCredential: boolean;
  onCreateCredential: () => void;
  onRevokeClient: () => void;
  onRevokeCredential: (credentialId: string) => void;
}) {
  const { t } = useT("agents");
  const active = client.status === "active";

  return (
    <Card className="py-0 shadow-none">
      <CardContent className="px-4 py-4">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <h4 className="truncate text-sm font-medium">{client.name}</h4>
              <Badge variant={active ? "outline" : "secondary"}>
                {client.status}
              </Badge>
            </div>
            <p className="mt-1 text-xs text-muted-foreground">
              {t(($) => $.tab_body.a2a.scopes)}: {client.scopes.join(", ")}
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            {active && (
              <Button
                variant="outline"
                size="sm"
                onClick={onCreateCredential}
                disabled={!canCreateCredential}
              >
                <KeyRound className="size-4" />
                {t(($) => $.tab_body.a2a.create_credential)}
              </Button>
            )}
            {client.status !== "revoked" && (
              <Button variant="ghost" size="sm" onClick={onRevokeClient}>
                <Trash2 className="size-4 text-destructive" />
                {t(($) => $.tab_body.a2a.revoke)}
              </Button>
            )}
          </div>
        </div>

        <div className="mt-4 border-t pt-3">
          <p className="text-xs font-medium">
            {t(($) => $.tab_body.a2a.credentials)}
          </p>
          {client.credentials.length === 0 ? (
            <p className="mt-2 text-xs text-muted-foreground">
              {t(($) => $.tab_body.a2a.credentials_empty)}
            </p>
          ) : (
            <ul className="mt-2 divide-y">
              {client.credentials.map((credential) => (
                <li
                  key={credential.id}
                  className="flex flex-wrap items-center justify-between gap-3 py-2 first:pt-0 last:pb-0"
                >
                  <div className="min-w-0 text-xs">
                    <div className="flex items-center gap-2">
                      <code className="truncate">{credential.tokenPrefix}…</code>
                      <Badge
                        variant={
                          credential.status === "active" ? "outline" : "secondary"
                        }
                      >
                        {credential.status}
                      </Badge>
                    </div>
                    <p className="mt-1 text-muted-foreground">
                      {t(($) => $.tab_body.a2a.expires)}: {formatDate(credential.expiresAt)}
                      {" · "}
                      {t(($) => $.tab_body.a2a.last_used)}: {formatDate(credential.lastUsedAt)}
                    </p>
                  </div>
                  {credential.status === "active" && (
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      onClick={() => onRevokeCredential(credential.id)}
                      aria-label={t(($) => $.tab_body.a2a.revoke_credential_aria)}
                    >
                      <Trash2 className="size-4 text-destructive" />
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

function SecretDialog({
  secret,
  onClose,
}: {
  secret: SecretState | null;
  onClose: () => void;
}) {
  const { t } = useT("agents");
  const [copiedTarget, setCopiedTarget] = useState<
    "token" | "config" | "curl" | "codex" | "claude" | null
  >(null);
  const [confirmed, setConfirmed] = useState(false);
  const localBundle = useMemo(() => buildSecretDebugBundle(secret), [secret]);
  const localConfig = localBundle?.config ?? null;
  const localConfigJson = useMemo(
    () => (localConfig ? serializeJson(localConfig) : ""),
    [localConfig],
  );
  const localCurl = localBundle?.curl ?? "";
  const mcpBundle = useMemo(() => buildSecretMCPBundle(secret), [secret]);

  const close = () => {
    setCopiedTarget(null);
    setConfirmed(false);
    onClose();
  };

  const copy = async (
    target: "token" | "config" | "curl" | "codex" | "claude",
    value: string,
  ) => {
    if (await copyText(value)) {
      setCopiedTarget(target);
      setTimeout(() => setCopiedTarget(null), 2000);
    }
  };

  return (
    <Dialog
      open={secret !== null}
      onOpenChange={(open) => {
        if (!open && confirmed) close();
      }}
    >
      <DialogContent
        showCloseButton={false}
        className="max-h-[90vh] overflow-y-auto sm:max-w-2xl"
      >
        <DialogHeader>
          <DialogTitle>{t(($) => $.tab_body.a2a.secret_title)}</DialogTitle>
          <DialogDescription>
            {t(($) => $.tab_body.a2a.secret_description, {
              client: secret?.clientName ?? "",
            })}
          </DialogDescription>
        </DialogHeader>
        <Alert>
          <AlertDescription>
            {t(($) => $.tab_body.a2a.secret_warning)}
          </AlertDescription>
        </Alert>
        <div className="flex min-w-0 gap-2">
          <Input
            readOnly
            value={secret?.token ?? ""}
            className="min-w-0 font-mono"
          />
          <Button
            variant="outline"
            size="icon"
            onClick={() => copy("token", secret?.token ?? "")}
            aria-label={t(($) => $.tab_body.a2a.copy_secret)}
          >
            {copiedTarget === "token" ? (
              <Check className="size-4" />
            ) : (
              <Copy className="size-4" />
            )}
          </Button>
        </div>
        {mcpBundle && (
          <div className="space-y-4 border-t pt-4">
            <div>
              <Label>{t(($) => $.tab_body.a2a.mcp_connect_title)}</Label>
              <p className="mt-1 text-xs text-muted-foreground">
                {t(($) => $.tab_body.a2a.mcp_connect_description)}
              </p>
            </div>
            {([
              ["codex", "Codex", mcpBundle.codexCommand],
              ["claude", "Claude Code", mcpBundle.claudeCommand],
            ] as const).map(([target, label, command]) => (
              <div key={target} className="space-y-2">
                <div className="flex items-center justify-between gap-2">
                  <Label>{label}</Label>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => void copy(target, command)}
                  >
                    {copiedTarget === target ? (
                      <Check className="size-4" />
                    ) : (
                      <Copy className="size-4" />
                    )}
                    {t(($) => $.tab_body.a2a.copy_command)}
                  </Button>
                </div>
                <pre className="overflow-auto rounded-md border bg-muted/40 p-3 text-xs leading-5">
                  {command}
                </pre>
              </div>
            ))}
            <p className="text-xs text-muted-foreground">
              {t(($) => $.tab_body.a2a.mcp_connect_warning)}
            </p>
          </div>
        )}
        {localConfig && (
          <div className="space-y-5 border-t pt-4">
            <div className="space-y-2">
              <div className="flex flex-wrap items-start justify-between gap-2">
                <div>
                  <Label>{t(($) => $.tab_body.a2a.local_config_title)}</Label>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {t(($) => $.tab_body.a2a.local_config_description)}
                  </p>
                </div>
                <div className="flex gap-2">
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => {
                      const freshBundle = buildSecretDebugBundle(secret);
                      if (freshBundle) {
                        void copy(
                          "config",
                          serializeJson(freshBundle.config),
                        );
                      }
                    }}
                  >
                    {copiedTarget === "config" ? (
                      <Check className="size-4" />
                    ) : (
                      <Copy className="size-4" />
                    )}
                    {t(($) => $.tab_body.a2a.copy)}
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => {
                      const freshBundle = buildSecretDebugBundle(secret);
                      if (freshBundle) {
                        downloadJson(
                          "multica-a2a.local.json",
                          freshBundle.config,
                        );
                      }
                    }}
                  >
                    <Download className="size-4" />
                    {t(($) => $.tab_body.a2a.download)}
                  </Button>
                </div>
              </div>
              <pre className="max-h-64 overflow-auto rounded-md border bg-muted/40 p-3 text-xs leading-5">
                {localConfigJson}
              </pre>
            </div>
            <div className="space-y-2">
              <div className="flex flex-wrap items-start justify-between gap-2">
                <div>
                  <Label>{t(($) => $.tab_body.a2a.local_curl_title)}</Label>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {t(($) => $.tab_body.a2a.local_curl_description)}
                  </p>
                </div>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => {
                    const freshBundle = buildSecretDebugBundle(secret);
                    if (freshBundle) {
                      void copy("curl", freshBundle.curl);
                    }
                  }}
                >
                  {copiedTarget === "curl" ? (
                    <Check className="size-4" />
                  ) : (
                    <Copy className="size-4" />
                  )}
                  {t(($) => $.tab_body.a2a.copy)}
                </Button>
              </div>
              <pre className="max-h-64 overflow-auto rounded-md border bg-muted/40 p-3 text-xs leading-5">
                {localCurl}
              </pre>
            </div>
          </div>
        )}
        <label className="flex items-center gap-2 text-sm">
          <Checkbox
            checked={confirmed}
            onCheckedChange={(value) => setConfirmed(value === true)}
          />
          {t(($) => $.tab_body.a2a.secret_confirm)}
        </label>
        <DialogFooter>
          <Button onClick={close} disabled={!confirmed}>
            {t(($) => $.tab_body.a2a.done)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
