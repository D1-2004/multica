export function normalizeCommandURL(url: string | undefined) {
  return url?.trim().replace(/\/+$/, "") ?? "";
}

/**
 * Copy-paste daemon setup commands for the current deployment.
 *
 * When /api/config supplies daemon_server_url + daemon_app_url (self-host /
 * Diamond-managed Aone), point the CLI at those hosts. Never invent the
 * public Multica Cloud addresses — `multica setup` already knows those.
 */
export function daemonSetupCommands(
  serverUrl: string | undefined,
  appUrl: string | undefined,
) {
  const normalizedServerUrl = normalizeCommandURL(serverUrl);
  const normalizedAppUrl = normalizeCommandURL(appUrl);
  if (normalizedServerUrl && normalizedAppUrl) {
    return {
      setupCmd: `multica setup self-host --server-url ${normalizedServerUrl} --app-url ${normalizedAppUrl}`,
      tokenCmd: `multica config set server_url ${normalizedServerUrl}
multica config set app_url ${normalizedAppUrl}
multica login --token <YOUR_TOKEN>
multica daemon start`,
    };
  }

  return {
    setupCmd: "multica setup",
    tokenCmd: `multica login --token <YOUR_TOKEN>
multica daemon start`,
  };
}
