"use client";

import { Suspense, useCallback, useEffect, useRef, useState } from "react";
import { useSearchParams } from "next/navigation";
import { AlertCircle, Loader2 } from "lucide-react";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { Button } from "@multica/ui/components/ui/button";
import {
  ContextConfigPage,
  type ContextConfigBinding,
  type ContextConfigPageProps,
} from "@multica/views/dingtalk";
import { useT } from "@multica/views/i18n";
import { browserForwarding } from "@/platform/forwarding";
import { createGroupPicker, isDingTalk, type PickedGroup } from "./jsapi";
import {
  beginOAuthState,
  buildDingTalkOAuthUrl,
  cleanConfigureUrl,
  clearOAuthState,
  navigateToAuthorization,
  oauthStateMatches,
  readConfigureParams,
  readConnectResult,
  replaceCurrentPage,
  savePendingConnect,
  savePendingConnectResult,
  savePendingParams,
  takePendingConnect,
  takePendingConnectResult,
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

/** How long after a provider sign-in returned its outcome is still carried
 * through a DingTalk sign-in the page needs first. */
const CONNECT_RETURN_CARRY_MS = 5 * 60 * 1000;

interface ConnectReturn {
  target: ConnectTarget | null;
  result: ConnectResult;
  at: number;
}

// Leaves for a connector's provider sign-in, remembering the scope it was
// started from; the provider flow returns to this route (see oauth.ts).
function openAuthorizeUrl(url: string, target: ConnectTarget) {
  savePendingConnect(target);
  navigateToAuthorization(url);
}

// /dingtalk/configure is the mobile page where DingTalk group members and
// individuals configure an agent's context capabilities. It is opened from an
// agent-issued link (`?link=<token>`) or the admin configure link
// (`?agent=<id>`). A redeemed link binds the page to its scope, kept in the
// URL as `?agent=&org=&scope_type=&scope_key=` (plus `tab=`), so reloads and
// sign-in round trips reopen that scope only. Sign-in reuses the FDE DingTalk
// OAuth flow; this route owns only that platform plumbing, the URL and the
// JSAPI group picker.
function DingTalkConfigureContent() {
  const { t } = useT("agents");
  const searchParams = useSearchParams();
  const setUser = useAuthStore((state) => state.setUser);
  const [stage, setStage] = useState<Stage>({ kind: "boot" });
  const [params, setParamsState] = useState<ConfigureParams>({});
  const paramsRef = useRef<ConfigureParams>({});
  const setParams = useCallback((next: ConfigureParams) => {
    paramsRef.current = next;
    setParamsState(next);
  }, []);
  const [connectResult, setConnectResult] = useState<ConnectResult | null>(null);
  const [initialScope, setInitialScope] = useState<ScopeRef | undefined>(undefined);
  const [pickGroup, setPickGroup] = useState<
    (() => Promise<PickedGroup | null>) | undefined
  >(undefined);
  const booted = useRef(false);
  const completedOAuth = useRef(false);
  // A provider sign-in outcome this page load took from the URL and the
  // scope it started from: a DingTalk sign-in the page needs right after
  // must not lose them.
  const connectReturn = useRef<ConnectReturn | null>(null);

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
      const carried = connectReturn.current;
      if (carried && Date.now() - carried.at < CONNECT_RETURN_CARRY_MS) {
        if (carried.target) savePendingConnect(carried.target);
        savePendingConnectResult(carried.result);
      }
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
          ...pending,
          agentId: pending.agentId ?? readConfigureParams(searchParams).agentId,
        };
        window.history.replaceState({}, "", cleanConfigureUrl(restored));
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
          // Forwarded sessions use the gateway-scoped HttpOnly cookie.
          if (!browserForwarding()) api.setToken(login.token);
          setUser(login.user);
          completedOAuth.current = true;
        } catch {
          setStage({ kind: "error", error: "failed" });
          return;
        }
        // A provider sign-in returned just before this DingTalk sign-in:
        // reopen its scope and report it now.
        const carriedResult = takePendingConnectResult();
        if (carriedResult) {
          const target = takePendingConnect();
          if (target && target.agentId === restored.agentId) {
            setInitialScope({
              scopeType: target.scopeType,
              scopeKey: target.scopeKey,
              ...(target.orgId ? { orgId: target.orgId } : {}),
            });
          }
          setConnectResult(carriedResult);
        }
        setStage({ kind: "ready" });
        return;
      }

      const initial = readConfigureParams(searchParams);
      // Back from a connector's provider sign-in: reopen the scope the
      // connection was started from and report the outcome once.
      const returned = readConnectResult(searchParams);
      if (returned) {
        const pending = takePendingConnect();
        if (!initial.agentId && pending) initial.agentId = pending.agentId;
        if (pending && pending.agentId === initial.agentId) {
          setInitialScope({
            scopeType: pending.scopeType,
            scopeKey: pending.scopeKey,
            ...(pending.orgId ? { orgId: pending.orgId } : {}),
          });
        }
        setConnectResult(returned);
        connectReturn.current = { target: pending, result: returned, at: Date.now() };
      }
      // Drop the link token and the connect outcome from the address bar
      // before anything can copy, share or sign (dd.config) the URL.
      if (initial.linkToken || returned) {
        window.history.replaceState({}, "", cleanConfigureUrl(initial));
      }
      setParams(initial);
      setStage({ kind: "ready" });
    };
    void run();
  }, [beginOAuth, searchParams, setParams, setUser]);

  // A redeemed link binds the page: the URL keeps the scope (and drops the
  // spent link) for reloads and sign-in round trips.
  const bind = useCallback(
    (binding: ContextConfigBinding) => {
      const next: ConfigureParams = {
        agentId: binding.agentId,
        binding: {
          scopeType: binding.scopeType,
          scopeKey: binding.scopeKey,
          ...(binding.orgId ? { orgId: binding.orgId } : {}),
        },
        ...(paramsRef.current.tab ? { tab: paramsRef.current.tab } : {}),
      };
      setParams(next);
      window.history.replaceState({}, "", cleanConfigureUrl(next));
    },
    [setParams],
  );

  const changeTab = useCallback(
    (tab: string) => {
      const next = { ...paramsRef.current, tab };
      setParams(next);
      window.history.replaceState({}, "", cleanConfigureUrl(next));
    },
    [setParams],
  );

  const binding: ContextConfigBinding | null =
    params.agentId && params.binding
      ? {
          agentId: params.agentId,
          scopeType: params.binding.scopeType,
          scopeKey: params.binding.scopeKey,
          orgId: params.binding.orgId ?? "",
        }
      : null;
  // A provider sign-in returns to the bound scope (on the default tab).
  const connectReturnTo = browserForwarding()
    ? `${window.location.origin}${cleanConfigureUrl({ agentId: params.agentId, binding: params.binding })}`
    : params.agentId && params.binding
      ? cleanConfigureUrl({ agentId: params.agentId, binding: params.binding })
      : undefined;

  const retry = () => {
    completedOAuth.current = false;
    void beginOAuth(paramsRef.current);
  };

  let content: React.ReactNode;
  if (stage.kind === "ready") {
    content = (
      <ContextConfigPage
        linkToken={params.linkToken}
        initialAgentId={params.agentId}
        binding={binding}
        onBind={bind}
        initialTab={params.tab}
        onTabChange={changeTab}
        pickGroup={pickGroup}
        onAuthRequired={() => void beginOAuth(paramsRef.current)}
        openAuthorizeUrl={openAuthorizeUrl}
        connectReturnTo={connectReturnTo}
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
