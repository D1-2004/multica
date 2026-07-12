"use client";

import { useCallback, useEffect, useState } from "react";
import { Crown, Shield, User, MoreHorizontal, UserMinus, Users, Clock, X, Mail, Search, UserPlus } from "lucide-react";
import { ActorAvatar } from "../../common/actor-avatar";
import type { MemberWithUser, MemberRole, Invitation, DingTalkUser } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Badge } from "@multica/ui/components/ui/badge";
import { Avatar, AvatarFallback, AvatarImage } from "@multica/ui/components/ui/avatar";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogCancel,
  AlertDialogAction,
} from "@multica/ui/components/ui/alert-dialog";
import {
  Select,
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from "@multica/ui/components/ui/select";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubTrigger,
  DropdownMenuSubContent,
} from "@multica/ui/components/ui/dropdown-menu";
import { toast } from "sonner";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace } from "@multica/core/paths";
import { memberListOptions, invitationListOptions, workspaceKeys } from "@multica/core/workspace/queries";
import { api } from "@multica/core/api";
import { useT } from "../../i18n";
import { SettingsCard, SettingsSection, SettingsTab } from "./settings-layout";

const ROLE_ICONS: Record<MemberRole, typeof Crown> = {
  owner: Crown,
  admin: Shield,
  member: User,
};

function initials(name: string): string {
  const chars = Array.from(name.trim() || "?");
  return chars.slice(0, 2).join("").toUpperCase();
}

function normalizeSearchText(value: string | undefined | null): string {
  return (value ?? "").trim().replace(/\s+/g, "").toLowerCase();
}

function dingTalkSearchScore(user: DingTalkUser, query: string, index: number): number {
  const needle = normalizeSearchText(query);
  if (!needle) return index;
  const fields = [
    user.name,
    user.email,
    user.mobile,
    user.user_id,
    user.union_id,
    user.title,
  ].map(normalizeSearchText).filter(Boolean);
  const name = normalizeSearchText(user.name);

  if (name === needle) return index / 1000;
  if (fields.some((field) => field === needle)) return 1 + index / 1000;
  if (name.startsWith(needle)) return 2 + index / 1000;
  if (fields.some((field) => field.startsWith(needle))) return 3 + index / 1000;
  if (name.includes(needle)) return 4 + index / 1000;
  if (fields.some((field) => field.includes(needle))) return 5 + index / 1000;
  return 10 + index / 1000;
}

function rankDingTalkUsers(users: DingTalkUser[], query: string): DingTalkUser[] {
  const seen = new Set<string>();
  return users
    .filter((user) => {
      const key = user.user_id || user.union_id || user.email;
      if (!key) return false;
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    })
    .map((user, index) => ({ user, score: dingTalkSearchScore(user, query, index) }))
    .sort((a, b) => a.score - b.score)
    .map(({ user }) => user);
}

function useRoleLabels() {
  const { t } = useT("settings");
  return {
    owner: {
      label: t(($) => $.members.roles.owner.label),
      description: t(($) => $.members.roles.owner.description),
      icon: ROLE_ICONS.owner,
    },
    admin: {
      label: t(($) => $.members.roles.admin.label),
      description: t(($) => $.members.roles.admin.description),
      icon: ROLE_ICONS.admin,
    },
    member: {
      label: t(($) => $.members.roles.member.label),
      description: t(($) => $.members.roles.member.description),
      icon: ROLE_ICONS.member,
    },
  } as const;
}

function MemberRow({
  member,
  canManage,
  canManageOwners,
  ownerCount,
  isSelf,
  busy,
  onRoleChange,
  onRemove,
}: {
  member: MemberWithUser;
  canManage: boolean;
  canManageOwners: boolean;
  /** Total number of owners in this workspace — needed to gate demoting the
   *  last owner per `workspace.go:497-507`. */
  ownerCount: number;
  isSelf: boolean;
  busy: boolean;
  onRoleChange: (role: MemberRole) => void;
  onRemove: () => void;
}) {
  const { t } = useT("settings");
  const roleConfig = useRoleLabels();
  const rc = roleConfig[member.role];
  const RoleIcon = rc.icon;
  const canEditRole = canManage && !isSelf && (member.role !== "owner" || canManageOwners);
  const canRemove = canManage && !isSelf && (member.role !== "owner" || canManageOwners);
  const isLastOwner = member.role === "owner" && ownerCount <= 1;
  const showMenu = canEditRole || canRemove;

  return (
    <div className="flex items-center gap-3 px-4 py-3">
      <ActorAvatar actorType="member" actorId={member.user_id} size="lg" />
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium truncate">{member.name}</div>
        <div className="text-xs text-muted-foreground truncate">{member.email}</div>
      </div>
      {showMenu && (
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button variant="ghost" size="icon-sm" disabled={busy}>
                <MoreHorizontal className="h-4 w-4 text-muted-foreground" />
              </Button>
            }
          />
          <DropdownMenuContent align="end" className="w-auto">
            {canEditRole && (
              <DropdownMenuSub>
                <DropdownMenuSubTrigger>
                  <Shield className="h-3.5 w-3.5" />
                  {t(($) => $.members.change_role)}
                </DropdownMenuSubTrigger>
                <DropdownMenuSubContent className="w-auto">
                  {(Object.entries(roleConfig) as [MemberRole, (typeof roleConfig)[MemberRole]][]).map(
                    ([role, config]) => {
                      if (role === "owner" && !canManageOwners) return null;
                      const Icon = config.icon;
                      const wouldDemoteLastOwner =
                        isLastOwner && role !== "owner";
                      return (
                        <DropdownMenuItem
                          key={role}
                          onClick={() =>
                            wouldDemoteLastOwner ? undefined : onRoleChange(role)
                          }
                          disabled={wouldDemoteLastOwner}
                          title={
                            wouldDemoteLastOwner
                              ? t(($) => $.members.cannot_demote_last_owner_title)
                              : undefined
                          }
                        >
                          <Icon className="h-3.5 w-3.5" />
                          <div className="flex flex-col">
                            <span>{config.label}</span>
                            <span className="text-xs text-muted-foreground font-normal">
                              {wouldDemoteLastOwner
                                ? t(($) => $.members.cannot_demote_last_owner)
                                : config.description}
                            </span>
                          </div>
                          {member.role === role && (
                            <span className="ml-auto text-xs text-muted-foreground">{"✓"}</span>
                          )}
                        </DropdownMenuItem>
                      );
                    }
                  )}
                </DropdownMenuSubContent>
              </DropdownMenuSub>
            )}
            {canEditRole && canRemove && <DropdownMenuSeparator />}
            {canRemove && (
              <DropdownMenuItem variant="destructive" onClick={onRemove}>
                <UserMinus className="h-3.5 w-3.5" />
                {t(($) => $.members.remove_action)}
              </DropdownMenuItem>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      )}
      <Badge variant="secondary">
        <RoleIcon className="h-3 w-3" />
        {rc.label}
      </Badge>
    </div>
  );
}

function InvitationRow({
  invitation,
  canManage,
  onRevoke,
  busy,
}: {
  invitation: Invitation;
  canManage: boolean;
  onRevoke: () => void;
  busy: boolean;
}) {
  const { t } = useT("settings");
  const roleConfig = useRoleLabels();
  const rc = roleConfig[invitation.role];

  return (
    <div className="flex items-center gap-3 px-4 py-3">
      <div className="flex h-8 w-8 items-center justify-center rounded-full bg-muted">
        <Mail className="h-4 w-4 text-muted-foreground" />
      </div>
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium truncate">{invitation.invitee_email}</div>
        <div className="flex items-center gap-1 text-xs text-muted-foreground">
          <Clock className="h-3 w-3" />
          <span>{t(($) => $.members.pending_status)}</span>
        </div>
      </div>
      {canManage && (
        <Button
          variant="ghost"
          size="icon-sm"
          disabled={busy}
          onClick={onRevoke}
          title={t(($) => $.members.revoke_invitation_tooltip)}
        >
          <X className="h-4 w-4 text-muted-foreground" />
        </Button>
      )}
      <Badge variant="outline">
        {rc.label}
      </Badge>
    </div>
  );
}

function DingTalkUserRow({
  user,
  checked,
  onToggle,
  userIdLabel,
  identityLabel,
}: {
  user: DingTalkUser;
  checked: boolean;
  onToggle: () => void;
  userIdLabel: string;
  identityLabel: string;
}) {
  const meta = [
    user.title,
    user.email || user.mobile,
    `${userIdLabel}: ${user.user_id}`,
  ].filter(Boolean).join(" · ");

  return (
    <label
      className={[
        "flex cursor-pointer items-center gap-3 rounded-lg px-3 py-3 transition-colors",
        checked ? "bg-primary/5 ring-1 ring-primary/20" : "hover:bg-accent/50",
      ].join(" ")}
    >
      <Checkbox checked={checked} onCheckedChange={onToggle} />
      <Avatar>
        {user.avatar_url && <AvatarImage src={user.avatar_url} alt={user.name} />}
        <AvatarFallback>{initials(user.name || user.user_id)}</AvatarFallback>
      </Avatar>
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-2">
          <span className="truncate text-sm font-medium">{user.name || user.user_id}</span>
          {!user.email && (
            <Badge variant="outline" className="shrink-0">
              {identityLabel}
            </Badge>
          )}
        </div>
        <div className="truncate text-xs text-muted-foreground">{meta}</div>
      </div>
    </label>
  );
}

export function MembersTab() {
  const { t } = useT("settings");
  const roleConfig = useRoleLabels();
  const user = useAuthStore((s) => s.user);
  const workspace = useCurrentWorkspace();
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: invitations = [] } = useQuery(invitationListOptions(wsId));

  const [dingtalkQuery, setDingtalkQuery] = useState("");
  const [dingtalkResults, setDingtalkResults] = useState<DingTalkUser[]>([]);
  const [selectedDingtalkUsers, setSelectedDingtalkUsers] = useState<Record<string, DingTalkUser>>({});
  const [dingtalkMemberRole, setDingtalkMemberRole] = useState<MemberRole>("member");
  const [dingtalkSearchAttempted, setDingtalkSearchAttempted] = useState(false);
  const [dingtalkSearchError, setDingtalkSearchError] = useState<string | null>(null);
  const [dingtalkLoading, setDingtalkLoading] = useState(false);
  const [dingtalkActionLoading, setDingtalkActionLoading] = useState(false);
  const [dingtalkPickerOpen, setDingtalkPickerOpen] = useState(false);
  const [memberActionId, setMemberActionId] = useState<string | null>(null);
  const [invitationActionId, setInvitationActionId] = useState<string | null>(null);
  const [confirmAction, setConfirmAction] = useState<{
    title: string;
    description: string;
    variant?: "destructive";
    onConfirm: () => Promise<void>;
  } | null>(null);

  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const canManageWorkspace = currentMember?.role === "owner" || currentMember?.role === "admin";
  const isOwner = currentMember?.role === "owner";
  const ownerCount = members.filter((m) => m.role === "owner").length;
  const selectedDingtalkList = Object.values(selectedDingtalkUsers);
  const dingtalkEmptyTitle = dingtalkSearchError
    ? t(($) => $.members.dingtalk_search_error_title)
    : dingtalkSearchAttempted
      ? t(($) => $.members.dingtalk_no_results)
      : t(($) => $.members.dingtalk_picker_empty_title);
  const dingtalkEmptyDescription = dingtalkSearchError
    ? t(($) => $.members.dingtalk_search_error_hint)
    : dingtalkSearchAttempted
      ? t(($) => $.members.dingtalk_picker_no_results_hint)
      : t(($) => $.members.dingtalk_picker_empty_description);
  const dingtalkAddButtonLabel = dingtalkActionLoading
    ? t(($) => $.members.dingtalk_adding_workspace)
    : t(($) => $.members.dingtalk_add_workspace);

  useEffect(() => {
    setDingtalkQuery("");
    setDingtalkResults([]);
    setSelectedDingtalkUsers({});
    setDingtalkSearchAttempted(false);
    setDingtalkSearchError(null);
    setDingtalkPickerOpen(false);
  }, [workspace?.id]);

  const searchDingTalkUsers = useCallback(async (query: string) => {
    if (!workspace || !query.trim()) return;
    setDingtalkLoading(true);
    setDingtalkSearchAttempted(true);
    setDingtalkSearchError(null);
    try {
      const users = await api.searchDingTalkUsers(workspace.id, query.trim(), 20);
      setDingtalkResults(rankDingTalkUsers(users, query));
    } catch (e) {
      setDingtalkResults([]);
      setDingtalkSearchError(e instanceof Error ? e.message : t(($) => $.members.toast_dingtalk_search_failed));
    } finally {
      setDingtalkLoading(false);
    }
  }, [workspace, t]);

  const handleDingTalkSearch = async () => {
    await searchDingTalkUsers(dingtalkQuery);
  };

  useEffect(() => {
    if (!dingtalkPickerOpen) return;

    const query = dingtalkQuery.trim();
    if (!query) {
      setDingtalkLoading(false);
      setDingtalkSearchAttempted(false);
      setDingtalkResults([]);
      setDingtalkSearchError(null);
      return;
    }

    const timer = window.setTimeout(() => {
      void searchDingTalkUsers(query);
    }, 320);
    return () => window.clearTimeout(timer);
  }, [dingtalkPickerOpen, dingtalkQuery, searchDingTalkUsers]);

  const toggleDingTalkUser = (dingtalkUser: DingTalkUser) => {
    setSelectedDingtalkUsers((prev) => {
      const next = { ...prev };
      if (next[dingtalkUser.user_id]) {
        delete next[dingtalkUser.user_id];
      } else {
        next[dingtalkUser.user_id] = dingtalkUser;
      }
      return next;
    });
  };

  const handleAddDingTalkWorkspaceMembers = async () => {
    if (!workspace) return;
    const users = Object.values(selectedDingtalkUsers);
    if (users.length === 0) {
      toast.error(t(($) => $.members.toast_dingtalk_select_member));
      return;
    }

    setDingtalkActionLoading(true);
    try {
      const resp = await api.addDingTalkWorkspaceMembers(workspace.id, {
        users,
        role: dingtalkMemberRole,
      });
      qc.invalidateQueries({ queryKey: workspaceKeys.members(wsId) });
      const unresolved = resp.unresolved_user_ids?.length ?? 0;
      if (resp.added_count > 0 && unresolved > 0) {
        toast.warning(t(($) => $.members.toast_dingtalk_add_partial, { added: resp.added_count, skipped: unresolved }));
      } else if (resp.added_count > 0) {
        toast.success(t(($) => $.members.toast_dingtalk_add_success, { count: resp.added_count }));
      } else if (resp.already_member_count > 0 && unresolved === 0) {
        toast.success(t(($) => $.members.toast_dingtalk_already_members, { count: resp.already_member_count }));
      } else {
        toast.error(t(($) => $.members.toast_dingtalk_identity_missing, { count: unresolved || users.length }));
      }
      if (resp.added_count > 0 || (resp.already_member_count > 0 && unresolved === 0)) {
        setSelectedDingtalkUsers({});
        setDingtalkPickerOpen(false);
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.members.toast_dingtalk_add_failed));
    } finally {
      setDingtalkActionLoading(false);
    }
  };

  const handleRevokeInvitation = (invitation: Invitation) => {
    if (!workspace) return;
    setConfirmAction({
      title: t(($) => $.members.revoke_invitation_title),
      description: t(($) => $.members.revoke_invitation_description, { email: invitation.invitee_email }),
      variant: "destructive",
      onConfirm: async () => {
        setInvitationActionId(invitation.id);
        try {
          await api.revokeInvitation(workspace.id, invitation.id);
          qc.invalidateQueries({ queryKey: workspaceKeys.invitations(wsId) });
          toast.success(t(($) => $.members.toast_invitation_revoked));
        } catch (e) {
          toast.error(e instanceof Error ? e.message : t(($) => $.members.toast_invitation_revoke_failed));
        } finally {
          setInvitationActionId(null);
        }
      },
    });
  };

  const handleRoleChange = async (memberId: string, role: MemberRole) => {
    if (!workspace) return;
    setMemberActionId(memberId);
    try {
      await api.updateMember(workspace.id, memberId, { role });
      qc.invalidateQueries({ queryKey: workspaceKeys.members(wsId) });
      toast.success(t(($) => $.members.toast_role_updated));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.members.toast_role_failed));
    } finally {
      setMemberActionId(null);
    }
  };

  const handleRemoveMember = (member: MemberWithUser) => {
    if (!workspace) return;
    setConfirmAction({
      title: t(($) => $.members.remove_member_title, { name: member.name }),
      description: t(($) => $.members.remove_member_description, { name: member.name, workspace: workspace.name }),
      variant: "destructive",
      onConfirm: async () => {
        setMemberActionId(member.id);
        try {
          await api.deleteMember(workspace.id, member.id);
          qc.invalidateQueries({ queryKey: workspaceKeys.members(wsId) });
          toast.success(t(($) => $.members.toast_member_removed));
        } catch (e) {
          toast.error(e instanceof Error ? e.message : t(($) => $.members.toast_member_remove_failed));
        } finally {
          setMemberActionId(null);
        }
      },
    });
  };

  if (!workspace) return null;

  return (
    <SettingsTab title={t(($) => $.page.tabs.members)}>
      <SettingsSection title={t(($) => $.members.section_title, { count: members.length })}>

        {canManageWorkspace && (
          <Card>
            <CardContent>
              <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
                <div className="min-w-0 space-y-1">
                  <div className="flex items-center gap-2">
                    <UserPlus className="h-4 w-4 text-muted-foreground" />
                    <h3 className="text-sm font-medium">{t(($) => $.members.dingtalk_title)}</h3>
                  </div>
                  <p className="text-xs text-muted-foreground">{t(($) => $.members.dingtalk_description)}</p>
                </div>
                <Button onClick={() => setDingtalkPickerOpen(true)}>
                  <UserPlus className="h-4 w-4" />
                  {t(($) => $.members.dingtalk_open_picker)}
                </Button>
              </div>
            </CardContent>
          </Card>
        )}

        {members.length > 0 ? (
          <SettingsCard>
            {members.map((m) => (
              <div key={m.id}>
                <MemberRow
                  member={m}
                  canManage={canManageWorkspace}
                  canManageOwners={isOwner}
                  ownerCount={ownerCount}
                  isSelf={m.user_id === user?.id}
                  busy={memberActionId === m.id}
                  onRoleChange={(role) => handleRoleChange(m.id, role)}
                  onRemove={() => handleRemoveMember(m)}
                />
              </div>
            ))}
          </SettingsCard>
        ) : (
          <p className="text-sm text-muted-foreground">{t(($) => $.members.no_members)}</p>
        )}
      </SettingsSection>

      {invitations.length > 0 && (
        <SettingsSection title={t(($) => $.members.pending_title, { count: invitations.length })}>
          <SettingsCard>
            {invitations.map((inv) => (
              <div key={inv.id}>
                <InvitationRow
                  invitation={inv}
                  canManage={canManageWorkspace}
                  onRevoke={() => handleRevokeInvitation(inv)}
                  busy={invitationActionId === inv.id}
                />
              </div>
            ))}
          </SettingsCard>
        </SettingsSection>
      )}

      <Dialog open={dingtalkPickerOpen} onOpenChange={setDingtalkPickerOpen}>
        <DialogContent
          className="flex h-[min(680px,calc(100vh-2rem))] flex-col gap-0 overflow-hidden p-0 sm:max-w-xl"
          showCloseButton={false}
        >
          <div className="flex shrink-0 items-start justify-between gap-4 border-b px-6 py-5">
            <DialogHeader className="gap-1">
              <DialogTitle className="text-2xl font-semibold leading-tight">
                {t(($) => $.members.dingtalk_picker_title)}
              </DialogTitle>
              <DialogDescription className="sr-only">
                {t(($) => $.members.dingtalk_picker_description)}
              </DialogDescription>
            </DialogHeader>
            <Button
              variant="ghost"
              size="icon-sm"
              onClick={() => setDingtalkPickerOpen(false)}
              title={t(($) => $.members.dingtalk_picker_close)}
            >
              <X className="h-4 w-4" />
            </Button>
          </div>

          <div className="shrink-0 px-6 py-4">
            <div className="flex h-14 items-center gap-3 rounded-xl border border-border bg-background px-4 shadow-xs">
              <Search className="h-5 w-5 shrink-0 text-muted-foreground" />
              <input
                autoFocus
                value={dingtalkQuery}
                onChange={(e) => setDingtalkQuery(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && dingtalkQuery.trim()) handleDingTalkSearch();
                }}
                placeholder={t(($) => $.members.dingtalk_search_placeholder)}
                className="min-w-0 flex-1 bg-transparent text-base outline-none placeholder:text-muted-foreground"
              />
            </div>
          </div>

          <div className="min-h-0 flex-1 overflow-y-auto px-6 pb-4">
            <div className="mb-2 flex items-center justify-between gap-3">
              <div className="text-sm font-medium text-muted-foreground">
                {dingtalkQuery.trim()
                  ? t(($) => $.members.dingtalk_picker_results)
                  : t(($) => $.members.dingtalk_picker_suggestions)}
              </div>
              {dingtalkLoading && (
                <div className="text-xs text-muted-foreground">{t(($) => $.members.dingtalk_searching)}</div>
              )}
            </div>

            {dingtalkResults.length > 0 ? (
              <div className="space-y-1.5">
                {dingtalkResults.map((dingtalkUser) => (
                  <DingTalkUserRow
                    key={dingtalkUser.user_id}
                    user={dingtalkUser}
                    checked={!!selectedDingtalkUsers[dingtalkUser.user_id]}
                    onToggle={() => toggleDingTalkUser(dingtalkUser)}
                    userIdLabel={t(($) => $.members.dingtalk_user_id)}
                    identityLabel={t(($) => $.members.dingtalk_identity_badge)}
                  />
                ))}
              </div>
            ) : (
              <div className="flex min-h-64 flex-col items-center justify-center rounded-xl border border-dashed border-border px-6 py-10 text-center">
                <div className="flex h-10 w-10 items-center justify-center rounded-full bg-muted">
                  <Users className="h-5 w-5 text-muted-foreground" />
                </div>
                <div className="mt-3 text-sm font-medium">{dingtalkEmptyTitle}</div>
                <div className="mt-1 max-w-xs text-xs text-muted-foreground">{dingtalkEmptyDescription}</div>
              </div>
            )}
          </div>

          <div className="flex shrink-0 flex-col gap-3 border-t bg-muted/40 px-6 py-4 sm:flex-row sm:items-center sm:justify-between">
            <div className="text-sm text-muted-foreground">
              {t(($) => $.members.dingtalk_selected_count, { count: selectedDingtalkList.length })}
            </div>
            <div className="grid gap-2 sm:grid-cols-[120px_auto]">
              <Select value={dingtalkMemberRole} onValueChange={(value) => setDingtalkMemberRole(value as MemberRole)}>
                <SelectTrigger size="sm">
                  <SelectValue>{() => roleConfig[dingtalkMemberRole].label}</SelectValue>
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="member">{roleConfig.member.label}</SelectItem>
                  <SelectItem value="admin">{roleConfig.admin.label}</SelectItem>
                </SelectContent>
              </Select>
              <Button
                onClick={handleAddDingTalkWorkspaceMembers}
                disabled={dingtalkActionLoading || selectedDingtalkList.length === 0}
              >
                <UserPlus className="h-4 w-4" />
                {dingtalkAddButtonLabel}
              </Button>
            </div>
          </div>
        </DialogContent>
      </Dialog>

      <AlertDialog open={!!confirmAction} onOpenChange={(v) => { if (!v) setConfirmAction(null); }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{confirmAction?.title}</AlertDialogTitle>
            <AlertDialogDescription>{confirmAction?.description}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t(($) => $.members.confirm_cancel)}</AlertDialogCancel>
            <AlertDialogAction
              variant={confirmAction?.variant === "destructive" ? "destructive" : "default"}
              onClick={async () => {
                await confirmAction?.onConfirm();
                setConfirmAction(null);
              }}
            >
              {t(($) => $.members.confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}
