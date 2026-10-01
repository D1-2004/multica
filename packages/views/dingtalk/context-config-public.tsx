"use client";

import type { ContextConfigAgentDetail } from "@multica/core/context-capabilities";
import { ConnectorLogo } from "../common/connector-logo";
import { SkillIcon } from "../skills/lib/skill-icon";
import { useT } from "../i18n";
import { ItemGroup } from "./context-config-ui";

interface PublicItem {
  key: string;
  name: string;
  description: string;
  /** Connector catalog slug ("" for a custom connector); null for a skill. */
  slug: string | null;
}

/**
 * 公开能力: read-only lists of what the agent makes available. 通用能力 are
 * granted to the agent and on everywhere; 企业公开能力 are offered so a
 * group, a person or the enterprise can switch them on.
 */
export function PublicCapabilities({ detail }: { detail: ContextConfigAgentDetail }) {
  const { t } = useT("agents");
  const globalIds = new Set([
    ...detail.global.connectors.map((connector) => connector.id),
    ...detail.global.skills.map((skill) => skill.id),
  ]);
  const common: PublicItem[] = [
    ...detail.global.connectors.map((connector) => ({
      key: `c:${connector.id}`,
      name: connector.name,
      description: "",
      slug: connector.catalogSlug,
    })),
    ...detail.global.skills.map((skill) => ({
      key: `s:${skill.id}`,
      name: skill.name,
      description: skill.description,
      slug: null,
    })),
  ];
  const offered: PublicItem[] = [
    ...detail.offers.connectors
      .filter((connector) => !globalIds.has(connector.id))
      .map((connector) => ({
        key: `c:${connector.id}`,
        name: connector.name,
        description: "",
        slug: connector.catalogSlug,
      })),
    ...detail.offers.skills
      .filter((skill) => !globalIds.has(skill.id))
      .map((skill) => ({ key: `s:${skill.id}`, name: skill.name, description: skill.description, slug: null })),
  ];
  return (
    <div className="space-y-5">
      <PublicList label={t(($) => $.context_config.public_common_title)} items={common} />
      <PublicList label={t(($) => $.context_config.public_org_title)} items={offered} />
    </div>
  );
}

function PublicList({ label, items }: { label: string; items: PublicItem[] }) {
  const { t } = useT("agents");
  return (
    <section aria-label={label}>
      <ItemGroup label={label} empty={t(($) => $.context_config.none)}>
        {items.map((item) => (
          <li key={item.key} className="flex items-center gap-3 p-3">
            {item.slug === null ? (
              <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground">
                <SkillIcon className="size-4" />
              </span>
            ) : (
              <ConnectorLogo slug={item.slug} />
            )}
            <div className="min-w-0 flex-1">
              <p className="truncate text-body font-medium">{item.name}</p>
              {item.description ? (
                <p className="line-clamp-2 text-caption text-muted-foreground">{item.description}</p>
              ) : null}
            </div>
          </li>
        ))}
      </ItemGroup>
    </section>
  );
}
