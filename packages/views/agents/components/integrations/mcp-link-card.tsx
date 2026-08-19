"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, Copy, KeyRound, Link2, Loader2, RotateCw, Trash2 } from "lucide-react";
import { toast } from "sonner";
import type { Agent } from "@multica/core/types";
import {
  agentA2AConfigOptions,
  useCreateAgentA2AClient,
  useCreateAgentA2ACredential,
  useDeleteAgentA2ACredential,
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
import { Input } from "@multica/ui/components/ui/input";
import { copyText } from "@multica/ui/lib/clipboard";
import { useT } from "../../../i18n";
import { buildAgentMCPLink } from "../tabs/a2a-export";

const LOCAL_MCP_CLIENT_NAME = "Local Coding Agent";
const DEFAULT_SCOPES = ["send", "read"] as const;
const DEFAULT_EXPIRY_DAYS = 90;
const EMPTY_CLIENTS: AgentA2AClient[] = [];

interface GeneratedLink {
  clientId: string;
  credentialId: string;
  url: string;
}

function credentialExpiry(): string {
  return new Date(
    Date.now() + DEFAULT_EXPIRY_DAYS * 24 * 60 * 60 * 1000,
  ).toISOString();
}

function findLocalMCPClient(clients: AgentA2AClient[]): AgentA2AClient | null {
  return (
    clients.find(
      (client) =>
        client.status === "active" &&
        client.name.toLowerCase() === LOCAL_MCP_CLIENT_NAME.toLowerCase(),
    ) ?? null
  );
}

export function AgentMCPLinkCard({ agent }: { agent: Agent }) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const configQuery = useQuery({
    ...agentA2AConfigOptions(wsId, agent.id),
    enabled: !!wsId && !!agent.id,
  });
  const createClient = useCreateAgentA2AClient(wsId, agent.id);
  const createCredential = useCreateAgentA2ACredential(wsId, agent.id);
  const deleteCredential = useDeleteAgentA2ACredential(wsId, agent.id);

  const endpoint = configQuery.data?.endpoint ?? null;
  const clients = configQuery.data?.clients ?? EMPTY_CLIENTS;
  const localClient = useMemo(() => findLocalMCPClient(clients), [clients]);
  const activeCredentials = useMemo(
    () =>
      localClient?.credentials.filter(
        (credential) => credential.status === "active",
      ) ?? [],
    [localClient],
  );
  const [generatedLink, setGeneratedLink] = useState<GeneratedLink | null>(null);
  const [copied, setCopied] = useState(false);
  const [generating, setGenerating] = useState(false);
  const [revokeOpen, setRevokeOpen] = useState(false);

  const linkExists = generatedLink !== null || activeCredentials.length > 0;
  const canGenerate =
    endpoint !== null &&
    endpoint.mcpUrl?.trim() !== "";
  const busy =
    generating ||
    createClient.isPending ||
    createCredential.isPending ||
    deleteCredential.isPending;

  const revokeCredentialIds = async (
    client: AgentA2AClient,
    includeGenerated: boolean,
  ) => {
    const credentialIds = new Set(
      client.credentials
        .filter((credential) => credential.status === "active")
        .map((credential) => credential.id),
    );
    if (includeGenerated && generatedLink?.clientId === client.id) {
      credentialIds.add(generatedLink.credentialId);
    }
    for (const credentialId of credentialIds) {
      await deleteCredential.mutateAsync({
        clientId: client.id,
        credentialId,
      });
    }
  };

  const handleGenerate = async () => {
    if (!endpoint || !canGenerate || !endpoint.mcpUrl?.trim()) return;
    setGenerating(true);
    setCopied(false);
    try {
      let client = localClient;
      if (client) {
        await revokeCredentialIds(client, true);
      } else {
        client = await createClient.mutateAsync({
          name: LOCAL_MCP_CLIENT_NAME,
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
      if (!token) throw new Error(t(($) => $.tab_body.integrations.mcp_link_generate_failed));

      const link = buildAgentMCPLink({
        mcpUrl: endpoint.mcpUrl,
        token,
      });
      setGeneratedLink({
        clientId: client.id,
        credentialId: credential.id,
        url: link.connectUrl,
      });
      toast.success(t(($) => $.tab_body.integrations.mcp_link_generated));
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.tab_body.integrations.mcp_link_generate_failed),
      );
    } finally {
      setGenerating(false);
    }
  };

  const handleCopy = async () => {
    if (!generatedLink) return;
    if (await copyText(generatedLink.url)) {
      setCopied(true);
      toast.success(t(($) => $.tab_body.a2a.copied));
      setTimeout(() => setCopied(false), 2000);
    } else {
      toast.error(t(($) => $.tab_body.a2a.copy_failed));
    }
  };

  const handleRevoke = async () => {
    const client =
      localClient ??
      clients.find((candidate) => candidate.id === generatedLink?.clientId) ??
      null;
    if (!client) return;
    try {
      await revokeCredentialIds(client, true);
      setGeneratedLink(null);
      setCopied(false);
      setRevokeOpen(false);
      toast.success(t(($) => $.tab_body.integrations.mcp_link_revoked));
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.tab_body.integrations.mcp_link_revoke_failed),
      );
    }
  };

  return (
    <section className="overflow-hidden rounded-lg border">
      <div className="flex items-start gap-3 p-4">
        <span className="flex size-9 shrink-0 items-center justify-center rounded-md border bg-muted/40 text-muted-foreground">
          <Link2 className="size-4" />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="text-sm font-medium">MCP</h3>
            {!configQuery.isLoading && !configQuery.isError && (
              <Badge variant={linkExists ? "default" : "secondary"}>
                {linkExists
                  ? t(($) => $.tab_body.integrations.mcp_link_active)
                  : t(($) => $.tab_body.integrations.mcp_link_not_created)}
              </Badge>
            )}
          </div>
          <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
            {t(($) => $.tab_body.integrations.mcp_link_intro)}
          </p>
        </div>
      </div>

      <div className="space-y-4 border-t px-4 py-4">
        {configQuery.isLoading ? (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" />
            {t(($) => $.tab_body.a2a.loading)}
          </p>
        ) : configQuery.isError ? (
          <div className="flex items-center justify-between gap-3">
            <p className="text-sm text-destructive">
              {t(($) => $.tab_body.a2a.load_failed)}
            </p>
            <Button variant="outline" size="sm" onClick={() => configQuery.refetch()}>
              {t(($) => $.tab_body.a2a.retry)}
            </Button>
          </div>
        ) : (
          <>
            {generatedLink ? (
              <>
                <div className="flex min-w-0 gap-2">
                  <Input
                    readOnly
                    value={generatedLink.url}
                    className="min-w-0 bg-muted/30 font-mono text-xs"
                    aria-label="MCP URL"
                  />
                  <Button onClick={handleCopy} className="shrink-0">
                    {copied ? <Check className="size-4" /> : <Copy className="size-4" />}
                    {copied
                      ? t(($) => $.tab_body.a2a.copied)
                      : t(($) => $.tab_body.integrations.mcp_copy_link)}
                  </Button>
                </div>
                <p className="text-xs leading-5 text-muted-foreground">
                  {t(($) => $.tab_body.integrations.mcp_link_once_warning)}
                </p>
              </>
            ) : linkExists ? (
              <div className="rounded-md bg-muted/30 px-3 py-2.5">
                <p className="text-xs font-medium">
                  {t(($) => $.tab_body.integrations.mcp_existing_link_title)}
                </p>
                <p className="mt-1 text-xs leading-5 text-muted-foreground">
                  {t(($) => $.tab_body.integrations.mcp_existing_link_description, {
                    prefix: activeCredentials[0]?.tokenPrefix ?? "mca2a_••••",
                  })}
                </p>
              </div>
            ) : (
              <p className="text-xs text-muted-foreground">
                {canGenerate
                  ? t(($) => $.tab_body.integrations.mcp_no_link_description)
                  : t(($) => $.tab_body.integrations.mcp_link_unavailable)}
              </p>
            )}

            <div className="flex flex-wrap gap-2">
              <Button size="sm" onClick={handleGenerate} disabled={!canGenerate || busy}>
                {generating ? (
                  <Loader2 className="size-4 animate-spin" />
                ) : linkExists ? (
                  <RotateCw className="size-4" />
                ) : (
                  <KeyRound className="size-4" />
                )}
                {linkExists
                  ? t(($) => $.tab_body.integrations.mcp_regenerate_link)
                  : t(($) => $.tab_body.integrations.mcp_generate_link)}
              </Button>
              {linkExists && (
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => setRevokeOpen(true)}
                  disabled={busy}
                >
                  <Trash2 className="size-4 text-destructive" />
                  {t(($) => $.tab_body.integrations.mcp_revoke_link)}
                </Button>
              )}
            </div>
            <p className="text-xs leading-5 text-muted-foreground">
              {t(($) => $.tab_body.integrations.mcp_link_security_hint)}
            </p>
          </>
        )}
      </div>

      <AlertDialog open={revokeOpen} onOpenChange={setRevokeOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.tab_body.integrations.mcp_revoke_link_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.tab_body.integrations.mcp_revoke_link_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t(($) => $.tab_body.a2a.cancel)}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={handleRevoke}>
              {t(($) => $.tab_body.integrations.mcp_revoke_link)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  );
}
