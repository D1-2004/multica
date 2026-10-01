import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient, ApiError, CHAT_DRAFT_RESTORE_CAPABILITY, errorCode } from "./client";
import { noopLogger } from "../logger";
import { setSchemaLogger } from "./schema";

afterEach(() => {
  vi.unstubAllGlobals();
  setSchemaLogger(noopLogger);
});

describe("ApiClient hosted websites", () => {
  it("lists hosted websites through the validated user endpoint", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify([
          {
            site_id: "site-1",
            public_site_id: "public-1",
            status: "active",
            latest_revision_id: "revision-1",
            latest_status: "active",
            created_at: "2026-08-29T10:00:00Z",
            updated_at: "2026-08-29T11:00:00Z",
            site_url: "https://sites.example.test/sites/public-1/",
          },
        ]),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    await expect(
      new ApiClient("https://api.example.test").listHostedSites(),
    ).resolves.toEqual([
      expect.objectContaining({
        siteId: "site-1",
        siteUrl: "https://sites.example.test/sites/public-1/",
      }),
    ]);
    expect(fetchMock).toHaveBeenCalledWith(
      "https://api.example.test/api/sitehosting/sites",
      expect.any(Object),
    );
  });

  it("falls back to an empty list for a malformed response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify([{ site_id: "site-1" }]), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );

    await expect(
      new ApiClient("https://api.example.test").listHostedSites(),
    ).resolves.toEqual([]);
  });

  it("deletes the encoded site ID through the user endpoint", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(
      new ApiClient("https://api.example.test").deleteHostedSite("site/id"),
    ).resolves.toBeUndefined();
    expect(fetchMock).toHaveBeenCalledWith(
      "https://api.example.test/api/sitehosting/sites/site%2Fid",
      expect.objectContaining({ method: "DELETE" }),
    );
  });
});

describe("ApiClient DSH trajectory", () => {
  const taskId = "11111111-1111-4111-8111-111111111111";
  const sessionId = "ses-test";
  const sha256 = "a".repeat(64);
  const jsonl = [
    JSON.stringify({ type: "session", id: sessionId }),
    JSON.stringify({ type: "turn/start", seq: 1, time: 1, data: {} }),
    "",
  ].join("\n");

  it("accepts the authenticated NDJSON contract", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(jsonl, {
          status: 200,
          headers: {
            "Content-Type": "application/x-ndjson; charset=utf-8",
            "X-DSH-Session-ID": sessionId,
            "X-Content-SHA256": sha256,
            "X-DSH-Event-Count": "1",
          },
        }),
      ),
    );

    const client = new ApiClient("https://api.example.test");
    await expect(client.getDSHTrajectory(taskId)).resolves.toEqual({
      session_id: sessionId,
      sha256,
      event_count: 1,
      jsonl,
    });
  });

  it("rejects a trajectory served with the wrong media type", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(jsonl, {
          status: 200,
          headers: {
            "Content-Type": "application/json",
            "X-DSH-Session-ID": sessionId,
            "X-Content-SHA256": sha256,
            "X-DSH-Event-Count": "1",
          },
        }),
      ),
    );

    const client = new ApiClient("https://api.example.test");
    await expect(client.getDSHTrajectory(taskId)).rejects.toThrow(
      "Invalid DSH trajectory metadata",
    );
  });
});

describe("ApiClient Runner contracts", () => {
  const bindingId = "11111111-1111-4111-8111-111111111111";
  const machineId = "22222222-2222-4222-8222-222222222222";
  const pairingId = "33333333-3333-4333-8333-333333333333";
  const agentId = "44444444-4444-4444-8444-444444444444";

	it("keeps account pairing and Agent mounting as separate calls", async () => {
		const fetchMock = vi.fn()
			.mockResolvedValueOnce(new Response(JSON.stringify({ id: pairingId, install_command: "install runner", expires_at: "2026-09-03T10:10:00Z" }), { status: 201, headers: { "Content-Type": "application/json" } }))
			.mockResolvedValue(new Response(null, { status: 204 }));
		vi.stubGlobal("fetch", fetchMock);
		const client = new ApiClient("https://api.example.test");
		await client.createAccountRunnerPairing();
		await client.mountAgentRunnerMachine(agentId, machineId);
		await client.renameAccountRunnerMachine(machineId, "Studio Mac");
		await client.revokeAccountRunnerMachine(machineId);
		expect(fetchMock.mock.calls.map(([url, init]) => [url, init?.method])).toEqual([
			["https://api.example.test/api/me/runner-pairings", "POST"],
			[`https://api.example.test/api/agents/${agentId}/runner-mount`, "PUT"],
			[`https://api.example.test/api/me/runner-machines/${machineId}`, "PATCH"],
			[`https://api.example.test/api/me/runner-machines/${machineId}`, "DELETE"],
		]);
	});

  it("validates and maps every human-facing Runner endpoint", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            machines: [
              {
                binding_id: bindingId,
                machine_id: machineId,
                name: "build-mac",
                os: "darwin",
                arch: "arm64",
                client_version: "0.2.0",
                roots: ["/Users/dev/project"],
                online: true,
                disconnected: false,
                last_seen_at: "2026-08-12T10:00:00Z",
                bound_at: "2026-08-12T09:00:00Z",
                mcp_servers: [{
                  name: "wiki",
                  title: "Company Wiki",
                  description: "Search company knowledge.",
                  version: "2.0.0",
                  transport: "stdio",
                  availability: "available",
                  detail_status: "available",
                  capabilities: ["tools"],
                  tools: [{ name: "search", title: "Search", description: "Search pages." }],
                  fingerprint: "sha256:wiki",
                }],
              },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            id: pairingId,
            install_command: "curl example.test | sh",
            expires_at: "2026-08-12T10:10:00Z",
          }),
          { status: 201, headers: { "Content-Type": "application/json" } },
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            user_code: "ABCD-EFGH",
            state: "device_pending",
            machine_name: "build-mac",
            os: "darwin",
            arch: "arm64",
            expires_at: "2026-08-12T10:10:00Z",
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ status: "approved", machine_id: machineId }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ status: "disconnected" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            reconnect_command: "curl example.test | sh -s -- --reconnect-token secret",
            expires_at: "2026-08-12T10:20:00Z",
          }),
          { status: 201, headers: { "Content-Type": "application/json" } },
        ),
      )
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await expect(client.listAgentRunnerBindings(agentId)).resolves.toEqual({
      machines: [
        expect.objectContaining({
          bindingId,
          machineId,
          name: "build-mac",
          online: true,
          mcpServers: [expect.objectContaining({
            name: "wiki",
            title: "Company Wiki",
            detailStatus: "available",
            tools: [{ name: "search", title: "Search", description: "Search pages." }],
          })],
        }),
      ],
    });
    await expect(client.createAgentRunnerPairing(agentId)).resolves.toEqual({
      id: pairingId,
      installCommand: "curl example.test | sh",
      expiresAt: "2026-08-12T10:10:00Z",
    });
    await expect(
      client.getRunnerDeviceAuthorization("ABCD-EFGH"),
    ).resolves.toMatchObject({
      userCode: "ABCD-EFGH",
      machineName: "build-mac",
    });
    await expect(
      client.finishRunnerDeviceAuthorization("ABCD-EFGH", "approve"),
    ).resolves.toEqual({ status: "approved", machineId });
    await expect(
      client.disconnectAgentRunnerBinding(agentId, bindingId),
    ).resolves.toBeUndefined();
    await expect(
      client.createAgentRunnerReconnectCommand(agentId, bindingId),
    ).resolves.toEqual({
      reconnectCommand:
        "curl example.test | sh -s -- --reconnect-token secret",
      expiresAt: "2026-08-12T10:20:00Z",
    });
    await expect(
      client.revokeAgentRunnerBinding(agentId, bindingId),
    ).resolves.toBeUndefined();

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      `https://api.example.test/api/agents/${agentId}/runner-bindings`,
      `https://api.example.test/api/agents/${agentId}/runner-pairings`,
      "https://api.example.test/api/runner/device-authorizations/ABCD-EFGH",
      "https://api.example.test/api/runner/device-authorizations/ABCD-EFGH/approve",
      `https://api.example.test/api/agents/${agentId}/runner-bindings/${bindingId}/disconnect`,
      `https://api.example.test/api/agents/${agentId}/runner-bindings/${bindingId}/reconnect-command`,
      `https://api.example.test/api/agents/${agentId}/runner-bindings/${bindingId}`,
    ]);
  });

  it("rejects malformed Runner responses instead of hiding contract drift", async () => {
    const fetchMock = vi.fn().mockImplementation(() =>
      Promise.resolve(
        new Response(JSON.stringify({ machines: "not-an-array" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");

    await expect(client.listAgentRunnerBindings(agentId)).rejects.toThrow();
    await expect(client.createAgentRunnerPairing(agentId)).rejects.toThrow();
    await expect(
      client.getRunnerDeviceAuthorization("ABCD-EFGH"),
    ).rejects.toThrow();
  });

  it("defaults disconnected to false for older Runner binding responses", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            machines: [
              {
                binding_id: bindingId,
                machine_id: machineId,
                name: "build-mac",
                os: "darwin",
                arch: "arm64",
                client_version: "0.1.0",
                roots: ["/Users/dev/Desktop"],
                online: false,
                last_seen_at: null,
                bound_at: "2026-08-12T09:00:00Z",
                mcp_servers: null,
              },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );

    await expect(
      new ApiClient("https://api.example.test").listAgentRunnerBindings(agentId),
    ).resolves.toMatchObject({
      machines: [{ disconnected: false, mcpServers: [] }],
    });
  });

  it("maps account Runner machines separately from their Agent bindings", async () => {
    const workspaceId = "55555555-5555-4555-8555-555555555555";
    const secondAgentId = "66666666-6666-4666-8666-666666666666";
    const secondBindingId = "77777777-7777-4777-8777-777777777777";
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            machines: [
              {
                machine_id: machineId,
                name: "build-mac",
                os: "darwin",
                arch: "arm64",
                client_version: "0.2.0",
                online: true,
                last_seen_at: "2026-08-12T10:00:00Z",
                mcp_servers: null,
                bindings: [
                  {
                    binding_id: bindingId,
                    workspace_id: workspaceId,
                    workspace_name: "Platform",
                    workspace_slug: "platform",
                    agent_id: agentId,
                    agent_name: "Coder",
                    roots: ["/Users/dev/project"],
                    disconnected: false,
                    bound_at: "2026-08-12T09:00:00Z",
                  },
                  {
                    binding_id: secondBindingId,
                    workspace_id: workspaceId,
                    workspace_name: "Platform",
                    workspace_slug: "platform",
                    agent_id: secondAgentId,
                    agent_name: "Reviewer",
                    roots: ["/Users/dev/review"],
                    disconnected: true,
                    bound_at: "2026-08-12T09:30:00Z",
                  },
                ],
              },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      )
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            reconnect_command:
              "curl example.test | sh -s -- --reconnect-token secret",
            expires_at: "2026-08-12T10:20:00Z",
          }),
          { status: 201, headers: { "Content-Type": "application/json" } },
        ),
      )
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await expect(client.listAccountRunnerBindings()).resolves.toEqual({
      machines: [
        expect.objectContaining({
          machineId,
          online: true,
          mcpServers: [],
          bindings: [
            expect.objectContaining({
              bindingId,
              agentId,
              workspaceSlug: "platform",
              disconnected: false,
            }),
            expect.objectContaining({
              bindingId: secondBindingId,
              agentId: secondAgentId,
              disconnected: true,
            }),
          ],
        }),
      ],
    });
    await expect(
      client.disconnectAccountRunnerBinding(bindingId),
    ).resolves.toBeUndefined();
    await expect(
      client.createAccountRunnerReconnectCommand(secondBindingId),
    ).resolves.toEqual({
      reconnectCommand:
        "curl example.test | sh -s -- --reconnect-token secret",
      expiresAt: "2026-08-12T10:20:00Z",
    });
    await expect(
      client.revokeAccountRunnerBinding(secondBindingId),
    ).resolves.toBeUndefined();

    expect(
      fetchMock.mock.calls.map(([url, init]) => ({
        url,
        method: init?.method ?? "GET",
      })),
    ).toEqual([
      {
        url: "https://api.example.test/api/me/runner-bindings",
        method: "GET",
      },
      {
        url: `https://api.example.test/api/me/runner-bindings/${bindingId}/disconnect`,
        method: "POST",
      },
      {
        url: `https://api.example.test/api/me/runner-bindings/${secondBindingId}/reconnect-command`,
        method: "POST",
      },
      {
        url: `https://api.example.test/api/me/runner-bindings/${secondBindingId}`,
        method: "DELETE",
      },
    ]);
  });

  it("degrades malformed account Runner responses without logging file roots", async () => {
    const warn = vi.fn();
    setSchemaLogger({ ...noopLogger, warn });
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            machines: "not-an-array",
            roots: ["/Users/private/secret-project"],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            reconnect_command: 42,
            expires_at: "2026-08-12T10:20:00Z",
          }),
          { status: 201, headers: { "Content-Type": "application/json" } },
        ),
      );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await expect(client.listAccountRunnerBindings()).resolves.toBeNull();
    await expect(
      client.createAccountRunnerReconnectCommand(bindingId),
    ).resolves.toBeNull();

    expect(warn).toHaveBeenCalledTimes(2);
    expect(JSON.stringify(warn.mock.calls)).not.toContain(
      "/Users/private/secret-project",
    );
  });
});

describe("ApiClient pull-request response schema", () => {
  const validPR = {
    id: "pr-1",
    provider: "github",
    workspace_id: "ws-1",
    repo_owner: "acme",
    repo_name: "widget",
    number: 7,
    title: "MUL-1: fix",
    state: "open",
    html_url: "https://github.example/acme/widget/pull/7",
    branch: "fix/mul-1",
    author_login: "octocat",
    author_avatar_url: null,
    merged_at: null,
    closed_at: null,
    pr_created_at: "2026-01-01T00:00:00Z",
    pr_updated_at: "2026-01-01T00:00:00Z",
    snapshot_available: true,
    checks_rollup: "failure",
    failed_check_names: ["backend"],
  };

  it("parses and defaults a valid pull-request list", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ pull_requests: [validPR] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );

    const result = await new ApiClient("https://api.example.test").listIssuePullRequests("issue-1");
    expect(result.pull_requests[0]).toMatchObject({
      id: "pr-1",
      failed_check_names: ["backend"],
      checks_total: 0,
    });
  });

  it("falls back safely when failed_check_names is malformed", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            pull_requests: [{ ...validPR, failed_check_names: "backend" }],
          }),
          {
            status: 200,
            headers: { "Content-Type": "application/json" },
          },
        ),
      ),
    );

    await expect(
      new ApiClient("https://api.example.test").listIssuePullRequests("issue-1"),
    ).resolves.toEqual({ pull_requests: [] });
  });
});

describe("ApiClient server Table query", () => {
  it("posts the canonical query to the group and branch endpoints", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            query_fingerprint: "sha256:query",
            total: 1001,
            groups: [
              {
                key: "status:todo",
                value: { kind: "status", status: "todo" },
                count: 1001,
              },
            ],
            next_cursor: null,
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            query_fingerprint: "sha256:query",
            group_key: "status:todo",
            parent_id: null,
            total: 0,
            rows: [],
            branch_total: 0,
            next_cursor: "next-page",
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            query_fingerprint: "sha256:query",
            total: 1001,
            facets: [
              {
                kind: "status",
                values: [
                  { key: "todo", count: 501 },
                  { key: "done", count: 500 },
                ],
              },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    const query = {
      scope: { kind: "workspace" as const },
      filters: { priorities: ["high" as const] },
      sort: { field: "title" as const, direction: "asc" as const },
    };

    await expect(
      client.listIssueTableGroups({
        query,
        group: { kind: "status" },
        page: { limit: 100, cursor: null },
      }),
    ).resolves.toMatchObject({ total: 1001, groups: [{ count: 1001 }] });
    await expect(
      client.listIssueTableRows({
        query,
        group: { kind: "status" },
        group_key: "status:todo",
        hierarchy: { enabled: true },
        parent_id: null,
        page: { limit: 50, cursor: null },
      }),
    ).resolves.toMatchObject({ branch_total: 0, next_cursor: "next-page" });
    await expect(
      client.listIssueTableFacets({
        query,
        facets: [{ kind: "status" }],
      }),
    ).resolves.toMatchObject({
      total: 1001,
      facets: [{ values: [{ key: "todo", count: 501 }, { key: "done", count: 500 }] }],
    });

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "https://api.example.test/api/issues/table/groups",
      "https://api.example.test/api/issues/table/rows",
      "https://api.example.test/api/issues/table/facets",
    ]);
    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({
      method: "POST",
      body: expect.stringContaining('"kind":"status"'),
    });
  });

  it("falls back safely when Table responses are malformed", async () => {
    const fetchMock = vi.fn().mockImplementation(() =>
      Promise.resolve(
        new Response(JSON.stringify({ total: "not-a-number" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    const query = {
      scope: { kind: "workspace" as const },
      filters: {},
      sort: { field: "position" as const, direction: "asc" as const },
    };

    await expect(
      client.listIssueTableGroups({
        query,
        group: { kind: "status" },
        page: { limit: 100, cursor: null },
      }),
    ).resolves.toEqual({
      query_fingerprint: "",
      total: 0,
      groups: [],
      next_cursor: null,
    });
    await expect(
      client.listIssueTableRows({
        query,
        group: { kind: "none" },
        group_key: null,
        hierarchy: { enabled: true },
        parent_id: null,
        page: { limit: 50, cursor: null },
      }),
    ).resolves.toEqual({
      query_fingerprint: "",
      group_key: null,
      parent_id: null,
      total: 0,
      rows: [],
      branch_total: 0,
      next_cursor: null,
    });
    await expect(
      client.listIssueTableFacets({
        query,
        facets: [{ kind: "status" }],
      }),
    ).resolves.toEqual({
      query_fingerprint: "",
      total: 0,
      facets: [],
    });
  });

  it("preserves future Table status and actor enum values", async () => {
    const responses = [
      {
        query_fingerprint: "sha256:future-status",
        total: 1,
        groups: [
          {
            key: "status:paused",
            value: { kind: "status", status: "paused" },
            count: 1,
          },
        ],
        next_cursor: null,
      },
      {
        query_fingerprint: "sha256:future-actor",
        total: 1,
        groups: [
          {
            key: "service:bot-1",
            value: {
              kind: "assignee",
              actor: { type: "service", id: "bot-1" },
            },
            count: 1,
          },
        ],
        next_cursor: null,
      },
    ];
    const fetchMock = vi.fn().mockImplementation(() =>
      Promise.resolve(
        new Response(JSON.stringify(responses.shift()), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    const query = {
      scope: { kind: "workspace" as const },
      filters: {},
      sort: { field: "position" as const, direction: "asc" as const },
    };

    await expect(
      client.listIssueTableGroups({ query, group: { kind: "status" } }),
    ).resolves.toMatchObject({
      total: 1,
      groups: [{ value: { kind: "status", status: "paused" } }],
    });
    await expect(
      client.listIssueTableGroups({ query, group: { kind: "assignee" } }),
    ).resolves.toMatchObject({
      total: 1,
      groups: [
        { value: { kind: "assignee", actor: { type: "service", id: "bot-1" } } },
      ],
    });
  });

  it("parses compound lane descriptors and posts the additive union", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          query_fingerprint: "sha256:compound",
          total: 2,
          groups: [
            {
              key: "parent:parent-1",
              value: {
                kind: "parent",
                parent_id: "parent-1",
                parent: {
                  id: "parent-1",
                  number: 10,
                  identifier: "MUL-10",
                  title: "Parent",
                  status: "todo",
                },
                value_state: "value",
              },
              count: 2,
              secondary_groups: [
                {
                  key: "compound:opaque:status:todo",
                  value: { kind: "status", status: "todo" },
                  count: 2,
                },
              ],
            },
          ],
          next_cursor: null,
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");
    const query = {
      scope: { kind: "workspace" as const },
      filters: {},
      sort: { field: "position" as const, direction: "asc" as const },
    };

    await expect(
      client.listIssueTableGroups({
        query,
        group: {
          kind: "compound",
          primary: "parent",
          secondary: "status",
          secondary_values: ["todo"],
        },
      }),
    ).resolves.toMatchObject({
      groups: [
        {
          value: { kind: "parent", parent: { title: "Parent" } },
          secondary_groups: [
            { value: { kind: "status", status: "todo" }, count: 2 },
          ],
        },
      ],
    });
    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({
      body: expect.stringContaining(
        '"kind":"compound","primary":"parent","secondary":"status","secondary_values":["todo"]',
      ),
    });
  });
});

describe("ApiClient issue move intent", () => {
  it("posts relative anchors without a client-authored position", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ id: "issue-1", position: 15 }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");

    await client.moveIssue("issue-1", {
      status: "in_progress",
      before_id: "issue-0",
      after_id: "issue-2",
    });

    expect(fetchMock).toHaveBeenCalledWith(
      "https://api.example.test/api/issues/issue-1/move",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          status: "in_progress",
          before_id: "issue-0",
          after_id: "issue-2",
        }),
      }),
    );
  });
});

describe("ApiClient workspace working agents", () => {
  it("supports an optional source-type filter", async () => {
    const payload = [
      {
        id: "agent-1",
        name: "Agent 1",
        avatar_url: null,
        running_task_count: 2,
        issue_ids: ["issue-1"],
      },
    ];
    const fetchMock = vi.fn().mockImplementation(() =>
      Promise.resolve(
        new Response(JSON.stringify(payload), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await expect(
      client.getWorkspaceWorkingAgents("issue", "assigned"),
    ).resolves.toEqual(payload);
    await expect(
      client.getWorkspaceWorkingAgents("issue"),
    ).resolves.toEqual(payload);
    await expect(client.getWorkspaceWorkingAgents()).resolves.toEqual(payload);
    await expect(
      client.getWorkspaceWorkingAgents("issue", undefined, "parent-1"),
    ).resolves.toEqual(payload);
    // The server rejects parent alongside scope, so a My Issues relation wins
    // and the parent is dropped rather than sent into a 400.
    await expect(
      client.getWorkspaceWorkingAgents("issue", "assigned", "parent-1"),
    ).resolves.toEqual(payload);
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "https://api.example.test/api/working-agents?type=issue&scope=mine&relation=assigned",
      "https://api.example.test/api/working-agents?type=issue",
      "https://api.example.test/api/working-agents",
      "https://api.example.test/api/working-agents?type=issue&parent=parent-1",
      "https://api.example.test/api/working-agents?type=issue&scope=mine&relation=assigned",
    ]);
  });
});

describe("ApiClient A2A config response schemas", () => {
  it.each([
    ["GET", (client: ApiClient) => client.getAgentA2AConfig("agent/1")],
    [
      "PUT",
      (client: ApiClient) =>
        client.updateAgentA2AConfig("agent/1", {
          enabled: true,
          cardName: "Coding Agent",
          cardDescription: "Builds projects",
          cardVersion: "1.0.0",
          cardSkills: [],
        }),
    ],
  ])(
    "rejects a malformed %s response instead of returning an empty config",
    async (_method, request) => {
      const warn = vi.fn();
      setSchemaLogger({ ...noopLogger, warn });
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          new Response(JSON.stringify({ endpoint: "malformed" }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        ),
      );

      await expect(
        request(new ApiClient("https://api.example.test")),
      ).rejects.toThrow("Invalid A2A configuration response");
      expect(warn).toHaveBeenCalledTimes(1);
      expect(warn.mock.calls[0]?.[1]).not.toHaveProperty("received");
    },
  );
});

describe("ApiClient A2A operator response schema", () => {
  function stubJSON(body: unknown) {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    ));
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  it("maps the shared identity and both forward views", async () => {
    stubJSON({
      operator: true,
      dws_identity: {
        uid: "7015073760",
        org_id: "439446171",
        display_name: "Tagggg",
        organization_name: "钉钉",
        deap_agent_uuid: "18265b7f",
        a2a_enabled: true,
        bound_at: "2026-09-29T12:00:00Z",
      },
      prod_forward: {
        accept: true,
        registrations: [
          { registry: "https://fde-workbench.dingtalk.com", registered_at: "2026-09-29T12:01:00Z", current: true },
          { registry: "https://pre-fde-workbench.dingtalk.com", registered_at: null, current: "yes", error: "HTTP 503" },
        ],
      },
      forward_target: {
        rpc_url: "https://pre.example.test/api/a2a/agents/agent_1234567890123/v1",
        agent_name: "Pre QwenTag",
        registered_at: "2026-09-29T12:01:00Z",
      },
    });
    const config = await new ApiClient("https://api.example.test").getAgentA2AOperatorConfig("agent/1");
    expect(config).toEqual({
      operator: true,
      dwsIdentity: {
        uid: "7015073760",
        orgId: "439446171",
        displayName: "Tagggg",
        organizationName: "钉钉",
        deapAgentUuid: "18265b7f",
        a2aEnabled: true,
        boundAt: "2026-09-29T12:00:00Z",
      },
      prodForward: {
        accept: true,
        blockedReason: "",
        registrations: [
          { registry: "https://fde-workbench.dingtalk.com", registeredAt: "2026-09-29T12:01:00Z", current: true, error: "" },
          { registry: "https://pre-fde-workbench.dingtalk.com", registeredAt: null, current: false, error: "HTTP 503" },
        ],
      },
      forwardTarget: {
        rpcUrl: "https://pre.example.test/api/a2a/agents/agent_1234567890123/v1",
        agentName: "Pre QwenTag",
        registeredAt: "2026-09-29T12:01:00Z",
      },
    });
  });

  it("fails closed to operator=false on a malformed response", async () => {
    const warn = vi.fn();
    setSchemaLogger({ ...noopLogger, warn });
    stubJSON({ operator: "yes", dws_identity: 7, prod_forward: "x" });
    const config = await new ApiClient("https://api.example.test").getAgentA2AOperatorConfig("agent/1");
    expect(config.operator).toBe(false);
    expect(config.dwsIdentity).toBeNull();
    expect(config.prodForward).toBeNull();

    // A malformed registration list degrades to no registrations, not a crash.
    stubJSON({ operator: true, prod_forward: { accept: false, blocked_reason: 3, registrations: "x" } });
    const degraded = await new ApiClient("https://api.example.test").getAgentA2AOperatorConfig("agent/1");
    expect(degraded.prodForward).toEqual({ accept: false, blockedReason: "", registrations: [] });

    stubJSON("not-an-object");
    const fallback = await new ApiClient("https://api.example.test").getAgentA2AOperatorConfig("agent/1");
    expect(fallback).toEqual({
      operator: false,
      dwsIdentity: null,
      prodForward: null,
      forwardTarget: null,
    });
  });

  it("sends the identity binding and the forward switch with snake_case fields", async () => {
    const fetchMock = stubJSON({ operator: true });
    const client = new ApiClient("https://api.example.test");
    await client.updateAgentA2AOperatorIdentity("agent/1", {
      uid: "7015073760",
      orgId: "439446171",
      displayName: "Tagggg",
    });
    let [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("https://api.example.test/api/agents/agent%2F1/a2a/operator/dws-identity");
    expect(init.method).toBe("PUT");
    expect(JSON.parse(String(init.body))).toEqual({
      uid: "7015073760",
      org_id: "439446171",
      display_name: "Tagggg",
      organization_name: "",
      deap_agent_uuid: "",
    });

    await client.updateAgentA2AProdForward("agent/1", { accept: false });
    [url, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(url).toBe("https://api.example.test/api/agents/agent%2F1/a2a/operator/prod-forward");
    expect(JSON.parse(String(init.body))).toEqual({ accept: false });
  });
});

describe("ApiClient Agent OKR response schema", () => {
  it("preserves stable row and label ids and exposes usage availability", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            usage_available: true,
            okrs: [
              {
                id: "objective-1",
                label_id: "label-o-1",
                position: 2,
                objective: "Ship",
                label: "O: Ship",
                color: "#6366f1",
                spend: {
                  total_tokens: 100,
                  total_cost_usd_ticks: 200,
                  uncosted_tokens: 30,
                  task_count: 2,
                  unpriced_task_count: 1,
                },
                key_results: [
                  {
                    id: "kr-1",
                    label_id: "label-kr-1",
                    position: 0,
                    text: "Reach 90%",
                    label: "KR: Reach 90%",
                    color: "#0ea5e9",
                  },
                ],
              },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );

    const response = await new ApiClient("https://api.example.test").listAgentOKRs(
      "agent-1",
    );
    expect(response.usage_available).toBe(true);
    expect(response.okrs[0]).toMatchObject({
      id: "objective-1",
      label_id: "label-o-1",
      position: 2,
      spend: { uncosted_tokens: 30 },
    });
    expect(response.okrs[0]?.key_results[0]).toMatchObject({
      id: "kr-1",
      label_id: "label-kr-1",
      position: 0,
    });
  });

  it("drops only malformed optional spend and keeps the objective", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            usage_available: "not-a-boolean",
            okrs: [
              {
                objective: "Visible goal",
                label: "O: Visible goal",
                color: "#6366f1",
                spend: "not-an-object",
                key_results: [],
              },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );

    const response = await new ApiClient("https://api.example.test").listAgentOKRs(
      "agent-1",
    );
    expect(response.usage_available).toBe(false);
    expect(response.okrs).toHaveLength(1);
    expect(response.okrs[0]?.objective).toBe("Visible goal");
    expect(response.okrs[0]?.spend).toBeUndefined();
  });
});

describe("ApiClient label response schemas", () => {
  const label = {
    id: "label-1",
    workspace_id: "workspace-1",
    resource_type: "issue",
    name: "Expensive",
    description: "Track costly work",
    color: "#3b82f6",
    usage_count: 2,
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-24T00:00:00Z",
  };

  it("parses list summaries without requiring a detail request", async () => {
    const fetchMock = vi.fn().mockImplementation(() =>
      Promise.resolve(new Response(
        JSON.stringify({
          labels: [
            {
              ...label,
              usage_summary: {
                total_tokens: 12_000,
                total_cost_usd_ticks: 4_200_000_000,
                uncosted_tokens: 2_000,
                task_count: 2,
                priced_task_count: 1,
                unpriced_task_count: 1,
              },
            },
          ],
          total: 1,
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      )),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    const response = await client.listLabels("issue", { includeUsage: true });
    await client.listLabels("skill");

    expect(response.labels[0]?.usage_summary).toMatchObject({
      total_tokens: 12_000,
      uncosted_tokens: 2_000,
      unpriced_task_count: 1,
    });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain(
      "resource_type=issue&include_usage=true",
    );
    expect(String(fetchMock.mock.calls[1]?.[0])).toContain("resource_type=skill");
    expect(String(fetchMock.mock.calls[1]?.[0])).not.toContain("include_usage");
  });

  it("parses label usage detail and forwards period, sorting, pagination, and timezone", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          label,
          summary: {
            total_tokens: 12_000,
            total_cost_usd_ticks: 4_200_000_000,
            uncosted_tokens: 2_000,
            task_count: 2,
            priced_task_count: 1,
            unpriced_task_count: 1,
          },
          daily: [
            {
              date: "2026-08-24",
              total_tokens: 12_000,
              total_cost_usd_ticks: 4_200_000_000,
              uncosted_tokens: 2_000,
              task_count: 2,
              priced_task_count: 1,
              unpriced_task_count: 1,
            },
          ],
          breakdown: [
            {
              provider: "opencode",
              model: "qwen3.8-max",
              total_tokens: 12_000,
              total_cost_usd_ticks: 4_200_000_000,
              uncosted_tokens: 2_000,
              task_count: 2,
              unpriced_task_count: 1,
            },
          ],
          tasks: [
            {
              task_id: "task-1",
              agent_id: "agent-1",
              agent_name: "Daily Reporter",
              issue_id: "issue-1",
              issue_identifier: "MUL-1",
              issue_title: "Investigate spend",
              status: "completed",
              provider: "",
              model: "",
              has_usage: true,
              is_priced: false,
              total_tokens: 12_000,
              total_cost_usd_ticks: 4_200_000_000,
              uncosted_tokens: 2_000,
              usage_breakdown: [
                {
                  provider: "opencode",
                  model: "qwen3.8-max",
                  total_tokens: 12_000,
                  total_cost_usd_ticks: 4_200_000_000,
                  uncosted_tokens: 2_000,
                  is_priced: false,
                },
              ],
              created_at: "2026-08-24T01:00:00Z",
              completed_at: "2026-08-24T01:01:00Z",
              activity_at: "2026-08-24T01:01:00Z",
              attribution: {
                source: "direct_human",
                precise: true,
                initiator: { id: "user-1", name: "Owner" },
                originator: { id: "user-1", name: "Owner" },
                evidence: { kind: "issue_assignment", ref_id: "issue-1" },
              },
            },
          ],
          pagination: { page: 2, page_size: 25, total: 27, total_pages: 2 },
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");

    const response = await client.getLabelUsage("label/1", {
      period: "90d",
      sort: "tokens",
      direction: "asc",
      tz: "Asia/Shanghai",
      page: 2,
      page_size: 25,
    });

    expect(response.tasks[0]).toMatchObject({
      agent_id: "agent-1",
      agent_name: "Daily Reporter",
      activity_at: "2026-08-24T01:01:00Z",
      uncosted_tokens: 2_000,
      attribution: {
        source: "direct_human",
        precise: true,
        initiator: { id: "user-1", name: "Owner" },
      },
    });
    expect(response.tasks[0]?.usage_breakdown).toHaveLength(1);
    const requestURL = String(fetchMock.mock.calls[0]?.[0]);
    expect(requestURL).toContain("/api/labels/label%2F1/usage?");
    expect(requestURL).toContain("period=90d");
    expect(requestURL).toContain("sort=tokens");
    expect(requestURL).toContain("direction=asc");
    expect(requestURL).toContain("tz=Asia%2FShanghai");
    expect(requestURL).toContain("page=2");
    expect(requestURL).toContain("page_size=25");
  });

  it("drops an incomplete list summary instead of claiming its known cost is complete", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            labels: [
              {
                ...label,
                usage_summary: {
                  total_tokens: 12_000,
                  total_cost_usd_ticks: 4_200_000_000,
                  task_count: 2,
                  priced_task_count: 1,
                  unpriced_task_count: 1,
                },
              },
            ],
            total: 1,
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );

    const response = await new ApiClient("https://api.example.test").listLabels(
      "issue",
      { includeUsage: true },
    );

    expect(response.labels[0]?.usage_summary).toBeUndefined();
  });

  it("falls back to an empty usage detail when the response is malformed", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ summary: "invalid", tasks: {} }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );

    await expect(
      new ApiClient("https://api.example.test").getLabelUsage("label-1", {
        period: "30d",
        sort: "cost",
        direction: "desc",
        tz: "UTC",
        page: 1,
        page_size: 25,
      }),
    ).resolves.toMatchObject({
      label: { id: "" },
      summary: { total_tokens: 0, uncosted_tokens: 0 },
      tasks: [],
    });
  });

  it("falls back safely for malformed label catalog, label, and resource responses", async () => {
    const fetchMock = vi.fn().mockImplementation(() =>
      Promise.resolve(
        new Response(JSON.stringify({ labels: "not-an-array", total: "not-a-number" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");

    await expect(client.listLabels("agent")).resolves.toEqual({ labels: [], total: 0 });
    await expect(client.getLabel("label-1")).resolves.toMatchObject({ id: "" });
    await expect(
      client.createLabel({ resource_type: "agent", name: "Ops", color: "#3b82f6" }),
    ).resolves.toMatchObject({ id: "" });
    await expect(
      client.updateLabel("label-1", { name: "Operations" }),
    ).resolves.toMatchObject({ id: "" });

    await expect(client.listLabelsForIssue("issue-1")).resolves.toEqual({ labels: [] });
    await expect(client.attachLabel("issue-1", "label-1")).resolves.toEqual({ labels: [] });
    await expect(client.detachLabel("issue-1", "label-1")).resolves.toEqual({ labels: [] });

    await expect(client.listLabelsForResource("agent", "agent-1")).resolves.toEqual({ labels: [] });
    await expect(
      client.attachLabelToResource("agent", "agent-1", "label-1"),
    ).resolves.toEqual({ labels: [] });
    await expect(
      client.detachLabelFromResource("agent", "agent-1", "label-1"),
    ).resolves.toEqual({ labels: [] });

    expect(fetchMock).toHaveBeenCalledTimes(10);
  });
});

describe("ApiClient agent builder runtime switch", () => {
  it("PATCHes the session runtime endpoint and returns the runtime the server bound", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ runtime_id: "runtime-b" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await expect(
      client.switchAgentBuilderRuntime("session-1", { runtime_id: "runtime-b" }),
    ).resolves.toEqual({ runtime_id: "runtime-b" });

    const call = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(call[0]).toContain("/api/agent-builder/sessions/session-1/runtime");
    expect(call[1].method).toBe("PATCH");
    expect(JSON.parse(String(call[1].body))).toEqual({ runtime_id: "runtime-b" });
  });

  it("falls back to the requested runtime id for a malformed success body", async () => {
    // A 2xx means the rebind committed onto the runtime we asked for, so the
    // fallback must say so. Reporting "unknown" here would leave the picker on
    // the old runtime while the conversation executes on the new one — the very
    // split this endpoint exists to close.
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ runtime_id: 42 }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await expect(
      client.switchAgentBuilderRuntime("session-1", { runtime_id: "runtime-b" }),
    ).resolves.toEqual({ runtime_id: "runtime-b" });
  });

  it("rejects without a fallback when the switch is refused", async () => {
    // 409 (a reply in flight) means nothing was committed, so the caller must
    // see a rejection and keep the old runtime selected.
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: "stop the current reply before switching runtime" }), {
        status: 409,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await expect(
      client.switchAgentBuilderRuntime("session-1", { runtime_id: "runtime-b" }),
    ).rejects.toBeInstanceOf(ApiError);
  });
});

describe("ApiClient notification preferences", () => {
  it("sends atomic preference updates with PATCH", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          workspace_id: "workspace-1",
          preferences: {
            status_changes: "muted",
            comments: "muted",
          },
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await expect(
      client.updateNotificationPreferences(
        { comments: "muted" },
        "workspace-one",
      ),
    ).resolves.toEqual({
      workspace_id: "workspace-1",
      preferences: {
        status_changes: "muted",
        comments: "muted",
      },
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "https://api.example.test/api/notification-preferences",
    );
    expect(fetchMock.mock.calls[0]?.[1]).toEqual(
      expect.objectContaining({
        method: "PATCH",
        headers: expect.objectContaining({
          "X-Workspace-Slug": "workspace-one",
        }),
        body: JSON.stringify({ preferences: { comments: "muted" } }),
      }),
    );
  });

  it("falls back safely when a preference response is malformed", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({ workspace_id: "workspace-1", preferences: [] }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );

    const client = new ApiClient("https://api.example.test");
    await expect(client.getNotificationPreferences()).resolves.toEqual({
      workspace_id: "",
      preferences: {},
    });
  });
});

describe("ApiClient", () => {
  it("uses the generic cloud sandbox HTTP contract for ASB", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            valid: true,
            quotas: [
              {
                network_zone: "ALITest",
                region: "cn-zhangjiakou",
                quota: 5,
                usage: 1,
                remaining: 4,
                volume_usage_gib: 0,
              },
            ],
          }),
          {
            status: 200,
            headers: { "Content-Type": "application/json" },
          },
        ),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ id: "runtime-1" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            current: null,
            active_release: null,
            can_publish: true,
          }),
          {
            status: 200,
            headers: { "Content-Type": "application/json" },
          },
        ),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify([]), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify([]), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            configured: true,
            api_key_hint: "1234",
            quotas: [
              {
                network_zone: "ALITest",
                region: "cn-zhangjiakou",
                quota: 5,
                usage: 1,
                remaining: 4,
                volume_usage_gib: 0,
              },
            ],
            updated_at: 1799200000,
          }),
          {
            status: 200,
            headers: { "Content-Type": "application/json" },
          },
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            configured: true,
            api_key_hint: "5678",
            quotas: [
              {
                network_zone: "ALITest",
                region: "cn-zhangjiakou",
                quota: 5,
                usage: 2,
                remaining: 3,
                volume_usage_gib: 0,
              },
            ],
            invalidated_sandbox_count: 2,
          }),
          {
            status: 200,
            headers: { "Content-Type": "application/json" },
          },
        ),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ id: "runtime-1" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await expect(
      client.validateASBRuntimeCredential({
        api_key: "tenant-key-1234",
      }),
    ).resolves.toMatchObject({
      valid: true,
      quotas: [
        {
          network_zone: "ALITest",
          region: "cn-zhangjiakou",
          quota: 5,
          usage: 1,
          remaining: 4,
        },
      ],
    });
    await client.createCloudSandboxRuntime({
      sandbox_backend: "asb",
      api_key: "tenant-key-1234",
      name: "ASB Explorer",
      artifact_ref: "registry.example.test/multica/asb@sha256:abc",
      artifact_build_id: "build-42",
      artifact_alias: "asb-candidate",
      artifact_digest: "sha256:abc",
      artifact_channel: "candidate",
      provider: "opencode",
      visibility: "private",
    });
    await expect(
      client.getCloudSandboxStableChannel("asb"),
    ).resolves.toMatchObject({ current: null, can_publish: true });
    await expect(
      client.listCloudSandboxStableReleases("asb"),
    ).resolves.toEqual([]);
    await expect(
      client.listCloudSandboxStableRuntimes("asb"),
    ).resolves.toEqual([]);
    await expect(
      client.getASBRuntimeCredential("runtime/1"),
    ).resolves.toMatchObject({
      configured: true,
      api_key_hint: "1234",
      quotas: [{ quota: 5, usage: 1, remaining: 4 }],
    });
    await expect(
      client.updateASBRuntimeCredential("runtime/1", {
        api_key: "tenant-key-5678",
      }),
    ).resolves.toMatchObject({
      configured: true,
      api_key_hint: "5678",
      quotas: [{ quota: 5, usage: 2, remaining: 3 }],
      invalidated_sandbox_count: 2,
    });
    await client.updateCloudSandboxRuntimeArtifact("runtime/1", {
      artifact_ref: "registry.example.test/multica/asb@sha256:def",
      artifact_build_id: "build-43",
      artifact_alias: "asb-next",
      artifact_digest: "sha256:def",
    });

    expect(fetchMock.mock.calls.map(([url, init]) => ({
      url,
      method: init?.method ?? "GET",
      body: init?.body,
    }))).toEqual([
      {
        url:
          "https://api.example.test/api/runtimes/asb-credential/validate",
        method: "POST",
        body: JSON.stringify({
          api_key: "tenant-key-1234",
        }),
      },
      {
        url: "https://api.example.test/api/runtimes/cloud-sandbox",
        method: "POST",
        body: JSON.stringify({
          sandbox_backend: "asb",
          api_key: "tenant-key-1234",
          name: "ASB Explorer",
          artifact_ref: "registry.example.test/multica/asb@sha256:abc",
          artifact_build_id: "build-42",
          artifact_alias: "asb-candidate",
          artifact_digest: "sha256:abc",
          artifact_channel: "candidate",
          provider: "opencode",
          visibility: "private",
        }),
      },
      {
        url:
          "https://api.example.test/api/runtimes/cloud-sandbox/stable-channel?sandbox_backend=asb",
        method: "GET",
        body: undefined,
      },
      {
        url:
          "https://api.example.test/api/runtimes/cloud-sandbox/stable-releases?sandbox_backend=asb&limit=20",
        method: "GET",
        body: undefined,
      },
      {
        url:
          "https://api.example.test/api/runtimes/cloud-sandbox/stable-runtimes?sandbox_backend=asb",
        method: "GET",
        body: undefined,
      },
      {
        url:
          "https://api.example.test/api/runtimes/runtime%2F1/asb-credential",
        method: "GET",
        body: undefined,
      },
      {
        url:
          "https://api.example.test/api/runtimes/runtime%2F1/asb-credential",
        method: "PATCH",
        body: JSON.stringify({
          api_key: "tenant-key-5678",
        }),
      },
      {
        url:
          "https://api.example.test/api/runtimes/runtime%2F1/cloud-sandbox-artifact",
        method: "PATCH",
        body: JSON.stringify({
          artifact_ref: "registry.example.test/multica/asb@sha256:def",
          artifact_build_id: "build-43",
          artifact_alias: "asb-next",
          artifact_digest: "sha256:def",
        }),
      },
    ]);
  });

  it("uses the enterprise employee identity HTTP contract", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            configured: true,
            can_manage: true,
            identity: {
              employee_id: "*2345",
              display_name: "Zhang San",
              status: "active",
              aip_id: "aip-1",
              agent_spiffe_id: "spiffe://agents.example/ns/multica/agents/agent-1",
              buc_status: "active",
              agent_identity_status: "active",
              refresh_expires_at: 1799200000,
            },
          }),
          {
            status: 200,
            headers: { "Content-Type": "application/json" },
          },
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            authorization_url:
              "https://login.alibaba-inc.com/oauth2/auth.htm?state=opaque",
            expires_at: 1799200000,
          }),
          {
            status: 200,
            headers: { "Content-Type": "application/json" },
          },
        ),
      )
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await expect(
      client.getAgentEnterpriseIdentityStatus("workspace-1", "agent-1"),
    ).resolves.toMatchObject({
      configured: true,
      canManage: true,
      identity: {
        employeeId: "*2345",
        displayName: "Zhang San",
        status: "active",
        aipId: "aip-1",
        bucStatus: "active",
        agentIdentityStatus: "active",
      },
    });
    await expect(
      client.beginAgentEnterpriseIdentityBinding(
        "workspace-1",
        "agent-1",
        "/ws/workspace-1/agents/agent-1?tab=identity",
      ),
    ).resolves.toEqual({
      authorizationUrl:
        "https://login.alibaba-inc.com/oauth2/auth.htm?state=opaque",
      expiresAt: 1799200000,
    });
    await expect(
      client.revokeAgentEnterpriseIdentity("workspace-1", "agent-1"),
    ).resolves.toBeUndefined();

    expect(fetchMock.mock.calls.map(([url, init]) => ({
      url,
      method: init?.method ?? "GET",
      body: init?.body,
    }))).toEqual([
      {
        url:
          "https://api.example.test/api/workspaces/workspace-1/agent-identity/enterprise/status?agent_id=agent-1",
        method: "GET",
        body: undefined,
      },
      {
        url:
          "https://api.example.test/api/workspaces/workspace-1/agent-identity/enterprise/oauth/start",
        method: "POST",
        body: JSON.stringify({
          agent_id: "agent-1",
          redirect_path: "/ws/workspace-1/agents/agent-1?tab=identity",
        }),
      },
      {
        url:
          "https://api.example.test/api/workspaces/workspace-1/agent-identity/enterprise?agent_id=agent-1",
        method: "DELETE",
        body: undefined,
      },
    ]);
  });

  it("uses the DingTalk account binding HTTP contract", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            bindings: [
              {
                id: "installation-1",
                workspace_id: "workspace-1",
                agent_id: "agent-1",
                dws_identity: {
                  status: "active",
                  account_display_name: "Zhang San",
                  account_avatar_url: null,
                  bound_at: "2026-07-14T09:30:00Z",
                },
                message_route: { status: "active" },
              },
            ],
            configured: true,
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            binding_id: "agent-1",
            qr_code_url: "https://dbase.example/#bindingToken=secret",
            expires_at: "2026-07-14T09:35:00Z",
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      )
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await expect(
      client.listDingTalkAccountBindings("workspace-1"),
    ).resolves.toMatchObject({
      bindings: [{ id: "installation-1", agentId: "agent-1" }],
      configured: true,
    });
    await expect(
      client.beginDingTalkAccountBinding("workspace-1", "agent-1", "message"),
    ).resolves.toEqual({
      bindingId: "agent-1",
      qrCodeUrl: "https://dbase.example/#bindingToken=secret",
      expiresAt: "2026-07-14T09:35:00Z",
    });
    await expect(
      client.updateDingTalkAccountBindingSurface("workspace-1", "agent-1", "chat"),
    ).resolves.toBeUndefined();
    await expect(
      client.deleteDingTalkAccountBinding("workspace-1", "agent-1", "message"),
    ).resolves.toBeUndefined();

    expect(fetchMock.mock.calls.map(([url, init]) => ({
      url,
      method: init?.method ?? "GET",
      body: init?.body,
    }))).toEqual([
      {
        url: "https://api.example.test/api/workspaces/workspace-1/dingtalk/account-bindings",
        method: "GET",
        body: undefined,
      },
      {
        url: "https://api.example.test/api/workspaces/workspace-1/dingtalk/account-bindings/begin",
        method: "POST",
        body: JSON.stringify({ agent_id: "agent-1", binding_mode: "message" }),
      },
      {
        url: "https://api.example.test/api/workspaces/workspace-1/dingtalk/account-bindings/agent-1/surface",
        method: "PATCH",
        body: JSON.stringify({ surface_type: "chat" }),
      },
      {
        url: "https://api.example.test/api/workspaces/workspace-1/dingtalk/account-bindings/agent-1?binding_mode=message",
        method: "DELETE",
        body: undefined,
      },
    ]);
  });

  it("uses the DingTalk native subscription and manual binding HTTP contract", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ native_subscription: true }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ native_subscription: "off" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ status: "active", unexpected: true }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            code: "message_binding_conflicts_with_native_subscription",
            error: "turn native subscription off before binding",
          }),
          { status: 409, headers: { "Content-Type": "application/json" } },
        ),
      );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await expect(
      client.setDingTalkNativeSubscription("workspace-1", "agent-1", true),
    ).resolves.toEqual({ nativeSubscription: true });
    // A drifted echo falls back to the requested state instead of throwing.
    await expect(
      client.setDingTalkNativeSubscription("workspace-1", "agent-1", false),
    ).resolves.toEqual({ nativeSubscription: false });
    await expect(
      client.bindDingTalkMessageRouteManually("workspace-1", "agent-1", {
        corpId: "ding8196cd9a2b2405da24f2f5cc6abecb85",
        uid: "7890",
        messageScope: "direct_only",
      }),
    ).resolves.toBeUndefined();
    const conflict = await client
      .bindDingTalkMessageRouteManually("workspace-1", "agent-1", {
        corpId: "ding8196cd9a2b2405da24f2f5cc6abecb85",
        uid: "7890",
        messageScope: "all",
      })
      .catch((error: unknown) => error);
    expect(conflict).toBeInstanceOf(ApiError);
    expect(errorCode(conflict)).toBe(
      "message_binding_conflicts_with_native_subscription",
    );

    expect(fetchMock.mock.calls.map(([url, init]) => ({
      url,
      method: init?.method ?? "GET",
      body: init?.body,
    }))).toEqual([
      {
        url: "https://api.example.test/api/workspaces/workspace-1/dingtalk/account-bindings/agent-1/native-subscription",
        method: "PUT",
        body: JSON.stringify({ enabled: true }),
      },
      {
        url: "https://api.example.test/api/workspaces/workspace-1/dingtalk/account-bindings/agent-1/native-subscription",
        method: "PUT",
        body: JSON.stringify({ enabled: false }),
      },
      {
        url: "https://api.example.test/api/workspaces/workspace-1/dingtalk/account-bindings/agent-1/message-route/manual",
        method: "POST",
        body: JSON.stringify({ corp_id: "ding8196cd9a2b2405da24f2f5cc6abecb85", uid: "7890", message_scope: "direct_only" }),
      },
      {
        url: "https://api.example.test/api/workspaces/workspace-1/dingtalk/account-bindings/agent-1/message-route/manual",
        method: "POST",
        body: JSON.stringify({ corp_id: "ding8196cd9a2b2405da24f2f5cc6abecb85", uid: "7890", message_scope: "all" }),
      },
    ]);
  });

  it("reads the native subscription stream status and survives a drifted response", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            native_subscription: true,
            stream: { state: "disconnected", last_error: "dial: refused", failures: 2 },
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ stream: "connected" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await expect(
      client.getDingTalkNativeSubscriptionStatus("workspace-1", "agent-1"),
    ).resolves.toEqual({
      nativeSubscription: true,
      stream: {
        state: "disconnected",
        lastConnectedAt: null,
        lastEventAt: null,
        lastError: "dial: refused",
        failures: 2,
      },
    });
    const drifted = await client.getDingTalkNativeSubscriptionStatus("workspace-1", "agent-1");
    expect(drifted.stream.state).toBe("unknown");
    expect(fetchMock.mock.calls.map(([url, init]) => [url, init?.method ?? "GET"])).toEqual([
      ["https://api.example.test/api/workspaces/workspace-1/dingtalk/account-bindings/agent-1/native-subscription", "GET"],
      ["https://api.example.test/api/workspaces/workspace-1/dingtalk/account-bindings/agent-1/native-subscription", "GET"],
    ]);
  });

  it("uses the Agent Identity GitHub HTTP contract", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            configured: true,
            connection: {
              connection_id: "connection-1",
              account_login: "octocat",
              account_id: "42",
              status: "ACTIVE",
              granted_scopes: "repo",
              refresh_expires_at: 1799200000000,
            },
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            state: "state-1",
            authorization_url: "https://github.com/login/oauth/authorize",
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            ok: true,
            refreshed: true,
            connection_id: "connection-1",
            account_login: "octocat",
            account_id: "42",
            granted_scopes: "repo",
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            ok: true,
            connection_id: "connection-1",
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await expect(
      client.getAgentIdentityGitHubStatus("workspace-1", "agent-1"),
    ).resolves.toMatchObject({
      configured: true,
      connection: { connectionId: "connection-1", accountLogin: "octocat" },
    });
    await expect(
      client.beginAgentIdentityGitHubOAuth(
        "workspace-1",
        "agent-1",
        "/ws/agents/agent-1?tab=integrations",
      ),
    ).resolves.toEqual({
      state: "state-1",
      authorizationUrl: "https://github.com/login/oauth/authorize",
    });
    await expect(
      client.testAgentIdentityGitHubConnection("workspace-1", "agent-1", "connection-1"),
    ).resolves.toMatchObject({
      ok: true,
      refreshed: true,
      connectionId: "connection-1",
    });
    await expect(
      client.disconnectAgentIdentityGitHubConnection("workspace-1", "agent-1", "connection-1"),
    ).resolves.toMatchObject({
      ok: true,
      connectionId: "connection-1",
    });

    expect(fetchMock.mock.calls.map(([url, init]) => ({
      url,
      method: init?.method ?? "GET",
      body: init?.body,
    }))).toEqual([
      {
        url: "https://api.example.test/api/workspaces/workspace-1/agent-identity/github/status?agent_id=agent-1",
        method: "GET",
        body: undefined,
      },
      {
        url: "https://api.example.test/api/workspaces/workspace-1/agent-identity/github/oauth/start",
        method: "POST",
        body: JSON.stringify({
          agent_id: "agent-1",
          return_url: "/ws/agents/agent-1?tab=integrations",
        }),
      },
      {
        url: "https://api.example.test/api/workspaces/workspace-1/agent-identity/github/connection-1/test?agent_id=agent-1",
        method: "POST",
        body: undefined,
      },
      {
        url: "https://api.example.test/api/workspaces/workspace-1/agent-identity/github/connection-1?agent_id=agent-1",
        method: "DELETE",
        body: undefined,
      },
    ]);
  });

  it("does not log the credential-bearing begin payload when parsing fails", async () => {
    const warn = vi.fn();
    setSchemaLogger({ ...noopLogger, warn });
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            qr_code_url:
              "https://dbase.example/#bindingToken=router-secret&callbackToken=callback-secret",
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );

    const client = new ApiClient("https://api.example.test");
    await expect(
      client.beginDingTalkAccountBinding("workspace-1", "agent-1", "identity"),
    ).resolves.toEqual({ bindingId: "", qrCodeUrl: "", expiresAt: "" });

    expect(JSON.stringify(warn.mock.calls)).not.toContain("router-secret");
    expect(JSON.stringify(warn.mock.calls)).not.toContain("callback-secret");
  });

  it("preserves HTTP status on failed requests", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ error: "workspace slug already exists" }), {
          status: 409,
          statusText: "Conflict",
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );

    const client = new ApiClient("https://api.example.test");

    try {
      await client.createWorkspace({ name: "Test", slug: "test" });
      throw new Error("expected createWorkspace to fail");
    } catch (error) {
      expect(error).toBeInstanceOf(ApiError);
      expect(error).toMatchObject({
        message: "workspace slug already exists",
        status: 409,
        statusText: "Conflict",
      });
    }
  });

  it("preserves planned and delivered comment coverage from issue task runs", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify([
            {
              id: "task-1",
              status: "queued",
              trigger_comment_id: "comment-3",
              coalesced_comment_ids: ["comment-1", "comment-2"],
              delivered_comment_ids: ["comment-1", "comment-2", "comment-3"],
            },
          ]),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );

    const client = new ApiClient("https://api.example.test");
    const tasks = await client.listTasksByIssue("issue-1");

    expect(tasks[0]?.trigger_comment_id).toBe("comment-3");
    expect(tasks[0]?.coalesced_comment_ids).toEqual([
      "comment-1",
      "comment-2",
    ]);
    expect(tasks[0]?.delivered_comment_ids).toEqual([
      "comment-1",
      "comment-2",
      "comment-3",
    ]);
  });

  it("preserves sandbox_id on issue task runs", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify([
            { id: "task-1", status: "completed", sandbox_id: "sbx_issue_exec_1" },
          ]),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );

    const client = new ApiClient("https://api.example.test");
    const tasks = await client.listTasksByIssue("issue-1");
    expect(tasks[0]?.sandbox_id).toBe("sbx_issue_exec_1");
  });

  it("keeps task runs when optional comment coverage is malformed", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify([
            {
              id: "task-1",
              status: "queued",
              coalesced_comment_ids: ["comment-1", 2],
              delivered_comment_ids: "not-an-array",
            },
            {
              id: "task-2",
              status: "completed",
              delivered_comment_ids: ["comment-2", "comment-3"],
            },
          ]),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );

    const client = new ApiClient("https://api.example.test");
    const tasks = await client.listTasksByIssue("issue-1");

    expect(tasks).toHaveLength(2);
    expect(tasks[0]?.coalesced_comment_ids).toBeUndefined();
    expect(tasks[0]?.delivered_comment_ids).toBeUndefined();
    expect(tasks[1]?.delivered_comment_ids).toEqual([
      "comment-2",
      "comment-3",
    ]);
  });

  it("parses per-run token usage on task runs", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify([
            {
              id: "task-1",
              status: "completed",
              usage: [
                {
                  provider: "anthropic",
                  model: "claude-opus-5",
                  input_tokens: 96_000,
                  output_tokens: 34_000,
                  cache_read_tokens: 712_000,
                  cache_write_tokens: 50_000,
                  cost_usd_ticks: 19_990_000_000,
                },
              ],
            },
            // No usage at all — a run from before usage reporting.
            { id: "task-2", status: "completed" },
          ]),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );

    const client = new ApiClient("https://api.example.test");
    const tasks = await client.listTasksByIssue("issue-1");

    expect(tasks[0]?.usage).toHaveLength(1);
    expect(tasks[0]?.usage?.[0]).toMatchObject({
      model: "claude-opus-5",
      input_tokens: 96_000,
      cache_read_tokens: 712_000,
      cost_usd_ticks: 19_990_000_000,
    });
    // Absent, not [] — "we have no figure" must stay distinguishable from
    // "the figure is zero" all the way to the UI.
    expect(tasks[1]?.usage).toBeUndefined();
  });

  it("keeps task runs when per-run usage is malformed", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify([
            // Usage is not an array at all.
            { id: "task-1", status: "completed", usage: "1.2M" },
            // Usage is an array, but an entry has a string where a count belongs.
            {
              id: "task-2",
              status: "completed",
              usage: [{ model: "claude-opus-5", input_tokens: "many" }],
            },
            {
              id: "task-3",
              status: "completed",
              usage: [{ model: "gpt-5.6-terra", input_tokens: 31_000 }],
            },
          ]),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );

    const client = new ApiClient("https://api.example.test");
    const tasks = await client.listTasksByIssue("issue-1");

    // A bad usage payload costs that row its figure and nothing else — the
    // execution log still lists every run.
    expect(tasks).toHaveLength(3);
    expect(tasks[0]?.usage).toBeUndefined();
    expect(tasks[1]?.usage).toBeUndefined();
    expect(tasks[2]?.usage?.[0]?.input_tokens).toBe(31_000);
    expect(tasks[2]?.usage?.[0]?.output_tokens).toBe(0);
  });

  it("uses the expected HTTP contract for autopilot endpoints", async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(
      new Response(JSON.stringify({ autopilots: [], runs: [], total: 0, id: "tr-1", autopilot_id: "ap-1", kind: "schedule" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    ));
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");

    await client.listAutopilots({ status: "active" });
    await client.getAutopilot("ap-1");
    await client.createAutopilot({
      title: "Daily triage",
      project_id: "project-1",
      assignee_id: "agent-1",
      execution_mode: "create_issue",
    });
    await client.updateAutopilot("ap-1", { status: "paused", project_id: null });
    await client.deleteAutopilot("ap-1");
    await client.triggerAutopilot("ap-1");
    await client.listAutopilotRuns("ap-1", { limit: 10, offset: 20 });
    await client.createAutopilotTrigger("ap-1", {
      kind: "schedule",
      cron_expression: "0 9 * * *",
      timezone: "UTC",
    });
    await client.updateAutopilotTrigger("ap-1", "tr-1", { enabled: false });
    await client.deleteAutopilotTrigger("ap-1", "tr-1");
    await client.rotateAutopilotTriggerWebhookToken("ap-1", "tr-1");

    const calls = fetchMock.mock.calls.map(([url, init]) => ({
      url,
      method: init?.method ?? "GET",
      body: init?.body,
    }));

    expect(calls).toMatchObject([
      { url: "https://api.example.test/api/autopilots?status=active", method: "GET" },
      { url: "https://api.example.test/api/autopilots/ap-1", method: "GET" },
      {
        url: "https://api.example.test/api/autopilots",
        method: "POST",
        body: JSON.stringify({
          title: "Daily triage",
          project_id: "project-1",
          assignee_id: "agent-1",
          execution_mode: "create_issue",
        }),
      },
      {
        url: "https://api.example.test/api/autopilots/ap-1",
        method: "PATCH",
        body: JSON.stringify({ status: "paused", project_id: null }),
      },
      { url: "https://api.example.test/api/autopilots/ap-1", method: "DELETE" },
      { url: "https://api.example.test/api/autopilots/ap-1/trigger", method: "POST" },
      { url: "https://api.example.test/api/autopilots/ap-1/runs?limit=10&offset=20", method: "GET" },
      {
        url: "https://api.example.test/api/autopilots/ap-1/triggers",
        method: "POST",
        body: JSON.stringify({
          kind: "schedule",
          cron_expression: "0 9 * * *",
          timezone: "UTC",
        }),
      },
      {
        url: "https://api.example.test/api/autopilots/ap-1/triggers/tr-1",
        method: "PATCH",
        body: JSON.stringify({ enabled: false }),
      },
      { url: "https://api.example.test/api/autopilots/ap-1/triggers/tr-1", method: "DELETE" },
      {
        url: "https://api.example.test/api/autopilots/ap-1/triggers/tr-1/rotate-webhook-token",
        method: "POST",
      },
    ]);
  });

  it("emits X-Client-* headers when identity is configured", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify([]), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test", {
      identity: { platform: "desktop", version: "1.2.3", os: "macos" },
    });
    await client.listWorkspaces();

    const headers = fetchMock.mock.calls[0]![1]!.headers as Record<string, string>;
    expect(headers["X-Client-Platform"]).toBe("desktop");
    expect(headers["X-Client-Version"]).toBe("1.2.3");
    expect(headers["X-Client-OS"]).toBe("macos");
  });

  it("omits X-Client-* headers when identity is not configured", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify([]), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await client.listWorkspaces();

    const headers = fetchMock.mock.calls[0]![1]!.headers as Record<string, string>;
    expect(headers["X-Client-Platform"]).toBeUndefined();
    expect(headers["X-Client-Version"]).toBeUndefined();
    expect(headers["X-Client-OS"]).toBeUndefined();
  });

  it("posts feedback kind and parses the response through the schema", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ id: "feedback-1", created_at: "2026-06-26T00:00:00Z" }), {
        status: 201,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    const response = await client.createFeedback({
      message: "Desktop route crashed",
      url: "app://desktop/acme/issues",
      workspace_id: "ws-1",
      kind: "bug",
    });

    expect(response).toEqual({
      id: "feedback-1",
      created_at: "2026-06-26T00:00:00Z",
    });
    expect(fetchMock).toHaveBeenCalledWith(
      "https://api.example.test/api/feedback",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          message: "Desktop route crashed",
          url: "app://desktop/acme/issues",
          workspace_id: "ws-1",
          kind: "bug",
        }),
      }),
    );
  });

  it("falls back to an empty feedback response when the server shape drifts", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ id: 42, created_at: "2026-06-26T00:00:00Z" }), {
          status: 201,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );

    const client = new ApiClient("https://api.example.test");
    await expect(client.createFeedback({ message: "hello" })).resolves.toEqual({
      id: "",
      created_at: "",
    });
  });

  it("uses the expected HTTP contract for comment trigger preview and suppress", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ agents: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({
          id: "comment-1",
          issue_id: "issue-1",
          author_type: "member",
          author_id: "user-1",
          content: "hello",
          type: "comment",
          parent_id: null,
          reactions: [],
          attachments: [],
          created_at: "2026-06-05T00:00:00Z",
          updated_at: "2026-06-05T00:00:00Z",
        }), {
          status: 201,
          headers: { "Content-Type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({
          id: "comment-1",
          issue_id: "issue-1",
          author_type: "member",
          author_id: "user-1",
          content: "updated",
          type: "comment",
          parent_id: null,
          reactions: [],
          attachments: [],
          created_at: "2026-06-05T00:00:00Z",
          updated_at: "2026-06-05T00:01:00Z",
        }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await client.previewCommentTriggers("issue-1", "hello", "parent-1", "comment-1");
    await client.createComment(
      "issue-1",
      "hello",
      "comment",
      "parent-1",
      ["attachment-1"],
      ["agent-1"],
    );
    await client.updateComment("comment-1", "updated", ["attachment-1"], ["agent-1"]);

    expect(fetchMock.mock.calls.map(([url, init]) => ({
      url,
      method: init?.method,
      body: init?.body,
    }))).toMatchObject([
      {
        url: "https://api.example.test/api/issues/issue-1/comments/trigger-preview",
        method: "POST",
        body: JSON.stringify({ content: "hello", parent_id: "parent-1", editing_comment_id: "comment-1" }),
      },
      {
        url: "https://api.example.test/api/issues/issue-1/comments",
        method: "POST",
        body: JSON.stringify({
          content: "hello",
          type: "comment",
          parent_id: "parent-1",
          attachment_ids: ["attachment-1"],
          suppress_agent_ids: ["agent-1"],
        }),
      },
      {
        url: "https://api.example.test/api/comments/comment-1",
        method: "PUT",
        body: JSON.stringify({
          content: "updated",
          attachment_ids: ["attachment-1"],
          suppress_agent_ids: ["agent-1"],
        }),
      },
    ]);
  });

  it("uses the Cloud Runtime node API contract", async () => {
    const node = {
      id: "node-1",
      owner_id: "user-1",
      instance_id: "i-0123456789abcdef0",
      region: "us-west-2",
      instance_type: "g5.xlarge",
      image_id: "ami-1",
      subnet_id: "subnet-1",
      name: "gpu-dev-01",
      status: "launching",
      tags: {},
      metadata: {},
      created_at: "2026-05-21T08:30:00Z",
      updated_at: "2026-05-21T08:30:00Z",
    };
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify([]), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify(node), {
          status: 201,
          headers: { "Content-Type": "application/json" },
        }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await client.listCloudRuntimeNodes({ limit: 20, offset: 5 });
    await client.createCloudRuntimeNode(
      { instance_type: "g5.xlarge", name: "gpu-dev-01" },
    );

    const listCall = fetchMock.mock.calls[0]!;
    const createCall = fetchMock.mock.calls[1]!;
    expect(listCall[0]).toBe(
      "https://api.example.test/api/cloud-runtime/nodes?limit=20&offset=5",
    );
    expect(createCall[0]).toBe(
      "https://api.example.test/api/cloud-runtime/nodes",
    );
    expect(createCall[1]).toMatchObject({
      method: "POST",
      body: JSON.stringify({
        instance_type: "g5.xlarge",
        name: "gpu-dev-01",
      }),
    });
  });

  it("uses the FC/E2B runtime creation API contract", async () => {
    const runtime = {
      id: "rt-fc",
      workspace_id: "ws-1",
      daemon_id: "fc-e2b:ws-1:fc-hermes",
      name: "FC-Hermes",
      runtime_mode: "cloud",
      provider: "hermes",
      launch_header: "",
      status: "online",
      device_info: "FC/E2B one-shot sandbox",
      metadata: { kind: "fc-e2b" },
      owner_id: "user-1",
      visibility: "private",
      last_seen_at: null,
      created_at: "2026-07-08T00:00:00Z",
      updated_at: "2026-07-08T00:00:00Z",
    };
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify([{ template: "multica-fc-hermes-v1" }]),
          {
            status: 200,
            headers: { "Content-Type": "application/json" },
          },
        ),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify(runtime), {
          status: 201,
          headers: { "Content-Type": "application/json" },
        }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await client.listFCE2BTemplates();
    await client.createFCE2BRuntime({
      name: "FC-Hermes",
      template_id: "multica-fc-hermes-v1",
      provider: "hermes",
      visibility: "private",
    });

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "https://api.example.test/api/runtimes/fc-e2b/templates",
    );
    expect(fetchMock.mock.calls[1]?.[0]).toBe(
      "https://api.example.test/api/runtimes/fc-e2b",
    );
    expect(fetchMock.mock.calls[1]?.[1]).toMatchObject({
      method: "POST",
      body: JSON.stringify({
        name: "FC-Hermes",
        template_id: "multica-fc-hermes-v1",
        provider: "hermes",
        visibility: "private",
      }),
    });
  });

  it("uses the FC/E2B runtime template update API contract", async () => {
    const runtime = {
      id: "rt-fc",
      workspace_id: "ws-1",
      daemon_id: "fc-e2b:ws-1:fc-hermes",
      name: "FC-Hermes",
      runtime_mode: "cloud",
      provider: "hermes",
      launch_header: "",
      status: "online",
      device_info: "FC/E2B one-shot sandbox",
      metadata: {
        kind: "fc-e2b",
        template: "multica-fc-team-v2",
        template_id: "tpl-v2",
      },
      owner_id: "user-1",
      visibility: "private",
      last_seen_at: null,
      created_at: "2026-07-08T00:00:00Z",
      updated_at: "2026-07-20T00:00:00Z",
    };
    const fetchMock = vi.fn().mockResolvedValueOnce(
      new Response(JSON.stringify(runtime), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    const updated = await client.updateFCE2BRuntimeTemplate("rt-fc", {
      template_id: "tpl-v2",
    });

    expect(updated.id).toBe("rt-fc");
    expect(fetchMock).toHaveBeenCalledWith(
      "https://api.example.test/api/runtimes/rt-fc/fc-e2b-template",
      expect.objectContaining({
        method: "PATCH",
        body: JSON.stringify({ template_id: "tpl-v2" }),
      }),
    );
  });

  it("falls back when Cloud Runtime node responses drift", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify([{ id: 123 }]), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ id: 123 }), {
          status: 201,
          headers: { "Content-Type": "application/json" },
        }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");

    await expect(client.listCloudRuntimeNodes()).resolves.toEqual([]);
    await expect(
      client.createCloudRuntimeNode({ instance_type: "g5.xlarge" }),
    ).resolves.toMatchObject({ id: "", status: "" });
  });

  it("deleteCloudRuntimeNode sends DELETE with JSON body containing instance id", async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(
      new Response(null, { status: 204 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await client.deleteCloudRuntimeNode("i-0123456789abcdef0");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, opts] = fetchMock.mock.calls[0]!;
    expect(url).toBe("https://api.example.test/api/cloud-runtime/nodes");
    expect(opts).toMatchObject({
      method: "DELETE",
      body: JSON.stringify({ instance_id: "i-0123456789abcdef0" }),
    });
    expect((opts.headers as Record<string, string>)["Content-Type"]).toBe(
      "application/json",
    );
  });

  describe("getAttachment", () => {
    it("returns the parsed attachment for a well-formed response", async () => {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          new Response(
            JSON.stringify({
              id: "att-1",
              workspace_id: "ws-1",
              issue_id: null,
              comment_id: null,
              uploader_type: "member",
              uploader_id: "u-1",
              filename: "report.md",
              url: "https://static.example.test/ws/att-1.md",
              download_url:
                "https://static.example.test/ws/att-1.md?Policy=p&Signature=s&Key-Pair-Id=k",
              content_type: "text/markdown",
              size_bytes: 123,
              created_at: "2026-05-11T00:00:00Z",
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        ),
      );

      const client = new ApiClient("https://api.example.test");
      const att = await client.getAttachment("att-1");

      expect(att.id).toBe("att-1");
      expect(att.download_url).toContain("Policy=");
    });

    it("falls back to an empty attachment when the response is missing download_url", async () => {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          new Response(JSON.stringify({ id: "att-1" }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        ),
      );

      const client = new ApiClient("https://api.example.test");
      const att = await client.getAttachment("att-1");

      // parseWithFallback returns the EMPTY_ATTACHMENT record so callers can
      // safely read `download_url` without crashing — they'll see "" and
      // surface a user-facing error instead of opening `undefined`.
      expect(att.id).toBe("");
      expect(att.download_url).toBe("");
    });
  });

  describe("getAttachmentTextContent", () => {
    it("returns body text and the original content type from the X-* header", async () => {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          new Response("# heading\n\nbody\n", {
            status: 200,
            headers: {
              "Content-Type": "text/plain; charset=utf-8",
              "X-Original-Content-Type": "text/markdown",
            },
          }),
        ),
      );

      const client = new ApiClient("https://api.example.test");
      const { text, originalContentType } =
        await client.getAttachmentTextContent("att-1");

      expect(text).toBe("# heading\n\nbody\n");
      expect(originalContentType).toBe("text/markdown");
    });

    it("throws PreviewTooLargeError on 413", async () => {
      const { PreviewTooLargeError } = await import("./client");
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          new Response("", { status: 413, statusText: "Payload Too Large" }),
        ),
      );

      const client = new ApiClient("https://api.example.test");
      await expect(client.getAttachmentTextContent("att-1")).rejects.toBeInstanceOf(
        PreviewTooLargeError,
      );
    });

    it("throws PreviewUnsupportedError on 415", async () => {
      const { PreviewUnsupportedError } = await import("./client");
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          new Response("", { status: 415, statusText: "Unsupported Media Type" }),
        ),
      );

      const client = new ApiClient("https://api.example.test");
      await expect(client.getAttachmentTextContent("att-1")).rejects.toBeInstanceOf(
        PreviewUnsupportedError,
      );
    });
  });

  describe("listChatMessagesPage deployment-order fallback", () => {
    const jsonResponse = (body: unknown, status: number, statusText = "") =>
      new Response(JSON.stringify(body), {
        status,
        statusText,
        headers: { "Content-Type": "application/json" },
      });

    it("falls back to the legacy full-list endpoint when the paged route 404s", async () => {
      const legacy = [
        {
          id: "m1",
          chat_session_id: "session-1",
          role: "user",
          content: "hi",
          task_id: null,
          created_at: "2026-06-01T00:00:00Z",
          quick_actions: [],
        },
        {
          id: "m2",
          chat_session_id: "session-1",
          role: "assistant",
          content: "yo",
          task_id: "task-1",
          created_at: "2026-06-01T00:00:01Z",
          quick_actions: [
            { label: "Continue", prompt: "Continue with the next step", primary: true },
          ],
        },
      ];
      const fetchMock = vi
        .fn()
        .mockResolvedValueOnce(jsonResponse({ error: "not found" }, 404, "Not Found"))
        .mockResolvedValueOnce(jsonResponse(legacy, 200));
      vi.stubGlobal("fetch", fetchMock);

      const client = new ApiClient("https://api.example.test");
      const page = await client.listChatMessagesPage("session-1", { limit: 50 });

      expect(fetchMock).toHaveBeenCalledTimes(2);
      expect(fetchMock.mock.calls[0]![0]).toBe(
        "https://api.example.test/api/chat/sessions/session-1/messages/page?limit=50",
      );
      expect(fetchMock.mock.calls[1]![0]).toBe(
        "https://api.example.test/api/chat/sessions/session-1/messages",
      );
      expect(page).toEqual({ messages: legacy, limit: 50, has_more: false, next_cursor: null });
    });

    it("keeps a valid reply when its optional quick actions are malformed", async () => {
      const malformed = [{
        id: "m1",
        chat_session_id: "session-1",
        role: "assistant",
        content: "safe reply",
        task_id: "task-1",
        created_at: "2026-06-01T00:00:00Z",
        quick_actions: [{ label: 42, prompt: false }],
      }];
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(malformed, 200)));

      const client = new ApiClient("https://api.example.test");
      await expect(client.listChatMessages("session-1")).resolves.toEqual([
        expect.objectContaining({ content: "safe reply", quick_actions: [] }),
      ]);
    });

    it("falls back to an empty page for a malformed paged response", async () => {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(jsonResponse({ messages: "broken" }, 200)),
      );

      const client = new ApiClient("https://api.example.test");
      await expect(
        client.listChatMessagesPage("session-1", { limit: 25 }),
      ).resolves.toEqual({ messages: [], limit: 25, has_more: false, next_cursor: null });
    });

    it("does NOT fall back on a cursor request — a 404 there propagates", async () => {
      const fetchMock = vi
        .fn()
        .mockResolvedValue(jsonResponse({ error: "not found" }, 404, "Not Found"));
      vi.stubGlobal("fetch", fetchMock);

      const client = new ApiClient("https://api.example.test");
      await expect(
        client.listChatMessagesPage("session-1", {
          before: { created_at: "2026-06-01T00:00:00Z", id: "m1" },
        }),
      ).rejects.toBeInstanceOf(ApiError);
      // Only the paged request fires; no legacy full-list call that would duplicate messages.
      expect(fetchMock).toHaveBeenCalledTimes(1);
    });

    it("propagates non-404 errors instead of masking them with the legacy list", async () => {
      const fetchMock = vi
        .fn()
        .mockResolvedValue(jsonResponse({ error: "boom" }, 500, "Internal Server Error"));
      vi.stubGlobal("fetch", fetchMock);

      const client = new ApiClient("https://api.example.test");
      await expect(client.listChatMessagesPage("session-1")).rejects.toMatchObject({
        status: 500,
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);
    });
  });

  describe("reportChatReplyReceived", () => {
    it("posts the rendered reply timing to the message receipt endpoint", async () => {
      const fetchMock = vi
        .fn()
        .mockResolvedValue(new Response(null, { status: 204 }));
      vi.stubGlobal("fetch", fetchMock);

      const client = new ApiClient("https://api.example.test");
      await client.reportChatReplyReceived("session-1", "message-1", {
        task_id: "task-1",
        trace_id: "trace-1",
        ws_received_at_unix_ms: 2_000,
        rendered_at_unix_ms: 2_250,
        client_received_at: "1970-01-01T00:00:02.250Z",
        elapsed_ms: 1_250,
      });

      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(fetchMock.mock.calls[0]).toMatchObject([
        "https://api.example.test/api/chat/sessions/session-1/messages/message-1/received",
        {
          method: "POST",
          body: JSON.stringify({
            task_id: "task-1",
            trace_id: "trace-1",
            ws_received_at_unix_ms: 2_000,
            rendered_at_unix_ms: 2_250,
            client_received_at: "1970-01-01T00:00:02.250Z",
            elapsed_ms: 1_250,
          }),
        },
      ]);
    });
  });

  describe("cancelTaskById response parsing", () => {
    const taskResponse = {
      id: "task-1",
      agent_id: "agent-1",
      runtime_id: "runtime-1",
      issue_id: "",
      status: "cancelled",
      priority: 0,
      dispatched_at: null,
      started_at: null,
      completed_at: "2026-06-12T06:40:00Z",
      result: null,
      error: null,
      created_at: "2026-06-12T06:39:00Z",
    };

    it("parses the cancelled chat message payload", async () => {
      const fetchMock = vi.fn().mockResolvedValue(
        new Response(JSON.stringify({
          ...taskResponse,
          cancelled_chat_message: {
            chat_session_id: "session-1",
            message_id: "message-1",
            content: "restore me",
            restore_to_input: true,
          },
        }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
      vi.stubGlobal("fetch", fetchMock);

      const client = new ApiClient("https://api.example.test");
      const result = await client.cancelTaskById("task-1");

      expect(fetchMock.mock.calls[0]).toMatchObject([
        "https://api.example.test/api/tasks/task-1/cancel",
        { method: "POST" },
      ]);
      expect(result.cancelled_chat_message).toEqual({
        chat_session_id: "session-1",
        message_id: "message-1",
        content: "restore me",
        restore_to_input: true,
      });
    });

    it("parses task attribution when the backend enriches it", async () => {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          new Response(JSON.stringify({
            ...taskResponse,
            attribution: {
              source: "direct_human",
              precise: true,
              initiator: { id: "user-1", name: "Ada", avatar_url: "https://x/a.png" },
              originator: { id: "user-1", name: "Ada" },
              evidence: { kind: "comment", ref_id: "comment-1" },
            },
          }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        ),
      );

      const client = new ApiClient("https://api.example.test");
      const result = await client.cancelTaskById("task-1");

      expect(result.attribution).toEqual({
        source: "direct_human",
        precise: true,
        initiator: { id: "user-1", name: "Ada", avatar_url: "https://x/a.png" },
        originator: { id: "user-1", name: "Ada" },
        evidence: { kind: "comment", ref_id: "comment-1" },
      });
    });

    it("leaves attribution absent on servers that predate it", async () => {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          new Response(JSON.stringify(taskResponse), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        ),
      );

      const client = new ApiClient("https://api.example.test");
      const result = await client.cancelTaskById("task-1");

      expect(result.attribution).toBeUndefined();
    });

    // The server only defers the empty-transcript judgment — and so only
    // withholds the synchronous restore — for clients that advertise this
    // capability (#5219). Drop the header and this client is treated as a
    // pre-#5219 build, quietly losing the deferred path it actually implements.
    it("advertises the durable draft-restore capability", async () => {
      const fetchMock = vi.fn().mockResolvedValue(
        new Response(JSON.stringify(taskResponse), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
      vi.stubGlobal("fetch", fetchMock);

      await new ApiClient("https://api.example.test").cancelTaskById("task-1");

      const init = fetchMock.mock.calls[0]?.[1] as { headers: Record<string, string> };
      expect(init.headers["X-Client-Capabilities"]).toBe(CHAT_DRAFT_RESTORE_CAPABILITY);
    });

    it("scopes queued edit cancellation to the expected chat session", async () => {
      const fetchMock = vi.fn().mockResolvedValue(
        new Response(JSON.stringify(taskResponse), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
      vi.stubGlobal("fetch", fetchMock);

      await new ApiClient("https://api.example.test").cancelTaskById("task-1", {
        queuedAction: "edit",
        sessionId: "session-1",
      });

      expect(fetchMock.mock.calls[0]?.[0]).toBe(
        "https://api.example.test/api/tasks/task-1/cancel" +
          "?expected_status=queued&chat_session_id=session-1&queue_action=edit",
      );
    });

    it("treats a null cancelled chat message as absent", async () => {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          new Response(JSON.stringify({
            ...taskResponse,
            cancelled_chat_message: null,
          }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        ),
      );

      const client = new ApiClient("https://api.example.test");
      const result = await client.cancelTaskById("task-1");

      expect(result.id).toBe("task-1");
      expect(result.cancelled_chat_message).toBeUndefined();
    });

    it.each([
      ["a missing task id", { ...taskResponse, id: undefined }],
      [
        "a malformed cancelled chat message",
        {
          ...taskResponse,
          cancelled_chat_message: {
            chat_session_id: "session-1",
            message_id: "message-1",
            content: "restore me",
            restore_to_input: "true",
          },
        },
      ],
      ["a null body", null],
    ])("falls back for %s", async (_label, body) => {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          new Response(JSON.stringify(body), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        ),
      );

      const client = new ApiClient("https://api.example.test");
      const result = await client.cancelTaskById("task-1");

      expect(result.id).toBe("");
      expect(result.cancelled_chat_message).toBeUndefined();
    });
  });

  it("clears a chat queue with one session-scoped request", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await new ApiClient("https://api.example.test").clearQueuedChatTasks("session-1");

    expect(fetchMock).toHaveBeenCalledWith(
      "https://api.example.test/api/chat/sessions/session-1/queued-tasks",
      expect.objectContaining({ method: "DELETE" }),
    );
  });

  describe("chat attachment wiring", () => {
    it("uploadFile includes chat_session_id in the FormData body", async () => {
      const fetchMock = vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ id: "att-1", url: "https://cdn/x" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
      vi.stubGlobal("fetch", fetchMock);

      const client = new ApiClient("https://api.example.test");
      const file = new File(["hi"], "hi.png", { type: "image/png" });
      await client.uploadFile(file, { chatSessionId: "session-123" });

      expect(fetchMock).toHaveBeenCalledTimes(1);
      const [url, init] = fetchMock.mock.calls[0]!;
      expect(url).toBe("https://api.example.test/api/upload-file");
      expect(init?.method).toBe("POST");
      const body = init?.body as FormData;
      expect(body).toBeInstanceOf(FormData);
      expect(body.get("chat_session_id")).toBe("session-123");
      expect(body.get("issue_id")).toBeNull();
      expect(body.get("comment_id")).toBeNull();
    });

    it("threads an AbortSignal into fetch so the coordinator can cancel it (MUL-5181)", async () => {
      const fetchMock = vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ id: "att-1", url: "https://cdn/x" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
      vi.stubGlobal("fetch", fetchMock);

      const client = new ApiClient("https://api.example.test");
      const controller = new AbortController();
      const file = new File(["hi"], "hi.png", { type: "image/png" });
      await client.uploadFile(file, { issueId: "issue-1" }, controller.signal);

      const [, init] = fetchMock.mock.calls[0]!;
      expect(init?.signal).toBe(controller.signal);
    });

    it("rejects with the fetch AbortError when the signal is already aborted", async () => {
      const fetchMock = vi.fn().mockImplementation((_url, init?: RequestInit) => {
        if (init?.signal?.aborted) {
          const err = new Error("The operation was aborted");
          err.name = "AbortError";
          return Promise.reject(err);
        }
        return Promise.resolve(new Response("{}", { status: 200 }));
      });
      vi.stubGlobal("fetch", fetchMock);

      const client = new ApiClient("https://api.example.test");
      const controller = new AbortController();
      controller.abort();
      const file = new File(["hi"], "hi.png", { type: "image/png" });

      await expect(
        client.uploadFile(file, undefined, controller.signal),
      ).rejects.toMatchObject({ name: "AbortError" });
    });

    it("sendChatMessage serialises attachment_ids onto the JSON body when present", async () => {
      const fetchMock = vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ message_id: "m1", task_id: "t1", created_at: "2026-08-01T00:00:00Z" }), {
          status: 201,
          headers: { "Content-Type": "application/json" },
        }),
      );
      vi.stubGlobal("fetch", fetchMock);

      const client = new ApiClient("https://api.example.test");
      await client.sendChatMessage("session-1", "hello", ["att-1", "att-2"]);

      const [, init] = fetchMock.mock.calls[0]!;
      expect(JSON.parse(init?.body as string)).toEqual({
        content: "hello",
        attachment_ids: ["att-1", "att-2"],
      });
    });

    it("sendChatMessage omits attachment_ids when the list is empty or undefined", async () => {
      const fetchMock = vi.fn().mockImplementation(() =>
        Promise.resolve(
          new Response(JSON.stringify({ message_id: "m1", task_id: "t1", created_at: "2026-08-01T00:00:00Z" }), {
            status: 201,
            headers: { "Content-Type": "application/json" },
          }),
        ),
      );
      vi.stubGlobal("fetch", fetchMock);

      const client = new ApiClient("https://api.example.test");
      await client.sendChatMessage("session-1", "hello");
      await client.sendChatMessage("session-1", "again", []);

      expect(JSON.parse(fetchMock.mock.calls[0]![1]?.body as string)).toEqual({ content: "hello" });
      expect(JSON.parse(fetchMock.mock.calls[1]![1]?.body as string)).toEqual({ content: "again" });
    });

    it("sendChatMessage accepts the server's null attachment_ids for text-only sends", async () => {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          new Response(JSON.stringify({
            message_id: "m1",
            task_id: "t1",
            created_at: "2026-08-01T00:00:00Z",
            attachment_ids: null,
          }), {
            status: 201,
            headers: { "Content-Type": "application/json" },
          }),
        ),
      );

      await expect(
        new ApiClient("https://api.example.test").sendChatMessage("session-1", "hello"),
      ).resolves.toMatchObject({ attachment_ids: undefined });
    });

    it("sendChatMessage rejects a malformed response", async () => {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          new Response(JSON.stringify({ message_id: "m1", task_id: 42 }), {
            status: 201,
            headers: { "Content-Type": "application/json" },
          }),
        ),
      );

      await expect(
        new ApiClient("https://api.example.test").sendChatMessage("session-1", "hello"),
      ).rejects.toThrow();
    });
  });
});

// The onboarding flow acts on a workspace the app has not navigated to, so
// these calls pass the slug explicitly. The server reads X-Workspace-Slug
// before ?workspace_id, so the header — not the param — is what has to carry
// the target workspace.
describe("ApiClient explicit workspace targeting", () => {
  function stubOk(body: unknown) {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  function slugHeaderOf(fetchMock: ReturnType<typeof vi.fn>): unknown {
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
    return (init.headers as Record<string, string>)["X-Workspace-Slug"];
  }

  it("sends the given slug on Mika creation", async () => {
    const fetchMock = stubOk({ id: "agent-1" });
    await new ApiClient("https://api.example.test").createMikaAgent(
      { runtime_id: "runtime-1", language: "en" },
      "proxima-centauri",
    );
    expect(slugHeaderOf(fetchMock)).toBe("proxima-centauri");
  });

  it("sends the given slug when listing another workspace's runtimes", async () => {
    const fetchMock = stubOk([]);
    await new ApiClient("https://api.example.test").listRuntimes(
      { workspace_id: "ws-2", owner: "me" },
      "proxima-centauri",
    );
    expect(slugHeaderOf(fetchMock)).toBe("proxima-centauri");
  });

  it("omits the header when no slug is given, leaving the ambient one", async () => {
    const fetchMock = stubOk([]);
    await new ApiClient("https://api.example.test").listRuntimes({
      workspace_id: "ws-2",
    });
    expect(slugHeaderOf(fetchMock)).toBeUndefined();
  });
});

describe("ApiClient model discovery response schema", () => {
  const completed = {
    id: "req-1",
    runtime_id: "rt-1",
    status: "completed",
    supported: true,
    created_at: "2026-07-29T00:00:00Z",
    updated_at: "2026-07-29T00:00:01Z",
    models: [{ id: "claude-sonnet-4-6", label: "Claude Sonnet 4.6" }],
  };

  function stubJSON(body: unknown) {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
  }

  it("parses a live completed discovery", async () => {
    stubJSON(completed);

    const result = await new ApiClient("https://api.example.test")
      .initiateListModels("rt-1");

    expect(result).toMatchObject({
      status: "completed",
      supported: true,
      models: [{ id: "claude-sonnet-4-6" }],
    });
  });

  it("keeps the cache markers on a server-cached snapshot", async () => {
    stubJSON({ ...completed, cached: true, cached_at: "2026-07-29T00:00:00Z" });

    const result = await new ApiClient("https://api.example.test")
      .initiateListModels("rt-1");

    expect(result.cached).toBe(true);
    expect(result.cached_at).toBe("2026-07-29T00:00:00Z");
  });

  // The picker drives a state machine off `status`, so a malformed body must
  // become an explicit failure — not a fabricated empty catalog, and not an
  // endless "discovering models" spinner.
  it("degrades a malformed initiate response to an explicit failure", async () => {
    stubJSON({ status: 7, models: "nope" });

    const result = await new ApiClient("https://api.example.test")
      .initiateListModels("rt-1");

    expect(result.status).toBe("failed");
    expect(result.supported).toBe(true);
    expect(result.error).toBe("invalid model discovery response");
    expect(result.runtime_id).toBe("rt-1");
  });

  it("degrades a malformed poll response to an explicit failure", async () => {
    stubJSON("not-an-object");

    const result = await new ApiClient("https://api.example.test")
      .getListModelsResult("rt-1", "req-9");

    expect(result.status).toBe("failed");
    expect(result.id).toBe("req-9");
    expect(result.runtime_id).toBe("rt-1");
  });

  it("stays usable against a backend that omits supported", async () => {
    const { supported: _omitted, ...withoutSupported } = completed;
    stubJSON(withoutSupported);

    const result = await new ApiClient("https://api.example.test")
      .getListModelsResult("rt-1", "req-1");

    expect(result.supported).toBe(true);
    expect(result.status).toBe("completed");
  });
});

/**
 * Mixed-version contract for subtree unsubscribe (MUL-5483).
 *
 * Web/desktop staging deploys on merge while the backend is deployed by hand,
 * so this client routinely runs against an older server. Subtree unsubscribe
 * must therefore be carried by its own PATH, never by a body field: Go's JSON
 * decoder drops unknown fields, so an old server would unsubscribe only the
 * root and still answer 200 — telling the user the whole tree was muted while
 * every child kept notifying. An unknown path 404s, which surfaces as a
 * rejected mutation the user can act on.
 */
describe("ApiClient unsubscribe endpoints", () => {
  function stubOK() {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  function requestOf(fetchMock: ReturnType<typeof vi.fn>) {
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    return { url, body: JSON.parse(String(init.body ?? "{}")) as Record<string, unknown> };
  }

  it("sends the subtree variant to its own endpoint", async () => {
    const fetchMock = stubOK();

    await new ApiClient("https://api.example.test")
      .unsubscribeFromIssueSubtree("issue-1", "user-1", "member");

    const { url, body } = requestOf(fetchMock);
    expect(url).toBe("https://api.example.test/api/issues/issue-1/unsubscribe/subtree");
    expect(body).toEqual({ user_id: "user-1", user_type: "member" });
  });

  it("never encodes subtree as a body field on the shared endpoint", async () => {
    const fetchMock = stubOK();

    await new ApiClient("https://api.example.test")
      .unsubscribeFromIssue("issue-1", "user-1", "member");

    const { url, body } = requestOf(fetchMock);
    expect(url).toBe("https://api.example.test/api/issues/issue-1/unsubscribe");
    // A `subtree` key here would be silently ignored by an older backend,
    // which is the exact silent-success failure this split exists to prevent.
    expect(body).not.toHaveProperty("subtree");
  });

  it("rejects when the backend does not know the subtree route", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ error: "not found" }), {
          status: 404,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );

    await expect(
      new ApiClient("https://api.example.test")
        .unsubscribeFromIssueSubtree("issue-1", "user-1", "member"),
    ).rejects.toBeInstanceOf(ApiError);
  });
});

describe("ApiClient startMikaOnboarding", () => {
  it("returns the opening a well-formed response reports", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            started: true,
            message_id: "message-1",
            created_at: "2026-01-01T00:00:00Z",
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );

    await expect(
      new ApiClient("https://api.example.test").startMikaOnboarding("session-1", {
        language: "en",
      }),
    ).resolves.toEqual({
      started: true,
      message_id: "message-1",
      created_at: "2026-01-01T00:00:00Z",
    });
  });

  it("falls back to started=false when the response is malformed", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ started: "yes" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );

    // started=false is the safe reading: the flow treats it as "someone else
    // already opened this conversation" and navigates, rather than acting on a
    // body it could not understand.
    await expect(
      new ApiClient("https://api.example.test").startMikaOnboarding("session-1", {
        language: "en",
      }),
    ).resolves.toEqual({ started: false });
  });

  it("tolerates a backend that omits the optional opening fields", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ started: false }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );

    await expect(
      new ApiClient("https://api.example.test").startMikaOnboarding("session-1", {
        language: "en",
      }),
    ).resolves.toEqual({ started: false });
  });
});

describe("ApiClient extractAgentVoice", () => {
  it("parses persona and reply_tone", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            persona: "A reliable teammate.",
            reply_tone: "Short sentences.",
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );

    await expect(
      new ApiClient("https://api.example.test").extractAgentVoice(
        "agent-1",
        "You are a teammate.",
      ),
    ).resolves.toEqual({
      persona: "A reliable teammate.",
      reply_tone: "Short sentences.",
    });
  });

  it("falls back to empty strings when the response is malformed", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ persona: 1 }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );

    await expect(
      new ApiClient("https://api.example.test").extractAgentVoice("agent-1"),
    ).resolves.toEqual({ persona: "", reply_tone: "" });
  });
});

describe("ApiClient agent scene memory", () => {
  it("lists scene memory rows through the validated endpoint", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify([
          {
            id: "mem-1",
            workspace_id: "ws-1",
            agent_id: "agent-1",
            org_id: "org-1",
            scene_key: "cid+abc",
            scene_kind: "dm",
            scene_title: "冬翔",
            memory_text: "GoalMate 是工具",
            memory_revision: 2,
            status: "clean",
            last_error: "",
            last_error_code: "",
            updated_at: "2026-09-01T12:00:00Z",
          },
        ]),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    await expect(
      new ApiClient("https://api.example.test").listAgentSceneMemory("agent-1"),
    ).resolves.toEqual([
      expect.objectContaining({
        id: "mem-1",
        scene_key: "cid+abc",
        memory_text: "GoalMate 是工具",
        memory_revision: 2,
      }),
    ]);
    expect(fetchMock).toHaveBeenCalledWith(
      "https://api.example.test/api/agents/agent-1/scene-memory",
      expect.any(Object),
    );
  });

  it("falls back to an empty list for a malformed response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ memory: "nope" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );

    await expect(
      new ApiClient("https://api.example.test").listAgentSceneMemory("agent-1"),
    ).resolves.toEqual([]);
  });

  it("loads one scene memory row by id", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          id: "mem-9",
          org_id: "org-old",
          scene_key: "cid+abc",
          scene_kind: "group",
          memory_text: "notes",
          memory_revision: 4,
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    await expect(
      new ApiClient("https://api.example.test").getAgentSceneMemory("agent-1", "mem-9"),
    ).resolves.toEqual(
      expect.objectContaining({ id: "mem-9", scene_key: "cid+abc", memory_revision: 4 }),
    );
    expect(fetchMock).toHaveBeenCalledWith(
      "https://api.example.test/api/agents/agent-1/scene-memory/mem-9",
      expect.any(Object),
    );
  });

  it("falls back to an empty row for a malformed memory response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify(["not", "a", "row"]), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
    await expect(
      new ApiClient("https://api.example.test").getAgentSceneMemory("agent-1", "mem-9"),
    ).resolves.toEqual(expect.objectContaining({ id: "", scene_key: "" }));
  });

  it("updates scene memory through PUT and parses the row", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          id: "mem-1",
          scene_key: "cid+abc",
          memory_text: "edited",
          memory_revision: 3,
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    await expect(
      new ApiClient("https://api.example.test").updateAgentSceneMemory("agent-1", "mem-1", {
        memory_text: "edited",
        expected_revision: 2,
      }),
    ).resolves.toEqual(
      expect.objectContaining({ id: "mem-1", memory_text: "edited", memory_revision: 3 }),
    );
    expect(fetchMock).toHaveBeenCalledWith(
      "https://api.example.test/api/agents/agent-1/scene-memory/mem-1",
      expect.objectContaining({ method: "PUT" }),
    );
  });

  it("lists scene relations from assoc recall and falls back when malformed", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          items: [
            {
              issue_id: "iss-1",
              purpose: "向 dxxh 确认空闲",
              status: "waiting",
              on_this_scene: true,
            },
          ],
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    await expect(
      new ApiClient("https://api.example.test").listAgentSceneRelations("agent-1", "cid+abc"),
    ).resolves.toEqual([
      expect.objectContaining({
        issue_id: "iss-1",
        purpose: "向 dxxh 确认空闲",
        on_this_scene: true,
      }),
    ]);
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ nope: true }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
    await expect(
      new ApiClient("https://api.example.test").listAgentSceneRelations("agent-1", "cid+abc"),
    ).resolves.toEqual([]);
  });
});


describe("ApiClient message automation boundaries", () => {
  const reply = (value: unknown) => vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(value), { status: 200 })));
  it("preserves a saved message interval and tolerates additive fields", async () => {
    reply({ id: "tr-1", autopilot_id: "ap-1", kind: "dingtalk_message", enabled: true, merge_interval_minutes: 7, future_field: true });
    await expect(new ApiClient("https://api.example.test").createAutopilotTrigger("ap-1", { kind: "dingtalk_message", merge_interval_minutes: 7 }))
      .resolves.toMatchObject({ id: "tr-1", enabled: true, merge_interval_minutes: 7 });
  });
  it.each([null, { id: "tr-1" }, { id: "tr-1", autopilot_id: "ap-1", kind: "dingtalk_message", merge_interval_minutes: "5" }])("rejects an unreadable trigger write without reporting success: %j", async (value) => {
    reply(value);
    await expect(new ApiClient("https://api.example.test").updateAutopilotTrigger("ap-1", "tr-1", { merge_interval_minutes: 5 })).rejects.toThrow("Reload to verify");
  });
  it("disables editing when detail data is malformed", async () => {
    reply({ autopilot: null, triggers: "unexpected" });
    await expect(new ApiClient("https://api.example.test").getAutopilot("ap-1"))
      .resolves.toMatchObject({ autopilot: { can_write: false, status: "paused" }, triggers: [] });
  });
});
