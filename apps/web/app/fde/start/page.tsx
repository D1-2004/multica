"use client";

import { Suspense, useCallback, useEffect, useRef, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { AlertCircle, CheckCircle2, Loader2 } from "lucide-react";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { completeOnboarding } from "@multica/core/onboarding";
import { paths } from "@multica/core/paths";
import type {
  BeginDingTalkInstallResponse,
  FDEOnboardingState,
  ProvisionFDEOnboardingResponse,
} from "@multica/core/types";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Button } from "@multica/ui/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@multica/ui/components/ui/card";
import { Input } from "@multica/ui/components/ui/input";
import { openDingTalkInstallPage, replaceCurrentPage } from "./navigation";

const oauthStateKey = "multica_fde_oauth_state";
const oauthCompleteKey = "multica_fde_dingtalk_authenticated";

type Stage = "auth" | "loading" | "create" | "provision" | "install" | "finalize" | "done" | "error";

function FDEStartContent() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const setUser = useAuthStore((state) => state.setUser);
  const [stage, setStage] = useState<Stage>("auth");
  const [error, setError] = useState("");
  const [state, setState] = useState<FDEOnboardingState | null>(null);
  const [workspaceName, setWorkspaceName] = useState("");
  const [result, setResult] = useState<ProvisionFDEOnboardingResponse | null>(null);
  const [install, setInstall] = useState<BeginDingTalkInstallResponse | null>(null);
  const [confirmationOpen, setConfirmationOpen] = useState(false);
  const booted = useRef(false);
  const openedInstall = useRef(false);

  const fail = useCallback((message: string) => {
    setError(message);
    setStage("error");
  }, []);

  const finishOnboarding = useCallback(async (response: ProvisionFDEOnboardingResponse) => {
    setStage("finalize");
    try {
      await completeOnboarding("fde", response.workspace.id);
      setStage("done");
    } catch {
      fail("工作区和机器人已经创建，但初始化状态更新失败，请重试");
    }
  }, [fail]);

  const provision = useCallback(async (workspaceNameInput: string) => {
    setStage("provision");
    setError("");
    try {
      const response = await api.provisionFDEOnboarding({ workspace_name: workspaceNameInput });
      setResult(response);
      if (response.install_complete) {
        await finishOnboarding(response);
        return;
      }
      if (!response.install) throw new Error("未能启动钉钉机器人创建流程");
      setInstall(response.install);
      setStage("install");
    } catch (cause) {
      fail(cause instanceof Error ? cause.message : "开通失败，请重试");
    }
  }, [fail, finishOnboarding]);

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
        if (onboarding.create_only !== true) throw new Error("FDE 开通服务正在升级，请稍后重试");
        setState(onboarding);
        setStage("create");
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
          await finishOnboarding(result);
        } else if (status.status === "error") {
          window.clearInterval(interval);
          fail(status.error_message || "钉钉机器人创建失败，请重试");
        }
      } catch {
        // A transient mobile-network failure must not abandon the device flow.
      }
    }, Math.max(2, install.poll_interval_seconds) * 1000);
    return () => window.clearInterval(interval);
  }, [fail, finishOnboarding, install, result, stage]);

  useEffect(() => {
    if (stage === "done") sessionStorage.removeItem(oauthCompleteKey);
  }, [stage]);

  const submit = () => {
    if (!workspaceName.trim()) return;
    setConfirmationOpen(true);
  };

  const confirmCreation = () => {
    const name = workspaceName.trim();
    if (!name) return;
    setConfirmationOpen(false);
    void provision(name);
  };

  const enterWorkspace = () => {
    if (!result) return;
    router.push(paths.workspace(result.workspace.slug).issues());
  };

  const loadingText = stage === "provision"
    ? "正在准备工作区和智能体…"
    : stage === "finalize"
      ? "正在完成初始化…"
      : stage === "auth"
        ? "正在验证钉钉身份…"
        : "正在加载开通状态…";

  const confirmationWorkspaceName = workspaceName.trim();

  const createConfirmation = (
    <AlertDialog open={confirmationOpen} onOpenChange={setConfirmationOpen}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>确认创建 FDE 工作区？</AlertDialogTitle>
          <AlertDialogDescription className="space-y-3 text-left text-pretty">
            <span className="block">
              确认后，系统将创建新的工作区“{confirmationWorkspaceName}”，并配置 FDE 智能体和云端运行时。随后会打开钉钉开放平台，请继续完成机器人的创建与发布。
            </span>
            <span className="block">
              移动端完成后可直接进入机器人会话；PC 端完成后请返回本页面查看结果。已有工作区不会被修改。
            </span>
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>取消</AlertDialogCancel>
          <AlertDialogAction onClick={confirmCreation}>确认创建并前往钉钉</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );

  return (
    <main className="min-h-dvh bg-gradient-to-b from-sky-50 to-white px-4 py-8 text-slate-950 sm:flex sm:items-center sm:justify-center">
      <Card className="mx-auto w-full max-w-md border-sky-100 shadow-lg shadow-sky-100/60">
        <CardHeader className="space-y-3 text-center">
          <div className="mx-auto flex size-12 items-center justify-center rounded-2xl bg-sky-600 text-lg font-bold text-white">FDE</div>
          <CardTitle className="text-2xl">{stage === "done" ? "FDE 开发者工作空间已就绪" : "创建专属 FDE 开发者工作空间"}</CardTitle>
          <CardDescription>{stage === "done" ? "工作区、FDE 智能体和钉钉机器人均已完成配置。" : "我们将新建一个独立工作区，并自动创建 FDE 智能体、绑定钉钉机器人。不会修改你已有的工作区。"}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-5">
          {(["auth", "loading", "provision", "finalize"] as Stage[]).includes(stage) && (
            <StatusLoading text={loadingText} />
          )}

          {stage === "create" && (
            <div className="space-y-6">
              <section className="space-y-3" aria-labelledby="existing-workspaces-title">
                <div>
                  <h2 id="existing-workspaces-title" className="text-sm font-medium text-slate-900">已有工作区（仅展示）</h2>
                  <p className="mt-1 text-xs text-slate-500">已有工作区不会被选择、修改或用于本次初始化。</p>
                </div>
                {state && state.workspaces.length > 0 ? (
                  <div className="space-y-2">
                    {state.workspaces.map((workspace) => (
                      <div key={workspace.id} className="rounded-xl border border-slate-200 bg-slate-100/80 px-4 py-3 opacity-70">
                        <p className="font-medium text-slate-700">{workspace.name}</p>
                        <p className="mt-1 truncate text-xs text-slate-500">{workspace.slug}</p>
                      </div>
                    ))}
                  </div>
                ) : (
                  <div className="rounded-xl border border-dashed border-slate-200 bg-slate-50 px-4 py-3 text-sm text-slate-500">暂无已有工作区</div>
                )}
              </section>
              <section className="space-y-4" aria-labelledby="new-workspace-title">
                <h2 id="new-workspace-title" className="text-sm font-medium text-slate-900">新建专属 FDE 开发者工作空间</h2>
                <div>
                  <label htmlFor="workspace-name" className="mb-2 block text-sm font-medium">工作区名称</label>
                  <Input id="workspace-name" value={workspaceName} onChange={(event) => setWorkspaceName(event.target.value)} placeholder="例如：我的 FDE 工作区" autoFocus />
                </div>
                <Button className="h-12 w-full" disabled={!workspaceName.trim()} onClick={submit}>创建并继续</Button>
              </section>
              {createConfirmation}
            </div>
          )}

          {stage === "install" && install && (
            <div className="space-y-4 text-center">
              <StatusLoading text="等待钉钉机器人创建完成…" />
              <p className="text-sm text-slate-600">请在钉钉开放平台完成机器人的创建与发布。如果页面没有自动打开，请点击下方按钮。PC 端完成后请返回本页面，本页会自动检测创建结果。</p>
              <Button className="h-12 w-full" onClick={() => void openDingTalkInstallPage(install.qr_code_url)}>前往创建钉钉机器人</Button>
            </div>
          )}

          {stage === "done" && result && (
            <div className="space-y-5 py-4 text-center">
              <CheckCircle2 className="mx-auto size-14 text-emerald-500" />
              <div>
                <h2 className="text-xl font-semibold">FDE 工作区和钉钉机器人已准备就绪</h2>
                <p className="mt-2 text-sm text-slate-600">已为你完成以下配置：</p>
              </div>
              <div className="space-y-3 rounded-xl border border-slate-200 bg-slate-50 px-4 py-4 text-left">
                <div>
                  <p className="text-xs text-slate-500">工作区</p>
                  <p className="mt-1 font-medium text-slate-900">{result.workspace.name}</p>
                </div>
                <ul className="space-y-2 text-sm text-slate-700">
                  <li className="flex gap-2"><CheckCircle2 className="mt-0.5 size-4 shrink-0 text-emerald-500" />创建 FDE 智能体和云端运行时</li>
                  <li className="flex gap-2"><CheckCircle2 className="mt-0.5 size-4 shrink-0 text-emerald-500" />创建并绑定钉钉机器人</li>
                </ul>
              </div>
              <p className="text-sm text-slate-600">现在你可以在钉钉中向机器人发送消息，也可以进入工作区管理 issue、智能体和运行时。</p>
              <Button className="h-12 w-full" onClick={enterWorkspace}>进入工作区</Button>
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
