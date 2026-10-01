import { ReusableDingTalkIdentitiesSchema } from "./schemas";
import { describe, expect, it } from "vitest";
import {
  AppConfigSchema,
  WecomInstallationSchema,
  ListWecomInstallationsResponseSchema,
  RedeemWecomBindingTokenResponseSchema,
  EMPTY_WECOM_INSTALLATION,
  EMPTY_LIST_WECOM_INSTALLATIONS_RESPONSE,
  EMPTY_REDEEM_WECOM_BINDING_TOKEN_RESPONSE,
  AgentTaskListSchema,
  AutopilotRunSchema,
  FALLBACK_AUTOPILOT_RUN,
  CommentTriggerPreviewSchema,
  DashboardAgentRunTimeListSchema,
  DashboardRunTimeDailyListSchema,
  DashboardUsageByAgentListSchema,
  DashboardUsageDailyListSchema,
  BeginDingTalkAccountBindingResponseSchema,
  AgentEnterpriseIdentityStatusResponseSchema,
  AgentIdentityGitHubStatusResponseSchema,
  BeginAgentIdentityGitHubOAuthResponseSchema,
  TestAgentIdentityGitHubConnectionResponseSchema,
  DashboardFailureByAgentListSchema,
  DashboardFailureDailyListSchema,
  ChatDraftRestoresResponseSchema,
  ChatPendingTaskSchema,
  PrioritizeQueuedChatTaskResponseSchema,
  CreateFeedbackResponseSchema,
  DingTalkAccountBindingsResponseSchema,
  DingTalkNativeSubscriptionResponseSchema,
  DuplicateIssueErrorBodySchema,
  EMPTY_BEGIN_DINGTALK_ACCOUNT_BINDING_RESPONSE,
  EMPTY_AGENT_ENTERPRISE_IDENTITY_STATUS_RESPONSE,
  EMPTY_AGENT_IDENTITY_GITHUB_STATUS_RESPONSE,
  EMPTY_DINGTALK_ACCOUNT_BINDINGS_RESPONSE,
  EMPTY_FDE_ONBOARDING_STATE,
  EMPTY_PROVISION_FDE_ONBOARDING_RESPONSE,
  EMPTY_CHAT_DRAFT_RESTORES,
  EMPTY_CHAT_PENDING_TASK,
  EMPTY_PRIORITIZE_QUEUED_CHAT_TASK_RESPONSE,
  EMPTY_CREATE_FEEDBACK_RESPONSE,
  EMPTY_INBOX_ITEMS,
  EMPTY_INBOX_UNREAD_SUMMARY,
  EMPTY_GITHUB_INSTALLATIONS,
  EMPTY_SEARCH_PROJECTS_RESPONSE,
  EMPTY_USER,
  InboxItemListSchema,
  InboxUnreadSummarySchema,
  ListDingTalkInstallationsResponseSchema,
  ListGitHubInstallationsResponseSchema,
  IssueTriggerPreviewSchema,
  FDEOnboardingStateSchema,
  ListIssuesResponseSchema,
  ListPropertiesResponseSchema,
  MALFORMED_RUNTIME_MODEL_LIST_REQUEST,
  RuntimeModelListRequestSchema,
  SearchProjectsResponseSchema,
  RuntimeHourlyActivityListSchema,
  RuntimeUsageByAgentListSchema,
  RuntimeUsageByHourListSchema,
  RuntimeUsageListSchema,
  SendChatMessageResponseSchema,
  SquadListSchema,
  SquadSchema,
  TimelineEntriesSchema,
  UserSchema,
  ProvisionFDEOnboardingResponseSchema,
  WorkspaceAccessTokenListSchema,
  WorkspaceAccessTokenSecretResponseSchema,
  EMPTY_WORKSPACE_ACCESS_TOKEN_SECRET_RESPONSE,
  AgentA2AConfigSchema,
  AgentA2AEndpointSchema,
  AgentA2AClientSchema,
  AgentA2ACredentialSecretResponseSchema,
  LabelUsageResponseSchema,
  EMPTY_AGENT_A2A_CONFIG,
  EMPTY_AGENT_A2A_CREDENTIAL_SECRET_RESPONSE,
  HostedSiteListSchema,
} from "./schemas";
import { IssueViewSchema, IssueViewListSchema } from "./schemas";
import { parseWithFallback } from "./schema";

describe("hosted site schemas", () => {
  it("parses the user-owned site list into camelCase values", () => {
    const parsed = HostedSiteListSchema.parse([
      {
        site_id: "site-1",
        public_site_id: "public-1",
        title: "Weekly Review",
        status: "active",
        latest_revision_id: "revision-1",
        latest_status: "active",
        created_at: "2026-08-29T10:00:00Z",
        updated_at: "2026-08-29T11:00:00Z",
        site_url: "https://sites.example.test/sites/public-1/",
      },
    ]);

    expect(parsed).toEqual([
      {
        siteId: "site-1",
        publicSiteId: "public-1",
        title: "Weekly Review",
        status: "active",
        activeRevisionId: null,
        latestRevisionId: "revision-1",
        latestStatus: "active",
        latestError: "",
        createdAt: "2026-08-29T10:00:00Z",
        updatedAt: "2026-08-29T11:00:00Z",
        siteUrl: "https://sites.example.test/sites/public-1/",
      },
    ]);
  });

  it("rejects a site without a public URL", () => {
    expect(
      HostedSiteListSchema.safeParse([
        {
          site_id: "site-1",
          public_site_id: "public-1",
          status: "active",
          latest_revision_id: "revision-1",
          latest_status: "active",
          created_at: "2026-08-29T10:00:00Z",
          updated_at: "2026-08-29T11:00:00Z",
        },
      ]).success,
    ).toBe(false);
  });
});

describe("workspace access schemas", () => {
  it("defaults legacy tokens to all permissions", () => {
    const parsed = WorkspaceAccessTokenListSchema.parse([{
      id: "t1", workspace_id: "w1", name: "DTA",
      version: 1, token_prefix: "dta_abc", expires_at: null, last_used_at: null,
      revoked_at: null, created_at: "2026-08-02T00:00:00Z", updated_at: "2026-08-02T00:00:00Z",
    }]);
    expect(parsed[0]?.permission).toBe("all");
    expect(parsed[0]).not.toHaveProperty("resource_scope");
    expect(parsed[0]).not.toHaveProperty("capabilities");
  });

  it("does not accept a token-create response without its one-time secret", () => {
    const parsed = parseWithFallback(
      {
        id: "t1", workspace_id: "w1", name: "prod",
        version: 1, token_prefix: "dta_abc",
        expires_at: null, last_used_at: null, created_at: "2026-08-02T00:00:00Z",
        updated_at: "2026-08-02T00:00:00Z", revoked_at: null,
      },
      WorkspaceAccessTokenSecretResponseSchema,
      EMPTY_WORKSPACE_ACCESS_TOKEN_SECRET_RESPONSE,
      { endpoint: "test", includeReceived: false },
    );
    expect(parsed).toEqual(EMPTY_WORKSPACE_ACCESS_TOKEN_SECRET_RESPONSE);
  });
});

describe("DTA permissions", () => {
  it("rejects unknown permission values instead of treating them as full access", () => {
    expect(WorkspaceAccessTokenListSchema.safeParse([{...EMPTY_WORKSPACE_ACCESS_TOKEN_SECRET_RESPONSE, permission: "admin"}]).success).toBe(false);
  });
});

describe("Agent A2A management schemas", () => {
  const skill = {
    id: "coding",
    name: "Coding",
    description: "Build a local project",
    tags: ["code"],
    examples: ["Create a Node.js project"],
  };

  const credential = {
    id: "credential-1",
    key_id: "key-1",
    token_prefix: "mca2a_key-1",
    status: "active",
    expires_at: null,
    last_used_at: null,
    created_at: "2026-08-09T00:00:00Z",
    revoked_at: null,
  };

  it("parses snake_case management data into camelCase domain values", () => {
    const parsed = AgentA2AConfigSchema.parse({
      endpoint: {
        public_agent_id: "public-agent-1",
        enabled: true,
        card_name: "Coding Agent",
        card_description: "Builds projects",
        card_version: "1.0.0",
        card_skills: [skill],
        card_url: "https://example.test/api/a2a/agents/public-agent-1/.well-known/agent-card.json",
        rpc_url: "https://example.test/api/a2a/agents/public-agent-1/v1",
        mcp_url: "https://example.test/api/mcp/agents/public-agent-1",
        workspace_mcp_url: "https://example.test/api/mcp/workspaces/workspace-1",
        protocol_version: "1.0",
      },
      agent_card: {
        name: "Coding Agent",
        description: "Builds projects",
        supportedInterfaces: [{
          url: "https://example.test/api/a2a/agents/public-agent-1/v1",
          protocolBinding: "JSONRPC",
          protocolVersion: "1.0",
        }],
        version: "1.0.0",
        capabilities: { streaming: false },
        securitySchemes: {
          bearerAuth: { type: "http", scheme: "bearer", bearerFormat: "opaque" },
        },
        securityRequirements: [{ schemes: { bearerAuth: [] } }],
        defaultInputModes: ["text/plain"],
        defaultOutputModes: ["text/plain"],
        skills: [skill],
      },
      clients: [{
        id: "client-1",
        name: "Local Coding Agent",
        status: "active",
        scopes: ["send", "read"],
        rate_limit_per_minute: 30,
        max_concurrent_tasks: 2,
        credentials: [credential],
        created_at: "2026-08-09T00:00:00Z",
        updated_at: "2026-08-09T00:00:00Z",
        revoked_at: null,
      }],
    });

    expect(parsed.endpoint).toMatchObject({
      publicAgentId: "public-agent-1",
      cardName: "Coding Agent",
      cardUrl: "https://example.test/api/a2a/agents/public-agent-1/.well-known/agent-card.json",
      rpcUrl: "https://example.test/api/a2a/agents/public-agent-1/v1",
      mcpUrl: "https://example.test/api/mcp/agents/public-agent-1",
      workspaceMcpUrl: "https://example.test/api/mcp/workspaces/workspace-1",
      protocolVersion: "1.0",
    });
    expect(parsed.agentCard?.supportedInterfaces[0]?.protocolBinding)
      .toBe("JSONRPC");
    expect(parsed.agentCard?.securityRequirements).toEqual([
      { schemes: { bearerAuth: [] } },
    ]);
    expect(parsed.clients[0]).toMatchObject({
      rateLimitPerMinute: 30,
      maxConcurrentTasks: 2,
    });
    expect(parsed.clients[0]?.credentials[0]).toMatchObject({
      keyId: "key-1",
      tokenPrefix: "mca2a_key-1",
      updatedAt: "2026-08-09T00:00:00Z",
    });
  });

  it("accepts all managed A2A task scopes and rejects unknown scopes", () => {
    const baseClient = {
      id: "client-1",
      name: "Unsupported caller",
      status: "active",
      rate_limit_per_minute: null,
      max_concurrent_tasks: null,
      credentials: [],
      created_at: "2026-08-09T00:00:00Z",
      updated_at: "2026-08-09T00:00:00Z",
      revoked_at: null,
    };

    expect(AgentA2AClientSchema.safeParse({
      ...baseClient,
      scopes: ["send", "read", "list", "cancel"],
    }).success).toBe(true);
    expect(AgentA2AClientSchema.safeParse({
      ...baseClient,
      scopes: ["send", "admin"],
    }).success).toBe(false);
  });

  it("ignores a malformed workspace MCP URL from a newer server", () => {
    const parsed = AgentA2AEndpointSchema.parse({
      public_agent_id: "agent-1",
      enabled: true,
      card_name: "Agent",
      card_description: "",
      card_version: "1.0",
      card_skills: [],
      card_url: "https://example.test/card",
      rpc_url: "https://example.test/rpc",
      workspace_mcp_url: 42,
    });
    expect(parsed.workspaceMcpUrl).toBe("");
  });

  it("falls back safely when the management response is malformed", () => {
    const parsed = parseWithFallback(
      { endpoint: null, agent_card: null, clients: "not-an-array" },
      AgentA2AConfigSchema,
      EMPTY_AGENT_A2A_CONFIG,
      { endpoint: "test", includeReceived: false },
    );
    expect(parsed).toEqual(EMPTY_AGENT_A2A_CONFIG);
  });

  it("keeps a disabled endpoint when PublicURL is not configured", () => {
    const parsed = AgentA2AConfigSchema.parse({
      endpoint: {
        public_agent_id: "public-agent-1",
        enabled: false,
        card_name: "Coding Agent",
        card_description: "Builds projects",
        card_version: "1.0.0",
        card_skills: [],
        card_url: "",
        rpc_url: "",
        protocol_version: "1.0",
      },
      agent_card: null,
      clients: [],
    });

    expect(parsed.endpoint).toMatchObject({
      enabled: false,
      cardUrl: "",
      rpcUrl: "",
      protocolVersion: "1.0",
    });
  });

  it("requires the one-time token in a credential-create response", () => {
    const parsed = parseWithFallback(
      { credential },
      AgentA2ACredentialSecretResponseSchema,
      EMPTY_AGENT_A2A_CREDENTIAL_SECRET_RESPONSE,
      { endpoint: "test", includeReceived: false },
    );
    expect(parsed).toEqual(EMPTY_AGENT_A2A_CREDENTIAL_SECRET_RESPONSE);
  });
});

describe("GitHub installation schemas", () => {
  it("defaults reusable installations for older server responses", () => {
    const parsed = ListGitHubInstallationsResponseSchema.parse({
      installations: [
        {
          id: "binding-1",
          workspace_id: "workspace-1",
          installation_id: 42,
          account_login: "acme",
          account_type: "Organization",
          account_avatar_url: null,
          created_at: "2026-07-29T00:00:00Z",
        },
      ],
      configured: true,
      can_manage: true,
    });

    expect(parsed.reusable_installations).toEqual([]);
  });

  it("fails closed on a malformed reusable installation response", () => {
    const parsed = parseWithFallback(
      {
        installations: [],
        reusable_installations: [{ id: 42, account_login: "acme" }],
        configured: true,
      },
      ListGitHubInstallationsResponseSchema,
      EMPTY_GITHUB_INSTALLATIONS,
      { endpoint: "test" },
    );

    expect(parsed).toEqual(EMPTY_GITHUB_INSTALLATIONS);
  });
});

describe("DingTalk installation schemas", () => {
  const installation = {
    id: "installation-1",
    workspace_id: "workspace-1",
    agent_id: "agent-1",
    client_id: "ding-app-key",
    installer_user_id: "user-1",
    status: "active",
    installed_at: "2026-08-12T00:00:00Z",
    created_at: "2026-08-12T00:00:00Z",
    updated_at: "2026-08-12T00:00:00Z",
  };

  it("accepts a complete installation response and preserves newer fields", () => {
    const parsed = ListDingTalkInstallationsResponseSchema.parse({
      installations: [{ ...installation, server_extension: true }],
      configured: true,
      server_extension: "kept",
    });

    expect(parsed.installations).toHaveLength(1);
    expect(parsed.installations[0]).toMatchObject(installation);
    expect(parsed).toHaveProperty("server_extension", "kept");
  });

  it("rejects an incomplete response instead of substituting empty data", () => {
    expect(
      ListDingTalkInstallationsResponseSchema.safeParse({
        installations: [{ id: "installation-1" }],
        configured: true,
      }).success,
    ).toBe(false);
    expect(
      ListDingTalkInstallationsResponseSchema.safeParse({ configured: true })
        .success,
    ).toBe(false);
  });
});

describe("DingTalk account binding schemas", () => {
  it("parses the list wire shape into camelCase domain values", () => {
    const parsed = DingTalkAccountBindingsResponseSchema.parse({
      bindings: [
        {
          id: "installation-1",
          workspace_id: "workspace-1",
          agent_id: "agent-1",
          dws_identity: {
            status: "active",
            source: "identity",
            organization_name: "Alibaba Group",
            account_display_name: "Zhang San",
            account_avatar_url: "https://example.test/avatar.png",
            bound_at: "2026-07-14T09:30:00Z",
          },
          message_route: {
            status: "active",
            account_display_name: "Digital Worker Zhang",
            account_avatar_url: "https://example.test/digital-worker.png",
            surface_type: "chat",
            message_scope: "custom",
            enabled_domains: ["channel", "calendar", "approval"],
            calendar_start_enabled: true,
            conversations: [
              {
                cid: "cid-group-1",
                name: "Project Alpha",
                avatar_media_id: "@media-alpha",
                avatar_url: "https://example.test/alpha.png",
              },
              {
                cid: "cid-group-2",
                name: "Project Beta",
              },
            ],
          },
          future_field: true,
        },
      ],
      configured: true,
    });

    expect(parsed).toEqual({
      bindings: [
        {
          id: "installation-1",
          workspaceId: "workspace-1",
          agentId: "agent-1",
          dwsIdentity: {
            status: "active",
            source: "identity",
            organizationName: "Alibaba Group",
            accountDisplayName: "Zhang San",
            accountAvatarUrl: "https://example.test/avatar.png",
            boundAt: "2026-07-14T09:30:00Z",
            nativeSubscription: false,
          },
          messageRoute: {
            status: "active",
            accountDisplayName: "Digital Worker Zhang",
            accountAvatarUrl: "https://example.test/digital-worker.png",
            surfaceType: "chat",
            messageScope: "custom",
            enabledDomains: ["channel", "calendar", "approval"],
            calendarStartEnabled: true,
            conversations: [
              {
                cid: "cid-group-1",
                name: "Project Alpha",
                avatarMediaId: "@media-alpha",
                avatarUrl: "https://example.test/alpha.png",
              },
              {
                cid: "cid-group-2",
                name: "Project Beta",
              },
            ],
            emojiConversations: [],
          },
        },
      ],
      configured: true,
      manualBindingAllowed: false,
    });
  });

  it("parses native subscription and the operator manual-binding flag", () => {
    const parsed = DingTalkAccountBindingsResponseSchema.parse({
      bindings: [
        {
          id: "installation-1",
          workspace_id: "workspace-1",
          agent_id: "agent-1",
          dws_identity: { status: "active", native_subscription: true },
          message_route: { status: "unbound" },
        },
        {
          id: "installation-2",
          workspace_id: "workspace-1",
          agent_id: "agent-2",
          dws_identity: { status: "active" },
          message_route: { status: "active" },
        },
      ],
      configured: true,
      manual_binding_allowed: true,
    });

    expect(parsed.manualBindingAllowed).toBe(true);
    expect(parsed.bindings[0]?.dwsIdentity.nativeSubscription).toBe(true);
    // Absent means off, and older servers omit the operator flag entirely.
    expect(parsed.bindings[1]?.dwsIdentity.nativeSubscription).toBe(false);
    expect(
      DingTalkAccountBindingsResponseSchema.parse({ bindings: [], configured: true })
        .manualBindingAllowed,
    ).toBe(false);
  });

  it("treats malformed native subscription and manual-binding flags as off", () => {
    expect(
      parseWithFallback(
        {
          bindings: [
            {
              id: "installation-1",
              workspace_id: "workspace-1",
              agent_id: "agent-1",
              dws_identity: { status: "active", native_subscription: "true" },
              message_route: { status: "unbound" },
            },
          ],
          configured: true,
          manual_binding_allowed: "yes",
        },
        DingTalkAccountBindingsResponseSchema,
        EMPTY_DINGTALK_ACCOUNT_BINDINGS_RESPONSE,
        { endpoint: "GET /api/workspaces/:id/dingtalk/account-bindings" },
      ),
    ).toMatchObject({
      bindings: [
        {
          id: "installation-1",
          dwsIdentity: { status: "active", nativeSubscription: false },
        },
      ],
      configured: true,
      manualBindingAllowed: false,
    });
  });

  it("parses the native subscription switch response and falls back when it drifts", () => {
    expect(
      DingTalkNativeSubscriptionResponseSchema.parse({ native_subscription: true }),
    ).toEqual({ nativeSubscription: true });
    expect(
      parseWithFallback(
        { native_subscription: "on" },
        DingTalkNativeSubscriptionResponseSchema,
        { nativeSubscription: false },
        {
          endpoint:
            "PUT /api/workspaces/:id/dingtalk/account-bindings/:agentId/native-subscription",
        },
      ),
    ).toEqual({ nativeSubscription: false });
  });

  it("preserves the auto processing surface from binding responses", () => {
    const parsed = DingTalkAccountBindingsResponseSchema.parse({
      bindings: [
        {
          id: "installation-1",
          workspace_id: "workspace-1",
          agent_id: "agent-1",
          dws_identity: { status: "unbound" },
          message_route: {
            status: "active",
            surface_type: "auto",
          },
        },
      ],
      configured: true,
    });

    expect(parsed.bindings[0]?.messageRoute.surfaceType).toBe("auto");
  });

  it("defaults old message-route responses to direct-only with no conversations", () => {
    const parsed = DingTalkAccountBindingsResponseSchema.parse({
      bindings: [
        {
          id: "installation-1",
          workspace_id: "workspace-1",
          agent_id: "agent-1",
          dws_identity: { status: "unbound" },
          message_route: { status: "active" },
        },
      ],
      configured: true,
    });

    expect(parsed.bindings[0]?.messageRoute).toEqual({
      status: "active",
      messageScope: "direct_only",
      enabledDomains: [],
      calendarStartEnabled: false,
      conversations: [],
      emojiConversations: [],
    });
  });

  it("parses the v2 message scope subscription into camelCase buckets", () => {
    const parsed = DingTalkAccountBindingsResponseSchema.parse({
      bindings: [
        {
          id: "installation-1",
          workspace_id: "workspace-1",
          agent_id: "agent-1",
          dws_identity: { status: "unbound" },
          message_route: {
            status: "active",
            message_scope: "custom",
            message_scope_version: 2,
            subscription: {
              direct_cids: [],
              group_cids: ["*"],
            },
          },
        },
      ],
      configured: true,
    });

    expect(parsed.bindings[0]?.messageRoute.messageScopeVersion).toBe(2);
    expect(parsed.bindings[0]?.messageRoute.subscription).toEqual({
      directCids: [],
      groupCids: ["*"],
      emojiReactionCids: [],
    });
  });

  it("parses emoji reaction scope and conversations, defaulting them when omitted", () => {
    const parseWith = (messageRoute: Record<string, unknown>) =>
      DingTalkAccountBindingsResponseSchema.parse({
        bindings: [
          {
            id: "installation-1",
            workspace_id: "workspace-1",
            agent_id: "agent-1",
            dws_identity: { status: "unbound" },
            message_route: { status: "active", ...messageRoute },
          },
        ],
        configured: true,
      }).bindings[0]?.messageRoute;

    const withEmoji = parseWith({
      message_scope: "custom",
      message_scope_version: 2,
      subscription: {
        direct_cids: [],
        group_cids: ["cid-group-1"],
        emoji_reaction_cids: ["cid-group-1"],
      },
      emoji_conversations: [
        { cid: "cid-group-1", name: "Project Alpha", avatar_url: "https://example.test/alpha.png" },
      ],
    });
    expect(withEmoji?.subscription).toEqual({
      directCids: [],
      groupCids: ["cid-group-1"],
      emojiReactionCids: ["cid-group-1"],
    });
    expect(withEmoji?.emojiConversations).toEqual([
      { cid: "cid-group-1", name: "Project Alpha", avatarUrl: "https://example.test/alpha.png" },
    ]);

    const legacy = parseWith({
      message_scope: "custom",
      message_scope_version: 2,
      subscription: { direct_cids: [], group_cids: ["cid-group-1"] },
    });
    expect(legacy?.subscription?.emojiReactionCids).toEqual([]);
    expect(legacy?.emojiConversations).toEqual([]);
  });

  it("keeps an explicit null subscription and drops malformed bucket payloads", () => {
    const parseWith = (subscription: unknown) =>
      DingTalkAccountBindingsResponseSchema.parse({
        bindings: [
          {
            id: "installation-1",
            workspace_id: "workspace-1",
            agent_id: "agent-1",
            dws_identity: { status: "unbound" },
            message_route: {
              status: "active",
              message_scope: "custom",
              message_scope_version: 2,
              subscription,
            },
          },
        ],
        configured: true,
      });

    expect(parseWith(null).bindings[0]?.messageRoute.subscription).toBeNull();
    expect(
      parseWith({ direct_cids: ["*"] }).bindings[0]?.messageRoute.subscription,
    ).toBeUndefined();
  });

  it("parses a safe message-route failure for user-visible diagnostics", () => {
    const parsed = DingTalkAccountBindingsResponseSchema.parse({
      bindings: [
        {
          id: "installation-1",
          workspace_id: "workspace-1",
          agent_id: "agent-1",
          dws_identity: { status: "unbound" },
          message_route: {
            status: "failed",
            error: {
              code: "source_already_bound",
              message: "消息源已绑定给其他 Agent，请解绑后重试",
              retryable: false,
            },
          },
        },
      ],
      configured: true,
    });

    expect(parsed.bindings[0]?.messageRoute.error).toEqual({
      code: "source_already_bound",
      message: "消息源已绑定给其他 Agent，请解绑后重试",
      retryable: false,
    });
  });

  it.each(["revoked", "unbound", "bound_to_other_agent", "inconsistent", "router_unavailable"] as const)(
    "preserves the %s reconciliation state",
    (status) => {
      const parsed = DingTalkAccountBindingsResponseSchema.parse({
        bindings: [
          {
            id: "installation-1",
            workspace_id: "workspace-1",
            agent_id: "agent-1",
            dws_identity: { status: "unbound" },
            message_route: { status },
          },
        ],
        configured: true,
      });

      expect(parsed.bindings[0]?.messageRoute.status).toBe(status);
    },
  );

  it("retains the binding as unavailable when Router returns an unknown reconciliation status", () => {
    expect(
      parseWithFallback(
        {
          bindings: [
            {
              id: "installation-1",
              workspace_id: "workspace-1",
              agent_id: "agent-1",
              dws_identity: { status: "unbound" },
              message_route: { status: "router_timeout" },
            },
          ],
          configured: true,
        },
        DingTalkAccountBindingsResponseSchema,
        EMPTY_DINGTALK_ACCOUNT_BINDINGS_RESPONSE,
        { endpoint: "GET /api/workspaces/:id/dingtalk/account-bindings" },
      ),
    ).toMatchObject({
      bindings: [{ id: "installation-1", messageRoute: { status: "router_unavailable" } }],
      configured: true,
    });
  });

  it("falls back safely when the reconciliation status is structurally malformed", () => {
    expect(
      parseWithFallback(
        {
          bindings: [
            {
              id: "installation-1",
              workspace_id: "workspace-1",
              agent_id: "agent-1",
              dws_identity: { status: "unbound" },
              message_route: { status: 42 },
            },
          ],
          configured: true,
        },
        DingTalkAccountBindingsResponseSchema,
        EMPTY_DINGTALK_ACCOUNT_BINDINGS_RESPONSE,
        { endpoint: "GET /api/workspaces/:id/dingtalk/account-bindings" },
      ),
    ).toEqual({ bindings: [], configured: false, manualBindingAllowed: false });
  });

  it("falls back safely when the binding list is malformed", () => {
    expect(
      parseWithFallback(
        { bindings: "not-an-array", configured: true },
        DingTalkAccountBindingsResponseSchema,
        EMPTY_DINGTALK_ACCOUNT_BINDINGS_RESPONSE,
        { endpoint: "GET /api/workspaces/:id/dingtalk/account-bindings" },
      ),
    ).toEqual({ bindings: [], configured: false, manualBindingAllowed: false });
  });

  it("parses begin and falls back when a credential-bearing response drifts", () => {
    expect(
      BeginDingTalkAccountBindingResponseSchema.parse({
        binding_id: "agent-1",
        qr_code_url: "https://dbase.example/#bindingToken=secret",
        expires_at: "2026-07-14T09:35:00Z",
      }),
    ).toEqual({
      bindingId: "agent-1",
      qrCodeUrl: "https://dbase.example/#bindingToken=secret",
      expiresAt: "2026-07-14T09:35:00Z",
    });
    expect(
      parseWithFallback(
        { qr_code_url: 42 },
        BeginDingTalkAccountBindingResponseSchema,
        EMPTY_BEGIN_DINGTALK_ACCOUNT_BINDING_RESPONSE,
        {
          endpoint: "POST /api/workspaces/:id/dingtalk/account-bindings/begin",
          includeReceived: false,
        },
      ),
    ).toEqual({ bindingId: "", qrCodeUrl: "", expiresAt: "" });
  });
});

describe("FDE onboarding schemas", () => {
  const workspace = {
    id: "workspace-fde",
    name: "My FDE Workspace",
    slug: "my-fde-workspace",
    description: null,
    context: null,
    settings: {},
    repos: [],
    issue_prefix: "FDE",
    avatar_url: null,
    created_at: "2026-07-17T00:00:00Z",
    updated_at: "2026-07-17T00:00:00Z",
  };

  it("defaults the create-only marker to false for an older backend", () => {
    expect(FDEOnboardingStateSchema.parse({ configured: true, workspaces: [] })).toEqual({
      configured: true,
      create_only: false,
      workspaces: [],
    });
  });

  it("falls back safely when the create-only workspace response is malformed", () => {
    expect(parseWithFallback(
      { configured: true, create_only: true, workspaces: [{ id: 42 }] },
      FDEOnboardingStateSchema,
      EMPTY_FDE_ONBOARDING_STATE,
      { endpoint: "GET /api/fde/onboarding" },
    )).toBe(EMPTY_FDE_ONBOARDING_STATE);
  });

  it("parses provisioning and falls back when its workspace is malformed", () => {
    expect(ProvisionFDEOnboardingResponseSchema.parse({
      workspace,
      runtime_id: "runtime-1",
      agent_id: "agent-1",
      agent_created: true,
      install_complete: true,
    }).workspace.name).toBe("My FDE Workspace");

    expect(parseWithFallback(
      { workspace: { id: 42 } },
      ProvisionFDEOnboardingResponseSchema,
      EMPTY_PROVISION_FDE_ONBOARDING_RESPONSE,
      { endpoint: "POST /api/fde/onboarding" },
    )).toBe(EMPTY_PROVISION_FDE_ONBOARDING_RESPONSE);
  });
});

describe("Agent Identity GitHub schemas", () => {
  it("parses status, OAuth start, and test wire shapes", () => {
    expect(
      AgentIdentityGitHubStatusResponseSchema.parse({
        configured: true,
        connection: {
          connection_id: "connection-1",
          account_login: "octocat",
          account_id: "42",
          status: "ACTIVE",
          granted_scopes: "repo",
          access_expires_at: 1783600000000,
          refresh_expires_at: 1799200000000,
          last_refresh_at: 1783590000000,
          last_test_at: 1783595000000,
        },
      }),
    ).toEqual({
      configured: true,
      connection: {
        connectionId: "connection-1",
        accountLogin: "octocat",
        accountId: "42",
        status: "ACTIVE",
        grantedScopes: "repo",
        accessExpiresAt: 1783600000000,
        refreshExpiresAt: 1799200000000,
        lastRefreshAt: 1783590000000,
        lastTestAt: 1783595000000,
      },
    });

    expect(
      BeginAgentIdentityGitHubOAuthResponseSchema.parse({
        state: "state-1",
        authorization_url: "https://github.com/login/oauth/authorize",
      }),
    ).toEqual({
      state: "state-1",
      authorizationUrl: "https://github.com/login/oauth/authorize",
    });

    expect(
      TestAgentIdentityGitHubConnectionResponseSchema.parse({
        ok: true,
        refreshed: true,
        connection_id: "connection-1",
        account_login: "octocat",
        account_id: "42",
        granted_scopes: "repo",
      }),
    ).toMatchObject({
      ok: true,
      refreshed: true,
      connectionId: "connection-1",
      accountLogin: "octocat",
    });
  });

  it("falls back safely when status drifts", () => {
    expect(
      parseWithFallback(
        { configured: true, connection: "not-an-object" },
        AgentIdentityGitHubStatusResponseSchema,
        EMPTY_AGENT_IDENTITY_GITHUB_STATUS_RESPONSE,
        { endpoint: "GET /api/workspaces/:id/agent-identity/github/status" },
      ),
    ).toEqual({ configured: false, connection: null });
  });
});

describe("Agent enterprise identity schemas", () => {
  it("parses manager-visible masked identity fields", () => {
    expect(
      AgentEnterpriseIdentityStatusResponseSchema.parse({
        configured: true,
        can_manage: true,
        identity: {
          employee_id: "*2345",
          display_name: "Zhang San",
          status: "active",
          aip_id: "aip-1",
          agent_spiffe_id:
            "spiffe://agents.example/ns/multica/agents/agent-1",
          buc_status: "active",
          agent_identity_status: "active",
          refresh_expires_at: 1799200000,
        },
      }),
    ).toEqual({
      configured: true,
      canManage: true,
      identity: {
        employeeId: "*2345",
        displayName: "Zhang San",
        status: "active",
        aipId: "aip-1",
        agentSpiffeId:
          "spiffe://agents.example/ns/multica/agents/agent-1",
        bucStatus: "active",
        agentIdentityStatus: "active",
        refreshExpiresAt: 1799200000,
      },
    });
  });

  it("parses member-visible status without identity identifiers", () => {
    expect(
      AgentEnterpriseIdentityStatusResponseSchema.parse({
        configured: true,
        can_manage: false,
        identity: {
          status: "needs_reauth",
          buc_status: "needs_reauth",
          agent_identity_status: "needs_reauth",
        },
      }),
    ).toEqual({
      configured: true,
      canManage: false,
      identity: {
        employeeId: "",
        displayName: "",
        status: "needs_reauth",
        aipId: "",
        agentSpiffeId: "",
        bucStatus: "needs_reauth",
        agentIdentityStatus: "needs_reauth",
        refreshExpiresAt: undefined,
      },
    });
  });

  it("falls back safely when identity status drifts", () => {
    expect(
      parseWithFallback(
        { configured: true, can_manage: true, identity: "not-an-object" },
        AgentEnterpriseIdentityStatusResponseSchema,
        EMPTY_AGENT_ENTERPRISE_IDENTITY_STATUS_RESPONSE,
        {
          endpoint:
            "GET /api/workspaces/:id/agent-identity/enterprise/status",
        },
      ),
    ).toEqual({
      configured: false,
      canManage: false,
      identity: null,
    });
  });
});

const baseIssue = {
  id: "11111111-1111-1111-1111-111111111111",
  workspace_id: "ws-1",
  number: 1,
  identifier: "MUL-1",
  title: "Test",
  description: null,
  status: "todo",
  priority: "medium",
  assignee_type: null,
  assignee_id: null,
  creator_type: "member",
  creator_id: "user-1",
  parent_issue_id: null,
  project_id: null,
  position: 0,
  stage: null,
  start_date: null,
  due_date: null,
  metadata: {},
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

describe("IssueSchema (via ListIssuesResponseSchema)", () => {
  it("accepts a primitive metadata KV map", () => {
    const payload = {
      issues: [
        {
          ...baseIssue,
          metadata: { pipeline_status: "waiting", pr_number: 3, is_blocked: true },
        },
      ],
      total: 1,
    };
    const parsed = ListIssuesResponseSchema.parse(payload);
    expect(parsed.issues[0]?.metadata).toEqual({
      pipeline_status: "waiting",
      pr_number: 3,
      is_blocked: true,
    });
  });

  it("defaults metadata to {} when the server omits it (older backend)", () => {
    const { metadata: _omit, ...issueWithoutMetadata } = baseIssue;
    const payload = { issues: [issueWithoutMetadata], total: 1 };
    const parsed = ListIssuesResponseSchema.parse(payload);
    expect(parsed.issues[0]?.metadata).toEqual({});
  });

  it("rejects metadata with non-primitive values (nested object)", () => {
    const payload = {
      issues: [{ ...baseIssue, metadata: { nested: { x: 1 } } }],
      total: 1,
    };
    expect(ListIssuesResponseSchema.safeParse(payload).success).toBe(false);
  });

  it("accepts a numeric stage", () => {
    const payload = { issues: [{ ...baseIssue, stage: 2 }], total: 1 };
    const parsed = ListIssuesResponseSchema.parse(payload);
    expect(parsed.issues[0]?.stage).toBe(2);
  });

  it("defaults stage to null when the server omits it (older backend)", () => {
    const { stage: _omit, ...issueWithoutStage } = baseIssue;
    const payload = { issues: [issueWithoutStage], total: 1 };
    const parsed = ListIssuesResponseSchema.parse(payload);
    expect(parsed.issues[0]?.stage).toBeNull();
  });

  it("accepts custom property values including multi_select arrays", () => {
    const payload = {
      issues: [
        {
          ...baseIssue,
          properties: { "def-1": "opt-a", "def-2": ["opt-x", "opt-y"], "def-3": 3.5, "def-4": true },
        },
      ],
      total: 1,
    };
    const parsed = ListIssuesResponseSchema.parse(payload);
    expect(parsed.issues[0]?.properties).toEqual({
      "def-1": "opt-a",
      "def-2": ["opt-x", "opt-y"],
      "def-3": 3.5,
      "def-4": true,
    });
  });

  it("defaults properties to {} when the server omits it (older backend)", () => {
    const parsed = ListIssuesResponseSchema.parse({ issues: [baseIssue], total: 1 });
    expect(parsed.issues[0]?.properties).toEqual({});
  });

  it("drops unknown-shaped property values instead of failing the issue parse", () => {
    // Forward compat: a future server type (actor/relation) may ship object
    // values. That one entry must disappear; the issue and its other
    // properties must survive — a full parse failure would blank the whole
    // list through parseWithFallback on installed desktop builds.
    const payload = {
      issues: [
        {
          ...baseIssue,
          properties: { "def-1": { nested: 1 }, "def-2": "opt-a" },
        },
      ],
      total: 1,
    };
    const parsed = ListIssuesResponseSchema.parse(payload);
    expect(parsed.issues[0]?.properties).toEqual({ "def-2": "opt-a" });
  });
});

describe("IssuePropertySchema (via ListPropertiesResponseSchema)", () => {
  const baseProperty = {
    id: "22222222-2222-2222-2222-222222222222",
    workspace_id: "ws-1",
    name: "Severity",
    type: "select",
    description: "",
    icon: "flag",
    config: { options: [{ id: "opt-1", name: "Critical", color: "#ef4444" }] },
    position: 1,
    archived: false,
    archived_at: null,
    usage_count: 2,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };

  it("parses a full definition", () => {
    const parsed = ListPropertiesResponseSchema.parse({ properties: [baseProperty], total: 1 });
    expect(parsed.properties[0]?.config.options?.[0]?.name).toBe("Critical");
    expect(parsed.properties[0]?.icon).toBe("flag");
  });

  it("survives a malformed response by defaulting the list", () => {
    const parsed = ListPropertiesResponseSchema.parse({});
    expect(parsed.properties).toEqual([]);
    expect(parsed.total).toBe(0);
  });

  it("keeps unknown property types as strings (forward compat)", () => {
    const parsed = ListPropertiesResponseSchema.parse({
      properties: [{ ...baseProperty, type: "relation", config: {} }],
      total: 1,
    });
    expect(parsed.properties[0]?.type).toBe("relation");
  });

  it("defaults config when the server sends none", () => {
    const { config: _omit, ...withoutConfig } = baseProperty;
    const parsed = ListPropertiesResponseSchema.parse({ properties: [withoutConfig], total: 1 });
    expect(parsed.properties[0]?.config).toEqual({});
  });

  it("defaults icon for an older server response", () => {
    const { icon: _omit, ...withoutIcon } = baseProperty;
    const parsed = ListPropertiesResponseSchema.parse({ properties: [withoutIcon], total: 1 });
    expect(parsed.properties[0]?.icon).toBe("");
  });
});

// POST /api/issues/preview-trigger feeds this schema through parseWithFallback
// in client.previewIssueTrigger with fallback { triggers: [], total_count: 0 }
// (MUL-3375). The four entry points read it to decide "will this start a run",
// so malformed / missing / null drift must degrade to "nothing will start"
// rather than throw into the picker/modal.
const PREVIEW_FALLBACK = { triggers: [], total_count: 0 };
const PREVIEW_ENDPOINT = { endpoint: "POST /api/issues/preview-trigger" };

describe("IssueTriggerPreviewSchema", () => {
  it("parses a well-formed response", () => {
    const parsed = IssueTriggerPreviewSchema.parse({
      triggers: [
        { issue_id: "i1", agent_id: "a1", source: "assign", handoff_supported: true },
        { issue_id: "i2", agent_id: "a2", source: "status", handoff_supported: false },
      ],
      total_count: 2,
    });
    expect(parsed.total_count).toBe(2);
    expect(parsed.triggers).toHaveLength(2);
    expect(parsed.triggers[0]).toMatchObject({ issue_id: "i1", agent_id: "a1", source: "assign", handoff_supported: true });
  });

  it("defaults missing top-level fields (empty / older backend)", () => {
    const parsed = IssueTriggerPreviewSchema.parse({});
    expect(parsed.triggers).toEqual([]);
    expect(parsed.total_count).toBe(0);
  });

  it("defaults missing optional item fields, keeping required issue_id", () => {
    const parsed = IssueTriggerPreviewSchema.parse({ triggers: [{ issue_id: "i1" }], total_count: 1 });
    expect(parsed.triggers[0]).toEqual({
      issue_id: "i1",
      agent_id: "",
      source: "",
      handoff_supported: false,
    });
  });

  it("parseWithFallback returns the fallback for a malformed shape (triggers not an array)", () => {
    const parsed = parseWithFallback(
      { triggers: "nope", total_count: 1 },
      IssueTriggerPreviewSchema,
      PREVIEW_FALLBACK,
      PREVIEW_ENDPOINT,
    );
    expect(parsed).toEqual(PREVIEW_FALLBACK);
  });

  it("parseWithFallback returns the fallback when an item drops the required issue_id", () => {
    const parsed = parseWithFallback(
      { triggers: [{ agent_id: "a1", source: "assign" }], total_count: 1 },
      IssueTriggerPreviewSchema,
      PREVIEW_FALLBACK,
      PREVIEW_ENDPOINT,
    );
    expect(parsed).toEqual(PREVIEW_FALLBACK);
  });

  it("parseWithFallback returns the fallback for a wrong-typed total_count", () => {
    const parsed = parseWithFallback(
      { triggers: [], total_count: "5" },
      IssueTriggerPreviewSchema,
      PREVIEW_FALLBACK,
      PREVIEW_ENDPOINT,
    );
    expect(parsed).toEqual(PREVIEW_FALLBACK);
  });

  it("parseWithFallback returns the fallback for null / non-object bodies", () => {
    expect(parseWithFallback(null, IssueTriggerPreviewSchema, PREVIEW_FALLBACK, PREVIEW_ENDPOINT)).toEqual(PREVIEW_FALLBACK);
    expect(parseWithFallback("oops", IssueTriggerPreviewSchema, PREVIEW_FALLBACK, PREVIEW_ENDPOINT)).toEqual(PREVIEW_FALLBACK);
  });
});

describe("TimelineEntriesSchema", () => {
  it("preserves source_task_id for agent failure comments", () => {
    const parsed = TimelineEntriesSchema.parse([
      {
        type: "comment",
        id: "comment-1",
        actor_type: "agent",
        actor_id: "agent-1",
        created_at: "2026-01-01T00:00:00Z",
        content: "API Error: 500 Internal server error",
        comment_type: "system",
        source_task_id: "task-1",
      },
    ]);

    expect(parsed[0]?.source_task_id).toBe("task-1");
  });
});

describe("AgentTaskListSchema", () => {
  const task = {
    id: "task-1",
    agent_id: "agent-1",
    runtime_id: "runtime-1",
    issue_id: "issue-1",
    status: "queued",
    priority: 0,
    dispatched_at: null,
    started_at: null,
    completed_at: null,
    result: null,
    error: null,
    created_at: "2026-07-10T00:00:00Z",
    trigger_comment_id: "comment-3",
  };

  it("preserves planned and delivered comment IDs for a task run", () => {
    const parsed = AgentTaskListSchema.parse([
      {
        ...task,
        coalesced_comment_ids: ["comment-1", "comment-2"],
        delivered_comment_ids: ["comment-1", "comment-2", "comment-3"],
      },
    ]);

    expect(parsed[0]?.trigger_comment_id).toBe("comment-3");
    expect(parsed[0]?.coalesced_comment_ids).toEqual([
      "comment-1",
      "comment-2",
    ]);
    expect(parsed[0]?.delivered_comment_ids).toEqual([
      "comment-1",
      "comment-2",
      "comment-3",
    ]);
  });

  it("accepts task payloads from older backends without comment coverage", () => {
    const parsed = AgentTaskListSchema.parse([task]);
    expect(parsed[0]?.coalesced_comment_ids).toBeUndefined();
    expect(parsed[0]?.delivered_comment_ids).toBeUndefined();
  });

  it("degrades malformed optional coverage without dropping task rows", () => {
    const parsed = AgentTaskListSchema.parse([
      {
        ...task,
        coalesced_comment_ids: ["comment-1", 2],
        delivered_comment_ids: "not-an-array",
      },
      {
        ...task,
        id: "task-2",
        delivered_comment_ids: ["comment-2", "comment-3"],
      },
    ]);

    expect(parsed).toHaveLength(2);
    expect(parsed[0]?.coalesced_comment_ids).toBeUndefined();
    expect(parsed[0]?.delivered_comment_ids).toBeUndefined();
    expect(parsed[1]?.delivered_comment_ids).toEqual([
      "comment-2",
      "comment-3",
    ]);
  });
});

describe("ChatDraftRestoresResponseSchema", () => {
  it("parses a well-formed response with attachments", () => {
    const parsed = parseWithFallback(
      {
        restores: [
          {
            id: "msg-1",
            chat_session_id: "s-1",
            task_id: "t-1",
            content: "run the thing",
            attachments: [{ id: "att-1", filename: "notes.txt" }],
            created_at: "2026-07-01T00:00:00Z",
          },
        ],
      },
      ChatDraftRestoresResponseSchema,
      EMPTY_CHAT_DRAFT_RESTORES,
      { endpoint: "test" },
    );
    expect(parsed.restores).toHaveLength(1);
    expect(parsed.restores[0]?.content).toBe("run the thing");
    expect(parsed.restores[0]?.attachments?.[0]?.id).toBe("att-1");
  });

  it("defaults a missing restores array instead of crashing the composer", () => {
    const parsed = parseWithFallback(
      {},
      ChatDraftRestoresResponseSchema,
      EMPTY_CHAT_DRAFT_RESTORES,
      { endpoint: "test" },
    );
    expect(parsed.restores).toEqual([]);
  });

  it("falls back to the empty response on a malformed row", () => {
    // A row without the consume key (id) is unusable — the whole response
    // falls back and the durable rows simply stay pending server-side.
    const parsed = parseWithFallback(
      { restores: [{ chat_session_id: "s-1", content: 42 }] },
      ChatDraftRestoresResponseSchema,
      EMPTY_CHAT_DRAFT_RESTORES,
      { endpoint: "test" },
    );
    expect(parsed).toEqual(EMPTY_CHAT_DRAFT_RESTORES);
  });
});

describe("ChatPendingTaskSchema", () => {
  const ENDPOINT = { endpoint: "GET /api/chat/sessions/:id/pending-task" };

  it("keeps legacy responses compatible when queued_tasks is absent", () => {
    const parsed = parseWithFallback(
      {
        task_id: "task-active",
        status: "running",
        created_at: "2026-07-01T00:00:00Z",
      },
      ChatPendingTaskSchema,
      EMPTY_CHAT_PENDING_TASK,
      ENDPOINT,
    );

    expect(parsed).toMatchObject({
      task_id: "task-active",
      status: "running",
    });
    expect(parsed.queued_tasks).toBeUndefined();
  });

  it("parses queued task summaries", () => {
    const parsed = ChatPendingTaskSchema.parse({
      task_id: "task-active",
      status: "running",
      queued_tasks: [
        {
          task_id: "task-queued",
          status: "queued",
          content: "Follow up after the current task",
          created_at: "2026-07-01T00:01:00Z",
        },
      ],
    });

    expect(parsed.queued_tasks).toEqual([
      expect.objectContaining({
        task_id: "task-queued",
        content: "Follow up after the current task",
      }),
    ]);
  });

  it("keeps a valid head and ignores only malformed queued rows", () => {
    const parsed = parseWithFallback(
      {
        task_id: "task-active",
        queued_tasks: [{ task_id: 42, status: "queued" }],
      },
      ChatPendingTaskSchema,
      EMPTY_CHAT_PENDING_TASK,
      ENDPOINT,
    );

    expect(parsed).toEqual({
      task_id: "task-active",
      queued_tasks: [],
    });
  });
});

describe("SendChatMessageResponseSchema", () => {
  const base = {
    message_id: "message-1",
    task_id: "task-1",
    created_at: "2026-08-05T00:00:00Z",
  };

  it("parses the server-authoritative queue position", () => {
    expect(SendChatMessageResponseSchema.parse({ ...base, queued: false }).queued).toBe(false);
  });

  it("ignores a malformed additive queue position without losing the accepted send", () => {
    expect(SendChatMessageResponseSchema.parse({ ...base, queued: "no" }).queued).toBeUndefined();
  });

  it("accepts a coordinator reply without a sandbox task id", () => {
    const parsed = SendChatMessageResponseSchema.parse({
      message_id: "message-1",
      created_at: "2026-08-05T00:00:00Z",
      assistant_message_id: "asst-1",
      assistant_content: "在的，今天先对哪件事？",
      assistant_message_kind: "coordinator",
      coordinator: {
        action: "reply",
        reason: "这是打招呼",
        source: "web",
        steps: [
          { seq: 1, type: "tool_use", tool: "assoc_recall", input: "{}" },
          { seq: 2, type: "tool_result", tool: "assoc_recall", output: '{"items":[]}' },
          { seq: 3, type: "text", content: "在的，今天先对哪件事？" },
        ],
      },
    });
    expect(parsed.task_id).toBeUndefined();
    expect(parsed.assistant_message_id).toBe("asst-1");
    expect(parsed.assistant_message_kind).toBe("coordinator");
    expect(parsed.coordinator?.action).toBe("reply");
    expect(parsed.coordinator?.steps?.map((step) => step.type)).toEqual([
      "tool_use",
      "tool_result",
      "text",
    ]);
  });
});

describe("PrioritizeQueuedChatTaskResponseSchema", () => {
  const ENDPOINT = {
    endpoint: "POST /api/chat/sessions/:id/queued-tasks/:taskId/prioritize",
  };

  it("parses the prioritized task id", () => {
    expect(
      PrioritizeQueuedChatTaskResponseSchema.parse({
        task_id: "task-queued",
        active_task_id: "task-active",
      }),
    ).toEqual({
      task_id: "task-queued",
      active_task_id: "task-active",
    });
  });

  it("falls back when task_id is malformed", () => {
    expect(
      parseWithFallback(
        { task_id: 42 },
        PrioritizeQueuedChatTaskResponseSchema,
        EMPTY_PRIORITIZE_QUEUED_CHAT_TASK_RESPONSE,
        ENDPOINT,
      ),
    ).toBe(EMPTY_PRIORITIZE_QUEUED_CHAT_TASK_RESPONSE);
  });
});

describe("CreateFeedbackResponseSchema", () => {
  const ENDPOINT = { endpoint: "POST /api/feedback" };

  it("parses a well-formed response and preserves extra fields", () => {
    const parsed = parseWithFallback(
      { id: "feedback-1", created_at: "2026-06-26T00:00:00Z", future_field: true },
      CreateFeedbackResponseSchema,
      EMPTY_CREATE_FEEDBACK_RESPONSE,
      ENDPOINT,
    );
    expect(parsed).toMatchObject({
      id: "feedback-1",
      created_at: "2026-06-26T00:00:00Z",
      future_field: true,
    });
  });

  it("returns the empty fallback for malformed feedback responses", () => {
    expect(
      parseWithFallback(
        { id: 123, created_at: "2026-06-26T00:00:00Z" },
        CreateFeedbackResponseSchema,
        EMPTY_CREATE_FEEDBACK_RESPONSE,
        ENDPOINT,
      ),
    ).toBe(EMPTY_CREATE_FEEDBACK_RESPONSE);
    expect(
      parseWithFallback(null, CreateFeedbackResponseSchema, EMPTY_CREATE_FEEDBACK_RESPONSE, ENDPOINT),
    ).toBe(EMPTY_CREATE_FEEDBACK_RESPONSE);
  });
});

// The duplicate-issue branch in create-issue.tsx feeds ApiError.body
// (typed as `unknown`) through this schema. Any future server drift that
// loses the contract MUST fail the parse so the UI falls back to a normal
// error toast instead of rendering an empty / partial duplicate card.
describe("DuplicateIssueErrorBodySchema", () => {
  const valid = {
    code: "active_duplicate_issue",
    error: "An active issue with this title already exists: MUL-12 – Login bug",
    issue: {
      id: "11111111-1111-1111-1111-111111111111",
      identifier: "MUL-12",
      title: "Login bug",
    },
  };

  it("accepts a well-formed body", () => {
    expect(DuplicateIssueErrorBodySchema.safeParse(valid).success).toBe(true);
  });

  it("accepts unknown extra fields via .loose()", () => {
    const forwardCompat = {
      ...valid,
      hint: "Try a different title",
      issue: { ...valid.issue, workspace_id: "ws-1", status: "todo" },
    };
    expect(DuplicateIssueErrorBodySchema.safeParse(forwardCompat).success).toBe(true);
  });

  it("rejects a renamed code (so renames degrade to the generic toast)", () => {
    const renamed = { ...valid, code: "duplicate_issue" };
    expect(DuplicateIssueErrorBodySchema.safeParse(renamed).success).toBe(false);
  });

  it("rejects a missing issue object", () => {
    const { issue: _omit, ...without } = valid;
    expect(DuplicateIssueErrorBodySchema.safeParse(without).success).toBe(false);
  });

  it("rejects a non-string issue.id", () => {
    const broken = { ...valid, issue: { ...valid.issue, id: 42 } };
    expect(DuplicateIssueErrorBodySchema.safeParse(broken).success).toBe(false);
  });

  it("accepts a missing error field (it is optional)", () => {
    const { error: _omit, ...without } = valid;
    expect(DuplicateIssueErrorBodySchema.safeParse(without).success).toBe(true);
  });
});

// `user.timezone` (Viewing tz) was added in the timezone-architecture RFC.
// A desktop build older than the server — or a server predating the
// `user.timezone` migration — will return a `/api/me` body with no
// `timezone` key. The schema must not fail closed on that: the field
// defaults to `null`, which the frontend resolves to the browser-detected
// tz at render time.
describe("UserSchema timezone drift", () => {
  const base = {
    id: "11111111-1111-1111-1111-111111111111",
    name: "Ada",
    email: "ada@example.com",
  };

  it("defaults timezone to null when the field is absent", () => {
    const parsed = UserSchema.parse(base);
    expect(parsed.timezone).toBe(null);
  });

  it("preserves an explicit IANA timezone", () => {
    const parsed = UserSchema.parse({ ...base, timezone: "Asia/Tokyo" });
    expect(parsed.timezone).toBe("Asia/Tokyo");
  });

  it("accepts an explicit null timezone", () => {
    const parsed = UserSchema.parse({ ...base, timezone: null });
    expect(parsed.timezone).toBe(null);
  });

  // Wrong-type drift: a future server bug sending `timezone` as a number
  // must not throw into the UI. parseWithFallback degrades the whole user
  // object to the explicit fallback (EMPTY_USER) so /api/me callers keep a
  // valid shape instead of white-screening.
  it("falls back to EMPTY_USER when timezone is the wrong type", () => {
    const parsed = parseWithFallback(
      { ...base, timezone: 42 },
      UserSchema,
      EMPTY_USER,
      { endpoint: "GET /api/me" },
    );
    expect(parsed).toBe(EMPTY_USER);
  });
});

describe("SquadListSchema member preview drift", () => {
  const baseSquad = {
    id: "squad-1",
    workspace_id: "ws-1",
    name: "Frontend Squad",
    description: "",
    instructions: "",
    avatar_url: null,
    leader_id: "agent-1",
    creator_id: "user-1",
    created_at: "2026-05-01T00:00:00Z",
    updated_at: "2026-05-01T00:00:00Z",
    archived_at: null,
    archived_by: null,
  };

  it("defaults preview fields when an older backend omits them", () => {
    const parsed = SquadListSchema.parse([baseSquad]);
    expect(parsed[0]?.member_count).toBe(0);
    expect(parsed[0]?.member_preview).toEqual([]);
  });

  it("defaults preview fields on a single squad response", () => {
    const parsed = SquadSchema.parse(baseSquad);
    expect(parsed.member_count).toBe(0);
    expect(parsed.member_preview).toEqual([]);
  });

  it("preserves lightweight member preview rows", () => {
    const parsed = SquadListSchema.parse([
      {
        ...baseSquad,
        member_count: 2,
        member_preview: [
          { member_type: "agent", member_id: "agent-1", role: "leader" },
          { member_type: "member", member_id: "user-2", role: "member" },
        ],
      },
    ]);
    expect(parsed[0]?.member_count).toBe(2);
    expect(parsed[0]?.member_preview).toHaveLength(2);
    expect(parsed[0]?.member_preview?.[0]?.role).toBe("leader");
  });
});

// The workspace dashboard and runtime-detail pages were re-pointed at the
// unified `task_usage_hourly` rollup. Every numeric field drives chart /
// KPI math, and string keys (date / agent_id / model) bucket the series.
// The contract these schemas must hold: a row missing a field degrades
// that field to a sane default rather than dropping the WHOLE array to
// the `[]` fallback — one drifted row must not blank the entire chart.
describe("dashboard + runtime usage schema drift", () => {
  it("coerces a missing numeric field to 0 instead of dropping the array", () => {
    const parsed = DashboardUsageDailyListSchema.parse([
      { date: "2026-05-19", model: "claude-opus-4-7", input_tokens: 100 },
    ]);
    expect(parsed).toHaveLength(1);
    expect(parsed[0]?.output_tokens).toBe(0);
    expect(parsed[0]?.cache_read_tokens).toBe(0);
    expect(parsed[0]?.cache_write_tokens).toBe(0);
  });

  it("coerces a missing date key to \"\" so the rest of the series survives", () => {
    const parsed = DashboardUsageDailyListSchema.parse([
      { model: "claude-opus-4-7", input_tokens: 5 },
    ]);
    expect(parsed).toHaveLength(1);
    expect(parsed[0]?.date).toBe("");
  });

  it("coerces a missing agent_id key to \"\" for the agent-runtime panel", () => {
    const parsed = DashboardAgentRunTimeListSchema.parse([
      { total_seconds: 42, task_count: 3, failed_count: 0 },
    ]);
    expect(parsed).toHaveLength(1);
    expect(parsed[0]?.agent_id).toBe("");
  });

  it("defaults agent_id on daily dashboard rows for older servers", () => {
    const usage = DashboardUsageDailyListSchema.parse([
      { date: "2026-05-19", input_tokens: 5 },
    ]);
    const runtime = DashboardRunTimeDailyListSchema.parse([
      { date: "2026-05-19", total_seconds: 42 },
    ]);

    expect(usage[0]?.agent_id).toBe("");
    expect(runtime[0]?.agent_id).toBe("");
  });

  it("defaults a missing cancelled_count to 0 so a pre-cancelled-count server still renders", () => {
    // cancelled_count was added when the run-time rollups started counting
    // runs the user stopped mid-flight. A backend predating it omits the
    // field; the row must survive with a 0 segment rather than drop the
    // whole series (installed desktop clients hit older backends).
    expect(
      DashboardAgentRunTimeListSchema.parse([
        { agent_id: "a", total_seconds: 42, task_count: 3, failed_count: 0 },
      ])[0]?.cancelled_count,
    ).toBe(0);
    expect(
      DashboardRunTimeDailyListSchema.parse([
        { date: "2026-05-19", total_seconds: 42, task_count: 3, failed_count: 0 },
      ])[0]?.cancelled_count,
    ).toBe(0);
  });

  it("coerces a missing agent_id key to \"\" for the usage-by-agent panel", () => {
    const parsed = DashboardUsageByAgentListSchema.parse([
      { model: "claude-opus-4-7", input_tokens: 7 },
    ]);
    expect(parsed[0]?.agent_id).toBe("");
  });

  it("coerces missing fields on every runtime usage schema", () => {
    expect(RuntimeUsageListSchema.parse([{ date: "2026-05-19" }])[0]?.input_tokens).toBe(0);
    expect(RuntimeHourlyActivityListSchema.parse([{ hour: 9 }])[0]?.count).toBe(0);
    expect(RuntimeUsageByAgentListSchema.parse([{ model: "x" }])[0]?.agent_id).toBe("");
    expect(RuntimeUsageByHourListSchema.parse([{ hour: 9 }])[0]?.model).toBe("");
  });

  it("defaults a missing provider to \"\" so an older server's rows still price by bare model", () => {
    // provider was added for cross-provider model disambiguation; a server
    // predating it omits the field. The schema must fill "" (→ bare-model
    // pricing lookup) rather than drop the row.
    expect(
      DashboardUsageDailyListSchema.parse([{ date: "2026-05-19", model: "claude-opus-4-7" }])[0]
        ?.provider,
    ).toBe("");
    expect(
      DashboardUsageByAgentListSchema.parse([{ model: "claude-opus-4-7" }])[0]?.provider,
    ).toBe("");
    expect(RuntimeUsageByAgentListSchema.parse([{ model: "x" }])[0]?.provider).toBe("");
  });

  it("rejects a non-array body so parseWithFallback can return its fallback", () => {
    expect(DashboardUsageDailyListSchema.safeParse(null).success).toBe(false);
    expect(DashboardFailureDailyListSchema.safeParse(null).success).toBe(false);
    expect(DashboardFailureByAgentListSchema.safeParse({ rows: [] }).success).toBe(
      false,
    );
    expect(RuntimeUsageListSchema.safeParse({ rows: [] }).success).toBe(false);
  });

  it("keeps a failure_reason the client build has never heard of", () => {
    // failure_reason is an open string, not an enum: the backend taxonomy
    // grows, and an installed desktop client must still count a reason its
    // build predates rather than dropping the row (and with it the day's
    // error total).
    const parsed = DashboardFailureDailyListSchema.parse([
      { date: "2026-05-19", failure_reason: "agent_error.brand_new", task_count: 3 },
    ]);
    expect(parsed).toHaveLength(1);
    expect(parsed[0]?.failure_reason).toBe("agent_error.brand_new");
    expect(parsed[0]?.task_count).toBe(3);
  });

  it("coerces a missing failure row field without dropping the array", () => {
    const daily = DashboardFailureDailyListSchema.parse([{ date: "2026-05-19" }]);
    expect(daily).toHaveLength(1);
    // "" is the succeeded bucket, so a reason-less row lands in the
    // denominator instead of inventing a failure that never happened.
    //
    // Defaulting to a failure bucket instead was considered and rejected: the
    // realistic drift here is someone adding `omitempty` to the Go struct
    // tag, which would strip the field from exactly the SUCCESS rows and turn
    // every window into a 100% error rate. Deflating a rate under drift is
    // the milder failure. TestDashboardFailureWireContractKeepsEmptyReason
    // (server/internal/handler/dashboard_test.go) guards the other side by
    // pinning that the server always emits the field.
    expect(daily[0]?.failure_reason).toBe("");
    expect(daily[0]?.task_count).toBe(0);

    const byAgent = DashboardFailureByAgentListSchema.parse([
      { failure_reason: "timeout", task_count: 2 },
    ]);
    expect(byAgent[0]?.agent_id).toBe("");
  });

  it("keeps unknown server-side fields via .loose()", () => {
    const parsed = RuntimeUsageListSchema.parse([
      { date: "2026-05-19", region: "us-east" },
    ]);
    expect((parsed[0] as Record<string, unknown>).region).toBe("us-east");
  });
});

describe("AppConfigSchema cdn_signed drift", () => {
  it("defaults cdn_signed to false when the server omits it (pre-MUL-3254 servers)", () => {
    const parsed = AppConfigSchema.parse({ cdn_domain: "cdn.example.com" });
    expect(parsed.cdn_signed).toBe(false);
  });

  it("coerces a malformed cdn_signed to false instead of failing the whole config", () => {
    const parsed = AppConfigSchema.parse({
      cdn_domain: "cdn.example.com",
      cdn_signed: "yes",
    });
    expect(parsed.cdn_signed).toBe(false);
    expect(parsed.cdn_domain).toBe("cdn.example.com");
  });

  it("keeps cdn_signed=true from a signing-enabled server", () => {
    const parsed = AppConfigSchema.parse({ cdn_signed: true });
    expect(parsed.cdn_signed).toBe(true);
  });

  it("parses frontend feature flag decisions", () => {
    const parsed = AppConfigSchema.parse({
      feature_flags: {
        composio_mcp_apps: true,
        malformed_future_flag: "yes",
      },
    });
    expect(parsed.feature_flags).toEqual({
      composio_mcp_apps: true,
      malformed_future_flag: false,
    });
  });

  it("defaults malformed feature_flags to an empty object", () => {
    const parsed = AppConfigSchema.parse({ feature_flags: ["not", "an", "object"] });
    expect(parsed.feature_flags).toEqual({});
  });

  it("parses server_version and leaves it undefined when the server omits it", () => {
    expect(AppConfigSchema.parse({ server_version: "1.2.3" }).server_version).toBe("1.2.3");
    expect(AppConfigSchema.parse({}).server_version).toBeUndefined();
  });
});

describe("AppConfigSchema dingtalk_client_id drift", () => {
  it("parses dingtalk_client_id when the server provides it", () => {
    const parsed = AppConfigSchema.parse({ dingtalk_client_id: "dingxxxxappkey" });
    expect(parsed.dingtalk_client_id).toBe("dingxxxxappkey");
  });

  it("leaves dingtalk_client_id undefined when a DingTalk-less server omits it", () => {
    const parsed = AppConfigSchema.parse({ allow_signup: true });
    expect(parsed.dingtalk_client_id).toBeUndefined();
  });

  it("coerces a malformed dingtalk_client_id to undefined instead of failing the whole config", () => {
    const parsed = AppConfigSchema.parse({
      allow_signup: true,
      google_client_id: "google-abc",
      dingtalk_client_id: 12345,
    });
    // The bad field is dropped, but the rest of the config still parses so the
    // login page can keep rendering the other providers (MUL API-compat rule).
    expect(parsed.dingtalk_client_id).toBeUndefined();
    expect(parsed.google_client_id).toBe("google-abc");
    expect(parsed.allow_signup).toBe(true);
  });
});

describe("AppConfigSchema lark_client_id drift", () => {
  it("parses lark_client_id when the server provides it", () => {
    const parsed = AppConfigSchema.parse({ lark_client_id: "cli_feishuappid" });
    expect(parsed.lark_client_id).toBe("cli_feishuappid");
  });

  it("leaves lark_client_id undefined when a Feishu-less server omits it", () => {
    const parsed = AppConfigSchema.parse({ cdn_domain: "cdn.example.com" });
    expect(parsed.lark_client_id).toBeUndefined();
  });

  it("drops a malformed lark_client_id instead of failing the parse", () => {
    const parsed = AppConfigSchema.parse({ lark_client_id: 12345 });
    expect(parsed.lark_client_id).toBeUndefined();
  });
});

describe("AppConfigSchema dingtalk_only drift", () => {
  it("parses dingtalk_only when the server locks sign-in to DingTalk", () => {
    const parsed = AppConfigSchema.parse({ dingtalk_only: true });
    expect(parsed.dingtalk_only).toBe(true);
  });

  it("leaves dingtalk_only undefined when an older server omits it", () => {
    const parsed = AppConfigSchema.parse({ allow_signup: true });
    expect(parsed.dingtalk_only).toBeUndefined();
  });

  it("coerces a malformed dingtalk_only to false without failing the whole config", () => {
    const parsed = AppConfigSchema.parse({
      allow_signup: true,
      dingtalk_client_id: "dingxxxxappkey",
      dingtalk_only: "yes",
    });
    // Bad boolean → false; the rest of the config still parses so the login
    // page keeps working (API-compat rule).
    expect(parsed.dingtalk_only).toBe(false);
    expect(parsed.dingtalk_client_id).toBe("dingxxxxappkey");
  });
});

describe("AppConfigSchema login_providers drift", () => {
  it("parses login_providers when the server locks sign-in to an allowlist", () => {
    const parsed = AppConfigSchema.parse({
      login_providers: ["dingtalk", "lark"],
    });
    expect(parsed.login_providers).toEqual(["dingtalk", "lark"]);
  });

  it("leaves login_providers undefined when an older server omits it", () => {
    const parsed = AppConfigSchema.parse({ allow_signup: true });
    expect(parsed.login_providers).toBeUndefined();
  });

  it("drops a malformed login_providers instead of failing the whole config", () => {
    const parsed = AppConfigSchema.parse({
      allow_signup: true,
      login_providers: "dingtalk",
    });
    expect(parsed.login_providers).toBeUndefined();
  });

  it("filters non-string entries out of login_providers", () => {
    const parsed = AppConfigSchema.parse({
      login_providers: ["dingtalk", 42, null, "lark"],
    });
    expect(parsed.login_providers).toEqual(["dingtalk", "lark"]);
  });
});

describe("InboxUnreadSummarySchema", () => {
  const ENDPOINT = { endpoint: "GET /api/inbox/unread-summary" };

  it("parses a well-formed summary and tolerates extra fields", () => {
    const parsed = parseWithFallback(
      [
        { workspace_id: "ws-1", count: 2 },
        { workspace_id: "ws-2", count: 0, future_field: "ignored" },
      ],
      InboxUnreadSummarySchema,
      EMPTY_INBOX_UNREAD_SUMMARY,
      ENDPOINT,
    );
    expect(parsed).toEqual([
      { workspace_id: "ws-1", count: 2 },
      { workspace_id: "ws-2", count: 0, future_field: "ignored" },
    ]);
  });

  it("returns the empty fallback (dot hidden) for a non-array body", () => {
    expect(
      parseWithFallback({ rows: [] }, InboxUnreadSummarySchema, EMPTY_INBOX_UNREAD_SUMMARY, ENDPOINT),
    ).toBe(EMPTY_INBOX_UNREAD_SUMMARY);
    expect(
      parseWithFallback(null, InboxUnreadSummarySchema, EMPTY_INBOX_UNREAD_SUMMARY, ENDPOINT),
    ).toBe(EMPTY_INBOX_UNREAD_SUMMARY);
  });

  it("returns the empty fallback when an entry has a wrong-typed count", () => {
    expect(
      parseWithFallback(
        [{ workspace_id: "ws-1", count: "lots" }],
        InboxUnreadSummarySchema,
        EMPTY_INBOX_UNREAD_SUMMARY,
        ENDPOINT,
      ),
    ).toBe(EMPTY_INBOX_UNREAD_SUMMARY);
  });
});

describe("InboxItemListSchema", () => {
  const ENDPOINT = { endpoint: "GET /api/inbox/archived" };

  const row = (overrides: Record<string, unknown> = {}) => ({
    id: "inbox-1",
    workspace_id: "ws-1",
    recipient_type: "member",
    recipient_id: "member-1",
    type: "new_comment",
    severity: "info",
    issue_id: "issue-1",
    title: "Issue title",
    body: null,
    read: false,
    archived: true,
    created_at: "2026-06-15T08:00:00Z",
    ...overrides,
  });

  it("parses a well-formed archived list and tolerates extra fields", () => {
    const parsed = parseWithFallback(
      [row({ issue_status: "in_progress", details: { comment_id: "c-1" }, future_field: 1 })],
      InboxItemListSchema,
      EMPTY_INBOX_ITEMS,
      ENDPOINT,
    );
    expect(parsed).toHaveLength(1);
    expect(parsed[0]).toMatchObject({ id: "inbox-1", archived: true });
  });

  it("keeps a notification type this client doesn't know yet", () => {
    // Enums stay lenient on purpose: a backend that ships a new inbox type
    // must not blank the whole archived list on older clients.
    const parsed = parseWithFallback(
      [row({ type: "some_future_type", severity: "future_severity" })],
      InboxItemListSchema,
      EMPTY_INBOX_ITEMS,
      ENDPOINT,
    );
    expect(parsed).toHaveLength(1);
  });

  it("accepts rows that omit the nullable optional fields", () => {
    const { body, issue_id, ...withoutOptionals } = row();
    void body;
    void issue_id;
    expect(
      parseWithFallback([withoutOptionals], InboxItemListSchema, EMPTY_INBOX_ITEMS, ENDPOINT),
    ).toHaveLength(1);
  });

  it("returns the empty fallback for a non-array body", () => {
    expect(
      parseWithFallback({ items: [] }, InboxItemListSchema, EMPTY_INBOX_ITEMS, ENDPOINT),
    ).toBe(EMPTY_INBOX_ITEMS);
    expect(
      parseWithFallback(null, InboxItemListSchema, EMPTY_INBOX_ITEMS, ENDPOINT),
    ).toBe(EMPTY_INBOX_ITEMS);
  });

  it("returns the empty fallback when a row is missing a required field", () => {
    const { id, ...withoutId } = row();
    void id;
    expect(
      parseWithFallback([withoutId], InboxItemListSchema, EMPTY_INBOX_ITEMS, ENDPOINT),
    ).toBe(EMPTY_INBOX_ITEMS);
  });

  it("returns the empty fallback when `archived` is wrong-typed", () => {
    expect(
      parseWithFallback(
        [row({ archived: "yes" })],
        InboxItemListSchema,
        EMPTY_INBOX_ITEMS,
        ENDPOINT,
      ),
    ).toBe(EMPTY_INBOX_ITEMS);
  });
});

describe("SearchProjectsResponseSchema date drift", () => {
  const ENDPOINT = { endpoint: "GET /api/projects/search" };

  const baseProject = {
    id: "p-1",
    workspace_id: "ws-1",
    title: "Launch",
    description: null,
    icon: null,
    status: "in_progress",
    priority: "high",
    lead_type: null,
    lead_id: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    issue_count: 0,
    done_count: 0,
    resource_count: 0,
    match_source: "title",
  };

  it("parses start_date / due_date when the backend returns them", () => {
    const parsed = parseWithFallback(
      { projects: [{ ...baseProject, start_date: "2026-03-01", due_date: "2026-03-31" }], total: 1 },
      SearchProjectsResponseSchema,
      EMPTY_SEARCH_PROJECTS_RESPONSE,
      ENDPOINT,
    );
    expect(parsed.projects[0]?.start_date).toBe("2026-03-01");
    expect(parsed.projects[0]?.due_date).toBe("2026-03-31");
  });

  // Frontend deploys before backend: an older backend omits the new keys. The
  // .default(null) must keep the whole batch parseable (→ null), not degrade
  // it to the empty fallback and blank the search results.
  it("defaults missing start_date / due_date to null without dropping results", () => {
    const parsed = parseWithFallback(
      { projects: [baseProject], total: 1 },
      SearchProjectsResponseSchema,
      EMPTY_SEARCH_PROJECTS_RESPONSE,
      ENDPOINT,
    );
    expect(parsed).not.toBe(EMPTY_SEARCH_PROJECTS_RESPONSE);
    expect(parsed.projects).toHaveLength(1);
    expect(parsed.projects[0]?.start_date).toBeNull();
    expect(parsed.projects[0]?.due_date).toBeNull();
  });
});

// The "run now" flow branches on run.status/reason_code to avoid a false-success
// toast (MUL-4525), so the trigger response must survive backend drift.
describe("AutopilotRunSchema", () => {
  const ENDPOINT = { endpoint: "POST /api/autopilots/:id/trigger" };
  const baseRun = {
    id: "run-1",
    autopilot_id: "ap-1",
    trigger_id: null,
    source: "manual",
    status: "issue_created",
    issue_id: "issue-1",
    task_id: null,
    triggered_at: "2026-07-14T00:00:00Z",
    completed_at: null,
    failure_reason: null,
    trigger_payload: null,
    result: null,
    created_at: "2026-07-14T00:00:00Z",
  };

  it("preserves a blocked run's status and reason_code", () => {
    const parsed = parseWithFallback(
      { ...baseRun, status: "skipped", failure_reason: "you are not allowed to trigger this autopilot's assignee agent", reason_code: "invocation_not_allowed" },
      AutopilotRunSchema,
      FALLBACK_AUTOPILOT_RUN,
      ENDPOINT,
    );
    expect(parsed.status).toBe("skipped");
    expect(parsed.reason_code).toBe("invocation_not_allowed");
  });

  it("tolerates an older server omitting reason_code", () => {
    const parsed = parseWithFallback(baseRun, AutopilotRunSchema, FALLBACK_AUTOPILOT_RUN, ENDPOINT);
    expect(parsed.status).toBe("issue_created");
    expect(parsed.reason_code).toBeUndefined();
  });

  it("degrades a malformed response to a non-success fallback (never a false success)", () => {
    const parsed = parseWithFallback("not-an-object", AutopilotRunSchema, FALLBACK_AUTOPILOT_RUN, ENDPOINT);
    expect(parsed).toBe(FALLBACK_AUTOPILOT_RUN);
    expect(parsed.status).toBe("failed");
  });
});

// The comment composer branches on preview.blocked to warn before sending
// (MUL-4525 §2), so the additive field must parse and degrade gracefully.
describe("CommentTriggerPreviewSchema.blocked", () => {
  it("parses blocked mention outcomes alongside agents", () => {
    const parsed = CommentTriggerPreviewSchema.parse({
      agents: [{ id: "a1", source: "mention_agent", reason: "" }],
      blocked: [
        { target_type: "squad", target_id: "s1", status: "blocked", reason_code: "invocation_not_allowed" },
      ],
    });
    expect(parsed.agents).toHaveLength(1);
    expect(parsed.blocked).toEqual([
      { target_type: "squad", target_id: "s1", status: "blocked", reason_code: "invocation_not_allowed" },
    ]);
  });

  it("defaults blocked to [] when an older server omits it", () => {
    const parsed = CommentTriggerPreviewSchema.parse({ agents: [] });
    expect(parsed.blocked).toEqual([]);
  });

  it("degrades a malformed blocked field to [] without dropping agents", () => {
    const parsed = CommentTriggerPreviewSchema.parse({
      agents: [{ id: "a1", source: "mention_agent", reason: "" }],
      blocked: "nope",
    });
    expect(parsed.agents).toHaveLength(1);
    expect(parsed.blocked).toEqual([]);
  });

  it("drops a single malformed blocked entry without discarding the valid ones", () => {
    const parsed = CommentTriggerPreviewSchema.parse({
      agents: [],
      blocked: [
        { target_type: "squad", target_id: "s1", status: "blocked", reason_code: "invocation_not_allowed" },
        { status: "blocked" }, // missing target_id → dropped individually
        { target_type: "agent", target_id: "a1", status: "blocked", reason_code: "runtime_offline" },
      ],
    });
    expect(parsed.blocked.map((b) => b.target_id)).toEqual(["s1", "a1"]);
  });
});

describe("RuntimeModelListRequestSchema", () => {
  const completed = {
    id: "req-1",
    runtime_id: "rt-1",
    status: "completed",
    supported: true,
    created_at: "2026-07-29T00:00:00Z",
    updated_at: "2026-07-29T00:00:01Z",
    models: [
      {
        id: "gpt-5.6-sol",
        label: "GPT-5.6-Sol",
        provider: "openai",
        default: true,
        pricing: {
          input: 5,
          output: 30,
          cache_read: 0.5,
          cache_write: 6.25,
          base_tier_max_input_tokens: 32000,
        },
        thinking: {
          supported_levels: [{ value: "high", label: "High" }],
          default_level: "low",
        },
        service_tiers: [{ id: "fast", name: "Fast" }],
      },
    ],
  };

  it("parses a live completed discovery, keeping the fields the UI branches on", () => {
    const parsed = parseWithFallback(
      completed,
      RuntimeModelListRequestSchema,
      MALFORMED_RUNTIME_MODEL_LIST_REQUEST,
      { endpoint: "test" },
    );
    expect(parsed.status).toBe("completed");
    expect(parsed.supported).toBe(true);
    expect(parsed.models?.[0]?.default).toBe(true);
    expect(parsed.models?.[0]?.pricing).toEqual({
      input: 5,
      output: 30,
      cache_read: 0.5,
      cache_write: 6.25,
      base_tier_max_input_tokens: 32000,
    });
    expect(parsed.models?.[0]?.thinking?.supported_levels).toEqual([
      { value: "high", label: "High" },
    ]);
    expect(parsed.models?.[0]?.service_tiers).toEqual([{ id: "fast", name: "Fast" }]);
    expect(parsed.cached).toBeUndefined();
  });

  it("keeps a selectable model when only its additive pricing is malformed", () => {
    const parsed = parseWithFallback(
      {
        ...completed,
        models: [{ ...completed.models[0], pricing: { input: "invalid" } }],
      },
      RuntimeModelListRequestSchema,
      MALFORMED_RUNTIME_MODEL_LIST_REQUEST,
      { endpoint: "test" },
    );
    expect(parsed.models?.[0]?.id).toBe("gpt-5.6-sol");
    expect(parsed.models?.[0]?.pricing).toBeUndefined();
  });

  it("keeps the additive cache markers when the server serves a snapshot", () => {
    const parsed = parseWithFallback(
      { ...completed, cached: true, cached_at: "2026-07-29T00:00:00Z" },
      RuntimeModelListRequestSchema,
      MALFORMED_RUNTIME_MODEL_LIST_REQUEST,
      { endpoint: "test" },
    );
    expect(parsed.cached).toBe(true);
    expect(parsed.cached_at).toBe("2026-07-29T00:00:00Z");
  });

  // A backend that predates MUL-5444 sends neither marker; an even older one
  // may omit `supported`. Both must stay usable rather than reading as
  // "runtime manages the model itself" off an undefined.
  it("defaults supported to true on an older backend that omits it", () => {
    const { supported: _omitted, ...withoutSupported } = completed;
    const parsed = parseWithFallback(
      withoutSupported,
      RuntimeModelListRequestSchema,
      MALFORMED_RUNTIME_MODEL_LIST_REQUEST,
      { endpoint: "test" },
    );
    expect(parsed.supported).toBe(true);
    expect(parsed.cached).toBeUndefined();
  });

  it("passes an unknown status through instead of failing the whole response", () => {
    const parsed = parseWithFallback(
      { ...completed, status: "superseded" },
      RuntimeModelListRequestSchema,
      MALFORMED_RUNTIME_MODEL_LIST_REQUEST,
      { endpoint: "test" },
    );
    expect(parsed.status).toBe("superseded");
  });

  // Malformed bodies must land on the "failed" fallback: `completed` would
  // fabricate an empty catalog and `pending` would spin the picker until the
  // client-side poll timeout.
  it("falls back to an explicit failure on a malformed body", () => {
    for (const malformed of [
      null,
      "nope",
      42,
      {},
      { status: 7 },
      { ...completed, status: undefined },
      { ...completed, supported: "yes" },
      { ...completed, models: "nope" },
      { ...completed, models: [{ label: "no id" }] },
    ]) {
      const parsed = parseWithFallback(
        malformed,
        RuntimeModelListRequestSchema,
        MALFORMED_RUNTIME_MODEL_LIST_REQUEST,
        { endpoint: "test" },
      );
      expect(parsed.status).toBe("failed");
      expect(parsed.supported).toBe(true);
      expect(parsed.error).toBe("invalid model discovery response");
    }
  });

  it("keeps unknown server fields instead of stripping them", () => {
    const parsed = parseWithFallback(
      { ...completed, future_field: "keep me" },
      RuntimeModelListRequestSchema,
      MALFORMED_RUNTIME_MODEL_LIST_REQUEST,
      { endpoint: "test" },
    );
    expect((parsed as unknown as { future_field?: string }).future_field).toBe(
      "keep me",
    );
  });
});

describe("IssueViewSchema", () => {
  const valid = {
    id: "v1",
    workspace_id: "ws1",
    owner_id: "u1",
    name: "Needs review",
    scope_type: "workspace",
    scope_id: null,
    scope_variant: null,
    visibility: "workspace",
    definition_version: 1,
    query: { statusFilters: ["in_review"] },
    display: { viewMode: "board" },
    revision: 3,
    created_at: "2026-08-06T00:00:00Z",
    updated_at: "2026-08-06T00:00:00Z",
  };

  it("parses a well-formed view and keeps unknown future fields", () => {
    const parsed = IssueViewSchema.parse({ ...valid, future_field: "keep me" });
    expect(parsed.name).toBe("Needs review");
    expect(parsed.query).toEqual({ statusFilters: ["in_review"] });
    expect((parsed as unknown as { future_field?: string }).future_field).toBe("keep me");
  });

  it("defaults missing definition blobs instead of failing", () => {
    const parsed = IssueViewSchema.parse({ id: "v2" });
    expect(parsed.query).toEqual({});
    expect(parsed.display).toEqual({});
    expect(parsed.revision).toBe(1);
  });

  it("degrades a malformed list response to [] via parseWithFallback", () => {
    expect(
      parseWithFallback({ nonsense: true }, IssueViewListSchema, [], {
        endpoint: "GET /api/issue-views",
      }),
    ).toEqual([]);
    expect(
      parseWithFallback(null, IssueViewListSchema, [], {
        endpoint: "GET /api/issue-views",
      }),
    ).toEqual([]);
  });

  it("degrades a malformed detail response to null — NOT an error", () => {
    // The sidebar's pinned view rows hinge on this distinction: a parse
    // fallback (null, no error) hides the row, while only a REAL 404
    // error may ever unpin. A malformed body must never destroy a pin.
    expect(
      parseWithFallback({ nonsense: true }, IssueViewSchema.nullable(), null, {
        endpoint: "GET /api/issue-views/{id}",
      }),
    ).toBeNull();
  });
});

// WeCom smart-bot installation schemas. These gate UI affordances (the Connect
// dialog, the "ask your operator" state, the revoked-vs-active badge), so a
// malformed response must degrade to the safe state rather than a broken one.
describe("WeCom installation schemas", () => {
  it("parses a well-formed installation", () => {
    const parsed = WecomInstallationSchema.parse({
      id: "i1",
      workspace_id: "w1",
      agent_id: "a1",
      bot_id: "aibot_xyz",
      installer_user_id: "u1",
      status: "active",
    });
    expect(parsed.bot_id).toBe("aibot_xyz");
    expect(parsed.status).toBe("active");
  });

  it("defaults a missing status to 'revoked', never 'active'", () => {
    // A broken read must not render a bot as connected when it may not be.
    const parsed = WecomInstallationSchema.parse({ id: "i1" });
    expect(parsed.status).toBe("revoked");
    expect(parsed.bot_id).toBe("");
  });

  it("keeps unknown forward-compat fields (loose) instead of failing the parse", () => {
    const parsed = WecomInstallationSchema.parse({ id: "i1", future_field: "keep" });
    expect((parsed as unknown as { future_field?: string }).future_field).toBe("keep");
  });

  it("defaults 'configured' to false so a malformed list renders the operator state", () => {
    const parsed = ListWecomInstallationsResponseSchema.parse({});
    expect(parsed.configured).toBe(false);
    expect(parsed.installations).toEqual([]);
  });

  it("falls back to the empty list when the response is not an object", () => {
    const parsed = parseWithFallback(
      "not json",
      ListWecomInstallationsResponseSchema,
      EMPTY_LIST_WECOM_INSTALLATIONS_RESPONSE,
      { endpoint: "GET /api/workspaces/:id/wecom/installations" },
    );
    expect(parsed).toEqual(EMPTY_LIST_WECOM_INSTALLATIONS_RESPONSE);
    expect(parsed.configured).toBe(false);
  });

  it("falls back on a malformed installation and redeem response", () => {
    const inst = parseWithFallback(42, WecomInstallationSchema, EMPTY_WECOM_INSTALLATION, {
      endpoint: "POST /api/workspaces/:id/wecom/install/byo",
    });
    expect(inst).toEqual(EMPTY_WECOM_INSTALLATION);

    const redeem = parseWithFallback(
      null,
      RedeemWecomBindingTokenResponseSchema,
      EMPTY_REDEEM_WECOM_BINDING_TOKEN_RESPONSE,
      { endpoint: "POST /api/wecom/binding/redeem" },
    );
    expect(redeem).toEqual(EMPTY_REDEEM_WECOM_BINDING_TOKEN_RESPONSE);
  });
});

describe("label usage task attribution schema", () => {
  it("drops malformed optional attribution without dropping the task", () => {
    const parsed = LabelUsageResponseSchema.parse({
      label: {
        id: "label-1",
        workspace_id: "workspace-1",
        name: "OKR",
        color: "#000000",
        created_at: "2026-08-25T00:00:00Z",
        updated_at: "2026-08-25T00:00:00Z",
      },
      summary: {
        total_tokens: 1,
        total_cost_usd_ticks: 2,
        uncosted_tokens: 0,
        task_count: 1,
        priced_task_count: 1,
        unpriced_task_count: 0,
      },
      tasks: [
        {
          task_id: "task-1",
          agent_id: "agent-1",
          agent_name: "Reporter",
          has_usage: true,
          is_priced: true,
          total_tokens: 1,
          total_cost_usd_ticks: 2,
          uncosted_tokens: 0,
          usage_breakdown: [],
          activity_at: "2026-08-25T01:00:00Z",
          attribution: "malformed",
        },
      ],
      pagination: { page: 1, page_size: 25, total: 1, total_pages: 1 },
    });

    expect(parsed.tasks).toHaveLength(1);
    expect(parsed.tasks[0]?.agent_id).toBe("agent-1");
    expect(parsed.tasks[0]?.attribution).toBeUndefined();
  });
});


describe("ReusableDingTalkIdentitiesSchema", () => {
  it("returns only display metadata and a source Agent reference", () => {
    const parsed = ReusableDingTalkIdentitiesSchema.parse({ identities: [{
      source_agent_id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", source_agent_name: "source",
      account_display_name: "Alice", organization_name: "Acme", dws_uid: "must-not-leak",
    }] });
    expect(parsed).toEqual([{ sourceAgentId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", sourceAgentName: "source", accountDisplayName: "Alice", organizationName: "Acme" }]);
  });
  it.each([{}, { identities: null }, { identities: [{}] }, { identities: [{ source_agent_id: "bad" }] }])("rejects malformed candidates %j", (data) => {
    expect(ReusableDingTalkIdentitiesSchema.safeParse(data).success).toBe(false);
  });
});
