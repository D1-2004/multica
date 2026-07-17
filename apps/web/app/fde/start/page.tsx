"use client";

import { Suspense, useCallback, useEffect, useRef, useState } from "react";
import { useSearchParams } from "next/navigation";
import { AlertCircle, CheckCircle2, Loader2 } from "lucide-react";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import type {
  BeginDingTalkInstallResponse,
  ProvisionFDEOnboardingResponse,
} from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@multica/ui/components/ui/card";
import { Input } from "@multica/ui/components/ui/input";
import { closeDingTalkPage, openDingTalkInstallPage, replaceCurrentPage } from "./navigation";

const oauthStateKey = "multica_fde_oauth_state";
const oauthCompleteKey = "multica_fde_dingtalk_authenticated";

type Stage = "auth" | "loading" | "create" | "provision" | "install" | "done" | "error";

function FDEStartContent() {
  const searchParams = useSearchParams();
  const setUser = useAuthStore((state) => state.setUser);
  const [stage, setStage] = useState<Stage>("auth");
  const [error, setError] = useState("");
  const [workspaceName, setWorkspaceName] = useState("");
  const [result, setResult] = useState<ProvisionFDEOnboardingResponse | null>(null);
  const [install, setInstall] = useState<BeginDingTalkInstallResponse | null>(null);
  const booted = useRef(false);
  const openedInstall = useRef(false);

  const fail = useCallback((message: string) => {
    setError(message);
    setStage("error");
  }, []);

  const provision = useCallback(async (input: { workspace_id?: string; workspace_name?: string }) => {
    setStage("provision");
    setError("");
    try {
      const response = await api.provisionFDEOnboarding(input);
      setResult(response);
      if (response.install_complete) {
        setStage("done");
        return;
      }
      if (!response.install) throw new Error("未能启动钉钉机器人创建流程");
      setInstall(response.install);
      setStage("install");
    } catch (cause) {
      fail(cause instanceof Error ? cause.message : "开通失败，请重试");
    }
  }, [fail]);

  useEffect(() => {
    if (booted.current) return;
    booted.current = true;
    const run = async () => {
      const beginOAuth = async () => {
        const config = await api.getConfig();
        if (!config.dingtalk_client_id) throw new Error("钉钉登录尚未配置");
        const stateValue = crypto.randomUUID();
        sessionStorage.setItem(oauthStateKey, stateValue);
        localStorage.setItem(oauthStateKey, stateValue);
        const params = new URLSearchParams({
          client_id: config.dingtalk_client_id,
          redirect_uri: `${window.location.origin}/fde/start`,
          response_type: "code",
          scope: "openid",
          prompt: "consent",
          state: stateValue,
        });
        replaceCurrentPage(`https://login.dingtalk.com/oauth2/auth?${params}`);
      };

      const code = searchParams.get("authCode") || searchParams.get("code");
      const returnedState = searchParams.get("state") || "";
      const oauthError = searchParams.get("error");
      if (oauthError) {
        fail(oauthError === "access_denied" ? "你已取消钉钉授权" : oauthError);
        return;
      }
      if (code) {
        const sessionState = sessionStorage.getItem(oauthStateKey);
        const durableState = localStorage.getItem(oauthStateKey);
        const stateMatches = returnedState !== ""
          && (returnedState === sessionState || returnedState === durableState);
        if (!stateMatches) {
          sessionStorage.removeItem(oauthStateKey);
          localStorage.removeItem(oauthStateKey);
          window.history.replaceState({}, "", "/fde/start");
          try {
            await beginOAuth();
          } catch (cause) {
            fail(cause instanceof Error ? cause.message : "无法启动钉钉登录");
          }
          return;
        }
        setStage("auth");
        try {
          const login = await api.fdeDingtalkLogin(code);
          api.setToken(login.token);
          setUser(login.user);
          sessionStorage.removeItem(oauthStateKey);
          localStorage.removeItem(oauthStateKey);
          sessionStorage.setItem(oauthCompleteKey, "1");
          window.history.replaceState({}, "", "/fde/start");
        } catch (cause) {
          fail(cause instanceof Error ? cause.message : "钉钉登录失败");
          return;
        }
      } else if (sessionStorage.getItem(oauthCompleteKey) !== "1") {
        try {
          await beginOAuth();
          return;
        } catch (cause) {
          fail(cause instanceof Error ? cause.message : "无法启动钉钉登录");
          return;
        }
      }

      setStage("loading");
      try {
        const onboarding = await api.getFDEOnboarding();
        if (!onboarding.configured) throw new Error("FDE 开通服务尚未配置完整");
        if (onboarding.dedicated !== true) throw new Error("FDE 开通服务正在升级，请稍后重试");
        if (onboarding.workspaces.length > 1) throw new Error("FDE 开通状态异常，请稍后重试");
        if (onboarding.workspaces.length === 1) {
          await provision({ workspace_id: onboarding.workspaces[0]?.id });
        } else {
          setStage("create");
        }
      } catch (cause) {
        fail(cause instanceof Error ? cause.message : "无法加载开通状态");
      }
    };
    void run();
  }, [fail, provision, searchParams, setUser]);

  useEffect(() => {
    if (stage !== "install" || !install || !result) return;
    if (!openedInstall.current) {
      openedInstall.current = true;
      void openDingTalkInstallPage(install.qr_code_url);
    }
    const interval = window.setInterval(async () => {
      try {
        const status = await api.getDingTalkInstallStatus(result.workspace.id, install.session_id);
        if (status.status === "success") {
          window.clearInterval(interval);
          setStage("done");
        } else if (status.status === "error") {
          window.clearInterval(interval);
          fail(status.error_message || "钉钉机器人创建失败，请重试");
        }
      } catch {
        // A transient mobile-network failure must not abandon the device flow.
      }
    }, Math.max(2, install.poll_interval_seconds) * 1000);
    return () => window.clearInterval(interval);
  }, [fail, install, result, stage]);

  useEffect(() => {
    if (stage === "done") sessionStorage.removeItem(oauthCompleteKey);
  }, [stage]);

  const submit = () => {
    if (!workspaceName.trim()) return;
    void provision({ workspace_name: workspaceName.trim() });
  };

  return (
    <main className="min-h-dvh bg-gradient-to-b from-sky-50 to-white px-4 py-8 text-slate-950 sm:flex sm:items-center sm:justify-center">
      <Card className="mx-auto w-full max-w-md border-sky-100 shadow-lg shadow-sky-100/60">
        <CardHeader className="space-y-3 text-center">
          <div className="mx-auto flex size-12 items-center justify-center rounded-2xl bg-sky-600 text-lg font-bold text-white">FDE</div>
          <CardTitle className="text-2xl">{stage === "done" ? "FDE 开发者工作空间已就绪" : "创建专属 FDE 开发者工作空间"}</CardTitle>
          <CardDescription>{stage === "done" ? "本次 FDE 初始化已经完成。" : "我们将新建一个独立工作区，并自动创建 FDE 智能体、绑定钉钉机器人。不会修改你已有的工作区。"}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-5">
          {(["auth", "loading", "provision"] as Stage[]).includes(stage) && (
            <StatusLoading text={stage === "provision" ? "正在准备工作区和智能体…" : stage === "auth" ? "正在验证钉钉身份…" : "正在加载开通状态…"} />
          )}

          {stage === "create" && (
            <div className="space-y-4">
              <div>
                <label htmlFor="workspace-name" className="mb-2 block text-sm font-medium">工作区名称</label>
                <Input id="workspace-name" value={workspaceName} onChange={(event) => setWorkspaceName(event.target.value)} placeholder="例如：我的 FDE 工作区" autoFocus />
              </div>
              <Button className="h-12 w-full" disabled={!workspaceName.trim()} onClick={submit}>创建并继续</Button>
            </div>
          )}

          {stage === "install" && install && (
            <div className="space-y-4 text-center">
              <StatusLoading text="等待钉钉机器人创建完成…" />
              <p className="text-sm text-slate-600">如果钉钉创建页面没有自动打开，请点击下面的按钮。</p>
              <Button className="h-12 w-full" onClick={() => void openDingTalkInstallPage(install.qr_code_url)}>前往创建钉钉机器人</Button>
            </div>
          )}

          {stage === "done" && result && (
            <div className="space-y-5 py-4 text-center">
              <CheckCircle2 className="mx-auto size-14 text-emerald-500" />
              <div>
                <h2 className="text-xl font-semibold">初始化已完成</h2>
                <p className="mt-2 text-sm text-slate-600">FDE 智能体和钉钉机器人已完成绑定。</p>
              </div>
              <div className="rounded-xl border border-slate-200 bg-slate-50 px-4 py-3 text-left">
                <p className="text-xs text-slate-500">专属 FDE 开发者工作空间</p>
                <p className="mt-1 font-medium text-slate-900">{result.workspace.name}</p>
              </div>
              <Button className="h-12 w-full" onClick={() => void closeDingTalkPage()}>返回钉钉</Button>
            </div>
          )}

          {stage === "error" && (
            <div className="space-y-4 text-center">
              <AlertCircle className="mx-auto size-12 text-rose-500" />
              <div><h2 className="font-semibold">暂时无法继续</h2><p className="mt-2 break-words text-sm text-slate-600">{error}</p></div>
              <Button variant="outline" className="h-11 w-full" onClick={() => window.location.reload()}>重试</Button>
            </div>
          )}
        </CardContent>
      </Card>
    </main>
  );
}

function StatusLoading({ text }: { text: string }) {
  return <div className="flex flex-col items-center gap-3 py-6 text-center"><Loader2 className="size-8 animate-spin text-sky-600" /><p className="text-sm text-slate-600">{text}</p></div>;
}

export default function FDEStartPage() {
  return <Suspense fallback={<main className="flex min-h-dvh items-center justify-center"><Loader2 className="size-8 animate-spin text-sky-600" /></main>}><FDEStartContent /></Suspense>;
}
