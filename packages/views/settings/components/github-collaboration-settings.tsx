"use client";

import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { GitCommitHorizontal, Link2, PanelRight } from "lucide-react";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Label } from "@multica/ui/components/ui/label";
import { Switch } from "@multica/ui/components/ui/switch";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useCurrentMember } from "@multica/core/permissions";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { deriveGitHubSettings } from "@multica/core/github";
import { api } from "@multica/core/api";
import type { Workspace } from "@multica/core/types";
import { useT } from "../../i18n";
import { GitHubMark } from "./github-mark";

type SettingsKey =
  | "github_enabled"
  | "github_pr_sidebar_enabled"
  | "co_authored_by_enabled"
  | "github_auto_link_prs_enabled";

export function GitHubCollaborationSettings() {
  const { t } = useT("settings");
  const workspace = useCurrentWorkspace();
  const { role } = useCurrentMember(workspace?.id ?? "");
  const canManage = role === "owner" || role === "admin";
  const qc = useQueryClient();
  const flags = deriveGitHubSettings(workspace);
  const [savingKey, setSavingKey] = useState<SettingsKey | null>(null);

  async function persistSetting(key: SettingsKey, next: boolean) {
    if (!workspace || savingKey) return;
    setSavingKey(key);
    try {
      const merged = {
        ...((workspace.settings as Record<string, unknown>) ?? {}),
        [key]: next,
      };
      const updated = await api.updateWorkspace(workspace.id, { settings: merged });
      qc.setQueryData(workspaceKeys.list(), (old: Workspace[] | undefined) =>
        old?.map((ws) => (ws.id === updated.id ? updated : ws)),
      );
      toast.success(t(($) => $.auto_save.toast_saved), {
        id: "settings-auto-save",
      });
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.github.toast_failed));
    } finally {
      setSavingKey(null);
    }
  }

  if (!workspace) return null;

  return (
    <div className="space-y-6">
      <p className="text-body text-muted-foreground">{t(($) => $.repository_settings.collaboration_description)}</p>
      <section className="space-y-3">
        <Card>
          <CardContent>
            <div className="flex items-start justify-between gap-4">
              <div className="flex items-start gap-3">
                <div className="rounded-md border bg-muted/50 p-2 text-muted-foreground">
                  <GitHubMark className="h-4 w-4" />
                </div>
                <div className="space-y-1">
                  <Label htmlFor="github-master" className="text-body font-medium">
                    {t(($) => $.github.section_master)}
                  </Label>
                  <p className="text-body text-muted-foreground">
                    {flags.enabled
                      ? t(($) => $.github.master_description_on)
                      : t(($) => $.github.master_description_off)}
                  </p>
                </div>
              </div>
              <Switch
                id="github-master"
                checked={flags.enabled}
                onCheckedChange={(v) => persistSetting("github_enabled", v)}
                disabled={!canManage || savingKey === "github_enabled"}
              />
            </div>
          </CardContent>
        </Card>
      </section>
      <section className="space-y-3">
        <h2 className="text-body font-semibold">{t(($) => $.github.section_features)}</h2>
        <Card className="gap-0 py-0">
          <CardContent className="divide-y divide-surface-border px-0">
            <FeatureRow
              id="github-pr-sidebar"
              icon={<PanelRight className="h-4 w-4" />}
              label={t(($) => $.github.feature_pr_sidebar_label)}
              description={
                <p className="text-body text-muted-foreground">
                  {t(($) => $.github.feature_pr_sidebar_description)}
                </p>
              }
              checked={flags.prSidebar}
              disabled={!canManage || !flags.enabled || savingKey === "github_pr_sidebar_enabled"}
              onCheckedChange={(v) => persistSetting("github_pr_sidebar_enabled", v)}
            />

            <FeatureRow
              id="github-coauthor"
              icon={<GitCommitHorizontal className="h-4 w-4" />}
              label={t(($) => $.github.feature_co_author_label)}
              description={
                <p className="text-body text-muted-foreground">
                  {t(($) => $.github.feature_co_author_description_prefix)}{" "}
                  <code className="rounded bg-muted px-1 py-0.5 text-caption">
                    {"Co-authored-by: multica-agent <github@multica.ai>"}
                  </code>{" "}
                  {t(($) => $.github.feature_co_author_description_suffix)}
                </p>
              }
              checked={flags.coAuthor}
              disabled={!canManage || !flags.enabled || savingKey === "co_authored_by_enabled"}
              onCheckedChange={(v) => persistSetting("co_authored_by_enabled", v)}
            />

            <FeatureRow
              id="github-auto-link"
              icon={<Link2 className="h-4 w-4" />}
              label={t(($) => $.github.feature_auto_link_label)}
              description={
                <p className="text-body text-muted-foreground">
                  {t(($) => $.github.feature_auto_link_description)}
                </p>
              }
              checked={flags.autoLinkPRs}
              disabled={!canManage || !flags.enabled || savingKey === "github_auto_link_prs_enabled"}
              onCheckedChange={(v) => persistSetting("github_auto_link_prs_enabled", v)}
            />
          </CardContent>
        </Card>
      </section>
    </div>
  );
}

function FeatureRow({
  id,
  icon,
  label,
  description,
  checked,
  disabled,
  onCheckedChange,
}: {
  id: string;
  icon: React.ReactNode;
  label: string;
  description: React.ReactNode;
  checked: boolean;
  disabled: boolean;
  onCheckedChange: (v: boolean) => void;
}) {
  return (
    <div className="flex items-start justify-between gap-4 px-4 py-3.5">
      <div className="flex items-start gap-3">
        <div className="rounded-md border bg-muted/50 p-2 text-muted-foreground">{icon}</div>
        <div className="space-y-1">
          <Label htmlFor={id} className="text-body font-medium">
            {label}
          </Label>
          {description}
        </div>
      </div>
      <Switch
        id={id}
        checked={checked}
        disabled={disabled}
        onCheckedChange={onCheckedChange}
      />
    </div>
  );
}
