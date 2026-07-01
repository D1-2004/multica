import { LoginPage } from "@multica/views/auth";
import { DragStrip } from "@multica/views/platform";
import { MulticaIcon } from "@multica/ui/components/common/multica-icon";
import { useConfigStore } from "@multica/core/config";

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
  // When the backend locks sign-in to DingTalk, hide the email form + Google
  // button here too (config is seeded by CoreProvider's AuthInitializer).
  const dingtalkOnly = useConfigStore((s) => s.dingtalkOnly);
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
      <LoginPage
        logo={<MulticaIcon bordered size="lg" />}
        onSuccess={() => {
          // Auth store update triggers AppContent re-render → shows DesktopShell.
          // Initial workspace navigation happens in routes.tsx via IndexRedirect.
        }}
        onGoogleLogin={dingtalkOnly ? undefined : openWebLogin}
        onDingtalkLogin={openWebLogin}
        dingtalkOnly={dingtalkOnly}
      />
    </div>
  );
}
