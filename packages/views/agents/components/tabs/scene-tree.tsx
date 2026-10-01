"use client";

import { useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Building2, ChevronRight, Loader2, Plus, User, Users } from "lucide-react";
import {
  agentTenantGroupsOptions,
  agentTenantPersonsOptions,
  type AgentTenant,
  type AgentUnassignedOrg,
} from "@multica/core/context-capabilities";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useT, useTimeAgo } from "../../../i18n";

/** A selected node of the 场域 tree: a tenant (`org`, key = OrgId), one of
 * its group chats (`scene`) or one of its people (`person`). */
export interface SceneSelection {
  orgId: string;
  type: "org" | "scene" | "person";
  key: string;
}

type Category = "groups" | "persons";

function categoryKey(orgId: string, category: Category): string {
  return `${orgId}:${category}`;
}

/** Tree nodes open because of a selection: its tenant, and the category a
 * group or person sits in. */
function openedBy(selection: SceneSelection | null): string[] {
  if (!selection) return [];
  if (selection.type === "org") return [selection.orgId];
  return [selection.orgId, categoryKey(selection.orgId, selection.type === "scene" ? "groups" : "persons")];
}

const ROW =
  "flex w-full min-w-0 items-center gap-2 rounded-md px-2 py-1 text-left text-body transition-colors hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring data-active:bg-accent data-active:font-medium data-active:text-accent-foreground data-active:hover:bg-accent";

/**
 * Left tree of the 场域 tab: each tenant (name + OrgId) with its 「群聊」
 * (newest first, paged) and 「个人」 (people, their 1:1 chats included), then
 * the orgs seen without a tenant, dimmed, each with 「创建租户」. Categories
 * load when they open.
 */
export function SceneTree({
  wsId,
  agentId,
  tenants,
  unassignedOrgs,
  selection,
  onSelect,
  onCreateTenant,
}: {
  wsId: string;
  agentId: string;
  tenants: AgentTenant[];
  unassignedOrgs: AgentUnassignedOrg[];
  selection: SceneSelection | null;
  onSelect: (selection: SceneSelection) => void;
  /** Opens 新建租户, prefilled with an OrgId for an unassigned org. Absent
   * when tenants are managed elsewhere (a Tag employee's single enterprise
   * is its DingTalk identity), which hides every create entry. */
  onCreateTenant?: (orgId?: string) => void;
}) {
  const { t } = useT("agents");
  const [open, setOpen] = useState<ReadonlySet<string>>(() => {
    const initial = new Set(openedBy(selection));
    // A single tenant opens with its group chats listed.
    const only = tenants.length === 1 ? tenants[0] : undefined;
    if (only) {
      initial.add(only.orgId);
      initial.add(categoryKey(only.orgId, "groups"));
    }
    return initial;
  });

  // A selection made elsewhere (a deep link, a created tenant) opens its
  // branch; nothing ever closes on its own.
  const selectionKey = selection ? `${selection.orgId}|${selection.type}|${selection.key}` : "";
  const [openedFor, setOpenedFor] = useState(selectionKey);
  if (selectionKey !== openedFor) {
    setOpenedFor(selectionKey);
    const needed = openedBy(selection);
    if (needed.some((key) => !open.has(key))) setOpen(new Set([...open, ...needed]));
  }

  const toggle = (key: string) =>
    setOpen((current) => {
      const next = new Set(current);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });

  const isSelected = (type: SceneSelection["type"], orgId: string, key: string) =>
    selection?.type === type && selection.orgId === orgId && selection.key === key;

  return (
    <div className="space-y-0.5">
      <ul aria-label={t(($) => $.tabs.scenes)}>
        {tenants.map((tenant) => {
          const expanded = open.has(tenant.orgId);
          return (
            <li key={tenant.orgId}>
              <div className="flex min-w-0 items-center">
                <ExpandButton
                  expanded={expanded}
                  label={tenant.name || tenant.orgId}
                  onClick={() => toggle(tenant.orgId)}
                />
                <button
                  type="button"
                  data-active={isSelected("org", tenant.orgId, tenant.orgId) ? "true" : undefined}
                  aria-current={isSelected("org", tenant.orgId, tenant.orgId) ? "true" : undefined}
                  onClick={() => {
                    onSelect({ orgId: tenant.orgId, type: "org", key: tenant.orgId });
                    if (!expanded) toggle(tenant.orgId);
                  }}
                  className={ROW}
                >
                  <Building2 className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                  <span className="min-w-0 truncate">{tenant.name || tenant.orgId}</span>
                  <span className="ml-auto min-w-0 max-w-[45%] shrink truncate font-mono text-caption font-normal text-muted-foreground">
                    {tenant.orgId}
                  </span>
                </button>
              </div>
              {expanded ? (
                <ul className="ml-3 border-l pl-1.5">
                  <CategoryNode
                    label={t(($) => $.tab_body.scenes.category_groups)}
                    icon={<Users className="size-3.5 shrink-0" aria-hidden="true" />}
                    count={tenant.groupCount}
                    expanded={open.has(categoryKey(tenant.orgId, "groups"))}
                    onToggle={() => toggle(categoryKey(tenant.orgId, "groups"))}
                  >
                    <GroupList
                      wsId={wsId}
                      agentId={agentId}
                      orgId={tenant.orgId}
                      isSelected={(key) => isSelected("scene", tenant.orgId, key)}
                      onSelect={(key) => onSelect({ orgId: tenant.orgId, type: "scene", key })}
                    />
                  </CategoryNode>
                  <CategoryNode
                    label={t(($) => $.tab_body.scenes.category_persons)}
                    icon={<User className="size-3.5 shrink-0" aria-hidden="true" />}
                    count={tenant.personCount}
                    expanded={open.has(categoryKey(tenant.orgId, "persons"))}
                    onToggle={() => toggle(categoryKey(tenant.orgId, "persons"))}
                  >
                    <PersonList
                      wsId={wsId}
                      agentId={agentId}
                      orgId={tenant.orgId}
                      isSelected={(key) => isSelected("person", tenant.orgId, key)}
                      onSelect={(key) => onSelect({ orgId: tenant.orgId, type: "person", key })}
                    />
                  </CategoryNode>
                </ul>
              ) : null}
            </li>
          );
        })}
      </ul>
      {unassignedOrgs.length > 0 ? (
        <ul aria-label={t(($) => $.tab_body.scenes.unassigned_title)} className="pt-2">
          {unassignedOrgs.map((org) => (
            <li
              key={org.orgId}
              className="flex min-w-0 items-center gap-2 px-2 py-1 pl-8 text-muted-foreground"
              title={t(($) => $.tab_body.scenes.unassigned_hint)}
            >
              <Building2 className="size-4 shrink-0 opacity-60" aria-hidden="true" />
              <span className="min-w-0 flex-1 truncate font-mono text-caption opacity-80">{org.orgId}</span>
              {onCreateTenant ? (
                <Button
                  size="xs"
                  variant="ghost"
                  className="shrink-0"
                  aria-label={t(($) => $.tab_body.scenes.tenant_create_for, { orgId: org.orgId })}
                  onClick={() => onCreateTenant(org.orgId)}
                >
                  {t(($) => $.tab_body.scenes.tenant_create_short)}
                </Button>
              ) : null}
            </li>
          ))}
        </ul>
      ) : null}
      {onCreateTenant ? (
        <div className="pt-1">
          <Button
            size="sm"
            variant="ghost"
            className="w-full justify-start text-muted-foreground"
            onClick={() => onCreateTenant()}
          >
            <Plus className="size-3.5" aria-hidden="true" />
            {t(($) => $.tab_body.scenes.tenant_create)}
          </Button>
        </div>
      ) : null}
    </div>
  );
}

function ExpandButton({
  expanded,
  label,
  onClick,
}: {
  expanded: boolean;
  label: string;
  onClick: () => void;
}) {
  const { t } = useT("agents");
  return (
    <button
      type="button"
      aria-expanded={expanded}
      aria-label={
        expanded
          ? t(($) => $.tab_body.scenes.collapse, { name: label })
          : t(($) => $.tab_body.scenes.expand, { name: label })
      }
      onClick={onClick}
      className="flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring"
    >
      <ChevronRight
        className={cn("size-3.5 transition-transform motion-reduce:transition-none", expanded && "rotate-90")}
        aria-hidden="true"
      />
    </button>
  );
}

function CategoryNode({
  label,
  icon,
  count,
  expanded,
  onToggle,
  children,
}: {
  label: string;
  icon: React.ReactNode;
  count: number;
  expanded: boolean;
  onToggle: () => void;
  children: React.ReactNode;
}) {
  return (
    <li>
      <button
        type="button"
        aria-expanded={expanded}
        onClick={onToggle}
        className="flex w-full min-w-0 items-center gap-1.5 rounded-md px-1 py-1 text-left text-caption font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
      >
        <ChevronRight
          className={cn("size-3.5 shrink-0 transition-transform motion-reduce:transition-none", expanded && "rotate-90")}
          aria-hidden="true"
        />
        {icon}
        <span className="min-w-0 truncate">{label}</span>
        {count > 0 ? <span className="ml-auto shrink-0 tabular-nums font-normal">{count}</span> : null}
      </button>
      {expanded ? <div className="pl-3">{children}</div> : null}
    </li>
  );
}

function ListNotice({ children }: { children: React.ReactNode }) {
  return <p className="px-2 py-1 text-caption text-muted-foreground">{children}</p>;
}

function GroupList({
  wsId,
  agentId,
  orgId,
  isSelected,
  onSelect,
}: {
  wsId: string;
  agentId: string;
  orgId: string;
  isSelected: (key: string) => boolean;
  onSelect: (key: string) => void;
}) {
  const { t } = useT("agents");
  const timeAgo = useTimeAgo();
  const query = useInfiniteQuery(agentTenantGroupsOptions(wsId, agentId, orgId));
  const groups = [
    ...new Map(
      (query.data?.pages ?? []).flatMap((page) => page.scenes).map((scene) => [scene.sceneKey, scene]),
    ).values(),
  ].filter((scene) => scene.kind !== "dm");

  if (query.isLoading) {
    return (
      <ListNotice>
        <Loader2 className="mr-1 inline size-3 animate-spin motion-reduce:animate-none" aria-hidden="true" />
        {t(($) => $.tab_body.scenes.loading)}
      </ListNotice>
    );
  }
  if (query.isError && groups.length === 0) {
    return (
      <div className="flex items-center gap-2 px-2 py-1">
        <span className="text-caption text-muted-foreground">{t(($) => $.tab_body.scenes.load_failed)}</span>
        <Button size="xs" variant="ghost" onClick={() => void query.refetch()}>
          {t(($) => $.tab_body.scenes.retry)}
        </Button>
      </div>
    );
  }
  if (groups.length === 0) return <ListNotice>{t(($) => $.tab_body.scenes.none)}</ListNotice>;
  return (
    <ul>
      {groups.map((group) => {
        const title = group.title || t(($) => $.tab_body.scenes.untitled_group);
        const selected = isSelected(group.sceneKey);
        return (
          <li key={group.sceneKey}>
            <button
              type="button"
              data-active={selected ? "true" : undefined}
              aria-current={selected ? "true" : undefined}
              onClick={() => onSelect(group.sceneKey)}
              className={ROW}
            >
              <span className="min-w-0 flex-1 truncate">{title}</span>
              {group.lastActiveAt ? (
                <span className="shrink-0 text-caption font-normal text-muted-foreground">
                  {timeAgo(group.lastActiveAt)}
                </span>
              ) : null}
            </button>
          </li>
        );
      })}
      {query.hasNextPage ? (
        <li>
          <Button
            size="xs"
            variant="ghost"
            className="w-full justify-start text-muted-foreground"
            disabled={query.isFetchingNextPage}
            onClick={() => void query.fetchNextPage()}
          >
            {query.isFetchingNextPage ? t(($) => $.tab_body.scenes.loading) : t(($) => $.tab_body.scenes.load_more)}
          </Button>
        </li>
      ) : null}
    </ul>
  );
}

function PersonList({
  wsId,
  agentId,
  orgId,
  isSelected,
  onSelect,
}: {
  wsId: string;
  agentId: string;
  orgId: string;
  isSelected: (key: string) => boolean;
  onSelect: (key: string) => void;
}) {
  const { t } = useT("agents");
  const timeAgo = useTimeAgo();
  const query = useQuery(agentTenantPersonsOptions(wsId, agentId, orgId));
  const persons = query.data ?? [];

  if (query.isLoading) {
    return (
      <ListNotice>
        <Loader2 className="mr-1 inline size-3 animate-spin motion-reduce:animate-none" aria-hidden="true" />
        {t(($) => $.tab_body.scenes.loading)}
      </ListNotice>
    );
  }
  if (query.isError && persons.length === 0) {
    return (
      <div className="flex items-center gap-2 px-2 py-1">
        <span className="text-caption text-muted-foreground">{t(($) => $.tab_body.scenes.load_failed)}</span>
        <Button size="xs" variant="ghost" onClick={() => void query.refetch()}>
          {t(($) => $.tab_body.scenes.retry)}
        </Button>
      </div>
    );
  }
  if (persons.length === 0) return <ListNotice>{t(($) => $.tab_body.scenes.none)}</ListNotice>;
  return (
    <ul>
      {persons.map((person) => {
        const selected = isSelected(person.staffId);
        return (
          <li key={person.staffId}>
            <button
              type="button"
              data-active={selected ? "true" : undefined}
              aria-current={selected ? "true" : undefined}
              onClick={() => onSelect(person.staffId)}
              className={ROW}
            >
              <span className="min-w-0 flex-1 truncate">{person.title || person.staffId}</span>
              {person.lastActiveAt ? (
                <span className="shrink-0 text-caption font-normal text-muted-foreground">
                  {timeAgo(person.lastActiveAt)}
                </span>
              ) : null}
            </button>
          </li>
        );
      })}
    </ul>
  );
}
