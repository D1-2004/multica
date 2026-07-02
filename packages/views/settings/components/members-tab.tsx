"use client";

import { useEffect, useState } from "react";
import { Crown, Shield, User, Plus, MoreHorizontal, UserMinus, Users, Clock, X, Mail, Search, Save, UserPlus, MessageCircle } from "lucide-react";
import { ActorAvatar } from "../../common/actor-avatar";
import type { MemberWithUser, MemberRole, Invitation, DingTalkUser, Workspace } from "@multica/core/types";
import { Input } from "@multica/ui/components/ui/input";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Badge } from "@multica/ui/components/ui/badge";
import { Avatar, AvatarFallback, AvatarImage } from "@multica/ui/components/ui/avatar";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
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

const ROLE_ICONS: Record<MemberRole, typeof Crown> = {
  owner: Crown,
  admin: Shield,
  member: User,
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value);
}

function getDingTalkChatId(settings: Record<string, unknown> | undefined): string {
  const dingtalk = settings?.dingtalk;
  if (isRecord(dingtalk) && typeof dingtalk.chat_id === "string") {
    return dingtalk.chat_id;
  }
  const flatChatId = settings?.dingtalk_chat_id ?? settings?.dingtalk_group_chat_id;
  return typeof flatChatId === "string" ? flatChatId : "";
}

function withDingTalkChatId(settings: Record<string, unknown>, chatId: string): Record<string, unknown> {
  const dingtalk = isRecord(settings.dingtalk) ? settings.dingtalk : {};
  return {
    ...settings,
    dingtalk: {
      ...dingtalk,
      chat_id: chatId.trim(),
    },
  };
}

function initials(name: string): string {
  const chars = Array.from(name.trim() || "?");
  return chars.slice(0, 2).join("").toUpperCase();
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
      <ActorAvatar actorType="member" actorId={member.user_id} size={32} />
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
  noEmailLabel,
}: {
  user: DingTalkUser;
  checked: boolean;
  onToggle: () => void;
  userIdLabel: string;
  noEmailLabel: string;
}) {
  const meta = [
    user.title,
    user.email || user.mobile,
    `${userIdLabel}: ${user.user_id}`,
  ].filter(Boolean).join(" · ");

  return (
    <label className="flex cursor-pointer items-center gap-3 px-4 py-3 hover:bg-accent/40">
      <Checkbox checked={checked} onCheckedChange={onToggle} />
      <Avatar size="sm">
        {user.avatar_url && <AvatarImage src={user.avatar_url} alt={user.name} />}
        <AvatarFallback>{initials(user.name || user.user_id)}</AvatarFallback>
      </Avatar>
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-2">
          <span className="truncate text-sm font-medium">{user.name || user.user_id}</span>
          {!user.email && (
            <Badge variant="outline" className="shrink-0">
              {noEmailLabel}
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

  const [inviteEmail, setInviteEmail] = useState("");
  const [inviteRole, setInviteRole] = useState<MemberRole>("member");
  const [inviteLoading, setInviteLoading] = useState(false);
  const [dingtalkQuery, setDingtalkQuery] = useState("");
  const [dingtalkResults, setDingtalkResults] = useState<DingTalkUser[]>([]);
  const [selectedDingtalkUsers, setSelectedDingtalkUsers] = useState<Record<string, DingTalkUser>>({});
  const [dingtalkInviteRole, setDingtalkInviteRole] = useState<MemberRole>("member");
  const [dingtalkGroupChatId, setDingtalkGroupChatId] = useState("");
  const [dingtalkSearchAttempted, setDingtalkSearchAttempted] = useState(false);
  const [dingtalkLoading, setDingtalkLoading] = useState(false);
  const [dingtalkConfigSaving, setDingtalkConfigSaving] = useState(false);
  const [dingtalkActionLoading, setDingtalkActionLoading] = useState<"workspace" | "group" | null>(null);
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
  const savedDingTalkChatId = getDingTalkChatId(workspace?.settings);
  const dingtalkGroupChatIdValue = dingtalkGroupChatId.trim();
  const dingtalkGroupDirty = dingtalkGroupChatIdValue !== savedDingTalkChatId.trim();

  useEffect(() => {
    setDingtalkGroupChatId(getDingTalkChatId(workspace?.settings));
    setDingtalkQuery("");
    setDingtalkResults([]);
    setSelectedDingtalkUsers({});
    setDingtalkSearchAttempted(false);
  }, [workspace?.id]);

  const handleInviteMember = async () => {
    if (!workspace) return;
    setInviteLoading(true);
    try {
      await api.createMember(workspace.id, {
        email: inviteEmail,
        role: inviteRole,
      });
      setInviteEmail("");
      setInviteRole("member");
      qc.invalidateQueries({ queryKey: workspaceKeys.invitations(wsId) });
      toast.success(t(($) => $.members.toast_invitation_sent));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.members.toast_invitation_failed));
    } finally {
      setInviteLoading(false);
    }
  };

  const handleDingTalkSearch = async () => {
    if (!workspace || !dingtalkQuery.trim()) return;
    setDingtalkLoading(true);
    setDingtalkSearchAttempted(true);
    try {
      const users = await api.searchDingTalkUsers(workspace.id, dingtalkQuery.trim(), 10);
      setDingtalkResults(users);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.members.toast_dingtalk_search_failed));
    } finally {
      setDingtalkLoading(false);
    }
  };

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

  const handleSaveDingTalkGroup = async () => {
    if (!workspace) return;
    setDingtalkConfigSaving(true);
    try {
      const updated = await api.updateWorkspace(workspace.id, {
        settings: withDingTalkChatId(workspace.settings ?? {}, dingtalkGroupChatId),
      });
      qc.setQueryData<Workspace[]>(workspaceKeys.list(), (old) =>
        old?.map((w) => (w.id === updated.id ? updated : w)) ?? old,
      );
      qc.invalidateQueries({ queryKey: workspaceKeys.list() });
      toast.success(t(($) => $.members.toast_dingtalk_group_saved));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.members.toast_dingtalk_group_save_failed));
    } finally {
      setDingtalkConfigSaving(false);
    }
  };

  const handleInviteDingTalkSelected = async () => {
    if (!workspace) return;
    const users = Object.values(selectedDingtalkUsers);
    if (users.length === 0) {
      toast.error(t(($) => $.members.toast_dingtalk_select_member));
      return;
    }

    setDingtalkActionLoading("workspace");
    let sent = 0;
    let skipped = 0;
    let failed = 0;
    try {
      for (const dingtalkUser of users) {
        const email = dingtalkUser.email?.trim();
        if (!email) {
          skipped += 1;
          continue;
        }
        try {
          await api.createMember(workspace.id, {
            email,
            role: dingtalkInviteRole,
          });
          sent += 1;
        } catch {
          failed += 1;
        }
      }
      qc.invalidateQueries({ queryKey: workspaceKeys.invitations(wsId) });
      if (sent === 0) {
        toast.error(skipped === users.length ? t(($) => $.members.toast_dingtalk_missing_email) : t(($) => $.members.toast_dingtalk_invite_failed));
      } else if (skipped > 0 || failed > 0) {
        toast.warning(t(($) => $.members.toast_dingtalk_invite_partial, { sent, skipped: skipped + failed }));
      } else {
        toast.success(t(($) => $.members.toast_dingtalk_invite_sent, { count: sent }));
      }
    } finally {
      setDingtalkActionLoading(null);
    }
  };

  const handleAddDingTalkGroupMembers = async () => {
    if (!workspace) return;
    const userIds = Object.keys(selectedDingtalkUsers);
    if (userIds.length === 0) {
      toast.error(t(($) => $.members.toast_dingtalk_select_member));
      return;
    }
    if (!dingtalkGroupChatIdValue) {
      toast.error(t(($) => $.members.toast_dingtalk_group_missing));
      return;
    }

    setDingtalkActionLoading("group");
    try {
      const resp = await api.addDingTalkGroupMembers(workspace.id, {
        chat_id: dingtalkGroupChatIdValue,
        user_ids: userIds,
      });
      toast.success(t(($) => $.members.toast_dingtalk_group_added, { count: resp.added_user_ids.length }));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.members.toast_dingtalk_group_failed));
    } finally {
      setDingtalkActionLoading(null);
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
    <div className="space-y-8">
      <section className="space-y-4">
        <div className="flex items-center gap-2">
          <Users className="h-4 w-4 text-muted-foreground" />
          <h2 className="text-sm font-semibold">{t(($) => $.members.section_title, { count: members.length })}</h2>
        </div>

        {canManageWorkspace && (
          <Card>
            <CardContent className="space-y-3">
              <div className="flex items-center gap-2">
                <Plus className="h-4 w-4 text-muted-foreground" />
                <h3 className="text-sm font-medium">{t(($) => $.members.invite_title)}</h3>
              </div>
              <div className="grid gap-3 sm:grid-cols-[1fr_120px_auto]">
                <Input
                  type="email"
                  value={inviteEmail}
                  onChange={(e) => setInviteEmail(e.target.value)}
                  placeholder={t(($) => $.members.invite_email_placeholder)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && inviteEmail.trim()) handleInviteMember();
                  }}
                />
                <Select value={inviteRole} onValueChange={(value) => setInviteRole(value as MemberRole)}>
                  <SelectTrigger size="sm">
                    <SelectValue>{() => roleConfig[inviteRole].label}</SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="member">{roleConfig.member.label}</SelectItem>
                    <SelectItem value="admin">{roleConfig.admin.label}</SelectItem>
                  </SelectContent>
                </Select>
                <Button
                  onClick={handleInviteMember}
                  disabled={inviteLoading || !inviteEmail.trim()}
                >
                  {inviteLoading ? t(($) => $.members.inviting) : t(($) => $.members.invite_button)}
                </Button>
              </div>
            </CardContent>
          </Card>
        )}

        {canManageWorkspace && (
          <Card>
            <CardContent className="space-y-4">
              <div className="space-y-1">
                <div className="flex items-center gap-2">
                  <MessageCircle className="h-4 w-4 text-muted-foreground" />
                  <h3 className="text-sm font-medium">{t(($) => $.members.dingtalk_title)}</h3>
                </div>
                <p className="text-xs text-muted-foreground">{t(($) => $.members.dingtalk_description)}</p>
              </div>

              <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto]">
                <Input
                  value={dingtalkGroupChatId}
                  onChange={(e) => setDingtalkGroupChatId(e.target.value)}
                  placeholder={t(($) => $.members.dingtalk_chat_id_placeholder)}
                />
                <Button
                  variant="outline"
                  onClick={handleSaveDingTalkGroup}
                  disabled={dingtalkConfigSaving || !dingtalkGroupDirty}
                >
                  <Save className="h-4 w-4" />
                  {dingtalkConfigSaving ? t(($) => $.members.dingtalk_saving_group) : t(($) => $.members.dingtalk_save_group)}
                </Button>
              </div>

              <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto]">
                <Input
                  value={dingtalkQuery}
                  onChange={(e) => setDingtalkQuery(e.target.value)}
                  placeholder={t(($) => $.members.dingtalk_search_placeholder)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && dingtalkQuery.trim()) handleDingTalkSearch();
                  }}
                />
                <Button
                  variant="secondary"
                  onClick={handleDingTalkSearch}
                  disabled={dingtalkLoading || !dingtalkQuery.trim()}
                >
                  <Search className="h-4 w-4" />
                  {dingtalkLoading ? t(($) => $.members.dingtalk_searching) : t(($) => $.members.dingtalk_search)}
                </Button>
              </div>

              <div className="overflow-hidden rounded-lg border border-border/70">
                {dingtalkResults.length > 0 ? (
                  <div className="max-h-72 divide-y divide-border/60 overflow-y-auto">
                    {dingtalkResults.map((dingtalkUser) => (
                      <DingTalkUserRow
                        key={dingtalkUser.user_id}
                        user={dingtalkUser}
                        checked={!!selectedDingtalkUsers[dingtalkUser.user_id]}
                        onToggle={() => toggleDingTalkUser(dingtalkUser)}
                        userIdLabel={t(($) => $.members.dingtalk_user_id)}
                        noEmailLabel={t(($) => $.members.dingtalk_no_email_badge)}
                      />
                    ))}
                  </div>
                ) : (
                  <div className="px-4 py-5 text-sm text-muted-foreground">
                    {dingtalkSearchAttempted ? t(($) => $.members.dingtalk_no_results) : t(($) => $.members.dingtalk_search_hint)}
                  </div>
                )}
              </div>

              <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                <div className="text-xs text-muted-foreground">
                  {t(($) => $.members.dingtalk_selected_count, { count: selectedDingtalkList.length })}
                </div>
                <div className="grid gap-2 sm:grid-cols-[120px_auto_auto]">
                  <Select value={dingtalkInviteRole} onValueChange={(value) => setDingtalkInviteRole(value as MemberRole)}>
                    <SelectTrigger size="sm">
                      <SelectValue>{() => roleConfig[dingtalkInviteRole].label}</SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="member">{roleConfig.member.label}</SelectItem>
                      <SelectItem value="admin">{roleConfig.admin.label}</SelectItem>
                    </SelectContent>
                  </Select>
                  <Button
                    variant="outline"
                    onClick={handleInviteDingTalkSelected}
                    disabled={dingtalkActionLoading !== null || selectedDingtalkList.length === 0}
                  >
                    <UserPlus className="h-4 w-4" />
                    {t(($) => $.members.dingtalk_invite_workspace)}
                  </Button>
                  <Button
                    onClick={handleAddDingTalkGroupMembers}
                    disabled={dingtalkActionLoading !== null || selectedDingtalkList.length === 0 || !dingtalkGroupChatIdValue}
                  >
                    <MessageCircle className="h-4 w-4" />
                    {t(($) => $.members.dingtalk_add_group)}
                  </Button>
                </div>
              </div>
            </CardContent>
          </Card>
        )}

        {members.length > 0 ? (
          <div className="overflow-hidden rounded-xl ring-1 ring-foreground/10">
            {members.map((m, i) => (
              <div key={m.id} className={i > 0 ? "border-t border-border/50" : ""}>
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
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">{t(($) => $.members.no_members)}</p>
        )}
      </section>

      {invitations.length > 0 && (
        <section className="space-y-4">
          <div className="flex items-center gap-2">
            <Clock className="h-4 w-4 text-muted-foreground" />
            <h2 className="text-sm font-semibold">{t(($) => $.members.pending_title, { count: invitations.length })}</h2>
          </div>
          <div className="overflow-hidden rounded-xl ring-1 ring-foreground/10">
            {invitations.map((inv, i) => (
              <div key={inv.id} className={i > 0 ? "border-t border-border/50" : ""}>
                <InvitationRow
                  invitation={inv}
                  canManage={canManageWorkspace}
                  onRevoke={() => handleRevokeInvitation(inv)}
                  busy={invitationActionId === inv.id}
                />
              </div>
            ))}
          </div>
        </section>
      )}

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
    </div>
  );
}
