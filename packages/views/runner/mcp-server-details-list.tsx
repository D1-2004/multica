"use client";

import { ChevronRight, Server, Wrench } from "lucide-react";
import type { RunnerMcpServer } from "@multica/core/runner";
import { Badge } from "@multica/ui/components/ui/badge";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@multica/ui/components/ui/collapsible";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";

export function McpServerDetailsList({
  servers,
  online,
  className,
}: {
  servers: RunnerMcpServer[];
  online: boolean;
  className?: string;
}) {
  const { t } = useT("common");

  return (
    <ul className={cn("divide-y overflow-hidden rounded-lg border bg-surface-raised/40", className)}>
      {servers.map((server) => {
        const capabilities = server.capabilities ?? [];
        const tools = server.tools ?? [];
        const detailsAvailable = server.detailStatus === "available";
        const displayTitle = server.title && server.title !== server.name
          ? server.title
          : null;
        return (
          <li key={server.name}>
            <Collapsible>
              <CollapsibleTrigger
                className="group flex w-full cursor-pointer items-start gap-3 px-3 py-3 text-left outline-none transition-colors hover:bg-accent/40 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
                aria-label={t(($) => $.mcp_details.open, { name: server.name })}
              >
                <span className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground">
                  <Server className="size-4" aria-hidden />
                </span>
                <span className="min-w-0 flex-1">
                  <span className="flex flex-wrap items-center gap-2">
                    <span className="truncate text-body font-medium">{server.name}</span>
                    <span
                      className={cn(
                        "size-1.5 shrink-0 rounded-full",
                        online && server.availability === "available"
                          ? "bg-success"
                          : "bg-muted-foreground/40",
                      )}
                      aria-hidden
                    />
                  </span>
                  {displayTitle ? (
                    <span className="mt-0.5 block text-caption text-foreground/80">{displayTitle}</span>
                  ) : null}
                  <span className="mt-0.5 block text-caption leading-5 text-muted-foreground">
                    {server.description || (detailsAvailable
                      ? t(($) => $.mcp_details.no_description)
                      : t(($) => $.mcp_details.details_unavailable))}
                  </span>
                  <span className="mt-1.5 flex flex-wrap gap-1.5">
                    <Badge variant="outline" className="text-micro uppercase">{server.transport}</Badge>
                    {server.version ? (
                      <Badge variant="outline" className="text-micro">
                        {t(($) => $.mcp_details.version, { version: server.version })}
                      </Badge>
                    ) : null}
                    {detailsAvailable ? (
                      <Badge variant="secondary" className="text-micro">
                        {t(($) => $.mcp_details.tool_count, { count: tools.length })}
                      </Badge>
                    ) : null}
                  </span>
                </span>
                <ChevronRight className="mt-2 size-4 shrink-0 text-muted-foreground transition-transform duration-200 group-data-[panel-open]:rotate-90" aria-hidden />
              </CollapsibleTrigger>
              <CollapsibleContent className="h-(--collapsible-panel-height) overflow-hidden transition-[height] duration-200 ease-out data-starting-style:h-0 data-ending-style:h-0">
                <div className="space-y-4 border-t bg-muted/10 px-4 py-4 pl-14">
                  {capabilities.length > 0 ? (
                    <div>
                      <p className="text-micro font-medium text-muted-foreground">{t(($) => $.mcp_details.capabilities)}</p>
                      <div className="mt-1.5 flex flex-wrap gap-1.5">
                        {capabilities.map((capability) => (
                          <Badge key={capability} variant="secondary" className="text-micro">{capability}</Badge>
                        ))}
                      </div>
                    </div>
                  ) : null}
                  {tools.length > 0 ? (
                    <div>
                      <p className="text-micro font-medium text-muted-foreground">{t(($) => $.mcp_details.tools)}</p>
                      <ul className="mt-2 space-y-2">
                        {tools.map((tool) => (
                          <li key={tool.name} className="flex items-start gap-2 rounded-md bg-background/70 px-3 py-2">
                            <Wrench className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" aria-hidden />
                            <div className="min-w-0">
                              <p className="break-all font-mono text-caption font-medium">{tool.name}</p>
                              {tool.title && tool.title !== tool.name ? (
                                <p className="mt-0.5 text-caption text-foreground/80">{tool.title}</p>
                              ) : null}
                              <p className="mt-0.5 text-caption leading-5 text-muted-foreground">
                                {tool.description || t(($) => $.mcp_details.no_tool_description)}
                              </p>
                            </div>
                          </li>
                        ))}
                      </ul>
                    </div>
                  ) : detailsAvailable ? (
                    <p className="text-caption text-muted-foreground">{t(($) => $.mcp_details.no_tools)}</p>
                  ) : (
                    <p className="text-caption text-muted-foreground">{t(($) => $.mcp_details.details_unavailable_hint)}</p>
                  )}
                </div>
              </CollapsibleContent>
            </Collapsible>
          </li>
        );
      })}
    </ul>
  );
}
