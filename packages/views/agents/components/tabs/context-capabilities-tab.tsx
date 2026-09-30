"use client";

import { useEffect, useMemo, useState } from "react";
import { Check, Copy, Loader2, Plug, RotateCcw, Users, User } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Agent } from "@multica/core/types";
import {
  agentContextCapabilitiesOptions,
  useSetAgentContextCapabilityOffers,
  type AgentContextCapabilities,
  type ContextScopeSummary,
} from "@multica/core/context-capabilities";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Input } from "@multica/ui/components/ui/input";
import { copyText } from "@multica/ui/lib/clipboard";
import { cn } from "@multica/ui/lib/utils";
import { SkillIcon } from "../../../skills/lib/skill-icon";
import { useT } from "../../../i18n";

interface OfferDraft {
  connectorIds: ReadonlySet<string>;
  skillIds: ReadonlySet<string>;
}

interface ContextCapabilitiesTabProps {
  agent: Agent;
  wsId: string;
  canEdit: boolean;
  onDirtyChange?: (dirty: boolean) => void;
}

/**
 * Admin side of context capabilities: the agent's offer catalog (which
 * library connectors and skills DingTalk groups and people may turn on for
 * themselves), read-only summaries of what they turned on, and the mobile
 * configuration link. Global grants are managed on the Skills / MCP tabs and
 * in the connector library; this tab never changes them.
 */
export function ContextCapabilitiesTab({
  agent,
  wsId,
  canEdit,
  onDirtyChange,
}: ContextCapabilitiesTabProps) {
  const { t } = useT("agents");
  const query = useQuery(agentContextCapabilitiesOptions(wsId, agent.id));
  const data = query.data ?? null;

  if (query.isLoading) {
    return (
      <Notice>
        <Loader2 className="size-4 animate-spin motion-reduce:animate-none" />
        {t(($) => $.tab_body.context_capabilities.loading)}
      </Notice>
    );
  }
  if (query.isError || !data) {
    return (
      <Notice>
        <span className="flex-1">{t(($) => $.tab_body.context_capabilities.load_failed)}</span>
        <Button variant="outline" size="sm" onClick={() => void query.refetch()}>
          {t(($) => $.tab_body.context_capabilities.retry)}
        </Button>
      </Notice>
    );
  }
  if (data.enabled !== true) {
    return <Notice>{t(($) => $.tab_body.context_capabilities.disabled)}</Notice>;
  }
  return (
    <ContextCapabilitiesEditor
      key={agent.id}
      agentId={agent.id}
      wsId={wsId}
      data={data}
      canEdit={canEdit}
      onDirtyChange={onDirtyChange}
    />
  );
}

function ContextCapabilitiesEditor({
  agentId,
  wsId,
  data,
  canEdit,
  onDirtyChange,
}: {
  agentId: string;
  wsId: string;
  data: AgentContextCapabilities;
  canEdit: boolean;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { t } = useT("agents");
  const saveOffers = useSetAgentContextCapabilityOffers(wsId, agentId);
  const [draft, setDraft] = useState<OfferDraft | null>(null);

  const serverConnectorIds = useMemo(
    () => new Set(data.offers.connectorIds),
    [data.offers.connectorIds],
  );
  const serverSkillIds = useMemo(
    () => new Set(data.offers.skillIds),
    [data.offers.skillIds],
  );
  const selectedConnectorIds = draft?.connectorIds ?? serverConnectorIds;
  const selectedSkillIds = draft?.skillIds ?? serverSkillIds;
  const dirty =
    draft !== null &&
    (!sameSet(draft.connectorIds, serverConnectorIds) ||
      !sameSet(draft.skillIds, serverSkillIds));
  const removesOffers =
    dirty &&
    ([...serverConnectorIds].some((id) => !selectedConnectorIds.has(id)) ||
      [...serverSkillIds].some((id) => !selectedSkillIds.has(id)));

  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);
  useEffect(() => () => onDirtyChange?.(false), [onDirtyChange]);

  const names = useMemo(() => {
    const map = new Map<string, string>();
    for (const connector of data.library.connectors) map.set(connector.id, connector.name);
    for (const skill of data.library.skills) map.set(skill.id, skill.name);
    return map;
  }, [data.library.connectors, data.library.skills]);

  const toggle = (kind: "connector" | "skill", id: string) => {
    if (!canEdit) return;
    const current = {
      connectorIds: new Set(selectedConnectorIds),
      skillIds: new Set(selectedSkillIds),
    };
    const target = kind === "connector" ? current.connectorIds : current.skillIds;
    if (target.has(id)) target.delete(id);
    else target.add(id);
    setDraft(current);
  };

  const save = async () => {
    if (!dirty) return;
    try {
      await saveOffers.mutateAsync({
        connectorIds: [...selectedConnectorIds],
        skillIds: [...selectedSkillIds],
      });
      setDraft(null);
      toast.success(t(($) => $.tab_body.context_capabilities.save_success));
    } catch (error) {
      toast.error(
        error instanceof Error && error.message
          ? error.message
          : t(($) => $.tab_body.context_capabilities.save_failed),
      );
    }
  };

  return (
    <div className="space-y-8">
      <p className="text-body leading-6 text-muted-foreground">
        {t(($) => $.tab_body.context_capabilities.intro)}
      </p>

      <Section
        title={t(($) => $.tab_body.context_capabilities.offers_title)}
        description={t(($) => $.tab_body.context_capabilities.offers_hint)}
      >
        <div className="space-y-4">
          <PickerGroup
            label={t(($) => $.tab_body.context_capabilities.connectors_label)}
            empty={t(($) => $.tab_body.context_capabilities.connectors_empty)}
          >
            {data.library.connectors.map((connector) => (
              <PickerRow
                key={connector.id}
                icon={<Plug className="size-3.5" />}
                name={connector.name}
                selected={selectedConnectorIds.has(connector.id)}
                disabled={!canEdit || saveOffers.isPending}
                onToggle={() => toggle("connector", connector.id)}
                badges={
                  <>
                    {connector.enabled !== true && (
                      <Badge variant="outline" className="text-micro">
                        {t(($) => $.tab_body.context_capabilities.connector_disabled)}
                      </Badge>
                    )}
                    {connector.authMode === "none" ? (
                      <Badge variant="secondary" className="text-micro">
                        {t(($) => $.tab_body.context_capabilities.auth_none)}
                      </Badge>
                    ) : connector.authMode === "bearer" ? (
                      <Badge variant="secondary" className="text-micro">
                        {t(($) => $.tab_body.context_capabilities.auth_bearer)}
                      </Badge>
                    ) : connector.authMode === "oauth" ? (
                      <Badge variant="secondary" className="text-micro">
                        {t(($) => $.tab_body.context_capabilities.auth_oauth)}
                      </Badge>
                    ) : null}
                  </>
                }
              />
            ))}
          </PickerGroup>
          <PickerGroup
            label={t(($) => $.tab_body.context_capabilities.skills_label)}
            empty={t(($) => $.tab_body.context_capabilities.skills_empty)}
          >
            {data.library.skills.map((skill) => (
              <PickerRow
                key={skill.id}
                icon={<SkillIcon className="size-3.5" />}
                name={skill.name}
                description={skill.description}
                selected={selectedSkillIds.has(skill.id)}
                disabled={!canEdit || saveOffers.isPending}
                onToggle={() => toggle("skill", skill.id)}
              />
            ))}
          </PickerGroup>
          {removesOffers && (
            <p className="rounded-md border border-dashed px-3 py-2 text-caption text-muted-foreground">
              {t(($) => $.tab_body.context_capabilities.removal_warning)}
            </p>
          )}
          {canEdit && (
            <div className="flex flex-wrap items-center justify-end gap-2">
              <Button
                variant="ghost"
                size="sm"
                disabled={!dirty || saveOffers.isPending}
                onClick={() => setDraft(null)}
              >
                <RotateCcw className="size-3.5" />
                {t(($) => $.tab_body.context_capabilities.reset)}
              </Button>
              <Button size="sm" disabled={!dirty || saveOffers.isPending} onClick={() => void save()}>
                {saveOffers.isPending && (
                  <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />
                )}
                {saveOffers.isPending
                  ? t(($) => $.tab_body.context_capabilities.saving)
                  : t(($) => $.tab_body.context_capabilities.save)}
              </Button>
            </div>
          )}
        </div>
      </Section>

      {data.configureUrl && (
        <Section
          title={t(($) => $.tab_body.context_capabilities.configure_title)}
          description={t(($) => $.tab_body.context_capabilities.configure_hint)}
        >
          <ConfigureLink url={data.configureUrl} />
        </Section>
      )}

      <Section
        title={t(($) => $.tab_body.context_capabilities.scenes_title)}
        description={t(($) => $.tab_body.context_capabilities.scenes_hint)}
      >
        <ScopeSummaryList
          scopes={data.scenes}
          names={names}
          icon={<Users className="size-4" />}
          untitled={t(($) => $.tab_body.context_capabilities.scene_untitled)}
          empty={t(($) => $.tab_body.context_capabilities.scenes_empty)}
        />
      </Section>

      <Section
        title={t(($) => $.tab_body.context_capabilities.persons_title)}
        description={t(($) => $.tab_body.context_capabilities.persons_hint)}
      >
        <ScopeSummaryList
          scopes={data.persons}
          names={names}
          icon={<User className="size-4" />}
          untitled={t(($) => $.tab_body.context_capabilities.person_untitled)}
          empty={t(($) => $.tab_body.context_capabilities.persons_empty)}
        />
      </Section>
    </div>
  );
}

function ConfigureLink({ url }: { url: string }) {
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
      toast.success(t(($) => $.tab_body.context_capabilities.copied));
    } else {
      toast.error(t(($) => $.tab_body.context_capabilities.copy_failed));
    }
  };
  return (
    <div className="flex items-center gap-2">
      <Input
        readOnly
        value={url}
        aria-label={t(($) => $.tab_body.context_capabilities.configure_title)}
        className="min-w-0 flex-1 font-mono text-caption"
        onFocus={(event) => event.currentTarget.select()}
      />
      <Button
        variant="outline"
        size="icon"
        onClick={() => void copy()}
        aria-label={t(($) => $.tab_body.context_capabilities.copy)}
      >
        {copied ? <Check className="size-4" /> : <Copy className="size-4" />}
      </Button>
    </div>
  );
}

function ScopeSummaryList({
  scopes,
  names,
  icon,
  untitled,
  empty,
}: {
  scopes: ContextScopeSummary[];
  names: ReadonlyMap<string, string>;
  icon: React.ReactNode;
  untitled: string;
  empty: string;
}) {
  const { t } = useT("agents");
  if (scopes.length === 0) {
    return <Notice>{empty}</Notice>;
  }
  return (
    <ul className="divide-y rounded-lg border bg-surface-raised/40">
      {scopes.map((scope) => {
        const enabled = scope.bindings.filter((binding) => binding.enabled === true);
        return (
          <li key={scope.scopeKey} className="flex items-start gap-3 p-3">
            <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground">
              {icon}
            </span>
            <div className="min-w-0 flex-1 space-y-1.5">
              <div className="flex min-w-0 items-baseline justify-between gap-3">
                <span className="truncate text-body font-medium">
                  {scope.scopeTitle || untitled}
                </span>
                {scope.credentialCount > 0 && (
                  <span className="shrink-0 text-caption text-muted-foreground">
                    {t(($) => $.tab_body.context_capabilities.credential_count, {
                      count: scope.credentialCount,
                    })}
                  </span>
                )}
              </div>
              {enabled.length === 0 ? (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.tab_body.context_capabilities.nothing_enabled)}
                </p>
              ) : (
                <div className="flex flex-wrap gap-1.5">
                  {enabled.map((binding) => (
                    <Badge
                      key={`${binding.resourceType}:${binding.resourceId}`}
                      variant="outline"
                      className="max-w-full text-micro"
                    >
                      <span className="truncate">
                        {names.get(binding.resourceId) ??
                          t(($) => $.tab_body.context_capabilities.unknown_item)}
                      </span>
                    </Badge>
                  ))}
                </div>
              )}
            </div>
          </li>
        );
      })}
    </ul>
  );
}

function PickerGroup({
  label,
  empty,
  children,
}: {
  label: string;
  empty: string;
  children: React.ReactNode[];
}) {
  return (
    <div className="space-y-1.5">
      <div className="text-caption font-medium text-muted-foreground">{label}</div>
      <div className="overflow-hidden rounded-lg border bg-card">
        {children.length === 0 ? (
          <div className="px-3 py-5 text-center text-caption text-muted-foreground">{empty}</div>
        ) : (
          <div className="max-h-72 space-y-0.5 overflow-y-auto p-1.5">{children}</div>
        )}
      </div>
    </div>
  );
}

function PickerRow({
  icon,
  name,
  description,
  badges,
  selected,
  disabled,
  onToggle,
}: {
  icon: React.ReactNode;
  name: string;
  description?: string;
  badges?: React.ReactNode;
  selected: boolean;
  disabled: boolean;
  onToggle: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onToggle}
      disabled={disabled}
      aria-pressed={selected}
      className={cn(
        "flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left transition-colors disabled:cursor-not-allowed",
        selected ? "bg-accent" : "hover:bg-accent/50",
      )}
    >
      {/* Indicator only — the row button owns the click. */}
      <Checkbox checked={selected} tabIndex={-1} className="pointer-events-none" />
      <span className="shrink-0 text-muted-foreground">{icon}</span>
      <span className="min-w-0 flex-1">
        <span className="flex min-w-0 items-center gap-1.5">
          <span className="truncate text-body font-medium">{name}</span>
          {badges}
        </span>
        {description ? (
          <span className="block truncate text-caption text-muted-foreground">{description}</span>
        ) : null}
      </span>
    </button>
  );
}

function Section({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children: React.ReactNode;
}) {
  return (
    <section className="space-y-3">
      <div>
        <h3 className="text-body font-medium">{title}</h3>
        <p className="mt-1 text-caption leading-5 text-muted-foreground">{description}</p>
      </div>
      {children}
    </section>
  );
}

function Notice({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-2 rounded-lg border border-dashed px-4 py-6 text-caption text-muted-foreground">
      {children}
    </div>
  );
}

function sameSet(a: ReadonlySet<string>, b: ReadonlySet<string>): boolean {
  if (a.size !== b.size) return false;
  for (const value of a) if (!b.has(value)) return false;
  return true;
}
