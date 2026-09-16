"use client";

import { GitHubConnectionSection } from "./github-connection-section";
import { useT } from "../../i18n";

export function RepositoryConnections() {
  const { t } = useT("settings");
  return (
    <div className="space-y-5">
      <p className="text-body text-muted-foreground">{t(($) => $.git_repo.description)}</p>
      <GitHubConnectionSection />
    </div>
  );
}
