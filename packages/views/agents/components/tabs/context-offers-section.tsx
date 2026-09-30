"use client";

import { useEffect, useMemo, useState } from "react";
import { Check, Copy } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  agentContextCapabilitiesOptions,
  useSetAgentOffer,
  type AgentContextCapabilities,
  type ContextResourceType,
  type ContextSkillItem,
} from "@multica/core/context-capabilities";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Switch } from "@multica/ui/components/ui/switch";
import { copyText } from "@multica/ui/lib/clipboard";
import { SkillIcon } from "../../../skills/lib/skill-icon";
import { useT } from "../../../i18n";
import { ConfirmDialog, ConnectorNotice, errorMessage } from "./connectors-ui";

/** How many scenes and people turned one offered resource on. */
export interface OfferUsage {
  scenes: number;
  people: number;
}

export const NO_USAGE: OfferUsage = { scenes: 0, people: 0 };

export function usageKey(resourceType: ContextResourceType, resourceId: string): string {
  return `${resourceType}:${resourceId}`;
}

/** Counts, per offered resource (`<resource_type>:<id>`), the scenes and
 * people with an enabled binding. Only literal `true` bindings count. */
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

/**
 * 「允许在场域 / 个人中开启」 for skills: the skill part of the agent's offer
 * catalog. Group chats, 1:1 chats and people may turn offered skills on for
 * themselves on the configuration page. Each switch saves at once; taking
 * away a skill that is in use asks first, because it turns off everywhere.
 * The catalog is saved whole, so the connector offers (switched per
 * connector in the 连接器 tab) are sent unchanged.
 */
export function ContextOffersSection({ agentId, wsId }: { agentId: string; wsId: string }) {
  const { t } = useT("agents");
  const query = useQuery(agentContextCapabilitiesOptions(wsId, agentId));
  const data = query.data ?? null;

  return (
    <section className="space-y-3" aria-labelledby="context-offers-skill">
      <div>
        <h3 id="context-offers-skill" className="text-body font-medium">
          {t(($) => $.tab_body.context_offers.title)}
        </h3>
        <p className="mt-1 max-w-2xl text-caption leading-5 text-muted-foreground">
          {t(($) => $.tab_body.context_offers.skills_hint)}
        </p>
      </div>
      {query.isLoading ? (
        <ConnectorNotice loading>{t(($) => $.tab_body.context_offers.loading)}</ConnectorNotice>
      ) : query.isError || !data ? (
        <ConnectorNotice>
          <span className="flex-1">{t(($) => $.tab_body.context_offers.load_failed)}</span>
          <Button variant="outline" size="sm" onClick={() => void query.refetch()}>
            {t(($) => $.tab_body.context_offers.retry)}
          </Button>
        </ConnectorNotice>
      ) : data.enabled !== true ? null : (
        <OfferList agentId={agentId} wsId={wsId} data={data} />
      )}
    </section>
  );
}

function OfferList({
  agentId,
  wsId,
  data,
}: {
  agentId: string;
  wsId: string;
  data: AgentContextCapabilities;
}) {
  const { t } = useT("agents");
  const setOffer = useSetAgentOffer(wsId, agentId);
  const [confirmRemove, setConfirmRemove] = useState<{ skill: ContextSkillItem; usage: OfferUsage } | null>(
    null,
  );
  const usage = useMemo(() => offerUsageByResource(data), [data]);
  const offered = useMemo(() => new Set(data.offers.skillIds), [data.offers.skillIds]);

  const save = async (id: string, next: boolean) => {
    try {
      await setOffer.mutateAsync({ resourceType: "skill", resourceId: id, offered: next });
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.context_offers.save_failed)));
    }
  };

  const toggle = (skill: ContextSkillItem, next: boolean) => {
    if (!next) {
      const used = usage.get(usageKey("skill", skill.id)) ?? NO_USAGE;
      if (used.scenes + used.people > 0) {
        setConfirmRemove({ skill, usage: used });
        return;
      }
    }
    void save(skill.id, next);
  };

  return (
    <>
      {data.library.skills.length === 0 ? (
        <ConnectorNotice>{t(($) => $.tab_body.context_offers.skills_empty)}</ConnectorNotice>
      ) : (
        <ul className="max-h-96 divide-y overflow-y-auto rounded-lg border bg-surface-raised/40">
          {data.library.skills.map((skill) => {
            const isOffered = offered.has(skill.id);
            const used = usage.get(usageKey("skill", skill.id)) ?? NO_USAGE;
            return (
              <li key={skill.id} className="flex items-center gap-3 p-3">
                <span className="flex size-9 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground">
                  <SkillIcon className="size-4" />
                </span>
                <div className="min-w-0 flex-1">
                  <span className="block truncate text-body font-medium">{skill.name}</span>
                  {skill.description ? (
                    <p className="truncate text-caption text-muted-foreground">{skill.description}</p>
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
                  disabled={setOffer.isPending}
                  onCheckedChange={(next) => toggle(skill, next)}
                  aria-label={t(($) => $.tab_body.context_offers.toggle_aria, { name: skill.name })}
                />
              </li>
            );
          })}
        </ul>
      )}
      <ConfirmDialog
        open={confirmRemove !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmRemove(null);
        }}
        title={t(($) => $.tab_body.context_offers.remove_title, { name: confirmRemove?.skill.name ?? "" })}
        description={t(($) => $.tab_body.context_offers.remove_description, {
          scenes: confirmRemove?.usage.scenes ?? 0,
          people: confirmRemove?.usage.people ?? 0,
        })}
        confirmLabel={t(($) => $.tab_body.context_offers.remove_confirm)}
        onConfirm={() => {
          const target = confirmRemove;
          setConfirmRemove(null);
          if (target) void save(target.skill.id, false);
        }}
      />
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
