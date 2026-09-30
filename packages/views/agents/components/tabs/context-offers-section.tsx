"use client";

import { useEffect, useMemo, useState } from "react";
import { Check, Copy, Loader2 } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  agentContextCapabilitiesOptions,
  useSetAgentContextCapabilityOffers,
  type AgentContextCapabilities,
  type ContextResourceType,
} from "@multica/core/context-capabilities";
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
import { Switch } from "@multica/ui/components/ui/switch";
import { copyText } from "@multica/ui/lib/clipboard";
import { SkillIcon } from "../../../skills/lib/skill-icon";
import { ConnectorLogo } from "../../../common/connector-logo";
import { useT } from "../../../i18n";

/** How many scenes and people turned one offered resource on. */
export interface OfferUsage {
  scenes: number;
  people: number;
}

const NO_USAGE: OfferUsage = { scenes: 0, people: 0 };

function usageKey(resourceType: ContextResourceType, resourceId: string): string {
  return `${resourceType}:${resourceId}`;
}

/** Counts, per offered resource, the scenes and people with an enabled
 * binding. Only literal `true` bindings count. */
export function offerUsageByResource(
  data: AgentContextCapabilities,
): ReadonlyMap<string, OfferUsage> {
  const usage = new Map<string, OfferUsage>();
  const add = (key: string, field: keyof OfferUsage) => {
    const current = usage.get(key) ?? { ...NO_USAGE };
    current[field] += 1;
    usage.set(key, current);
  };
  for (const scene of data.scenes) {
    for (const binding of scene.bindings) {
      if (binding.enabled === true) add(usageKey(binding.resourceType, binding.resourceId), "scenes");
    }
  }
  for (const person of data.persons) {
    for (const binding of person.bindings) {
      if (binding.enabled === true) add(usageKey(binding.resourceType, binding.resourceId), "people");
    }
  }
  return usage;
}

interface OfferRow {
  id: string;
  name: string;
  description: string;
  badges: string[];
  catalogSlug: string;
}

/**
 * 「允许在场域 / 个人中开启」: the connector or skill part of the agent's offer
 * catalog. Group chats, 1:1 chats and people may turn offered items on for
 * themselves on the configuration page. Each switch saves at once; taking
 * away an item that is in use asks first, because it turns off everywhere.
 * The catalog is saved whole, so the other resource type is sent unchanged.
 */
export function ContextOffersSection({
  agentId,
  wsId,
  resourceType,
  isWorkspaceAdmin = false,
  showConfigureLink = false,
}: {
  agentId: string;
  wsId: string;
  resourceType: ContextResourceType;
  /** Only workspace admins may offer more connectors; the server lists only
   * the already offered ones to anyone else. Irrelevant for skills. */
  isWorkspaceAdmin?: boolean;
  showConfigureLink?: boolean;
}) {
  const { t } = useT("agents");
  const query = useQuery(agentContextCapabilitiesOptions(wsId, agentId));
  const data = query.data ?? null;

  return (
    <section className="space-y-3" aria-labelledby={`context-offers-${resourceType}`}>
      <div>
        <h3 id={`context-offers-${resourceType}`} className="text-body font-medium">
          {t(($) => $.tab_body.context_offers.title)}
        </h3>
        <p className="mt-1 max-w-2xl text-caption leading-5 text-muted-foreground">
          {resourceType === "connector"
            ? t(($) => $.tab_body.context_offers.connectors_hint)
            : t(($) => $.tab_body.context_offers.skills_hint)}
        </p>
      </div>
      {query.isLoading ? (
        <Notice>
          <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />
          {t(($) => $.tab_body.context_offers.loading)}
        </Notice>
      ) : query.isError || !data ? (
        <Notice>
          <span className="flex-1">{t(($) => $.tab_body.context_offers.load_failed)}</span>
          <Button variant="outline" size="sm" onClick={() => void query.refetch()}>
            {t(($) => $.tab_body.context_offers.retry)}
          </Button>
        </Notice>
      ) : data.enabled !== true ? null : (
        <OfferList
          agentId={agentId}
          wsId={wsId}
          data={data}
          resourceType={resourceType}
          isWorkspaceAdmin={isWorkspaceAdmin}
        />
      )}
      {showConfigureLink && data?.configureUrl ? (
        <div className="space-y-1.5 pt-1">
          <p className="text-caption font-medium text-muted-foreground">
            {t(($) => $.tab_body.context_offers.configure_title)}
          </p>
          <p className="text-caption text-muted-foreground">
            {t(($) => $.tab_body.context_offers.configure_hint)}
          </p>
          <ConfigureLink url={data.configureUrl} />
        </div>
      ) : null}
    </section>
  );
}

function OfferList({
  agentId,
  wsId,
  data,
  resourceType,
  isWorkspaceAdmin,
}: {
  agentId: string;
  wsId: string;
  data: AgentContextCapabilities;
  resourceType: ContextResourceType;
  isWorkspaceAdmin: boolean;
}) {
  const { t } = useT("agents");
  const saveOffers = useSetAgentContextCapabilityOffers(wsId, agentId);
  const [confirmRemove, setConfirmRemove] = useState<{ row: OfferRow; usage: OfferUsage } | null>(null);
  const usage = useMemo(() => offerUsageByResource(data), [data]);
  const offered = useMemo(
    () => new Set(resourceType === "connector" ? data.offers.connectorIds : data.offers.skillIds),
    [data.offers.connectorIds, data.offers.skillIds, resourceType],
  );

  const rows: OfferRow[] =
    resourceType === "connector"
      ? data.library.connectors.map((connector) => ({
          id: connector.id,
          name: connector.name,
          description: "",
          catalogSlug: connector.catalogSlug,
          badges: [
            ...(connector.enabled !== true
              ? [t(($) => $.tab_body.context_offers.connector_disabled)]
              : []),
            ...(connector.authMode === "none"
              ? [t(($) => $.tab_body.context_offers.auth_none)]
              : connector.authMode === "bearer"
                ? [t(($) => $.tab_body.context_offers.auth_bearer)]
                : connector.authMode === "oauth"
                  ? [t(($) => $.tab_body.context_offers.auth_oauth)]
                  : []),
          ],
        }))
      : data.library.skills.map((skill) => ({
          id: skill.id,
          name: skill.name,
          description: skill.description,
          badges: [],
          catalogSlug: "",
        }));

  const save = async (id: string, next: boolean) => {
    const current = new Set(offered);
    if (next) current.add(id);
    else current.delete(id);
    try {
      await saveOffers.mutateAsync(
        resourceType === "connector"
          ? { connectorIds: [...current], skillIds: data.offers.skillIds }
          : { connectorIds: data.offers.connectorIds, skillIds: [...current] },
      );
    } catch (error) {
      toast.error(
        error instanceof Error && error.message
          ? error.message
          : t(($) => $.tab_body.context_offers.save_failed),
      );
    }
  };

  const toggle = (row: OfferRow, next: boolean) => {
    if (!next) {
      const used = usage.get(usageKey(resourceType, row.id)) ?? NO_USAGE;
      if (used.scenes + used.people > 0) {
        setConfirmRemove({ row, usage: used });
        return;
      }
    }
    void save(row.id, next);
  };

  return (
    <>
      {resourceType === "connector" && !isWorkspaceAdmin ? (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.tab_body.context_offers.admin_only_connectors)}
        </p>
      ) : null}
      {rows.length === 0 ? (
        <Notice>
          {resourceType === "connector"
            ? t(($) => $.tab_body.context_offers.connectors_empty)
            : t(($) => $.tab_body.context_offers.skills_empty)}
        </Notice>
      ) : (
        <ul className="max-h-96 divide-y overflow-y-auto rounded-lg border bg-surface-raised/40">
          {rows.map((row) => {
            const isOffered = offered.has(row.id);
            const used = usage.get(usageKey(resourceType, row.id)) ?? NO_USAGE;
            return (
              <li key={row.id} className="flex items-center gap-3 p-3">
                {resourceType === "connector" ? (
                  <ConnectorLogo slug={row.catalogSlug} className="size-9 rounded-md" />
                ) : (
                  <span className="flex size-9 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground">
                    <SkillIcon className="size-4" />
                  </span>
                )}
                <div className="min-w-0 flex-1">
                  <div className="flex min-w-0 flex-wrap items-center gap-1.5">
                    <span className="truncate text-body font-medium">{row.name}</span>
                    {row.badges.map((badge) => (
                      <Badge key={badge} variant="secondary" className="text-micro">
                        {badge}
                      </Badge>
                    ))}
                  </div>
                  {row.description ? (
                    <p className="truncate text-caption text-muted-foreground">{row.description}</p>
                  ) : null}
                  {isOffered ? (
                    <p className="text-caption text-muted-foreground">
                      {t(($) => $.tab_body.context_offers.usage, {
                        scenes: used.scenes,
                        people: used.people,
                      })}
                    </p>
                  ) : null}
                </div>
                <Switch
                  checked={isOffered}
                  disabled={saveOffers.isPending}
                  onCheckedChange={(next) => toggle(row, next)}
                  aria-label={t(($) => $.tab_body.context_offers.toggle_aria, { name: row.name })}
                />
              </li>
            );
          })}
        </ul>
      )}
      <AlertDialog
        open={confirmRemove !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmRemove(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.tab_body.context_offers.remove_title, {
                name: confirmRemove?.row.name ?? "",
              })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.tab_body.context_offers.remove_description, {
                scenes: confirmRemove?.usage.scenes ?? 0,
                people: confirmRemove?.usage.people ?? 0,
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t(($) => $.tab_body.context_offers.cancel)}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                const target = confirmRemove;
                setConfirmRemove(null);
                if (target) void save(target.row.id, false);
              }}
            >
              {t(($) => $.tab_body.context_offers.remove_confirm)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

/** Read-only configuration page URL with a copy button. */
export function ConfigureLink({ url }: { url: string }) {
  const { t } = useT("agents");
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(false), 2000);
    return () => window.clearTimeout(timer);
  }, [copied]);
  const copy = async () => {
    if (await copyText(url)) {
      setCopied(true);
      toast.success(t(($) => $.tab_body.context_offers.copied));
    } else {
      toast.error(t(($) => $.tab_body.context_offers.copy_failed));
    }
  };
  return (
    <div className="flex items-center gap-2">
      <Input
        readOnly
        value={url}
        aria-label={t(($) => $.tab_body.context_offers.configure_title)}
        className="min-w-0 flex-1 font-mono text-caption"
        onFocus={(event) => event.currentTarget.select()}
      />
      <Button
        variant="outline"
        size="icon"
        onClick={() => void copy()}
        aria-label={t(($) => $.tab_body.context_offers.copy)}
      >
        {copied ? <Check className="size-4" /> : <Copy className="size-4" />}
      </Button>
    </div>
  );
}

function Notice({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-2 rounded-lg border border-dashed px-4 py-6 text-caption text-muted-foreground">
      {children}
    </div>
  );
}
