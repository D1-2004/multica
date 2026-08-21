"use client";

import { Card, CardContent } from "@multica/ui/components/ui/card";
import { KeyRound, LockKeyhole, Upload } from "lucide-react";
import { useT } from "../../i18n";
import { MCPSetupCard } from "./mcp-setup-card";
import { SettingsSection, SettingsTab } from "./settings-layout";

const USAGE_STEPS = [
  { key: "api_key", icon: KeyRound },
  { key: "import", icon: Upload },
  { key: "security", icon: LockKeyhole },
] as const;

export function MCPConnectionsTab() {
  const { t } = useT("settings");

  return (
    <SettingsTab
      title={t(($) => $.mcp.title)}
      description={t(($) => $.mcp.description)}
    >
      <MCPSetupCard />

      <SettingsSection title={t(($) => $.mcp.usage.title)}>
        <Card className="gap-0 py-0 shadow-none">
          <CardContent className="divide-y divide-surface-border px-0">
            {USAGE_STEPS.map(({ key, icon: Icon }, index) => (
              <div key={key} className="flex gap-3 px-4 py-3.5">
                <span className="flex size-8 shrink-0 items-center justify-center rounded-lg border border-surface-border bg-muted/30 text-muted-foreground">
                  <Icon className="size-4" />
                </span>
                <div className="min-w-0">
                  <p className="text-body font-medium">
                    {index + 1}. {t(($) => $.mcp.usage.steps[key].title)}
                  </p>
                  <p className="mt-0.5 text-caption leading-5 text-muted-foreground">
                    {t(($) => $.mcp.usage.steps[key].description)}
                  </p>
                </div>
              </div>
            ))}
          </CardContent>
        </Card>
      </SettingsSection>
    </SettingsTab>
  );
}
