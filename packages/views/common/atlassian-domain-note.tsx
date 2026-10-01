"use client";

import { useState } from "react";
import { Check, Copy } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";

/** Domain an Atlassian org admin adds under Rovo → Rovo MCP server. */
export const ATLASSIAN_MCP_DOMAIN = "https://fde-workbench.dingtalk.com/**";

export const ATLASSIAN_DOMAIN_DOCS =
  "https://support.atlassian.com/security-and-access-policies/docs/control-atlassian-mcp-server-settings/";

export function AtlassianDomainNote({
  body,
  copyLabel,
  copiedLabel,
  docsLabel,
}: {
  body: string;
  copyLabel: string;
  copiedLabel: string;
  docsLabel: string;
}) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    await navigator.clipboard.writeText(ATLASSIAN_MCP_DOMAIN);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1500);
  };
  return (
    <div className="space-y-2 rounded-lg border border-surface-border bg-background px-3 py-3">
      <p className="text-body">{body}</p>
      <div className="flex flex-wrap items-center gap-2">
        <code className="break-all rounded bg-muted px-2 py-1 text-caption">{ATLASSIAN_MCP_DOMAIN}</code>
        <Button type="button" size="sm" variant="outline" onClick={() => void copy().catch(() => undefined)}>
          {copied ? <Check className="size-4" /> : <Copy className="size-4" />}
          {copied ? copiedLabel : copyLabel}
        </Button>
        <a className="text-caption underline" href={ATLASSIAN_DOMAIN_DOCS} target="_blank" rel="noreferrer">
          {docsLabel}
        </a>
      </div>
    </div>
  );
}
