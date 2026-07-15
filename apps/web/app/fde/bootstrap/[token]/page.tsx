"use client";

import { Suspense, useEffect, useRef, useState } from "react";
import { useParams, useSearchParams } from "next/navigation";
import { api } from "@multica/core/api";
import { Button } from "@multica/ui/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@multica/ui/components/ui/card";
import { CheckCircle2, Loader2 } from "lucide-react";

type PageState =
  | { kind: "loading"; message: string }
  | { kind: "ready" }
  | { kind: "expired" }
  | { kind: "error"; message: string; retry: "auth" | "complete" | null };

function startDingTalkAuthentication(token: string, clientID: string) {
  const next = `/fde/bootstrap/${encodeURIComponent(token)}?authenticated=1`;
  const params = new URLSearchParams({
    client_id: clientID,
    redirect_uri: `${window.location.origin}/auth/callback`,
    response_type: "code",
    scope: "openid",
    prompt: "consent",
    state: `provider:dingtalk,next:${next}`,
  });
  window.location.href = `https://login.dingtalk.com/oauth2/auth?${params}`;
}

async function completeFDEWorkspace(
  token: string,
  setState: (state: PageState) => void,
) {
  try {
    await api.completeFDEBootstrapIntent(token);
    setState({ kind: "ready" });
  } catch (error: unknown) {
    const message = error instanceof Error ? error.message : "创建失败，请稍后重试";
    const lower = message.toLowerCase();
    if (lower.includes("expired") || lower.includes("过期")) {
      setState({ kind: "expired" });
      return;
    }
    const mismatch = lower.includes("different dingtalk") || lower.includes("belongs to");
    setState({
      kind: "error",
      message: mismatch ? "当前钉钉账号不是这个链接所绑定的用户" : message,
      retry: mismatch
        ? null
        : lower.includes("authentication") || lower.includes("unauthorized")
          ? "auth"
          : "complete",
    });
  }
}

function FDEBootstrapContent() {
  const params = useParams<{ token: string }>();
  const searchParams = useSearchParams();
  const token = typeof params.token === "string" ? params.token : "";
  const authenticated = searchParams.get("authenticated") === "1";
  const started = useRef(false);
  const [state, setState] = useState<PageState>({
    kind: "loading",
    message: authenticated ? "正在创建工作空间…" : "正在验证链接…",
  });

  useEffect(() => {
    if (started.current) return;
    started.current = true;

    if (!token) {
      setState({ kind: "expired" });
      return;
    }

    if (authenticated) {
      void completeFDEWorkspace(token, setState);
      return;
    }

    Promise.all([api.getFDEBootstrapIntentStatus(token), api.getConfig()])
      .then(([intent, config]) => {
        if (intent.status === "expired") {
          setState({ kind: "expired" });
          return;
        }
        if (!config.dingtalk_client_id) {
          setState({ kind: "error", message: "钉钉认证暂不可用", retry: null });
          return;
        }
        setState({ kind: "loading", message: "正在打开钉钉认证…" });
        startDingTalkAuthentication(token, config.dingtalk_client_id);
      })
      .catch(() => {
        setState({ kind: "error", message: "暂时无法验证链接，请稍后重试", retry: null });
      });
  }, [authenticated, token]);

  const retryAuthentication = async () => {
    setState({ kind: "loading", message: "正在打开钉钉认证…" });
    try {
      const config = await api.getConfig();
      if (!config.dingtalk_client_id) throw new Error("missing DingTalk client id");
      startDingTalkAuthentication(token, config.dingtalk_client_id);
    } catch {
      setState({ kind: "error", message: "钉钉认证暂不可用", retry: null });
    }
  };

  const retryCompletion = () => {
    setState({ kind: "loading", message: "正在创建工作空间…" });
    void completeFDEWorkspace(token, setState);
  };

  return (
    <main className="flex min-h-screen items-center justify-center bg-muted/30 p-6">
      <Card className="w-full max-w-md">
        <CardHeader className="text-center">
          {state.kind === "ready" ? (
            <CheckCircle2 className="mx-auto mb-2 size-10 text-emerald-600" aria-hidden="true" />
          ) : state.kind === "loading" ? (
            <Loader2 className="mx-auto mb-2 size-8 animate-spin text-muted-foreground" aria-hidden="true" />
          ) : null}
          <CardTitle>
            {state.kind === "ready"
              ? "创建完成"
              : state.kind === "expired"
                ? "链接已失效"
                : state.kind === "error"
                  ? "暂时无法创建"
                  : "正在处理"}
          </CardTitle>
          <CardDescription>
            {state.kind === "ready"
              ? "创建完成，可以返回钉钉"
              : state.kind === "expired"
                ? "请返回钉钉重新获取创建链接"
                : state.kind === "error"
                  ? state.message
                  : state.message}
          </CardDescription>
        </CardHeader>
        {state.kind === "error" && state.retry ? (
          <CardContent className="flex justify-center">
            <Button onClick={state.retry === "auth" ? retryAuthentication : retryCompletion}>
              {state.retry === "auth" ? "重新认证" : "重试创建"}
            </Button>
          </CardContent>
        ) : null}
      </Card>
    </main>
  );
}

export default function FDEBootstrapPage() {
  return (
    <Suspense fallback={null}>
      <FDEBootstrapContent />
    </Suspense>
  );
}
