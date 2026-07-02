import { LoginPage } from "@multica/views/auth";
import { DragStrip } from "@multica/views/platform";
import { MulticaIcon } from "@multica/ui/components/common/multica-icon";
import { isLoginProviderAllowed, useConfigStore } from "@multica/core/config";
import { Loader2 } from "lucide-react";

function requireRuntimeAppUrl(): string {
  const runtimeConfig = window.desktopAPI.runtimeConfig;
  if (!runtimeConfig.ok) {
    throw new Error(
      "Invariant violated: DesktopLoginPage rendered before App accepted runtime config",
    );
  }
  return runtimeConfig.config.appUrl;
}

export function DesktopLoginPage() {
  const webUrl = requireRuntimeAppUrl();
  // When the backend locks sign-in with LOGIN_PROVIDERS, hide the closed
  // entry points here too (config is seeded by CoreProvider's AuthInitializer).
  const loginProviders = useConfigStore((s) => s.loginProviders);
  const allows = (provider: string) =>
    isLoginProviderAllowed(loginProviders, provider);
  // Only offer the Feishu button when the backend actually has a Feishu app
  // configured — unlike Google/DingTalk this is gated on config so
  // Feishu-less deployments don't show a dead button.
  const larkClientId = useConfigStore((s) => s.larkClientId);
  // Wait for /api/config before rendering the form so a provider-locked
  // backend doesn't flash the email + Google buttons first.
  const authConfigLoaded = useConfigStore((s) => s.authConfigLoaded);
  // Both OAuth providers hand off to the web login page (which renders whichever
  // provider buttons the backend has configured) in the default browser with the
  // platform=desktop flag. The web callback redirects the token back via the
  // multica:// deep link.
  const openWebLogin = () => {
    window.desktopAPI.openExternal(
      `${webUrl}/login?platform=desktop`,
    );
  };

  return (
    <div className="flex h-screen flex-col">
      <DragStrip />
      {authConfigLoaded ? (
        <LoginPage
          logo={<MulticaIcon bordered size="lg" />}
          onSuccess={() => {
            // Auth store update triggers AppContent re-render → shows DesktopShell.
            // Initial workspace navigation happens in routes.tsx via IndexRedirect.
          }}
          onGoogleLogin={allows("google") ? openWebLogin : undefined}
          onDingtalkLogin={allows("dingtalk") ? openWebLogin : undefined}
          onLarkLogin={allows("lark") && larkClientId ? openWebLogin : undefined}
          oauthOnly={!allows("email")}
        />
      ) : (
        <div className="flex flex-1 items-center justify-center">
          <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
        </div>
      )}
    </div>
  );
}
