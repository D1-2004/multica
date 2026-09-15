"use client";

import { useWorkspaceId } from "@multica/core/hooks";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@multica/ui/components/ui/tabs";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { SettingsTab } from "./settings-layout";
import { RepositoriesTab } from "./repositories-tab";
import { RepositoryConnections } from "./repository-connections";
import { GitHubCollaborationSettings } from "./github-collaboration-settings";

const SECTIONS = ["repositories", "connections", "collaboration"] as const;

export function RepositorySettings() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const navigation = useNavigation();
  const section = navigation.searchParams.get("section");
  const previousTab = navigation.searchParams.get("tab");
  const activeSection = SECTIONS.find((value) => value === section)
    ?? (previousTab === "git" || previousTab === "github" ? "connections" : "repositories");

  const changeSection = (value: string) => {
    const params = new URLSearchParams(navigation.searchParams);
    params.set("tab", "repositories");
    params.set("section", value);
    navigation.replace(`${navigation.pathname}?${params.toString()}`);
  };

  return (
    <SettingsTab title={t(($) => $.page.tabs.repositories)} description={t(($) => $.repository_settings.description)}>
      <Tabs value={activeSection} onValueChange={changeSection} className="min-w-0 gap-6">
        <TabsList variant="line" className="max-w-full overflow-x-auto">
          {SECTIONS.map((value) => <TabsTrigger key={value} value={value} className="shrink-0">
            {t(($) => $.repository_settings.sections[value])}
          </TabsTrigger>)}
        </TabsList>
        <TabsContent value="repositories"><RepositoriesTab key={wsId} /></TabsContent>
        <TabsContent value="connections"><RepositoryConnections key={wsId} /></TabsContent>
        <TabsContent value="collaboration"><GitHubCollaborationSettings key={wsId} /></TabsContent>
      </Tabs>
    </SettingsTab>
  );
}
