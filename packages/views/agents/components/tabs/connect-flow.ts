"use client";

import { useEffect, useRef } from "react";
import { toast } from "sonner";
import { useConfigStore } from "@multica/core/config";
import { connectorBrandName } from "../../../common/connector-logo";
import { useNavigation } from "../../../navigation";
import { isDesktopShell } from "../../../platform/local-directory";
import { openExternal } from "../../../platform/open-external";
import { useT } from "../../../i18n";

/** URL parameter of the open app dialog (deep-linkable). */
export const APP_PARAM = "app";

/** Replaces the current URL with `edit` applied to its search parameters. */
export function useReplaceSearch(): (edit: (params: URLSearchParams) => void) => void {
  const navigation = useNavigation();
  return (edit) => {
    const params = new URLSearchParams(navigation.searchParams);
    edit(params);
    const search = params.toString();
    navigation.replace(`${navigation.pathname}${search ? `?${search}` : ""}`);
  };
}

/**
 * Reports a provider sign-in result the server redirected back with
 * (`?connected=<slug>` or `?connect_error=<code>`) once as a toast, and
 * removes those parameters so a reload does not repeat it. `app` stays (or,
 * after a success, is set to the connected app), so the dialog the sign-in
 * started from opens again.
 */
export function useConnectReturnToast() {
  const { t } = useT("agents");
  const navigation = useNavigation();
  const replaceSearch = useReplaceSearch();
  const consumed = useRef(false);
  const connected = navigation.searchParams.get("connected");
  const error = navigation.searchParams.get("connect_error");

  useEffect(() => {
    if (consumed.current || (connected === null && error === null)) return;
    consumed.current = true;
    if (error !== null) {
      toast.error(
        error === "access_denied"
          ? t(($) => $.internal_mcp.catalog.returned_denied)
          : error === "browser_mismatch"
            ? t(($) => $.internal_mcp.catalog.returned_browser_mismatch)
            : t(($) => $.internal_mcp.catalog.returned_error),
      );
    } else {
      toast.success(
        t(($) => $.internal_mcp.catalog.returned_connected, { name: connectorBrandName(connected ?? "") }),
      );
    }
    replaceSearch((params) => {
      params.delete("connected");
      params.delete("connect_error");
      if (connected && error === null && !params.get(APP_PARAM)) params.set(APP_PARAM, connected);
    });
  }, [connected, error, replaceSearch, t]);
}

/**
 * Desktop never starts a provider sign-in itself: the start response binds
 * the sign-in to the browser that receives it, and desktop API responses land
 * in the app's own cookie jar. It opens `path` on the web in the system
 * browser instead, where the connect runs (the pages refetch on focus).
 * Returns true when it took over the connect.
 */
export function useDesktopConnectHandoff(): (path: string) => boolean {
  const { t } = useT("agents");
  const daemonAppUrl = useConfigStore((s) => s.daemonAppUrl);
  return (path) => {
    if (!isDesktopShell()) return false;
    const appUrl = daemonAppUrl.trim().replace(/\/+$/, "");
    if (!appUrl) {
      toast.error(t(($) => $.internal_mcp.catalog.connect_on_web));
      return true;
    }
    openExternal(`${appUrl}${path}`);
    toast.info(t(($) => $.internal_mcp.catalog.continue_in_browser));
    return true;
  };
}
