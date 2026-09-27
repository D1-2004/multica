"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { ArrowUpRight, BookOpenText, Check, Copy, Network } from "lucide-react";
import { useNavigation } from "../navigation";
import { useT } from "../i18n";

const toolLabels = {
  get_knowledge_graph_schema: "graph_schema",
  get_knowledge_node_schema: "node_schema",
  query_knowledge_cypher: "cypher",
  search_knowledge: "search",
} as const;

type ToolName = keyof typeof toolLabels;

export function SemanticaPage() {
  const { t } = useT("agents");
  const workspaceId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const status = useQuery({
    queryKey: ["workspaces", workspaceId, "semantica-mcp-relay"],
    queryFn: () => api.getSemanticaMCPStatus(workspaceId),
    enabled: !!workspaceId,
    refetchOnWindowFocus: true,
  });
  const [question, setQuestion] = useState(() => t(($) => $.semantica.example));
  const [copyFailed, setCopyFailed] = useState(false);
  const available = status.data?.available === true && !!status.data.agentId;

  async function copyAndOpenChat() {
    if (!available || !status.data?.agentId || !question.trim()) return;
    try {
      await navigator.clipboard.writeText(question.trim());
      setCopyFailed(false);
      navigation.push(paths.chatWithAgent(status.data.agentId));
    } catch {
      setCopyFailed(true);
    }
  }

  function toolLabel(name: string) {
    switch (name as ToolName) {
      case "get_knowledge_graph_schema": return t(($) => $.semantica.graph_schema);
      case "get_knowledge_node_schema": return t(($) => $.semantica.node_schema);
      case "query_knowledge_cypher": return t(($) => $.semantica.cypher);
      case "search_knowledge": return t(($) => $.semantica.search);
      default: return t(($) => $.semantica.other_tool);
    }
  }

  return <main className="mx-auto max-w-5xl space-y-8 px-6 py-8">
    <header className="space-y-3">
      <p className="text-caption font-medium text-muted-foreground">{t(($) => $.semantica.kicker)}</p>
      <h1 className="text-display-sm font-semibold tracking-tight">{t(($) => $.semantica.title)}</h1>
      <p className="max-w-2xl text-body text-muted-foreground">{t(($) => $.semantica.description)}</p>
    </header>

    <section className="rounded-2xl border border-border border-l-4 border-l-primary bg-card p-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="space-y-2">
          <h2 className="flex items-center gap-2 text-title font-semibold"><Network className="size-5" />{t(($) => $.semantica.agent_title)}</h2>
          <p className="text-body text-muted-foreground">
            {status.isLoading ? t(($) => $.semantica.loading) : status.isError ? t(($) => $.semantica.load_failed) : available ? status.data?.agentName ?? t(($) => $.semantica.agent_fallback) : t(($) => $.semantica.unavailable_hint)}
          </p>
        </div>
        <Badge variant={available ? "default" : "secondary"}>
          {available ? t(($) => $.semantica.available) : t(($) => $.semantica.unavailable)}
        </Badge>
      </div>
      <p className="mt-4 text-caption text-muted-foreground">{t(($) => $.semantica.status_note)}</p>
    </section>

    <section className="grid gap-5 md:grid-cols-[minmax(0,1fr)_minmax(0,1.05fr)]">
      <div className="space-y-4 rounded-2xl border border-border bg-card p-6">
        <h2 className="text-title font-semibold">{t(($) => $.semantica.use_title)}</h2>
        <ol className="space-y-4 text-body">
          <li><span className="font-semibold">1. {t(($) => $.semantica.step_agent)}</span><p className="mt-1 text-muted-foreground">{t(($) => $.semantica.step_agent_hint)}</p></li>
          <li><span className="font-semibold">2. {t(($) => $.semantica.step_ask)}</span><p className="mt-1 text-muted-foreground">{t(($) => $.semantica.step_ask_hint)}</p></li>
          <li><span className="font-semibold">3. {t(($) => $.semantica.step_result)}</span><p className="mt-1 text-muted-foreground">{t(($) => $.semantica.step_result_hint)}</p></li>
        </ol>
      </div>

      <div className="space-y-4 rounded-2xl border border-border bg-card p-6">
        <h2 className="text-title font-semibold">{t(($) => $.semantica.example_title)}</h2>
        <label className="block text-label font-medium" htmlFor="semantica-question">{t(($) => $.semantica.question_label)}</label>
        <textarea id="semantica-question" className="min-h-32 w-full resize-y rounded-lg border border-input bg-background p-3 text-body outline-none focus-visible:ring-2 focus-visible:ring-ring" value={question} onChange={(event) => setQuestion(event.target.value)} />
        <p className="text-caption text-muted-foreground">{t(($) => $.semantica.paste_hint)}</p>
        {copyFailed && <p role="alert" className="text-caption text-destructive">{t(($) => $.semantica.copy_failed)}</p>}
        <div className="flex flex-wrap gap-2">
          <Button onClick={copyAndOpenChat} disabled={!available || !question.trim()}><Copy className="mr-2 size-4" />{t(($) => $.semantica.copy_and_open)}</Button>
          {available && status.data?.agentId && <Button variant="outline" onClick={() => navigation.push(paths.chatWithAgent(status.data!.agentId!))}><ArrowUpRight className="mr-2 size-4" />{t(($) => $.semantica.open_chat)}</Button>}
        </div>
      </div>
    </section>

    <section className="space-y-3">
      <h2 className="flex items-center gap-2 text-title font-semibold"><BookOpenText className="size-5" />{t(($) => $.semantica.tools_title)}</h2>
      {available ? <div className="grid gap-3 sm:grid-cols-2">{status.data?.tools.map((tool) =>
        <div key={tool} className="rounded-xl border border-border bg-card p-4">
          <p className="flex items-center gap-2 text-body font-medium"><Check className="size-4 text-primary" />{toolLabel(tool)}</p>
          <code className="mt-1 block break-all text-caption text-muted-foreground">{tool}</code>
        </div>)}</div> : <p className="text-body text-muted-foreground">{t(($) => $.semantica.tools_unavailable)}</p>}
      <p className="text-caption text-muted-foreground">{t(($) => $.semantica.tool_note)}</p>
    </section>
  </main>;
}
