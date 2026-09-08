"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, CheckCircle2, Laptop, Loader2, XCircle } from "lucide-react";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@multica/ui/components/ui/card";
import { useT } from "../i18n";
import { useNavigation } from "../navigation";

type CompletionState = "approved" | "denied" | null;

export function RunnerAuthorizePage({ code }: { code: string | null }) {
  const { t } = useT("agents");
  const user = useAuthStore((state) => state.user);
  const isAuthLoading = useAuthStore((state) => state.isLoading);
  const navigation = useNavigation();
  const normalizedCode = code?.trim().toUpperCase() ?? "";
  const [submitting, setSubmitting] = useState<"approve" | "deny" | null>(
    null,
  );
  const [completion, setCompletion] = useState<CompletionState>(null);
  const [submitError, setSubmitError] = useState<string | null>(null);

  const authorization = useQuery({
    queryKey: ["runner-device-authorization", normalizedCode],
    queryFn: async () => {
      const result = await api.getRunnerDeviceAuthorization(normalizedCode);
      if (!result.userCode) {
        throw new Error(t(($) => $.tab_body.runner.authorize_load_failed));
      }
      return result;
    },
    enabled: !isAuthLoading && !!user && !!normalizedCode && !completion,
    retry: false,
  });

  const finish = async (action: "approve" | "deny") => {
    setSubmitting(action);
    setSubmitError(null);
    try {
      const result = await api.finishRunnerDeviceAuthorization(
        normalizedCode,
        action,
      );
      if (result.status !== (action === "approve" ? "approved" : "denied")) {
        throw new Error(t(($) => $.tab_body.runner.authorize_submit_failed));
      }
      setCompletion(result.status);
    } catch (error) {
      setSubmitError(
        error instanceof Error
          ? error.message
          : t(($) => $.tab_body.runner.authorize_submit_failed),
      );
    } finally {
      setSubmitting(null);
    }
  };

  if (!normalizedCode) {
    return (
      <AuthorizationShell>
        <ErrorState message={t(($) => $.tab_body.runner.authorize_missing_code)} />
      </AuthorizationShell>
    );
  }

  if (isAuthLoading) {
    return (
      <AuthorizationShell>
        <LoadingState label={t(($) => $.tab_body.runner.authorize_loading)} />
      </AuthorizationShell>
    );
  }

  if (!user) {
    const next = `/runners/authorize?code=${encodeURIComponent(normalizedCode)}`;
    return (
      <AuthorizationShell>
        <div className="space-y-4">
          <p className="text-sm text-muted-foreground">
            {t(($) => $.tab_body.runner.authorize_sign_in_description)}
          </p>
          <Button
            onClick={() =>
              navigation.push(`/login?next=${encodeURIComponent(next)}`)
            }
          >
            {t(($) => $.tab_body.runner.authorize_sign_in)}
          </Button>
        </div>
      </AuthorizationShell>
    );
  }

  if (completion) {
    return (
      <AuthorizationShell>
        <div className="space-y-3 text-center">
          {completion === "approved" ? (
            <CheckCircle2
              className="mx-auto h-10 w-10 text-emerald-600"
              aria-hidden
            />
          ) : (
            <XCircle className="mx-auto h-10 w-10 text-muted-foreground" aria-hidden />
          )}
          <p className="text-base font-medium">
            {completion === "approved"
              ? t(($) => $.tab_body.runner.authorize_approved_title)
              : t(($) => $.tab_body.runner.authorize_denied_title)}
          </p>
          <p className="text-sm text-muted-foreground">
            {t(($) => $.tab_body.runner.authorize_close_hint)}
          </p>
        </div>
      </AuthorizationShell>
    );
  }

  if (authorization.isLoading) {
    return (
      <AuthorizationShell>
        <LoadingState label={t(($) => $.tab_body.runner.authorize_loading)} />
      </AuthorizationShell>
    );
  }

  if (authorization.isError || !authorization.data) {
    return (
      <AuthorizationShell>
        <ErrorState
          message={
            authorization.error instanceof Error
              ? authorization.error.message
              : t(($) => $.tab_body.runner.authorize_load_failed)
          }
        />
      </AuthorizationShell>
    );
  }

  const pending = authorization.data;
  const isActionable = pending.state === "device_pending";

  return (
    <AuthorizationShell>
      <div className="space-y-5">
        <div className="flex items-start gap-3">
          <div className="rounded-lg bg-muted p-2.5">
            <Laptop className="h-5 w-5" aria-hidden />
          </div>
          <div className="min-w-0">
            <p className="truncate text-base font-medium">{pending.machineName}</p>
            <p className="text-xs text-muted-foreground">
              {pending.os}/{pending.arch} · {pending.userCode}
            </p>
          </div>
        </div>

        <div
          role="alert"
          className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2.5 text-xs text-amber-700 dark:text-amber-400"
        >
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden />
          <span>{t(($) => $.tab_body.runner.authorize_warning)}</span>
        </div>

        {!isActionable && (
          <p className="text-sm text-muted-foreground">
            {t(($) => $.tab_body.runner.authorize_already_finished)}
          </p>
        )}

        {submitError && <p className="text-sm text-destructive">{submitError}</p>}

        <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
          <Button
            variant="outline"
            disabled={!isActionable || submitting !== null}
            onClick={() => void finish("deny")}
          >
            {submitting === "deny" && (
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden />
            )}
            {t(($) => $.tab_body.runner.authorize_deny)}
          </Button>
          <Button
            disabled={!isActionable || submitting !== null}
            onClick={() => void finish("approve")}
          >
            {submitting === "approve" && (
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden />
            )}
            {t(($) => $.tab_body.runner.authorize_approve)}
          </Button>
        </div>
      </div>
    </AuthorizationShell>
  );
}

function AuthorizationShell({ children }: { children: React.ReactNode }) {
  const { t } = useT("agents");
  return (
    <main className="mx-auto flex min-h-screen max-w-lg items-center p-6">
      <Card className="w-full">
        <CardHeader>
          <CardTitle>{t(($) => $.tab_body.runner.authorize_title)}</CardTitle>
        </CardHeader>
        <CardContent>{children}</CardContent>
      </Card>
    </main>
  );
}

function LoadingState({ label }: { label: string }) {
  return (
    <div className="flex items-center gap-2 py-6 text-sm text-muted-foreground">
      <Loader2 className="h-4 w-4 animate-spin" aria-hidden />
      {label}
    </div>
  );
}

function ErrorState({ message }: { message: string }) {
  return (
    <div className="space-y-2 py-3">
      <XCircle className="h-7 w-7 text-destructive" aria-hidden />
      <p className="text-sm text-destructive">{message}</p>
    </div>
  );
}
