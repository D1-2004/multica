"use client";

import { useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  AlertCircle,
  Building2,
  Check,
  ChevronDown,
  Copy,
  List,
  PanelLeftClose,
  PanelLeftOpen,
  Plus,
  RefreshCw,
  Settings2,
  Tag as TagIcon,
  Trash2,
  UserPlus,
} from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { agentListOptions } from "@multica/core/workspace/queries";
import {
  useAdoptTagTenant,
  useApplyTag,
  useCreateTagTenant,
  useDeleteTagTenant,
  useRenameTagTenant,
  useWorkspaceTag,
  type TagState,
  type TagTenant,
} from "@multica/core/tag";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { NativeSelect, NativeSelectOption } from "@multica/ui/components/ui/native-select";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { AgentDetailPage } from "../agents/components/agent-detail-page";
import { TagTenantConfig } from "./tenant-config";
import { AppLink, useNavigation } from "../navigation";
import { useT } from "../i18n";

/** URL query param holding the selected tenant id; absent means the shared
 * (template) configuration. Distinct from the scenes tab's `tenant` param. */
const TENANT_PARAM = "tag_tenant";

/** View param of the tenant pane; the shared pane keeps `view`. */
const TENANT_VIEW_PARAM = "tview";

/** Params that only mean something inside one tenant's pane. */
const AGENT_SCOPED_PARAMS = ["tenant", "node", "scene", "scene_tab", "app"];

type TagDialog = "new" | "adopt" | "apply" | "manage" | null;

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/**
 * The workspace Tag, laid out left and right: the shared configuration every
 * tenant inherits (the template agent's instructions, skills, connectors and
 * runtime) on the left; the selected tenant on the right, with its identity,
 * event perception, scenes and recent work. Without a tenant the right side
 * lists the tenants.
 */
export function TagPage() {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const { data: state, isLoading } = useWorkspaceTag(wsId);
  const [dialog, setDialog] = useState<TagDialog>(null);
  const [deleting, setDeleting] = useState<TagTenant | null>(null);
  const [sharedCollapsed, setSharedCollapsed] = useState(false);

  const selectedId = navigation.searchParams.get(TENANT_PARAM);
  const tag = state?.tag ?? null;
  const tenants = state?.tenants ?? [];
  const tenant = selectedId ? (tenants.find((item) => item.id === selectedId) ?? null) : null;

  const selectTenant = (id: string | null, view?: string) => {
    const params = new URLSearchParams(navigation.searchParams);
    if (id) params.set(TENANT_PARAM, id);
    else params.delete(TENANT_PARAM);
    for (const key of [...AGENT_SCOPED_PARAMS, TENANT_VIEW_PARAM]) params.delete(key);
    if (view) params.set(TENANT_VIEW_PARAM, view);
    const query = params.toString();
    navigation.replace(`${navigation.pathname}${query ? `?${query}` : ""}`);
  };

  if (isLoading) {
    return (
      <div className="flex flex-1 flex-col gap-4 p-6">
        <Skeleton className="h-6 w-48" />
        <Skeleton className="h-40 w-full" />
      </div>
    );
  }

  if (!tag || !state) {
    return (
      <div className="flex flex-1 items-center justify-center p-6">
        <div className="max-w-md rounded-lg border border-dashed px-6 py-10 text-center">
          <TagIcon className="mx-auto h-6 w-6 text-muted-foreground" aria-hidden="true" />
          <p className="mt-3 text-body font-medium">{t(($) => $.tag_page.empty_title)}</p>
          <p className="mt-1 text-caption text-muted-foreground">
            {state?.canOperate ? t(($) => $.tag_page.empty_operator) : t(($) => $.tag_page.empty_member)}
          </p>
          {state?.canOperate ? (
            <Button
              className="mt-4"
              size="sm"
              render={<AppLink href={`${paths.settings()}?tab=tag`} />}
              nativeButton={false}
            >
              {t(($) => $.tag_page.open_settings)}
            </Button>
          ) : null}
        </div>
      </div>
    );
  }

  const behind =
    tag.latestRevision != null && tenants.some((item) => item.appliedRevision !== tag.latestRevision);
  const showBanner = state.canManage && tenants.length > 0 && (tag.hasUnpublishedChanges || behind);

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      {showBanner ? (
        <div className="flex shrink-0 items-center gap-2 border-b border-amber-500/30 bg-amber-500/10 px-6 py-2 text-caption text-amber-900 dark:text-amber-100">
          <RefreshCw className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          <span className="flex-1">
            {tag.hasUnpublishedChanges
              ? t(($) => $.tag_page.pending_banner)
              : t(($) => $.tag_page.behind_banner, { revision: tag.latestRevision ?? 0 })}
          </span>
          <Button
            variant="outline"
            size="sm"
            className="h-6 border-amber-500/40 bg-background/70 text-caption"
            onClick={() => setDialog("apply")}
          >
            {t(($) => $.tag_page.apply)}
          </Button>
        </div>
      ) : null}

      <header className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-2 border-b px-4 py-3 sm:px-6">
        <TagIcon className="h-5 w-5 text-muted-foreground" aria-hidden="true" />
        <h1 className="text-title-sm font-semibold">{t(($) => $.tag_page.title)}</h1>
        {tag.latestRevision != null ? (
          <span className="text-caption text-muted-foreground">
            {t(($) => $.tag_page.latest, { revision: tag.latestRevision })}
          </span>
        ) : null}
        <span className="text-caption text-muted-foreground">
          {t(($) => $.tag_page.tenants_heading, { count: tenants.length })}
        </span>
        {state.canManage ? (
          <div className="ml-auto flex flex-wrap items-center gap-2">
            <Button size="sm" onClick={() => setDialog("new")}>
              <Plus className="h-4 w-4" aria-hidden="true" />
              {t(($) => $.tag_page.new_tenant)}
            </Button>
            <Button size="sm" variant="outline" onClick={() => setDialog("adopt")}>
              <UserPlus className="h-4 w-4" aria-hidden="true" />
              {t(($) => $.tag_page.adopt_tenant)}
            </Button>
            {tenants.length > 0 ? (
              <>
                <Button size="sm" variant="outline" onClick={() => setDialog("apply")}>
                  <RefreshCw className="h-4 w-4" aria-hidden="true" />
                  {t(($) => $.tag_page.apply)}
                </Button>
                <Button size="sm" variant="outline" onClick={() => setDialog("manage")}>
                  <Settings2 className="h-4 w-4" aria-hidden="true" />
                  {t(($) => $.tag_page.manage_tenants)}
                </Button>
              </>
            ) : null}
          </div>
        ) : null}
      </header>

      <div className="flex min-h-0 flex-1 flex-col overflow-y-auto lg:flex-row lg:overflow-hidden">
        <section
          aria-label={t(($) => $.tag_page.shared_title)}
          className={cn(
            "flex flex-col border-b lg:min-h-0 lg:border-b-0 lg:border-r",
            sharedCollapsed ? "lg:w-12 lg:shrink-0" : "min-h-[560px] lg:w-[44%] lg:shrink-0",
          )}
        >
          <PaneHeader
            collapsed={sharedCollapsed}
            title={t(($) => $.tag_page.shared_title)}
            hint={t(($) => $.tag_page.shared_hint)}
            action={
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7"
                aria-label={sharedCollapsed ? t(($) => $.tag_page.shared_expand) : t(($) => $.tag_page.shared_collapse)}
                aria-expanded={!sharedCollapsed}
                onClick={() => setSharedCollapsed((value) => !value)}
              >
                {sharedCollapsed ? (
                  <PanelLeftOpen className="h-4 w-4" aria-hidden="true" />
                ) : (
                  <PanelLeftClose className="h-4 w-4" aria-hidden="true" />
                )}
              </Button>
            }
          />
          {sharedCollapsed ? null : (
            <AgentDetailPage key={tag.agentId} agentId={tag.agentId} tagView={{ role: "template", embedded: true }} />
          )}
        </section>

        <section aria-label={t(($) => $.tag_page.tenant_pane_title)} className="flex min-h-[560px] min-w-0 flex-1 flex-col lg:min-h-0">
          <PaneHeader
            title={t(($) => $.tag_page.tenant_pane_title)}
            hint={tenant ? <AgentIdChip agentId={tenant.employeeAgentId} /> : t(($) => $.tag_page.tenant_pane_hint)}
            action={<TagTenantSwitcher state={state} selected={tenant} onSelect={(id) => selectTenant(id)} />}
          />
          {tenant ? (
            <AgentDetailPage
              key={tenant.employeeAgentId}
              agentId={tenant.employeeAgentId}
              tagView={{
                role: "employee",
                embedded: true,
                viewParam: TENANT_VIEW_PARAM,
                renderTenantConfig: (props) => <TagTenantConfig {...props} />,
              }}
            />
          ) : (
            <TenantOverview
              tenants={tenants}
              canManage={state.canManage}
              onSelect={(id) => selectTenant(id)}
              onNew={() => setDialog("new")}
            />
          )}
        </section>
      </div>

      {dialog === "new" ? (
        <NewTenantDialog
          wsId={wsId}
          onClose={() => setDialog(null)}
          onCreated={(id) => {
            setDialog(null);
            // A new tenant starts by issuing its digital employee's identity.
            selectTenant(id);
          }}
        />
      ) : null}
      {dialog === "adopt" ? (
        <AdoptTenantDialog
          wsId={wsId}
          state={state}
          onClose={() => setDialog(null)}
          onAdopted={(id) => {
            setDialog(null);
            selectTenant(id);
          }}
        />
      ) : null}
      {dialog === "apply" ? <ApplyDialog wsId={wsId} state={state} onClose={() => setDialog(null)} /> : null}
      {dialog === "manage" ? (
        <ManageTenantsDialog wsId={wsId} state={state} onClose={() => setDialog(null)} onDelete={setDeleting} />
      ) : null}
      {deleting ? (
        <DeleteTenantDialog
          wsId={wsId}
          tenant={deleting}
          onClose={() => setDeleting(null)}
          onDeleted={() => {
            // Leave the deleted tenant's page; the manage dialog stays open.
            if (deleting.id === tenant?.id) selectTenant(null);
            setDeleting(null);
          }}
        />
      ) : null}
    </div>
  );
}

function PaneHeader({
  title,
  hint,
  action,
  collapsed = false,
}: {
  title: string;
  hint: ReactNode;
  action?: ReactNode;
  collapsed?: boolean;
}) {
  if (collapsed) {
    return <div className="flex shrink-0 justify-center border-b px-2 py-2">{action}</div>;
  }
  return (
    <div className="flex shrink-0 items-center gap-3 border-b bg-muted/30 px-4 py-2.5 sm:px-6">
      <div className="min-w-0 flex-1">
        <h2 className="text-body font-semibold">{title}</h2>
        <div className="truncate text-caption text-muted-foreground">{hint}</div>
      </div>
      {action}
    </div>
  );
}

/** The tenant employee's Agent ID: the one identifier a tenant keeps of its
 * own, for calls and logs. */
function AgentIdChip({ agentId }: { agentId: string }) {
  const { t } = useT("agents");
  return (
    <span className="inline-flex max-w-full items-center gap-1.5">
      <span>{t(($) => $.tag_page.agent_id)}</span>
      <code className="truncate font-mono">{agentId}</code>
      <button
        type="button"
        className="rounded p-0.5 hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        aria-label={t(($) => $.tag_page.copy_agent_id)}
        onClick={() => {
          void navigator.clipboard?.writeText(agentId).then(
            () => toast.success(t(($) => $.tag_page.copied)),
            () => undefined,
          );
        }}
      >
        <Copy className="h-3 w-3" aria-hidden="true" />
      </button>
    </span>
  );
}

function TenantOverview({
  tenants,
  canManage,
  onSelect,
  onNew,
}: {
  tenants: TagTenant[];
  canManage: boolean;
  onSelect: (id: string) => void;
  onNew: () => void;
}) {
  const { t } = useT("agents");
  if (tenants.length === 0) {
    return (
      <div className="flex flex-1 items-center justify-center p-6">
        <div className="max-w-sm rounded-lg border border-dashed px-6 py-10 text-center">
          <Building2 className="mx-auto h-6 w-6 text-muted-foreground" aria-hidden="true" />
          <p className="mt-3 text-body font-medium">{t(($) => $.tag_page.tenants_empty)}</p>
          <p className="mt-1 text-caption text-muted-foreground">{t(($) => $.tag_page.tenants_empty_hint)}</p>
          {canManage ? (
            <Button className="mt-4" size="sm" onClick={onNew}>
              <Plus className="h-4 w-4" aria-hidden="true" />
              {t(($) => $.tag_page.new_tenant)}
            </Button>
          ) : null}
        </div>
      </div>
    );
  }
  return (
    <div className="min-h-0 flex-1 overflow-y-auto p-4 sm:p-6">
      <ul className="grid gap-3 xl:grid-cols-2">
        {tenants.map((item) => (
          <li key={item.id}>
            <button
              type="button"
              onClick={() => onSelect(item.id)}
              className="flex w-full items-start gap-3 rounded-xl border px-4 py-3 text-left transition-colors hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <Building2 className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
              <div className="min-w-0 flex-1 space-y-0.5">
                <div className="flex items-center gap-1.5">
                  <span className="truncate text-body font-medium">{item.name}</span>
                  {item.orgId ? <span className="font-mono text-caption text-muted-foreground">{item.orgId}</span> : null}
                </div>
                <TenantStatus tenant={item} />
                <div className="truncate font-mono text-caption text-muted-foreground">{item.employeeAgentId}</div>
              </div>
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

function TenantStatus({ tenant, className }: { tenant: TagTenant; className?: string }) {
  const { t } = useT("agents");
  const warn = !tenant.bound || tenant.employeeArchived;
  let label: string;
  if (tenant.employeeArchived) label = t(($) => $.tag_page.employee_archived);
  else if (!tenant.bound) label = t(($) => $.tag_page.unbound);
  else if (tenant.appliedRevision != null) label = t(($) => $.tag_page.applied, { revision: tenant.appliedRevision });
  else label = t(($) => $.tag_page.not_applied);
  return (
    <div className={cn("text-caption", warn ? "text-amber-700 dark:text-amber-300" : "text-muted-foreground", className)}>
      {label}
    </div>
  );
}

/** Picks the tenant shown on the right; 租户列表 goes back to the list. */
function TagTenantSwitcher({
  state,
  selected,
  onSelect,
}: {
  state: TagState;
  selected: TagTenant | null;
  onSelect: (id: string | null) => void;
}) {
  const { t } = useT("agents");
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={<Button variant="outline" size="sm" className="h-8 max-w-[260px] gap-1.5" />}
        aria-label={t(($) => $.tag_page.switcher_aria)}
      >
        <Building2 className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
        <span className="truncate font-medium">{selected ? selected.name : t(($) => $.tag_page.pick_tenant)}</span>
        {selected?.orgId ? <span className="font-mono text-caption text-muted-foreground">{selected.orgId}</span> : null}
        <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-80">
        <DropdownMenuGroup>
          <DropdownMenuItem onClick={() => onSelect(null)} className="gap-2">
            <List className="h-4 w-4 shrink-0" aria-hidden="true" />
            <span className="flex-1">{t(($) => $.tag_page.tenant_list)}</span>
            {!selected ? <Check className="h-4 w-4 shrink-0" aria-hidden="true" /> : null}
          </DropdownMenuItem>
        </DropdownMenuGroup>
        {state.tenants.length > 0 ? (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuGroup>
              <DropdownMenuLabel className="text-caption text-muted-foreground">
                {t(($) => $.tag_page.tenants_heading, { count: state.tenants.length })}
              </DropdownMenuLabel>
              {state.tenants.map((item) => (
                <DropdownMenuItem key={item.id} onClick={() => onSelect(item.id)} className="items-start gap-2 py-2">
                  <Building2 className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-1.5">
                      <span className="truncate font-medium">{item.name}</span>
                      {item.orgId ? (
                        <span className="font-mono text-caption text-muted-foreground">{item.orgId}</span>
                      ) : null}
                    </div>
                    <TenantStatus tenant={item} />
                  </div>
                  {selected?.id === item.id ? <Check className="h-4 w-4 shrink-0" aria-hidden="true" /> : null}
                </DropdownMenuItem>
              ))}
            </DropdownMenuGroup>
          </>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function NewTenantDialog({
  wsId,
  onClose,
  onCreated,
}: {
  wsId: string;
  onClose: () => void;
  onCreated: (tenantId: string) => void;
}) {
  const { t } = useT("agents");
  const [name, setName] = useState("");
  const create = useCreateTagTenant(wsId);
  const trimmed = name.trim();

  const submit = () => {
    if (!trimmed || create.isPending) return;
    create.mutate(trimmed, {
      onSuccess: (result) => {
        toast.success(t(($) => $.tag_page.tenant_created, { name: trimmed }));
        if (result.tenant) onCreated(result.tenant.id);
        else onClose();
      },
      onError: (error) => toast.error(t(($) => $.tag_page.action_failed, { message: errorMessage(error) })),
    });
  };

  return (
    <Dialog open onOpenChange={(open) => (!open ? onClose() : undefined)}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t(($) => $.tag_page.new_tenant_title)}</DialogTitle>
          <DialogDescription>{t(($) => $.tag_page.new_tenant_description)}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-2">
          <Label htmlFor="tag-tenant-name">{t(($) => $.tag_page.tenant_name)}</Label>
          <Input
            id="tag-tenant-name"
            value={name}
            maxLength={64}
            autoFocus
            placeholder={t(($) => $.tag_page.tenant_name_placeholder)}
            onChange={(event) => setName(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") submit();
            }}
          />
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            {t(($) => $.tag_page.cancel)}
          </Button>
          <Button disabled={!trimmed || create.isPending} onClick={submit}>
            {t(($) => $.tag_page.create)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function AdoptTenantDialog({
  wsId,
  state,
  onClose,
  onAdopted,
}: {
  wsId: string;
  state: TagState;
  onClose: () => void;
  onAdopted: (tenantId: string) => void;
}) {
  const { t } = useT("agents");
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const adopt = useAdoptTagTenant(wsId);
  const taken = useMemo(
    () => new Set([state.tag?.agentId ?? "", ...state.tenants.map((item) => item.employeeAgentId)]),
    [state],
  );
  const candidates = agents.filter((agent) => !agent.archived_at && !taken.has(agent.id));
  const [agentId, setAgentId] = useState("");
  const [name, setName] = useState("");
  const trimmed = name.trim();

  const submit = () => {
    if (!agentId || !trimmed || adopt.isPending) return;
    adopt.mutate(
      { agentId, name: trimmed },
      {
        onSuccess: (result) => {
          toast.success(t(($) => $.tag_page.tenant_adopted, { name: trimmed }));
          if (result.tenant) onAdopted(result.tenant.id);
          else onClose();
        },
        onError: (error) => toast.error(t(($) => $.tag_page.action_failed, { message: errorMessage(error) })),
      },
    );
  };

  return (
    <Dialog open onOpenChange={(open) => (!open ? onClose() : undefined)}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t(($) => $.tag_page.adopt_title)}</DialogTitle>
          <DialogDescription>{t(($) => $.tag_page.adopt_description)}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          <div className="grid gap-2">
            <Label htmlFor="tag-adopt-agent">{t(($) => $.tag_page.adopt_agent)}</Label>
            <NativeSelect
              id="tag-adopt-agent"
              value={agentId}
              onChange={(event) => {
                const next = event.target.value;
                setAgentId(next);
                if (!name.trim()) setName(agents.find((agent) => agent.id === next)?.name ?? "");
              }}
            >
              <NativeSelectOption value="">{t(($) => $.tag_page.adopt_agent_placeholder)}</NativeSelectOption>
              {candidates.map((agent) => (
                <NativeSelectOption key={agent.id} value={agent.id}>
                  {agent.name}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          </div>
          <div className="grid gap-2">
            <Label htmlFor="tag-adopt-name">{t(($) => $.tag_page.tenant_name)}</Label>
            <Input
              id="tag-adopt-name"
              value={name}
              maxLength={64}
              placeholder={t(($) => $.tag_page.tenant_name_placeholder)}
              onChange={(event) => setName(event.target.value)}
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            {t(($) => $.tag_page.cancel)}
          </Button>
          <Button disabled={!agentId || !trimmed || adopt.isPending} onClick={submit}>
            {t(($) => $.tag_page.adopt_submit)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ApplyDialog({ wsId, state, onClose }: { wsId: string; state: TagState; onClose: () => void }) {
  const { t } = useT("agents");
  const applicable = state.tenants.filter((item) => !item.employeeArchived);
  const [selected, setSelected] = useState<Set<string>>(() => new Set(applicable.map((item) => item.id)));
  const [note, setNote] = useState("");
  const apply = useApplyTag(wsId);

  const toggle = (id: string) =>
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  const submit = () => {
    if (selected.size === 0 || apply.isPending) return;
    apply.mutate(
      { tenantIds: [...selected], note: note.trim() },
      {
        onSuccess: (result) => {
          const applied = result.results.filter((item) => item.applied).length;
          toast.success(t(($) => $.tag_page.apply_done, { revision: result.revision, count: applied }));
          const skipped = result.results.reduce(
            (sum, item) => sum + item.skippedSkillIds.length + item.skippedConnectorIds.length + item.skippedPluginIds.length + item.skippedOfferIds.length,
            0,
          );
          if (skipped > 0) toast.warning(t(($) => $.tag_page.apply_skipped, { count: skipped }));
          onClose();
        },
        onError: (error) => toast.error(t(($) => $.tag_page.action_failed, { message: errorMessage(error) })),
      },
    );
  };

  return (
    <Dialog open onOpenChange={(open) => (!open ? onClose() : undefined)}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t(($) => $.tag_page.apply_title)}</DialogTitle>
          <DialogDescription>{t(($) => $.tag_page.apply_description)}</DialogDescription>
        </DialogHeader>
        <div className="divide-y rounded-lg border">
          {applicable.map((item) => (
            <label key={item.id} className="flex cursor-pointer items-center gap-3 px-3 py-2.5">
              <Checkbox checked={selected.has(item.id)} onCheckedChange={() => toggle(item.id)} />
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-1.5">
                  <span className="truncate text-body font-medium">{item.name}</span>
                  {item.orgId ? <span className="font-mono text-caption text-muted-foreground">{item.orgId}</span> : null}
                </div>
                <TenantStatus tenant={item} />
              </div>
            </label>
          ))}
        </div>
        <div className="grid gap-2">
          <Label htmlFor="tag-apply-note">{t(($) => $.tag_page.apply_note)}</Label>
          <Input id="tag-apply-note" value={note} maxLength={200} onChange={(event) => setNote(event.target.value)} />
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            {t(($) => $.tag_page.cancel)}
          </Button>
          <Button disabled={selected.size === 0 || apply.isPending} onClick={submit}>
            {t(($) => $.tag_page.apply_submit, { count: selected.size })}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** 管理租户: rename or delete any tenant of the Tag. */
function ManageTenantsDialog({
  wsId,
  state,
  onClose,
  onDelete,
}: {
  wsId: string;
  state: TagState;
  onClose: () => void;
  onDelete: (tenant: TagTenant) => void;
}) {
  const { t } = useT("agents");
  return (
    <Dialog open onOpenChange={(open) => (!open ? onClose() : undefined)}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>{t(($) => $.tag_page.manage_title)}</DialogTitle>
          <DialogDescription>{t(($) => $.tag_page.manage_description)}</DialogDescription>
        </DialogHeader>
        {state.tenants.length === 0 ? (
          <p className="py-6 text-center text-caption text-muted-foreground">{t(($) => $.tag_page.tenants_empty)}</p>
        ) : (
          <div className="max-h-[60vh] divide-y overflow-y-auto rounded-lg border">
            {state.tenants.map((item) => (
              <ManagedTenantRow key={item.id} wsId={wsId} tenant={item} onDelete={() => onDelete(item)} />
            ))}
          </div>
        )}
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            {t(($) => $.tag_page.close)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ManagedTenantRow({ wsId, tenant, onDelete }: { wsId: string; tenant: TagTenant; onDelete: () => void }) {
  const { t } = useT("agents");
  const rename = useRenameTagTenant(wsId);
  const [name, setName] = useState(tenant.name);
  const trimmed = name.trim();
  const changed = trimmed !== "" && trimmed !== tenant.name;

  const save = () => {
    if (!changed || rename.isPending) return;
    rename.mutate(
      { tenantId: tenant.id, name: trimmed },
      {
        onSuccess: () => toast.success(t(($) => $.tag_page.tenant_renamed, { name: trimmed })),
        onError: (error) => toast.error(t(($) => $.tag_page.action_failed, { message: errorMessage(error) })),
      },
    );
  };

  return (
    <div className="flex items-start gap-3 px-3 py-3">
      <Building2 className="mt-2 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
      <div className="min-w-0 flex-1 space-y-1.5">
        <div className="flex gap-2">
          <Input
            value={name}
            maxLength={64}
            aria-label={t(($) => $.tag_page.tenant_name)}
            onChange={(event) => setName(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") save();
            }}
          />
          <Button variant="outline" disabled={!changed || rename.isPending} onClick={save}>
            {t(($) => $.tag_page.save)}
          </Button>
        </div>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-0.5">
          {tenant.orgId ? <span className="font-mono text-caption text-muted-foreground">{tenant.orgId}</span> : null}
          <TenantStatus tenant={tenant} />
          <span className="min-w-0 truncate text-caption text-muted-foreground">
            {t(($) => $.tag_page.employee_agent, { name: tenant.employeeName })}
          </span>
        </div>
      </div>
      <Button
        variant="ghost"
        size="icon"
        className="shrink-0 text-destructive hover:text-destructive"
        aria-label={t(($) => $.tag_page.delete_tenant_named, { name: tenant.name })}
        onClick={onDelete}
      >
        <Trash2 className="h-4 w-4" aria-hidden="true" />
      </Button>
    </div>
  );
}

function DeleteTenantDialog({
  wsId,
  tenant,
  onClose,
  onDeleted,
}: {
  wsId: string;
  tenant: TagTenant;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const { t } = useT("agents");
  const remove = useDeleteTagTenant(wsId);
  // An archived employee has nothing left to archive.
  const [archiveEmployee, setArchiveEmployee] = useState(!tenant.employeeArchived);
  return (
    <Dialog open onOpenChange={(open) => (!open ? onClose() : undefined)}>
      <DialogContent className="max-w-md" showCloseButton={false}>
        <div className="flex items-start gap-3">
          <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-destructive/10">
            <AlertCircle className="h-5 w-5 text-destructive" />
          </div>
          <DialogHeader className="flex-1 gap-1">
            <DialogTitle className="text-body font-semibold">{t(($) => $.tag_page.delete_tenant)}</DialogTitle>
            <DialogDescription className="text-caption">
              {t(($) => $.tag_page.delete_tenant_confirm, { name: tenant.name })}
            </DialogDescription>
          </DialogHeader>
        </div>
        {tenant.employeeArchived ? null : (
          <label className="flex cursor-pointer items-start gap-3 rounded-lg border px-3 py-2.5">
            <Checkbox
              className="mt-0.5"
              checked={archiveEmployee}
              onCheckedChange={(checked) => setArchiveEmployee(checked === true)}
            />
            <div className="min-w-0 space-y-0.5">
              <div className="text-body">{t(($) => $.tag_page.archive_employee, { name: tenant.employeeName })}</div>
              <div className="text-caption text-muted-foreground">
                {archiveEmployee ? t(($) => $.tag_page.archive_employee_hint) : t(($) => $.tag_page.keep_employee_hint)}
              </div>
            </div>
          </label>
        )}
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            {t(($) => $.tag_page.cancel)}
          </Button>
          <Button
            variant="destructive"
            disabled={remove.isPending}
            onClick={() =>
              remove.mutate(
                { tenantId: tenant.id, employeeAgentId: tenant.employeeAgentId, archiveEmployee },
                {
                  onSuccess: () => {
                    toast.success(t(($) => $.tag_page.tenant_deleted, { name: tenant.name }));
                    onDeleted();
                  },
                  onError: (error) => toast.error(t(($) => $.tag_page.action_failed, { message: errorMessage(error) })),
                },
              )
            }
          >
            {t(($) => $.tag_page.delete_tenant)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
