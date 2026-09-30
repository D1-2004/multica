"use client";

import { Suspense, useCallback, useEffect, useRef, useState } from "react";
import { useSearchParams } from "next/navigation";
import { AlertCircle, Loader2 } from "lucide-react";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { Button } from "@multica/ui/components/ui/button";
import { ContextConfigPage, type ContextConfigPageProps } from "@multica/views/dingtalk";
import { useT } from "@multica/views/i18n";
import { createGroupPicker, isDingTalk, type PickedGroup } from "./jsapi";
import {
  beginOAuthState,
  buildDingTalkOAuthUrl,
  cleanConfigureUrl,
  clearOAuthState,
  navigateToAuthorization,
  oauthStateMatches,
  readConnectResult,
  replaceCurrentPage,
  savePendingConnect,
  savePendingParams,
  takePendingConnect,
  takePendingParams,
  type ConfigureParams,
  type ConnectTarget,
} from "./oauth";
import styles from "./configure.module.css";

type AuthErrorKind = "denied" | "failed" | "unconfigured" | "loop";

type Stage =
  | { kind: "boot" }
  | { kind: "auth" }
  | { kind: "ready" }
  | { kind: "error"; error: AuthErrorKind };

type ConnectResult = NonNullable<ContextConfigPageProps["connectResult"]>;
type ScopeRef = NonNullable<ContextConfigPageProps["initialScope"]>;

// Leaves for a connector's provider sign-in, remembering the scope it was
// started from; the provider flow returns to this route (see oauth.ts).
function openAuthorizeUrl(url: string, target: ConnectTarget) {
  savePendingConnect(target);
  navigateToAuthorization(url);
}

// /dingtalk/configure is the mobile page where DingTalk group members and
// individuals configure an agent's context capabilities. It is opened from an
// agent-issued link (`?link=<token>`) or the admin configure link
// (`?agent=<id>`). Sign-in reuses the FDE DingTalk OAuth flow; this route owns
// only that platform plumbing and the JSAPI group picker.
function DingTalkConfigureContent() {
  const { t } = useT("agents");
  const searchParams = useSearchParams();
  const setUser = useAuthStore((state) => state.setUser);
  const [stage, setStage] = useState<Stage>({ kind: "boot" });
  const [params, setParams] = useState<ConfigureParams>({});
  const [connectResult, setConnectResult] = useState<ConnectResult | null>(null);
  const [initialScope, setInitialScope] = useState<ScopeRef | undefined>(undefined);
  const [pickGroup, setPickGroup] = useState<
    (() => Promise<PickedGroup | null>) | undefined
  >(undefined);
  const booted = useRef(false);
  const completedOAuth = useRef(false);

  const beginOAuth = useCallback(async (current: ConfigureParams) => {
    if (completedOAuth.current) {
      // A fresh DingTalk session was still rejected; redirecting again would
      // loop between this page and the DingTalk consent screen.
      setStage({ kind: "error", error: "loop" });
      return;
    }
    setStage({ kind: "auth" });
    try {
      const config = await api.getConfig();
      if (!config.dingtalk_client_id) {
        setStage({ kind: "error", error: "unconfigured" });
        return;
      }
      savePendingParams(current);
      const state = beginOAuthState();
      replaceCurrentPage(
        buildDingTalkOAuthUrl(config.dingtalk_client_id, window.location.origin, state),
      );
    } catch {
      setStage({ kind: "error", error: "failed" });
    }
  }, []);

  useEffect(() => {
    if (isDingTalk()) setPickGroup(() => createGroupPicker());
  }, []);

  // Toasts render in the root layout, outside `.shell`; put the sRGB token
  // fallback on <html> while this route is mounted (see configure.module.css).
  useEffect(() => {
    const className = styles.legacyColorRoot;
    if (!className) return;
    const root = document.documentElement;
    root.classList.add(className);
    return () => root.classList.remove(className);
  }, []);

  useEffect(() => {
    if (booted.current) return;
    booted.current = true;
    const run = async () => {
      const code = searchParams.get("authCode") || searchParams.get("code");
      const oauthError = searchParams.get("error");
      if (code || oauthError) {
        const pending = takePendingParams();
        const restored: ConfigureParams = {
          linkToken: pending.linkToken,
          agentId: pending.agentId ?? (searchParams.get("agent") || undefined),
        };
        window.history.replaceState({}, "", cleanConfigureUrl(restored.agentId));
        setParams(restored);
        if (oauthError) {
          clearOAuthState();
          setStage({ kind: "error", error: oauthError === "access_denied" ? "denied" : "failed" });
          return;
        }
        if (!oauthStateMatches(searchParams.get("state") ?? "")) {
          clearOAuthState();
          await beginOAuth(restored);
          return;
        }
        clearOAuthState();
        setStage({ kind: "auth" });
        try {
          const login = await api.fdeDingtalkLogin(code ?? "");
          api.setToken(login.token);
          setUser(login.user);
          completedOAuth.current = true;
        } catch {
          setStage({ kind: "error", error: "failed" });
          return;
        }
        setStage({ kind: "ready" });
        return;
      }

      const initial: ConfigureParams = {
        linkToken: searchParams.get("link") || undefined,
        agentId: searchParams.get("agent") || undefined,
      };
      // Back from a connector's provider sign-in: reopen the scope the
      // connection was started from and report the outcome once.
      const returned = readConnectResult(searchParams);
      if (returned) {
        const pending = takePendingConnect();
        if (!initial.agentId && pending) initial.agentId = pending.agentId;
        if (pending && pending.agentId === initial.agentId) {
          setInitialScope({ scopeType: pending.scopeType, scopeKey: pending.scopeKey });
        }
        setConnectResult(returned);
      }
      // Drop the link token and the connect outcome from the address bar
      // before anything can copy, share or sign (dd.config) the URL.
      if (initial.linkToken || returned) {
        window.history.replaceState({}, "", cleanConfigureUrl(initial.agentId));
      }
      setParams(initial);
      setStage({ kind: "ready" });
    };
    void run();
  }, [beginOAuth, searchParams, setUser]);

  const retry = () => {
    completedOAuth.current = false;
    void beginOAuth(params);
  };

  let content: React.ReactNode;
  if (stage.kind === "ready") {
    content = (
      <ContextConfigPage
        linkToken={params.linkToken}
        initialAgentId={params.agentId}
        pickGroup={pickGroup}
        onAuthRequired={() => void beginOAuth(params)}
        openAuthorizeUrl={openAuthorizeUrl}
        initialScope={initialScope}
        connectResult={connectResult}
      />
    );
  } else if (stage.kind === "error") {
    const message =
      stage.error === "denied"
        ? t(($) => $.context_config.web.oauth_denied)
        : stage.error === "unconfigured"
          ? t(($) => $.context_config.web.oauth_unconfigured)
          : stage.error === "loop"
            ? t(($) => $.context_config.web.auth_loop)
            : t(($) => $.context_config.web.oauth_failed);
    content = (
      <main className="flex min-h-dvh flex-col items-center justify-center gap-4 px-4 text-center">
        <AlertCircle className="size-10 text-destructive" />
        <p className="max-w-sm text-body text-muted-foreground text-pretty">{message}</p>
        <Button variant="outline" className="h-11 min-w-40" onClick={retry}>
          {t(($) => $.context_config.web.retry)}
        </Button>
      </main>
    );
  } else {
    content = <Loading text={stage.kind === "auth" ? t(($) => $.context_config.web.signing_in) : ""} />;
  }

  return <div className={`${styles.shell} bg-background text-foreground`}>{content}</div>;
}

function Loading({ text }: { text: string }) {
  return (
    <main className="flex min-h-dvh flex-col items-center justify-center gap-3 px-4" role="status">
      <Loader2 className="size-8 animate-spin text-muted-foreground motion-reduce:animate-none" />
      {text ? <p className="text-body text-muted-foreground">{text}</p> : null}
    </main>
  );
}

export default function DingTalkConfigurePage() {
  return (
    <Suspense fallback={<Loading text="" />}>
      <DingTalkConfigureContent />
    </Suspense>
  );
}
