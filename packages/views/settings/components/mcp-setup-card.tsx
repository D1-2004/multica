"use client";

import { useMemo, useState } from "react";
import { Check, Copy, Loader2, Server, ShieldCheck, TriangleAlert } from "lucide-react";
import { api } from "@multica/core/api";
import { Alert, AlertDescription } from "@multica/ui/components/ui/alert";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { copyText } from "@multica/ui/lib/clipboard";
import { toast } from "sonner";
import { useT } from "../../i18n";
import { ProviderLogo } from "../../runtimes/components/provider-logo";
import { SettingsSection } from "./settings-layout";
import {
  buildMulticaMCPEndpoint,
  buildMulticaMCPImport,
  MULTICA_MCP_CLIENTS,
  type MulticaMCPClientOption,
} from "./mcp-import";

const MCP_TOKEN_EXPIRY_DAYS = 90;

interface GeneratedImport {
  content: string;
  copied: boolean;
}

export function MCPSetupCard({
  onTokenCreated,
}: {
  onTokenCreated?: () => Promise<void>;
}) {
  const { t } = useT("settings");
  const endpoint = useMemo(
    () =>
      buildMulticaMCPEndpoint(
        api.getBaseUrl(),
        typeof window === "undefined" ? "" : window.location.origin,
      ),
    [],
  );
  const [selectedClient, setSelectedClient] = useState<MulticaMCPClientOption | null>(null);
  const [creating, setCreating] = useState(false);
  const [generatedImport, setGeneratedImport] = useState<GeneratedImport | null>(null);
  const [endpointCopied, setEndpointCopied] = useState(false);

  const closeDialog = () => {
    if (creating) return;
    setSelectedClient(null);
    setGeneratedImport(null);
  };

  const handleCopyEndpoint = async () => {
    if (await copyText(endpoint)) {
      setEndpointCopied(true);
      toast.success(t(($) => $.mcp.endpoint_copied));
      setTimeout(() => setEndpointCopied(false), 2000);
    } else {
      toast.error(t(($) => $.mcp.copy_failed));
    }
  };

  const handleCreateImport = async () => {
    if (!selectedClient) return;
    setCreating(true);
    try {
      const result = await api.createPersonalAccessToken({
        name: `Multica MCP · ${selectedClient.name}`,
        expires_in_days: MCP_TOKEN_EXPIRY_DAYS,
      });
      const content = buildMulticaMCPImport(
        selectedClient.key,
        endpoint,
        result.token,
      );
      const copied = await copyText(content);
      setGeneratedImport({ content, copied });
      await onTokenCreated?.();
      if (copied) {
        toast.success(t(($) => $.mcp.config_copied));
      } else {
        toast.error(t(($) => $.mcp.copy_failed));
      }
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.mcp.create_failed),
      );
    } finally {
      setCreating(false);
    }
  };

  const handleCopyGenerated = async () => {
    if (!generatedImport) return;
    const copied = await copyText(generatedImport.content);
    setGeneratedImport({ ...generatedImport, copied });
    if (copied) {
      toast.success(t(($) => $.mcp.config_copied));
    } else {
      toast.error(t(($) => $.mcp.copy_failed));
    }
  };

  return (
    <SettingsSection
      title={t(($) => $.mcp.service_title)}
      description={t(($) => $.mcp.service_description)}
    >
      <Card className="gap-0 overflow-hidden py-0 shadow-none">
        <CardContent className="p-0">
          <div className="space-y-2.5 p-4">
            <div className="flex items-center gap-2 text-caption font-medium text-muted-foreground">
              <Server className="size-3.5" />
              {t(($) => $.mcp.endpoint_label)}
            </div>
            <div className="flex min-w-0 gap-2">
              <Input
                readOnly
                value={endpoint}
                aria-label={t(($) => $.mcp.endpoint_aria)}
                className="min-w-0 bg-muted/30 font-mono text-caption"
              />
              <Button
                type="button"
                variant="outline"
                size="icon"
                className="shrink-0"
                onClick={handleCopyEndpoint}
                aria-label={t(($) => $.mcp.copy_endpoint)}
              >
                {endpointCopied ? <Check className="size-4" /> : <Copy className="size-4" />}
              </Button>
            </div>
          </div>

          <div className="space-y-3 border-t border-surface-border p-4">
            <p className="text-caption font-medium text-muted-foreground">
              {t(($) => $.mcp.clients_label)}
            </p>
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
              {MULTICA_MCP_CLIENTS.map((client) => (
                <button
                  key={client.key}
                  type="button"
                  className="group flex min-h-20 cursor-pointer flex-col items-center justify-center gap-2 rounded-lg border border-surface-border bg-background px-3 py-3 text-center transition-colors duration-200 hover:border-primary/40 hover:bg-surface-hover focus-visible:border-ring focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
                  onClick={() => {
                    setSelectedClient(client);
                    setGeneratedImport(null);
                  }}
                  aria-label={t(($) => $.mcp.configure_aria, {
                    client: client.name,
                  })}
                >
                  <span className="flex size-9 items-center justify-center rounded-lg border border-surface-border bg-muted/40 text-foreground transition-colors group-hover:bg-background">
                    <ProviderLogo provider={client.logoProvider} className="size-5" />
                  </span>
                  <span className="text-caption font-medium text-foreground">
                    {client.name}
                  </span>
                </button>
              ))}
            </div>
          </div>

          <div className="flex gap-2.5 border-t border-surface-border bg-muted/20 px-4 py-3 text-caption leading-5 text-muted-foreground">
            <ShieldCheck className="mt-0.5 size-4 shrink-0 text-success" />
            <p>{t(($) => $.mcp.security_hint)}</p>
          </div>
        </CardContent>
      </Card>

      <Dialog
        open={selectedClient !== null}
        onOpenChange={(open) => {
          if (!open) closeDialog();
        }}
      >
        <DialogContent className="sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>
              {t(($) => $.mcp.dialog.title, {
                client: selectedClient?.name ?? "",
              })}
            </DialogTitle>
            <DialogDescription>
              {t(($) => $.mcp.dialog.description, {
                client: selectedClient?.name ?? "",
                days: MCP_TOKEN_EXPIRY_DAYS,
              })}
            </DialogDescription>
          </DialogHeader>

          {generatedImport ? (
            <>
              <Alert>
                {generatedImport.copied ? <Check /> : <TriangleAlert />}
                <AlertDescription>
                  <span className="font-medium text-foreground">
                    {generatedImport.copied
                      ? t(($) => $.mcp.config_copied)
                      : t(($) => $.mcp.config_ready)}
                  </span>
                  {" "}
                  {t(($) => $.mcp.dialog.one_time_hint)}
                </AlertDescription>
              </Alert>
              <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all rounded-lg border border-surface-border bg-muted/30 p-3 font-mono text-caption leading-5 select-all">
                {generatedImport.content}
              </pre>
              <p className="text-caption leading-5 text-muted-foreground">
                {selectedClient
                  ? t(($) => $.mcp.destinations[selectedClient.key])
                  : null}
              </p>
              <DialogFooter>
                <Button type="button" variant="outline" onClick={handleCopyGenerated}>
                  <Copy className="size-4" />
                  {t(($) => $.mcp.dialog.copy_again)}
                </Button>
                <Button type="button" onClick={closeDialog}>
                  {t(($) => $.mcp.dialog.done)}
                </Button>
              </DialogFooter>
            </>
          ) : (
            <>
              <div className="rounded-lg border border-surface-border bg-muted/20 px-3.5 py-3 text-caption leading-5 text-muted-foreground">
                {selectedClient
                  ? t(($) => $.mcp.destinations[selectedClient.key])
                  : null}
              </div>
              <DialogFooter>
                <Button type="button" variant="outline" onClick={closeDialog} disabled={creating}>
                  {t(($) => $.mcp.dialog.cancel)}
                </Button>
                <Button type="button" onClick={handleCreateImport} disabled={creating}>
                  {creating ? <Loader2 className="size-4 animate-spin" /> : <Copy className="size-4" />}
                  {creating
                    ? t(($) => $.mcp.dialog.creating)
                    : t(($) => $.mcp.dialog.create_and_copy)}
                </Button>
              </DialogFooter>
            </>
          )}
        </DialogContent>
      </Dialog>
    </SettingsSection>
  );
}
