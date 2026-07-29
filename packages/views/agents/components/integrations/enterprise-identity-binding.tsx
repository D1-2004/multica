"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Building2, RefreshCw, ShieldCheck, Unplug } from "lucide-react";
import {
  agentEnterpriseIdentityStatusOptions,
  useBeginAgentEnterpriseIdentityBinding,
  useRevokeAgentEnterpriseIdentity,
  useTestAgentEnterpriseIdentity,
} from "@multica/core/agent-enterprise-identity";
import { useWorkspaceId } from "@multica/core/hooks";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { useT } from "../../../i18n";

function currentReturnPath(): string {
  if (typeof window === "undefined") return "/";
  const url = new URL(window.location.href);
  url.searchParams.delete("enterprise_identity");
  return `${url.pathname}${url.search}${url.hash}`;
}

function clearOAuthReturnParam() {
  if (typeof window === "undefined") return;
  const url = new URL(window.location.href);
  if (!url.searchParams.has("enterprise_identity")) return;
  url.searchParams.delete("enterprise_identity");
  window.history.replaceState(null, "", `${url.pathname}${url.search}${url.hash}`);
}

function errorMessage(error: unknown, defaultMessage: string): string {
  return error instanceof Error && error.message ? error.message : defaultMessage;
}

function formatExpiry(epochSeconds?: number): string | null {
  if (!epochSeconds) return null;
  const value = new Date(epochSeconds * 1000);
  if (!Number.isFinite(value.getTime())) return null;
  return value.toLocaleString();
}

export function EnterpriseIdentityBindingCard({
  agentId,
  canManage,
}: {
  agentId: string;
  canManage: boolean;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const [employeeId, setEmployeeId] = useState("");
  const [actionError, setActionError] = useState<string | null>(null);
  const [actionMessage, setActionMessage] = useState<string | null>(null);
  const { data, isPending, refetch } = useQuery({
    ...agentEnterpriseIdentityStatusOptions(wsId, agentId),
    enabled: !!wsId && !!agentId,
  });
  const beginBinding = useBeginAgentEnterpriseIdentityBinding(wsId);
  const testIdentity = useTestAgentEnterpriseIdentity(wsId);
  const revokeIdentity = useRevokeAgentEnterpriseIdentity(wsId);
  const identity = data?.identity ?? null;
  const canMutate = canManage && data?.canManage === true;
  const needsReauth = identity?.status === "needs_reauth";
  const refreshExpiresAt = useMemo(
    () => formatExpiry(identity?.refreshExpiresAt),
    [identity?.refreshExpiresAt],
  );

  useEffect(() => {
    if (typeof window === "undefined") return;
    const params = new URLSearchParams(window.location.search);
    if (params.get("enterprise_identity") !== "connected") return;
    setActionMessage(
      t(($) => $.tab_body.integrations.enterprise_identity_connected),
    );
    setActionError(null);
    clearOAuthReturnParam();
    void refetch();
  }, [refetch, t]);

  function statusLabel(status: string): string {
    switch (status) {
      case "needs_reauth":
        return t(
          ($) => $.tab_body.integrations.enterprise_identity_status_reauth,
        );
      case "revoked":
        return t(
          ($) => $.tab_body.integrations.enterprise_identity_status_revoked,
        );
      case "active":
        return t(
          ($) => $.tab_body.integrations.enterprise_identity_status_active,
        );
      default:
        return status;
    }
  }

  async function connect() {
    const normalizedEmployeeId = employeeId.trim();
    if (!/^[1-9][0-9]*$/.test(normalizedEmployeeId)) {
      setActionError(
        t(($) => $.tab_body.integrations.enterprise_identity_employee_invalid),
      );
      return;
    }
    setActionError(null);
    setActionMessage(null);
    try {
      const response = await beginBinding.mutateAsync({
        agentId,
        employeeId: normalizedEmployeeId,
        redirectPath: currentReturnPath(),
      });
      if (!response.authorizationUrl) {
        setActionError(
          t(($) => $.tab_body.integrations.enterprise_identity_connect_failed),
        );
        return;
      }
      window.location.assign(response.authorizationUrl);
    } catch (error) {
      setActionError(
        errorMessage(
          error,
          t(($) => $.tab_body.integrations.enterprise_identity_connect_failed),
        ),
      );
    }
  }

  async function test() {
    setActionError(null);
    setActionMessage(null);
    try {
      await testIdentity.mutateAsync(agentId);
      setActionMessage(
        t(($) => $.tab_body.integrations.enterprise_identity_test_ok),
      );
    } catch (error) {
      setActionError(
        errorMessage(
          error,
          t(($) => $.tab_body.integrations.enterprise_identity_test_failed),
        ),
      );
    }
  }

  async function revoke() {
    setActionError(null);
    setActionMessage(null);
    try {
      await revokeIdentity.mutateAsync(agentId);
      setEmployeeId("");
      setActionMessage(
        t(($) => $.tab_body.integrations.enterprise_identity_revoked),
      );
    } catch (error) {
      setActionError(
        errorMessage(
          error,
          t(($) => $.tab_body.integrations.enterprise_identity_revoke_failed),
        ),
      );
    }
  }

  return (
    <section
      className="rounded-lg border"
      data-testid="enterprise-identity-binding-card"
    >
      <div className="flex items-start gap-3 p-4">
        <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border bg-muted/40 text-muted-foreground">
          <Building2 className="h-4 w-4" />
        </span>
        <div className="min-w-0 flex-1 space-y-1">
          <h3 className="text-sm font-medium">
            {t(($) => $.tab_body.integrations.enterprise_identity_title)}
          </h3>
          <p className="text-xs leading-relaxed text-muted-foreground">
            {t(($) => $.tab_body.integrations.enterprise_identity_description)}
          </p>
        </div>
      </div>

      <div className="space-y-3 border-t px-4 py-3">
        {isPending ? (
          <p className="text-xs text-muted-foreground">
            {t(($) => $.tab_body.integrations.enterprise_identity_loading)}
          </p>
        ) : data?.configured !== true ? (
          <p className="text-xs text-muted-foreground">
            {t(
              ($) => $.tab_body.integrations.enterprise_identity_not_configured,
            )}
          </p>
        ) : identity ? (
          <>
            <div className="space-y-1">
              {canMutate ? (
                <>
                  <p className="text-sm font-medium">
                    {identity.displayName ||
                      t(
                        ($) =>
                          $.tab_body.integrations
                            .enterprise_identity_employee_fallback,
                      )}
                  </p>
                  {identity.employeeId ? (
                    <p className="text-xs text-muted-foreground">
                      {t(
                        ($) =>
                          $.tab_body.integrations.enterprise_identity_employee,
                      )}
                      : {identity.employeeId}
                    </p>
                  ) : null}
                  {identity.aipId ? (
                    <p className="text-xs text-muted-foreground">
                      {t(
                        ($) => $.tab_body.integrations.enterprise_identity_aip,
                      )}
                      : {identity.aipId}
                    </p>
                  ) : null}
                </>
              ) : null}
              <p className="text-xs text-muted-foreground">
                {t(($) => $.tab_body.integrations.enterprise_identity_status)}:{" "}
                {statusLabel(identity.status)}
              </p>
              {canMutate ? (
                <>
                  <p className="text-xs text-muted-foreground">
                    {t(
                      ($) =>
                        $.tab_body.integrations.enterprise_identity_buc_status,
                    )}
                    : {statusLabel(identity.bucStatus)}
                  </p>
                  <p className="text-xs text-muted-foreground">
                    {t(
                      ($) =>
                        $.tab_body.integrations
                          .enterprise_identity_agent_status,
                    )}
                    : {statusLabel(identity.agentIdentityStatus)}
                  </p>
                </>
              ) : null}
              {canMutate && refreshExpiresAt ? (
                <p className="text-xs text-muted-foreground">
                  {t(
                    ($) =>
                      $.tab_body.integrations
                        .enterprise_identity_refresh_expires,
                  )}
                  : {refreshExpiresAt}
                </p>
              ) : null}
              {!canMutate ? (
                <p className="text-xs text-muted-foreground">
                  {t(
                    ($) =>
                      $.tab_body.integrations.enterprise_identity_members_note,
                  )}
                </p>
              ) : null}
            </div>
            {canMutate && needsReauth ? (
              <div className="space-y-1.5">
                <Label htmlFor="enterprise-identity-employee-id" className="text-xs">
                  {t(
                    ($) =>
                      $.tab_body.integrations.enterprise_identity_employee_id,
                  )}
                </Label>
                <Input
                  id="enterprise-identity-employee-id"
                  inputMode="numeric"
                  value={employeeId}
                  onChange={(event) => setEmployeeId(event.target.value)}
                />
              </div>
            ) : null}
            {canMutate ? (
              <div className="flex flex-wrap gap-2">
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={test}
                disabled={testIdentity.isPending || needsReauth}
              >
                <ShieldCheck className="mr-2 h-4 w-4" />
                {testIdentity.isPending
                  ? t(
                      ($) =>
                        $.tab_body.integrations.enterprise_identity_testing,
                    )
                  : t(($) => $.tab_body.integrations.enterprise_identity_test)}
              </Button>
              <Button
                type="button"
                size="sm"
                variant={needsReauth ? "default" : "outline"}
                onClick={connect}
                disabled={beginBinding.isPending}
              >
                <RefreshCw className="mr-2 h-4 w-4" />
                {beginBinding.isPending
                  ? t(
                      ($) =>
                        $.tab_body.integrations.enterprise_identity_connecting,
                    )
                  : t(
                      ($) =>
                        $.tab_body.integrations.enterprise_identity_reconnect,
                    )}
              </Button>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                onClick={revoke}
                disabled={revokeIdentity.isPending}
              >
                <Unplug className="mr-2 h-4 w-4" />
                {t(($) => $.tab_body.integrations.enterprise_identity_revoke)}
              </Button>
              </div>
            ) : null}
          </>
        ) : canMutate ? (
          <>
            <div className="space-y-1.5">
              <Label htmlFor="enterprise-identity-employee-id" className="text-xs">
                {t(
                  ($) => $.tab_body.integrations.enterprise_identity_employee_id,
                )}
              </Label>
              <Input
                id="enterprise-identity-employee-id"
                inputMode="numeric"
                value={employeeId}
                onChange={(event) => setEmployeeId(event.target.value)}
                placeholder={t(
                  ($) =>
                    $.tab_body.integrations
                      .enterprise_identity_employee_placeholder,
                )}
              />
            </div>
            <Button
              type="button"
              size="sm"
              onClick={connect}
              disabled={beginBinding.isPending}
            >
              <Building2 className="mr-2 h-4 w-4" />
              {beginBinding.isPending
                ? t(
                    ($) =>
                      $.tab_body.integrations.enterprise_identity_connecting,
                  )
                : t(($) => $.tab_body.integrations.enterprise_identity_connect)}
            </Button>
          </>
        ) : (
          <>
            <p className="text-xs text-muted-foreground">
              {t(($) => $.tab_body.integrations.enterprise_identity_unbound)}
            </p>
            <p className="text-xs text-muted-foreground">
              {t(($) => $.tab_body.integrations.enterprise_identity_members_note)}
            </p>
          </>
        )}
        {actionMessage ? (
          <p className="text-xs text-emerald-600">{actionMessage}</p>
        ) : null}
        {actionError ? (
          <p className="text-xs text-destructive">{actionError}</p>
        ) : null}
      </div>
    </section>
  );
}
