import { test, expect, type Page } from "@playwright/test";
import { TestApiClient } from "./fixtures";
import { waitForPageText } from "./helpers";

// DSH plugins: adding and removing one, on both surfaces that can change the
// set, plus the runtime gate.
//
// Auth and workspace bootstrap go through the real backend like every other
// spec. The plugin endpoints are mocked at the network boundary because a real
// import reaches out to the npm registry and a real binding needs a live DSH
// runtime — neither belongs in a UI test. What the test does assert for real is
// the request body each control produces, because that body is the contract:
// the agent binding endpoint replaces the whole set rather than applying a
// delta, so a control that sends the wrong list silently changes which plugins
// a task boots with.

const E2E_WORKER =
  process.env.TEST_PARALLEL_INDEX ?? process.env.TEST_WORKER_INDEX ?? "0";
const E2E_RUN_ID =
  process.env.E2E_RUN_ID ?? `${Date.now().toString(36)}-${process.pid.toString(36)}`;
const EMAIL = `e2e-dsh-${E2E_WORKER}-${E2E_RUN_ID}@multica.ai`;
const NAME = "E2E DSH User";

const AGENT_ID = "33333333-3333-4333-8333-333333333333";
const DSH_RUNTIME_ID = "44444444-4444-4444-8444-444444444444";
const HERMES_RUNTIME_ID = "55555555-5555-4555-8555-555555555555";

const LENS_ID = "66666666-6666-4666-8666-666666666666";
const OTHER_ID = "77777777-7777-4777-8777-777777777777";

interface Setup {
  slug: string;
  userId: string;
}

async function login(page: Page): Promise<Setup> {
  const api = new TestApiClient();
  const data = await api.login(EMAIL, NAME);
  const userId: string | undefined = data?.user?.id;
  if (!userId) throw new Error("login did not return a user id");
  const workspace = await api.ensureWorkspace(
    `E2E DSH WS ${E2E_WORKER}`,
    `e2e-dsh-${E2E_WORKER}-${E2E_RUN_ID}`,
  );
  await api.markUserOnboarded();
  const token = api.getToken();
  if (!token) throw new Error("login did not return a token");
  await page.addInitScript((t) => {
    localStorage.setItem("multica_token", t);
    localStorage.setItem("multica:chat:isOpen", "false");
  }, token);
  return { slug: workspace.slug, userId };
}

function mockAgent(ownerId: string, workspaceId: string, runtimeId: string) {
  return {
    id: AGENT_ID,
    workspace_id: workspaceId,
    runtime_id: runtimeId,
    name: "DSH Plugin Test Agent",
    description: "",
    instructions: "",
    avatar_url: null,
    runtime_mode: "cloud",
    runtime_config: {},
    custom_args: [],
    visibility: "workspace",
    status: "idle",
    max_concurrent_tasks: 1,
    model: "",
    owner_id: ownerId,
    skills: [],
    created_at: "2026-06-30T00:00:00Z",
    updated_at: "2026-06-30T00:00:00Z",
    archived_at: null,
    archived_by: null,
    composio_toolkit_allowlist: [],
  };
}

function mockRuntime(id: string, name: string, provider: string) {
  return {
    id,
    name,
    provider,
    status: "online",
    runtime_mode: "cloud",
    visibility: "workspace",
    metadata: { kind: "cloud-sandbox", sandbox_backend: "aliyun_fc" },
    created_at: "2026-06-30T00:00:00Z",
    updated_at: "2026-06-30T00:00:00Z",
  };
}

function mockPlugin(id: string, packageName: string, version: string) {
  return {
    id,
    workspace_id: "ws-mock",
    package_name: packageName,
    display_name: packageName,
    description: `${packageName} description`,
    homepage: "",
    source_kind: "npm",
    source_spec: `npm:${packageName}@${version}`,
    resolved_version: version,
    integrity: `sha256-${"a".repeat(64)}`,
    bundle_rows: ["mcp-lens"],
    config_row: "",
    config: {},
    catalog: "awesome-dsh-plugin",
    validated_dsh_version: "",
    created_by: null,
    created_at: "2026-06-30T00:00:00Z",
    updated_at: "2026-06-30T00:00:00Z",
  };
}

interface PluginMocks {
  /** Bodies captured from PUT /api/agents/{id}/dsh-plugins, in order. */
  writes: { id: string; enabled?: boolean }[][];
  /** Ids passed to DELETE /api/dsh-plugins/{id}. */
  deleted: string[];
  /** Bodies captured from POST /api/dsh-plugins. */
  imports: Record<string, unknown>[];
}

/**
 * Mock every endpoint the plugin surfaces read, with `attached` as the
 * server-side state the agent binding starts from.
 */
async function mockPluginApis(
  page: Page,
  ownerId: string,
  runtimeId: string,
  attached: { id: string; enabled: boolean }[],
): Promise<PluginMocks> {
  const captured: PluginMocks = { writes: [], deleted: [], imports: [] };
  const workspacePlugins = [
    mockPlugin(LENS_ID, "dsh-mcp-lens", "0.1.0-rc.9"),
    mockPlugin(OTHER_ID, "dsh-context", "0.43.0"),
  ];
  // Server-side state the mocks mutate, so a detach is visible on refetch.
  let bound = [...attached];

  await page.route("**/api/runtimes**", (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify([
        mockRuntime(DSH_RUNTIME_ID, "DSH", "dsh"),
        mockRuntime(HERMES_RUNTIME_ID, "FC", "hermes"),
      ]),
    });
  });

  await page.route("**/api/agents**", (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const workspaceId = url.searchParams.get("workspace_id") ?? "ws-mock";

    if (url.pathname.endsWith(`/api/agents/${AGENT_ID}/dsh-plugins`)) {
      if (request.method() === "PUT") {
        const body = request.postDataJSON?.() ?? {};
        const plugins = (body.plugins ?? []) as { id: string; enabled?: boolean }[];
        captured.writes.push(plugins);
        bound = plugins.map((row) => ({ id: row.id, enabled: row.enabled !== false }));
        return route.fulfill({ status: 204, body: "" });
      }
      if (request.method() === "GET") {
        return route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(
            bound.map((row) => {
              const plugin = workspacePlugins.find((p) => p.id === row.id);
              return { ...plugin, enabled: row.enabled };
            }),
          ),
        });
      }
    }

    if (request.method() === "GET" && url.pathname.endsWith("/api/agents")) {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([mockAgent(ownerId, workspaceId, runtimeId)]),
      });
    }
    return route.fallback();
  });

  await page.route("**/api/dsh-plugins**", (route) => {
    const request = route.request();
    const url = new URL(request.url());

    if (url.pathname.endsWith("/api/dsh-plugins")) {
      if (request.method() === "POST") {
        captured.imports.push(request.postDataJSON?.() ?? {});
        return route.fulfill({
          status: 201,
          contentType: "application/json",
          body: JSON.stringify({
            status: "created",
            plugin: mockPlugin(LENS_ID, "dsh-mcp-lens", "0.1.0-rc.9"),
            warnings: [],
          }),
        });
      }
      if (request.method() === "GET") {
        return route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(workspacePlugins),
        });
      }
    }

    const deleteMatch = url.pathname.match(/\/api\/dsh-plugins\/([0-9a-f-]+)$/);
    if (request.method() === "DELETE" && deleteMatch) {
      captured.deleted.push(deleteMatch[1]!);
      return route.fulfill({ status: 204, body: "" });
    }

    if (url.pathname.endsWith("/api/dsh-plugins/bindings")) {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(
          bound.map((row) => ({
            agent_id: AGENT_ID,
            dsh_plugin_id: row.id,
            enabled: row.enabled,
          })),
        ),
      });
    }

    if (url.pathname.endsWith("/api/dsh-plugins/catalog")) {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          entries: [],
          total: 0,
          limit: 30,
          offset: 0,
          state: {
            catalog: "awesome-dsh-plugin",
            catalog_version: "2026.906.3145",
            entry_count: 3195,
            refreshed_at: "2026-09-06T08:00:00Z",
            source_package: "dsh-plugin-catalog",
            source_repo: "https://github.com/awesome-dsh-plugin/awesome-dsh-plugin",
            source_site: "https://awesome-dsh-plugin.com",
            license: "CC0-1.0",
            official: false,
          },
        }),
      });
    }

    if (url.pathname.endsWith("/api/dsh-plugins/catalog/categories")) {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([]),
      });
    }

    return route.fallback();
  });

  return captured;
}

test.describe("DSH plugins", () => {
  test("attaching a plugin to an agent sends the whole set, and detaching sends it back", async ({
    page,
  }) => {
    const { slug, userId } = await login(page);
    // Start with nothing attached, so the first write is unambiguous.
    const captured = await mockPluginApis(page, userId, DSH_RUNTIME_ID, []);

    await page.goto(`/${slug}/agents/${AGENT_ID}`, { waitUntil: "domcontentloaded" });
    await waitForPageText(page, "DSH Plugin Test Agent");

    const tab = page.getByRole("button", { name: "Plugins", exact: true });
    await expect(tab).toBeVisible({ timeout: 15000 });
    await tab.click();

    // Empty state first — nothing is attached yet.
    await expect(page.getByText("No plugins attached")).toBeVisible();

    // Attach: the PUT carries the full set, which here is the one new plugin.
    await page.getByRole("button", { name: "Attach", exact: true }).first().click();
    await page
      .getByRole("listitem")
      .filter({ hasText: "dsh-mcp-lens" })
      .getByRole("button", { name: "Attach", exact: true })
      .click();

    await expect
      .poll(() => captured.writes.at(-1))
      .toEqual([{ id: LENS_ID, enabled: true }]);

    // The row appears, reading back the mocked server state.
    await expect(page.getByText("dsh-mcp-lens")).toBeVisible();

    // Detach: the PUT carries the set WITHOUT it — an empty list, not a delta.
    await page.getByRole("button", { name: /Detach dsh-mcp-lens/i }).click();
    await expect.poll(() => captured.writes.at(-1)).toEqual([]);
    await expect(page.getByText("No plugins attached")).toBeVisible();
  });

  test("disabling an attached plugin keeps it in the set", async ({ page }) => {
    const { slug, userId } = await login(page);
    const captured = await mockPluginApis(page, userId, DSH_RUNTIME_ID, [
      { id: LENS_ID, enabled: true },
    ]);

    await page.goto(`/${slug}/agents/${AGENT_ID}`, { waitUntil: "domcontentloaded" });
    await waitForPageText(page, "DSH Plugin Test Agent");
    await page.getByRole("button", { name: "Plugins", exact: true }).click();

    // A disabled plugin stays bound so its configuration survives; only the
    // composed set at task start drops it. Removing it from the list instead
    // would silently discard that configuration.
    await page.getByRole("switch", { name: /Enable dsh-mcp-lens/i }).click();
    await expect
      .poll(() => captured.writes.at(-1))
      .toEqual([{ id: LENS_ID, enabled: false }]);
  });

  test("the Plugins tab is not offered on a non-DSH runtime", async ({ page }) => {
    const { slug, userId } = await login(page);
    // Same agent, bound to a Hermes runtime. The composed plugin set only
    // reaches a DeepSeek Harness sandbox, so the tab must not appear.
    await mockPluginApis(page, userId, HERMES_RUNTIME_ID, []);

    await page.goto(`/${slug}/agents/${AGENT_ID}`, { waitUntil: "domcontentloaded" });
    await waitForPageText(page, "DSH Plugin Test Agent");

    await expect(page.getByRole("button", { name: "Activity" })).toBeVisible({
      timeout: 15000,
    });
    await expect(
      page.getByRole("button", { name: "Plugins", exact: true }),
    ).toHaveCount(0);
  });

  test("the workspace page imports a plugin and removes it", async ({ page }) => {
    const { slug, userId } = await login(page);
    const captured = await mockPluginApis(page, userId, DSH_RUNTIME_ID, []);

    await page.goto(`/${slug}/dsh-plugins`, { waitUntil: "domcontentloaded" });
    await waitForPageText(page, "DSH Plugins");

    // Both imported plugins list.
    await expect(page.getByText("dsh-mcp-lens")).toBeVisible({ timeout: 15000 });
    await expect(page.getByText("dsh-context")).toBeVisible();

    // Import: the body carries the spec verbatim, in npm's own vocabulary.
    await page.getByRole("button", { name: "Import plugin" }).click();
    await page
      .getByLabel("Package")
      .fill("npm:dsh-mcp-lens@0.1.0-rc.9");
    await page.getByRole("button", { name: "Import", exact: true }).click();
    await expect
      .poll(() => captured.imports.at(-1))
      .toMatchObject({ source: "npm:dsh-mcp-lens@0.1.0-rc.9" });

    // Remove: the confirm is accepted and the id reaches DELETE.
    page.once("dialog", (dialog) => void dialog.accept());
    await page
      .getByRole("listitem")
      .filter({ hasText: "dsh-mcp-lens" })
      .getByRole("button", { name: "Remove" })
      .click();
    await expect.poll(() => captured.deleted).toContain(LENS_ID);
  });
});
