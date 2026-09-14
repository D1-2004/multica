import { z } from "zod";
import type {
  Agent,
  AgentSceneMemory,
  AgentSceneRelation,
  AgentTemplate,
  AgentTemplateSummary,
  AgentBuilderRuntimeSwitch,
  AgentBuilderSession,
  AgentBuilderSessionSummary,
  Attachment,
  AutopilotRun,
  BillingBalance,
  BillingBatchesPage,
  BillingCheckoutSessionStatus,
  BillingPriceTier,
  BillingTopupsPage,
  BillingTransactionsPage,
  CancelTaskResponse,
  AddDingTalkGroupMembersResponse,
  AddDingTalkWorkspaceMembersResponse,
  CreateBillingCheckoutSessionResponse,
  CreateBillingPortalSessionResponse,
  DingTalkUserSearchResponse,
  BeginDingTalkAccountBindingResponse,
  DingTalkAccountBindingsResponse,
  DingTalkMessageScope,
  DingTalkProcessingSurface,
  AgentIdentityGitHubStatusResponse,
  BeginAgentIdentityGitHubOAuthResponse,
  DisconnectAgentIdentityGitHubConnectionResponse,
  TestAgentIdentityGitHubConnectionResponse,
  AgentEnterpriseIdentityStatusResponse,
  BeginAgentEnterpriseIdentityBindingResponse,
  GroupedIssuesResponse,
  GitHubAgentPreview,
  GitHubInstallation,
  ListGitHubAgentRepositoriesResponse,
  ListGitHubInstallationsResponse,
  AgentSource,
  AgentSourceSyncPreview,
  AgentSourceBranches,
  CreateGitHubAgentResponse,
  SyncAgentSourceResponse,
  FDEOnboardingState,
  ProvisionFDEOnboardingResponse,
  ChatMessage,
  ChatDraftRestoresResponse,
  ChatPendingTask,
  PrioritizeQueuedChatTaskResponse,
  SendChatMessageResponse,
  StartMikaOnboardingResponse,
  Comment,
  CreateAgentFromTemplateResponse,
  CronPreviewResponse,
  WecomInstallation,
  ListWecomInstallationsResponse,
  RedeemWecomBindingTokenResponse,
  GitHubConnectResponse,
  GitHubPullRequest,
  InboxItem,
  InboxWorkspaceUnread,
  Label,
  LabelUsageResponse,
  IssueProperty,
  ListPropertiesResponse,
  QuickAction,
  ListQuickActionsResponse,
  IssuePropertiesResponse,
  IssueTableGroupDescriptor,
  IssueTableFacetsResponse,
  IssueTableGroupsResponse,
  IssueTableRowsResponse,
  ListIssuesResponse,
  ListGitHubRepositoriesResponse,
  ListLabelsResponse,
  ListWebhookDeliveriesResponse,
  NotificationPreferenceResponse,
  ResourceLabelsResponse,
  RuntimeModelListRequest,
  SearchIssuesResponse,
  SearchProjectsResponse,
  Squad,
  TimelineEntry,
  User,
  WebhookDelivery,
  WorkspaceAccessToken,
  WorkspaceAccessTokenSecretResponse,
  AgentA2AClient,
  AgentA2AConfig,
  AgentA2ACredential,
  AgentA2ACredentialSecretResponse,
} from "../types";
import type {
  CloudRuntimeNode,
  FCE2BStableChannel,
  FCE2BStableRelease,
} from "../runtimes/cloud-runtime";
import type { CreateFeedbackResponse } from "../feedback/types";
import type { HostedSite } from "../sitehosting/types";
import type {
  ProductFeatureRelease,
  ProductFeatureReleasePage,
  ProductFeatureReleaseSummary,
} from "../product-features/types";
import type {
  AgentDshPlugin,
  DshPlugin,
  DshPluginBinding,
  DshPluginCatalogCategory,
  DshPluginCatalogEntry,
  DshPluginCatalogPage,
  DshPluginCatalogState,
  DshPluginRegistryResult,
  DshPluginSourceKind,
  DshPluginFile,
  DshPluginFileContent,
  DshPluginFileListing,
  DshPluginUpdate,
  ImportDshPluginResult,
} from "../dsh-plugins/types";

export const HostedSiteSchema = z
  .object({
    site_id: z.string(),
    public_site_id: z.string(),
    title: z.string().optional(),
    status: z.string(),
    active_revision_id: z.string().nullable().optional(),
    latest_revision_id: z.string(),
    latest_status: z.string(),
    latest_error: z.string().optional(),
    created_at: z.string(),
    updated_at: z.string(),
    site_url: z.url(),
  })
  .loose()
  .transform(
    (site): HostedSite => ({
      siteId: site.site_id,
      publicSiteId: site.public_site_id,
      title: site.title ?? "",
      status: site.status,
      activeRevisionId: site.active_revision_id ?? null,
      latestRevisionId: site.latest_revision_id,
      latestStatus: site.latest_status,
      latestError: site.latest_error ?? "",
      createdAt: site.created_at,
      updatedAt: site.updated_at,
      siteUrl: site.site_url,
    }),
  );

export const HostedSiteListSchema = z.array(HostedSiteSchema);

const ProductFeatureReleaseSummarySchema = z
  .object({
    id: z.string(),
    title: z.string(),
    version_label: z.string(),
    published_at: z.string(),
  })
  .loose()
  .transform(
    (release): ProductFeatureReleaseSummary => ({
      id: release.id,
      title: release.title,
      versionLabel: release.version_label,
      publishedAt: release.published_at,
    }),
  );

export const ProductFeatureReleaseSchema = z
  .object({
    id: z.string(),
    feature_id: z.string(),
    feature_slug: z.string(),
    release_type: z.enum(["new", "improvement"]).catch("improvement"),
    title: z.string(),
    description: z.string(),
    use_cases: z.string(),
    usage_guide: z.string(),
    version_label: z.string(),
    image_requirement: z
      .enum(["none", "latest_at_publish", "min_version"])
      .catch("none"),
    required_image_version: z.string().nullable(),
    requires_image_upgrade: z.boolean().optional(),
    previous_release_id: z.string().nullable(),
    previous_release: ProductFeatureReleaseSummarySchema.nullable().optional(),
    published_at: z.string(),
    created_at: z.string(),
  })
  .loose()
  .transform(
    (release): ProductFeatureRelease => ({
      id: release.id,
      featureId: release.feature_id,
      featureSlug: release.feature_slug,
      releaseType: release.release_type,
      title: release.title,
      description: release.description,
      useCases: release.use_cases,
      usageGuide: release.usage_guide,
      versionLabel: release.version_label,
      imageRequirement: release.image_requirement,
      requiredImageVersion: release.required_image_version,
      requiresImageUpgrade: release.requires_image_upgrade === true,
      previousReleaseId: release.previous_release_id,
      previousRelease: release.previous_release ?? null,
      publishedAt: release.published_at,
      createdAt: release.created_at,
    }),
  );

export const ProductFeatureReleasePageSchema = z
  .object({
    releases: z.array(ProductFeatureReleaseSchema),
    total: z.number(),
    limit: z.number(),
    offset: z.number(),
  })
  .loose()
  .transform(
    (page): ProductFeatureReleasePage => ({
      releases: page.releases,
      total: page.total,
      limit: page.limit,
      offset: page.offset,
    }),
  );

export const EMPTY_PRODUCT_FEATURE_RELEASE: ProductFeatureRelease = {
  id: "",
  featureId: "",
  featureSlug: "",
  releaseType: "new",
  title: "",
  description: "",
  useCases: "",
  usageGuide: "",
  versionLabel: "",
  imageRequirement: "none",
  requiredImageVersion: null,
  requiresImageUpgrade: false,
  previousReleaseId: null,
  previousRelease: null,
  publishedAt: "",
  createdAt: "",
};

export const EMPTY_PRODUCT_FEATURE_RELEASE_PAGE: ProductFeatureReleasePage = {
  releases: [],
  total: 0,
  limit: 30,
  offset: 0,
};

const DingTalkBindingErrorSchema = z
  .object({
    code: z.string(),
    message: z.string(),
    retryable: z.boolean(),
  })
  .loose();

const dingTalkAccountBindingStatuses = [
  "active",
  "pending",
  "failed",
  "skipped",
  "revoked",
  "unbound",
  "bound_to_other_agent",
  "inconsistent",
  "router_unavailable",
] as const;

const DingTalkAccountBindingStatusSchema = z.enum(
  dingTalkAccountBindingStatuses,
);
const DingTalkAccountBindingStatusInputSchema = z.preprocess(
  (status) =>
    typeof status === "string" &&
    !dingTalkAccountBindingStatuses.some((known) => known === status)
      ? "router_unavailable"
      : status,
  DingTalkAccountBindingStatusSchema,
);

const DingTalkAccountBindingOutcomeSchema = z
  .object({
    status: DingTalkAccountBindingStatusInputSchema,
    source: z.literal("identity").nullable().optional(),
    organization_name: z.string().nullable().optional(),
    account_display_name: z.string().nullable().optional(),
    account_avatar_url: z.string().nullable().optional(),
    bound_at: z.string().nullable().optional(),
    error: DingTalkBindingErrorSchema.nullable().optional().catch(undefined),
  })
  .loose()
  .transform((outcome) => ({
    status: outcome.status,
    source: outcome.source,
    organizationName: outcome.organization_name,
    accountDisplayName: outcome.account_display_name,
    accountAvatarUrl: outcome.account_avatar_url,
    boundAt: outcome.bound_at,
    error: outcome.error,
  }));

const DingTalkConversationSummarySchema = z
  .object({
    cid: z.string(),
    name: z.string(),
    avatar_media_id: z.string().nullable().optional(),
    avatar_url: z.string().nullable().optional(),
  })
  .loose()
  .transform((conversation) => ({
    cid: conversation.cid,
    name: conversation.name,
    ...(conversation.avatar_media_id !== undefined
      ? { avatarMediaId: conversation.avatar_media_id }
      : {}),
    ...(conversation.avatar_url !== undefined
      ? { avatarUrl: conversation.avatar_url }
      : {}),
  }));

function normalizeDingTalkMessageScope(scope?: string): DingTalkMessageScope {
  switch (scope) {
    case "custom":
    case "all":
      return scope;
    case "direct_only":
    default:
      return "direct_only";
  }
}

function normalizeDingTalkProcessingSurface(
  surface?: string | null,
): DingTalkProcessingSurface | undefined {
  return surface === "issue" || surface === "chat" || surface === "auto"
    ? surface
    : undefined;
}

const DingTalkMessageRouteOutcomeSchema = z
  .object({
    status: DingTalkAccountBindingStatusInputSchema,
    organization_name: z.string().nullable().optional(),
    account_display_name: z.string().nullable().optional(),
    account_avatar_url: z.string().nullable().optional(),
    surface_type: z.string().nullable().optional(),
    bound_at: z.string().nullable().optional(),
    message_scope: z.string().optional(),
    message_scope_version: z.number().optional(),
    subscription: z
      .object({
        direct_cids: z.array(z.string()),
        group_cids: z.array(z.string()),
        emoji_reaction_cids: z.array(z.string()).optional(),
      })
      .nullable()
      .optional()
      .catch(undefined),
    enabled_domains: z.array(z.string()).optional().default([]),
    calendar_start_enabled: z.boolean().optional(),
    conversations: z
      .array(DingTalkConversationSummarySchema)
      .optional()
      .default([]),
    emoji_conversations: z.array(DingTalkConversationSummarySchema).optional().default([]),
    error: DingTalkBindingErrorSchema.nullable().optional().catch(undefined),
  })
  .loose()
  .transform((outcome) => ({
    status: outcome.status,
    ...(outcome.organization_name !== undefined
      ? { organizationName: outcome.organization_name }
      : {}),
    ...(outcome.account_display_name !== undefined
      ? { accountDisplayName: outcome.account_display_name }
      : {}),
    ...(outcome.account_avatar_url !== undefined
      ? { accountAvatarUrl: outcome.account_avatar_url }
      : {}),
    ...(normalizeDingTalkProcessingSurface(outcome.surface_type)
      ? {
          surfaceType: normalizeDingTalkProcessingSurface(outcome.surface_type),
        }
      : {}),
    ...(outcome.bound_at !== undefined ? { boundAt: outcome.bound_at } : {}),
    ...(outcome.error !== undefined ? { error: outcome.error } : {}),
    messageScope: normalizeDingTalkMessageScope(outcome.message_scope),
    ...(outcome.message_scope_version !== undefined
      ? { messageScopeVersion: outcome.message_scope_version }
      : {}),
    ...(outcome.subscription !== undefined
      ? {
          subscription:
            outcome.subscription === null
              ? null
              : {
                  directCids: outcome.subscription.direct_cids,
                  groupCids: outcome.subscription.group_cids,
                  emojiReactionCids: outcome.subscription.emoji_reaction_cids ?? [],
                },
        }
      : {}),
    enabledDomains: outcome.enabled_domains,
    calendarStartEnabled: outcome.calendar_start_enabled ?? false,
    conversations: outcome.conversations,
    emojiConversations: outcome.emoji_conversations,
  }));

const DingTalkAccountBindingSchema = z
  .object({
    id: z.string(),
    workspace_id: z.string(),
    agent_id: z.string(),
    dws_identity: DingTalkAccountBindingOutcomeSchema,
    message_route: DingTalkMessageRouteOutcomeSchema,
  })
  .loose()
  .transform((binding) => ({
    id: binding.id,
    workspaceId: binding.workspace_id,
    agentId: binding.agent_id,
    dwsIdentity: binding.dws_identity,
    messageRoute: binding.message_route,
  }));

export const DingTalkAccountBindingsResponseSchema = z
  .object({
    bindings: z.array(DingTalkAccountBindingSchema),
    configured: z.boolean(),
  })
  .loose()
  .transform((response) => ({
    bindings: response.bindings,
    configured: response.configured,
  }));

export const EMPTY_DINGTALK_ACCOUNT_BINDINGS_RESPONSE: DingTalkAccountBindingsResponse =
  {
    bindings: [],
    configured: false,
  };

export const BeginDingTalkAccountBindingResponseSchema = z
  .object({
    binding_id: z.string(),
    qr_code_url: z.string(),
    expires_at: z.string(),
  })
  .loose()
  .transform((response) => ({
    bindingId: response.binding_id,
    qrCodeUrl: response.qr_code_url,
    expiresAt: response.expires_at,
  }));

export const EMPTY_BEGIN_DINGTALK_ACCOUNT_BINDING_RESPONSE: BeginDingTalkAccountBindingResponse =
  {
    bindingId: "",
    qrCodeUrl: "",
    expiresAt: "",
  };

const FDEWorkspaceSchema = z
  .object({
    id: z.string(),
    name: z.string(),
    slug: z.string(),
    description: z.string().nullable().default(null),
    context: z.string().nullable().default(null),
    settings: z.record(z.string(), z.unknown()).default({}),
    repos: z
      .array(
        z
          .object({
            url: z.string(),
            description: z.string().optional(),
          })
          .loose(),
      )
      .default([]),
    issue_prefix: z.string().default(""),
    avatar_url: z.string().nullable().default(null),
    created_at: z.string(),
    updated_at: z.string(),
  })
  .loose();

const FDEInstallSchema = z
  .object({
    session_id: z.string(),
    qr_code_url: z.string(),
    expires_in_seconds: z.number(),
    poll_interval_seconds: z.number(),
  })
  .loose();

export const FDEOnboardingStateSchema = z
  .object({
    configured: z.boolean(),
    create_only: z.boolean().optional().default(false),
    workspaces: z.array(FDEWorkspaceSchema).default([]),
  })
  .loose();

export const EMPTY_FDE_ONBOARDING_STATE: FDEOnboardingState = {
  configured: false,
  create_only: false,
  workspaces: [],
};

export const ProvisionFDEOnboardingResponseSchema = z
  .object({
    workspace: FDEWorkspaceSchema,
    runtime_id: z.string(),
    agent_id: z.string(),
    agent_created: z.boolean(),
    install_complete: z.boolean(),
    install: FDEInstallSchema.optional(),
  })
  .loose();

export const EMPTY_PROVISION_FDE_ONBOARDING_RESPONSE: ProvisionFDEOnboardingResponse =
  {
    workspace: {
      id: "",
      name: "",
      slug: "",
      description: null,
      context: null,
      settings: {},
      repos: [],
      issue_prefix: "",
      avatar_url: null,
      created_at: "",
      updated_at: "",
    },
    runtime_id: "",
    agent_id: "",
    agent_created: false,
    install_complete: false,
  };

const AgentIdentityGitHubConnectionSchema = z
  .object({
    connection_id: z.string(),
    account_login: z.string(),
    account_id: z.string(),
    status: z.string(),
    granted_scopes: z.string().optional().default(""),
    access_expires_at: z.number().nullable().optional(),
    refresh_expires_at: z.number().nullable().optional(),
    last_refresh_at: z.number().nullable().optional(),
    last_test_at: z.number().nullable().optional(),
  })
  .loose()
  .transform((connection) => ({
    connectionId: connection.connection_id,
    accountLogin: connection.account_login,
    accountId: connection.account_id,
    status: connection.status,
    grantedScopes: connection.granted_scopes,
    accessExpiresAt: connection.access_expires_at,
    refreshExpiresAt: connection.refresh_expires_at,
    lastRefreshAt: connection.last_refresh_at,
    lastTestAt: connection.last_test_at,
  }));

export const AgentIdentityGitHubStatusResponseSchema = z
  .object({
    configured: z.boolean(),
    connection: AgentIdentityGitHubConnectionSchema.nullable().optional(),
  })
  .loose()
  .transform((response) => ({
    configured: response.configured,
    connection: response.connection ?? null,
  }));

export const EMPTY_AGENT_IDENTITY_GITHUB_STATUS_RESPONSE: AgentIdentityGitHubStatusResponse =
  {
    configured: false,
    connection: null,
  };

export const BeginAgentIdentityGitHubOAuthResponseSchema = z
  .object({
    state: z.string(),
    authorization_url: z.string(),
  })
  .loose()
  .transform((response) => ({
    state: response.state,
    authorizationUrl: response.authorization_url,
  }));

export const EMPTY_BEGIN_AGENT_IDENTITY_GITHUB_OAUTH_RESPONSE: BeginAgentIdentityGitHubOAuthResponse =
  {
    state: "",
    authorizationUrl: "",
  };

export const TestAgentIdentityGitHubConnectionResponseSchema = z
  .object({
    ok: z.boolean(),
    refreshed: z.boolean().optional().default(false),
    connection_id: z.string().optional(),
    account_login: z.string().optional(),
    account_id: z.string().optional(),
    granted_scopes: z.string().optional(),
  })
  .loose()
  .transform((response) => ({
    ok: response.ok,
    refreshed: response.refreshed,
    connectionId: response.connection_id,
    accountLogin: response.account_login,
    accountId: response.account_id,
    grantedScopes: response.granted_scopes,
  }));

export const EMPTY_TEST_AGENT_IDENTITY_GITHUB_CONNECTION_RESPONSE: TestAgentIdentityGitHubConnectionResponse =
  {
    ok: false,
    refreshed: false,
  };

export const DisconnectAgentIdentityGitHubConnectionResponseSchema = z
  .object({
    ok: z.boolean(),
    connection_id: z.string().optional(),
  })
  .loose()
  .transform((response) => ({
    ok: response.ok,
    connectionId: response.connection_id,
  }));

export const EMPTY_DISCONNECT_AGENT_IDENTITY_GITHUB_CONNECTION_RESPONSE: DisconnectAgentIdentityGitHubConnectionResponse =
  {
    ok: false,
  };

const AgentEnterpriseIdentityConnectionSchema = z
  .object({
    employee_id: z.string().default(""),
    display_name: z.string().default(""),
    status: z.string(),
    aip_id: z.string().default(""),
    agent_spiffe_id: z.string().default(""),
    buc_status: z.string().default(""),
    agent_identity_status: z.string().default(""),
    refresh_expires_at: z.number().optional(),
  })
  .loose()
  .transform((identity) => ({
    employeeId: identity.employee_id,
    displayName: identity.display_name,
    status: identity.status,
    aipId: identity.aip_id,
    agentSpiffeId: identity.agent_spiffe_id,
    bucStatus: identity.buc_status,
    agentIdentityStatus: identity.agent_identity_status,
    refreshExpiresAt: identity.refresh_expires_at,
  }));

export const AgentEnterpriseIdentityStatusResponseSchema = z
  .object({
    configured: z.boolean(),
    can_manage: z.boolean().default(false),
    identity: AgentEnterpriseIdentityConnectionSchema.nullable().optional(),
  })
  .loose()
  .transform((response) => ({
    configured: response.configured,
    canManage: response.can_manage,
    identity: response.identity ?? null,
  }));

export const EMPTY_AGENT_ENTERPRISE_IDENTITY_STATUS_RESPONSE: AgentEnterpriseIdentityStatusResponse =
  {
    configured: false,
    canManage: false,
    identity: null,
  };

export const BeginAgentEnterpriseIdentityBindingResponseSchema = z
  .object({
    authorization_url: z.string(),
    expires_at: z.number(),
  })
  .loose()
  .transform((response) => ({
    authorizationUrl: response.authorization_url,
    expiresAt: response.expires_at,
  }));

export const EMPTY_BEGIN_AGENT_ENTERPRISE_IDENTITY_BINDING_RESPONSE: BeginAgentEnterpriseIdentityBindingResponse =
  {
    authorizationUrl: "",
    expiresAt: 0,
  };

export const EMPTY_LIST_GITHUB_INSTALLATIONS_RESPONSE: ListGitHubInstallationsResponse =
  {
    installations: [],
    configured: false,
    repository_browse_configured: false,
    can_manage: false,
  };

export const GitHubConnectResponseSchema = z
  .object({
    url: z.string().optional(),
    configured: z.boolean().optional().default(false),
  })
  .loose();

export const EMPTY_GITHUB_CONNECT_RESPONSE: GitHubConnectResponse = {
  configured: false,
};

export const GitHubRepositorySchema = z
  .object({
    id: z.number(),
    full_name: z.string(),
    html_url: z.string(),
    clone_url: z.string(),
    description: z.string().nullable(),
    private: z.boolean(),
    archived: z.boolean(),
    default_branch: z.string(),
  })
  .loose();

export const ListGitHubRepositoriesResponseSchema = z
  .object({
    repositories: z.array(GitHubRepositorySchema).default([]),
    total_count: z.number().optional().default(0),
    next_page: z.number().nullable().optional().default(null),
  })
  .loose();

export const EMPTY_LIST_GITHUB_REPOSITORIES_RESPONSE: ListGitHubRepositoriesResponse =
  {
    repositories: [],
    total_count: 0,
    next_page: null,
  };

export const GitHubPullRequestSchema = z
  .object({
    id: z.string(),
    provider: z.string().optional().default("github"),
    workspace_id: z.string(),
    repo_owner: z.string(),
    repo_name: z.string(),
    number: z.number(),
    title: z.string(),
    state: z.string(),
    html_url: z.string(),
    branch: z.string().nullable(),
    author_login: z.string().nullable(),
    author_avatar_url: z.string().nullable(),
    merged_at: z.string().nullable(),
    closed_at: z.string().nullable(),
    pr_created_at: z.string(),
    pr_updated_at: z.string(),
    mergeable: z.string().nullable().optional(),
    merge_state_status: z.string().nullable().optional(),
    snapshot_available: z.boolean().optional(),
    checks_rollup: z.string().nullable().optional(),
    checks_conclusion: z.string().nullable().optional(),
    checks_total: z.number().optional().default(0),
    checks_passed: z.number().optional().default(0),
    checks_failed: z.number().optional().default(0),
    checks_running: z.number().optional().default(0),
    checks_pending: z.number().optional().default(0),
    failed_check_names: z.array(z.string()).optional().default([]),
    snapshot_stale: z.boolean().optional().default(false),
    snapshot_fetched_at: z.string().nullable().optional(),
    mergeable_state: z.string().nullable().optional(),
    additions: z.number().optional().default(0),
    deletions: z.number().optional().default(0),
    changed_files: z.number().optional().default(0),
  })
  .loose();

export const IssuePullRequestsResponseSchema = z
  .object({
    pull_requests: z.array(GitHubPullRequestSchema).default([]),
  })
  .loose();

export const EMPTY_ISSUE_PULL_REQUESTS_RESPONSE: {
  pull_requests: GitHubPullRequest[];
} = {
  pull_requests: [],
};

// Label responses are consumed by settings tables and resource pickers. Keep
// the resource type lenient so newer server scopes do not break older clients,
// while defaulting fields that predate scoped label catalogs.
// Human attribution is shared by Agent task lists and label-usage task rows.
// Keep every nested field defensive so an older backend or a departed member
// degrades only this additive object rather than the containing response.
const AttributionUserSchema = z
  .object({
    id: z.string().default(""),
    name: z.string().optional(),
    email: z.string().optional(),
    avatar_url: z.string().optional(),
  })
  .loose();

const TaskEvidenceSchema = z
  .object({
    kind: z.string().default(""),
    ref_id: z.string().default(""),
  })
  .loose();

const TaskAttributionSchema = z
  .object({
    source: z.string().default("unattributed"),
    precise: z.boolean().default(false),
    initiator: AttributionUserSchema.optional(),
    originator: AttributionUserSchema.optional(),
    evidence: TaskEvidenceSchema.optional(),
    rule_version_id: z.string().optional(),
    delegated_from_task_id: z.string().optional(),
    retry_of_task_id: z.string().optional(),
    rerun_of_task_id: z.string().optional(),
  })
  .loose();

export const LabelUsageSummarySchema = z
  .object({
    total_tokens: z.number().nonnegative(),
    total_cost_usd_ticks: z.number().nonnegative(),
    uncosted_tokens: z.number().nonnegative(),
    task_count: z.number().int().nonnegative(),
    priced_task_count: z.number().int().nonnegative(),
    unpriced_task_count: z.number().int().nonnegative(),
  })
  .loose();

export const LabelSchema = z
  .object({
    id: z.string(),
    workspace_id: z.string(),
    resource_type: z.string().optional().default("issue"),
    name: z.string(),
    description: z.string().optional().default(""),
    color: z.string(),
    usage_count: z.number().optional().default(0),
    usage_summary: LabelUsageSummarySchema.optional().catch(undefined),
    created_at: z.string(),
    updated_at: z.string(),
  })
  .loose();

export const EMPTY_LABEL: Label = {
  id: "",
  workspace_id: "",
  resource_type: "issue",
  name: "",
  description: "",
  color: "#6b7280",
  usage_count: 0,
  created_at: "",
  updated_at: "",
};

const LabelUsageDailySchema = z
  .object({
    date: z.string(),
    total_tokens: z.number().nonnegative(),
    total_cost_usd_ticks: z.number().nonnegative(),
    uncosted_tokens: z.number().nonnegative(),
    task_count: z.number().int().nonnegative(),
    priced_task_count: z.number().int().nonnegative(),
    unpriced_task_count: z.number().int().nonnegative(),
  })
  .loose();

const LabelUsageBreakdownSchema = z
  .object({
    provider: z.string(),
    model: z.string(),
    total_tokens: z.number().nonnegative(),
    total_cost_usd_ticks: z.number().nonnegative(),
    uncosted_tokens: z.number().nonnegative(),
    task_count: z.number().int().nonnegative(),
    unpriced_task_count: z.number().int().nonnegative(),
  })
  .loose();

const LabelUsageTaskBreakdownSchema = z
  .object({
    provider: z.string(),
    model: z.string(),
    total_tokens: z.number().nonnegative(),
    total_cost_usd_ticks: z.number().nonnegative(),
    uncosted_tokens: z.number().nonnegative(),
    is_priced: z.boolean(),
  })
  .loose();

const LabelUsageTaskSchema = z
  .object({
    task_id: z.string(),
    agent_id: z.string().default(""),
    agent_name: z.string().default(""),
    issue_id: z.string().default(""),
    issue_identifier: z.string().default(""),
    issue_title: z.string().default(""),
    status: z.string().default(""),
    provider: z.string().default(""),
    model: z.string().default(""),
    has_usage: z.boolean(),
    total_tokens: z.number().nonnegative(),
    total_cost_usd_ticks: z.number().nonnegative(),
    uncosted_tokens: z.number().nonnegative(),
    is_priced: z.boolean(),
    usage_breakdown: z.array(LabelUsageTaskBreakdownSchema).default([]),
    created_at: z.string().default(""),
    completed_at: z.string().nullable().optional(),
    activity_at: z.string(),
    attribution: TaskAttributionSchema.optional().catch(undefined),
  })
  .loose();

const LabelUsagePaginationSchema = z
  .object({
    page: z.number().int().positive().default(1),
    page_size: z.number().int().positive().default(25),
    total: z.number().int().nonnegative().default(0),
    total_pages: z.number().int().nonnegative().default(0),
  })
  .loose();

export const LabelUsageResponseSchema = z
  .object({
    label: LabelSchema,
    summary: LabelUsageSummarySchema,
    daily: z.array(LabelUsageDailySchema).default([]),
    breakdown: z.array(LabelUsageBreakdownSchema).default([]),
    tasks: z.array(LabelUsageTaskSchema).default([]),
    pagination: LabelUsagePaginationSchema,
  })
  .loose();

export const EMPTY_LABEL_USAGE_RESPONSE: LabelUsageResponse = {
  label: EMPTY_LABEL,
  summary: {
    total_tokens: 0,
    total_cost_usd_ticks: 0,
    uncosted_tokens: 0,
    task_count: 0,
    priced_task_count: 0,
    unpriced_task_count: 0,
  },
  daily: [],
  breakdown: [],
  tasks: [],
  pagination: { page: 1, page_size: 25, total: 0, total_pages: 0 },
};

export const ListLabelsResponseSchema = z
  .object({
    labels: z.array(LabelSchema).default([]),
    total: z.number().default(0),
  })
  .loose();

export const EMPTY_LIST_LABELS_RESPONSE: ListLabelsResponse = {
  labels: [],
  total: 0,
};

export const ResourceLabelsResponseSchema = z
  .object({
    labels: z.array(LabelSchema).default([]),
  })
  .loose();

export const EMPTY_RESOURCE_LABELS_RESPONSE: ResourceLabelsResponse = {
  labels: [],
};

// Saved issue views (MUL-4796). `query`/`display` are opaque definition
// blobs interpreted client-side per `definition_version` — keep them as
// loose records so newer servers can add fields freely. `scope_type` /
// `visibility` stay lenient strings; downstream code uses explicit `===`
// comparisons and default branches per the API-compat rules.
export const IssueViewSchema = z
  .object({
    id: z.string(),
    workspace_id: z.string().default(""),
    owner_id: z.string().default(""),
    name: z.string().default(""),
    scope_type: z.string().default("workspace"),
    scope_id: z.string().nullish(),
    scope_variant: z.string().nullish(),
    visibility: z.string().default("private"),
    definition_version: z.number().default(1),
    query: z.record(z.string(), z.unknown()).default({}),
    display: z.record(z.string(), z.unknown()).default({}),
    revision: z.number().default(1),
    created_at: z.string().default(""),
    updated_at: z.string().default(""),
  })
  .loose();

export type IssueView = z.infer<typeof IssueViewSchema>;

export const IssueViewListSchema = z.array(IssueViewSchema);

export const IssueViewPreferenceSchema = z
  .object({
    scope_type: z.string().default("workspace"),
    scope_id: z.string().nullish(),
    prefs: z
      .object({
        hidden: z.array(z.string()).default([]),
        order: z.array(z.string()).default([]),
      })
      .loose()
      .default({ hidden: [], order: [] }),
    updated_at: z.string().default(""),
  })
  .loose();

export type IssueViewPreference = z.infer<typeof IssueViewPreferenceSchema>;

export const EMPTY_ISSUE_VIEW_PREFERENCE: IssueViewPreference = {
  scope_type: "workspace",
  scope_id: null,
  prefs: { hidden: [], order: [] },
  updated_at: "",
};

export interface CreateIssueViewRequest {
  name: string;
  scope_type: "workspace" | "my" | "project";
  scope_id?: string | null;
  scope_variant?:
    "assigned" | "created" | "involved" | "any" | "members" | "agents" | null;
  visibility: "private" | "workspace";
  definition_version: number;
  query: Record<string, unknown>;
  display: Record<string, unknown>;
}

// Custom property definitions. `type` stays a lenient string so newer server
// types don't break installed clients; UI narrows with isKnownPropertyType.
export const IssuePropertySchema = z
  .object({
    id: z.string(),
    workspace_id: z.string(),
    name: z.string(),
    type: z.string(),
    description: z.string().optional().default(""),
    icon: z.string().optional().default(""),
    config: z
      .object({
        options: z
          .array(
            z
              .object({
                id: z.string(),
                name: z.string(),
                color: z.string().optional().default("#6b7280"),
              })
              .loose(),
          )
          .optional(),
      })
      .loose()
      .default({}),
    position: z.number().optional().default(0),
    archived: z.boolean().optional().default(false),
    archived_at: z.string().nullable().optional(),
    usage_count: z.number().optional().default(0),
    created_at: z.string(),
    updated_at: z.string(),
  })
  .loose();

export const EMPTY_ISSUE_PROPERTY: IssueProperty = {
  id: "",
  workspace_id: "",
  name: "",
  type: "text",
  description: "",
  icon: "",
  config: {},
  position: 0,
  archived: false,
  usage_count: 0,
  created_at: "",
  updated_at: "",
};

// Quick actions (MUL-5465). `visibility` and `status` stay z.string() rather
// than z.enum: they are server-driven, and a newer server adding a value must
// degrade to the UI's default branch, not blank the whole list.
export const QuickActionSchema = z
  .object({
    id: z.string(),
    workspace_id: z.string(),
    name: z.string(),
    description: z.string().optional().default(""),
    assignee_type: z.string(),
    assignee_id: z.string(),
    prompt: z.string().optional().default(""),
    visibility: z.string().optional().default("public"),
    status: z.string().optional().default("active"),
    last_used_at: z.string().nullable().optional().default(null),
    use_count: z.number().optional().default(0),
    created_by_id: z.string().optional().default(""),
    created_at: z.string(),
    updated_at: z.string(),
    target_name: z.string().optional(),
    // Both default to the pessimistic reading on an older server: "not known to
    // be public" and "not known to be missing" keep the settings row honest
    // rather than asserting a state the server never sent.
    target_public: z.boolean().optional().default(false),
    target_missing: z.boolean().optional().default(false),
  })
  .loose();

export const EMPTY_QUICK_ACTION: QuickAction = {
  id: "",
  workspace_id: "",
  name: "",
  description: "",
  assignee_type: "agent",
  assignee_id: "",
  prompt: "",
  visibility: "public",
  status: "active",
  last_used_at: null,
  use_count: 0,
  created_by_id: "",
  created_at: "",
  updated_at: "",
  target_public: false,
  target_missing: true,
};

export const ListQuickActionsResponseSchema = z
  .object({
    quick_actions: z.array(QuickActionSchema).default([]),
  })
  .loose();

export const EMPTY_LIST_QUICK_ACTIONS_RESPONSE: ListQuickActionsResponse = {
  quick_actions: [],
};

export const QuickActionRenderSchema = z
  .object({
    content: z.string().default(""),
  })
  .loose();

export const ListPropertiesResponseSchema = z
  .object({
    properties: z.array(IssuePropertySchema).default([]),
    total: z.number().default(0),
  })
  .loose();

export const EMPTY_LIST_PROPERTIES_RESPONSE: ListPropertiesResponse = {
  properties: [],
  total: 0,
};

// Value bag: keyed by definition UUID; values are primitives or string
// arrays (multi_select). The preprocess step drops entries with unknown
// shapes BEFORE validation — a newer server shipping an object-shaped value
// (future actor/relation types) must degrade to "that one property missing",
// never fail the whole IssueSchema and blank the list via parseWithFallback.
export const IssuePropertyValuesSchema = z.preprocess(
  (raw) => {
    if (typeof raw !== "object" || raw === null || Array.isArray(raw))
      return {};
    const out: Record<string, unknown> = {};
    for (const [key, value] of Object.entries(raw)) {
      const ok =
        typeof value === "string" ||
        typeof value === "number" ||
        typeof value === "boolean" ||
        (Array.isArray(value) &&
          value.every((item) => typeof item === "string"));
      if (ok) out[key] = value;
    }
    return out;
  },
  z
    .record(
      z.string(),
      z.union([z.string(), z.number(), z.boolean(), z.array(z.string())]),
    )
    .default({}),
);

export const IssuePropertiesResponseSchema = z
  .object({
    properties: IssuePropertyValuesSchema,
  })
  .loose();

export const EMPTY_ISSUE_PROPERTIES_RESPONSE: IssuePropertiesResponse = {
  properties: {},
};

export interface AppConfigResponse {
  cdn_domain: string;
  // True when the CDN domain serves private content via time-bounded signed
  // URLs (CloudFront signing) — raw storage URLs on that domain are NOT
  // publicly fetchable and must not be used as native media sources
  // (MUL-3254). Older servers omit the field; treat that as false.
  cdn_signed?: boolean;
  allow_signup: boolean;
  google_client_id?: string;
  dingtalk_client_id?: string;
  // Legacy single-provider lock: true when the sign-in allowlist is exactly
  // ["dingtalk"]. Newer servers also send login_providers; older servers omit
  // both fields — treat that as unrestricted.
  dingtalk_only?: boolean;
  // Sign-in allowlist (e.g. ["dingtalk","lark"]): the login screen must only
  // offer the listed entry points; the unlisted routes are closed
  // server-side. Empty/omitted means unrestricted.
  login_providers?: string[];
  // Feishu (Lark) app AppID. When present the login screen renders the
  // "Continue with Feishu" button. Older servers omit it.
  lark_client_id?: string;
  posthog_key?: string;
  posthog_host?: string;
  analytics_environment?: string;
  daemon_server_url?: string;
  daemon_app_url?: string;
  workspace_creation_disabled?: boolean;
  /** Whether this deployment offers the self-hosted Git provider integration
   * (self-host only; off on the managed cloud). Absent/false hides the whole
   * Settings → Integrations "Git providers" section. */
  vcs_integration_available?: boolean;
  feature_flags?: Record<string, boolean>;
  server_version?: string;
}

// ---------------------------------------------------------------------------
// Schemas for the highest-risk API endpoints — those whose responses drive
// the issue detail page (timeline, comments, subscribers) and the issues
// list. These are the surfaces that white-screened in #2143 / #2147 / #2192.
//
// These schemas are intentionally LENIENT:
//   - String enums are stored as `z.string()` rather than `z.enum([...])`.
//     A new server-side enum value should render as a generic fallback in
//     the UI, never crash a `safeParse`.
//   - Optional fields are unioned with `null` and given fallbacks where
//     existing UI code already coerces them.
//   - Arrays default to `[]` so a missing `reactions` / `attachments` /
//     `entries` field doesn't take the page down.
//   - Every object schema ends with `.loose()` so unknown server-side
//     fields pass through unchanged. zod 4's `.object()` defaults to STRIP,
//     which would silently delete fields the schema didn't explicitly list
//     — fine while the TS type doesn't claim them, but the moment a future
//     PR adds a TS field without updating the schema, the cast `as T` lies
//     and the field shows up as `undefined` at runtime. `.loose()` removes
//     that synchronisation hazard.
//
// These schemas are deliberately not typed as `z.ZodType<TimelineEntry>` /
// `z.ZodType<Issue>` etc. — the strict TS types narrow string fields to
// literal unions, which would defeat the leniency above. `parseWithFallback`
// returns the parsed value cast to the caller-supplied `T`, so the strict
// type still flows out at the call site; the schema only guards shape.
// ---------------------------------------------------------------------------

const ReactionSchema = z.object({
  id: z.string(),
  comment_id: z.string(),
  actor_type: z.string(),
  actor_id: z.string(),
  emoji: z.string(),
  created_at: z.string(),
});

// Nested attachments embedded in timeline/comment responses stay lenient on
// purpose: a single malformed attachment must not knock the whole timeline
// into the fallback `[]`.
const AttachmentSchema = z
  .object({
    id: z.string(),
  })
  .loose();

const ChatQuickActionSchema = z
  .object({
    label: z.string(),
    prompt: z.string(),
    primary: z.boolean().optional(),
  })
  .loose();

const ChatCoordinatorStepSchema = z
  .object({
    seq: z.number(),
    type: z
      .enum(["tool_use", "tool_result", "thinking", "text", "error"])
      .catch("error"),
    tool: z.string().optional(),
    content: z.string().optional(),
    input: z.string().optional(),
    output: z.string().optional(),
    error: z.boolean().optional(),
  })
  .loose();

const ChatCoordinatorIssueResultSchema = z
  .object({
    action: z.string().catch("unknown"),
    issue_id: z.string().uuid(),
    issue_identifier: z.string().catch("").optional(),
    issue_title: z.string().catch("").optional(),
    comment_id: z.string().uuid().catch("").optional(),
    task_id: z.string().uuid().catch("").optional(),
  })
  .loose();

export const ChatCoordinatorTraceSchema = z
  .object({
    action: z.string().optional(),
    look_into: z.string().optional(),
    reason: z.string().optional(),
    elapsed_ms: z.number().optional(),
    source: z.string().optional(),
    steps: z.array(ChatCoordinatorStepSchema).catch([]).optional(),
    issue_results: z
      .array(ChatCoordinatorIssueResultSchema.nullable().catch(null))
      .catch([])
      .transform((items) => items.filter((item) => item != null))
      .optional(),
  })
  .loose();

export const ChatMessageSchema = z
  .object({
    id: z.string(),
    chat_session_id: z.string(),
    role: z.enum(["user", "assistant"]).catch("assistant"),
    content: z.string().default(""),
    task_id: z.string().nullable().default(null),
    created_at: z.string().default(""),
    attachments: z.array(AttachmentSchema).optional(),
    failure_reason: z.string().nullable().optional(),
    elapsed_ms: z.number().nullable().optional(),
    message_kind: z
      .enum([
        "message",
        "no_response",
        "onboarding_kickoff",
        "onboarding_opening",
        "coordinator",
      ])
      .catch("message")
      .optional(),
    coordinator: ChatCoordinatorTraceSchema.optional(),
    // Optional additive data degrades independently: a malformed suggestion
    // must not hide the assistant reply that contains it.
    quick_actions: z
      .array(ChatQuickActionSchema)
      .catch([])
      .optional()
      .default([]),
  })
  .loose();

export const ChatMessageListSchema = z.array(ChatMessageSchema).default([]);
export const EMPTY_CHAT_MESSAGE_LIST: ChatMessage[] = [];

export const ChatMessagesPageSchema = z
  .object({
    messages: z.array(ChatMessageSchema).default([]),
    limit: z.number().default(50),
    has_more: z.boolean().default(false),
    next_cursor: z
      .object({
        created_at: z.string(),
        id: z.string(),
      })
      .loose()
      .nullable()
      .optional(),
  })
  .loose();

// Standalone attachment lookup (`GET /api/attachments/{id}`) is the source of
// truth for click-time download URLs. The two fields the download flow opens
// in a new tab — `download_url` and `url` — must be strings, otherwise we'd
// happily `window.open(undefined)`. `filename` gates the toast/title and is
// also enforced so a missing value falls back to the empty record below.
//
// `markdown_url` is parsed lenient: a server old enough to predate
// MUL-3192 omits the field, in which case the schema defaults it to "".
// Callers that need to persist a URL into markdown should go through the
// `useFileUpload` helper (which falls back to the legacy
// `attachmentDownloadPath` shape when `markdown_url` is empty), so the
// empty-string default does not silently break any persistence path.
export const AttachmentResponseSchema = z
  .object({
    id: z.string(),
    url: z.string(),
    download_url: z.string(),
    markdown_url: z.string().optional().default(""),
    filename: z.string(),
    chat_session_id: z.string().nullable().optional(),
    chat_message_id: z.string().nullable().optional(),
  })
  .loose();

export const EMPTY_ATTACHMENT: Attachment = {
  id: "",
  workspace_id: "",
  issue_id: null,
  comment_id: null,
  chat_session_id: null,
  chat_message_id: null,
  uploader_type: "",
  uploader_id: "",
  filename: "",
  url: "",
  download_url: "",
  markdown_url: "",
  content_type: "",
  size_bytes: 0,
  created_at: "",
};

// All object schemas use `.loose()` so unknown server-side fields pass
// through unchanged. zod 4's `.object()` defaults to STRIP, which would
// silently drop new fields and surface as a "field neither showed up in
// the UI" mystery the next time the TS type adopted them but the schema
// wasn't updated in lock-step. `.loose()` removes that synchronisation
// hazard — the schema validates the shape it knows about and leaves the
// rest alone.
const TimelineEntrySchema = z
  .object({
    type: z.string(),
    id: z.string(),
    actor_type: z.string(),
    actor_id: z.string(),
    created_at: z.string(),
    action: z.string().optional(),
    details: z.record(z.string(), z.unknown()).optional(),
    content: z.string().optional(),
    parent_id: z.string().nullable().optional(),
    updated_at: z.string().optional(),
    comment_type: z.string().optional(),
    reactions: z.array(ReactionSchema).optional(),
    attachments: z.array(AttachmentSchema).optional(),
    source_task_id: z.string().nullable().optional(),
    coalesced_count: z.number().optional(),
  })
  .loose();

// /timeline returns a flat array of TimelineEntry, oldest first. The
// previously cursor-paginated wrapper was removed (#1929) — at observed data
// sizes (p99 ~30 entries per issue) paged delivery only created bugs.
export const TimelineEntriesSchema = z.array(TimelineEntrySchema);

export const EMPTY_TIMELINE_ENTRIES: TimelineEntry[] = [];

const OptionalStringSchema = z.preprocess(
  (value) => (typeof value === "string" ? value : undefined),
  z.string().optional(),
);

const BooleanWithDefaultSchema = (fallback: boolean) =>
  z.preprocess(
    (value) => (typeof value === "boolean" ? value : undefined),
    z.boolean().default(fallback),
  );

const FeatureFlagsSchema = z.preprocess(
  (value) =>
    value && typeof value === "object" && !Array.isArray(value)
      ? value
      : undefined,
  z.record(z.string(), BooleanWithDefaultSchema(false)).default({}),
);

export const AppConfigSchema = z
  .object({
    cdn_domain: z.string().default(""),
    cdn_signed: BooleanWithDefaultSchema(false),
    allow_signup: BooleanWithDefaultSchema(true),
    google_client_id: OptionalStringSchema,
    dingtalk_client_id: OptionalStringSchema,
    dingtalk_only: BooleanWithDefaultSchema(false).optional(),
    login_providers: z.preprocess(
      (value) =>
        Array.isArray(value)
          ? value.filter((item) => typeof item === "string")
          : undefined,
      z.array(z.string()).optional(),
    ),
    lark_client_id: OptionalStringSchema,
    posthog_key: OptionalStringSchema,
    posthog_host: OptionalStringSchema,
    analytics_environment: OptionalStringSchema,
    daemon_server_url: OptionalStringSchema,
    daemon_app_url: OptionalStringSchema,
    workspace_creation_disabled: BooleanWithDefaultSchema(false).optional(),
    vcs_integration_available: BooleanWithDefaultSchema(false).optional(),
    feature_flags: FeatureFlagsSchema,
    server_version: OptionalStringSchema,
  })
  .loose();

export const EMPTY_APP_CONFIG: AppConfigResponse = {
  cdn_domain: "",
  cdn_signed: false,
  allow_signup: true,
  google_client_id: "",
  dingtalk_client_id: "",
  dingtalk_only: false,
  login_providers: [],
  lark_client_id: "",
  daemon_server_url: "",
  daemon_app_url: "",
  workspace_creation_disabled: false,
  vcs_integration_available: false,
  feature_flags: {},
};

// Preference keys may grow over time, so keep both the key and value spaces
// forward-compatible while still rejecting non-string persisted data.
export const NotificationPreferenceResponseSchema = z
  .object({
    workspace_id: z.string(),
    preferences: z.record(z.string(), z.string()).default({}),
  })
  .loose();

export const EMPTY_NOTIFICATION_PREFERENCE_RESPONSE: NotificationPreferenceResponse =
  {
    workspace_id: "",
    preferences: {},
  };

export const CreateFeedbackResponseSchema = z
  .object({
    id: z.string(),
    created_at: z.string(),
  })
  .loose();

export const EMPTY_CREATE_FEEDBACK_RESPONSE: CreateFeedbackResponse = {
  id: "",
  created_at: "",
};

export const CommentSchema = z
  .object({
    id: z.string(),
    issue_id: z.string(),
    author_type: z.string(),
    author_id: z.string(),
    content: z.string(),
    type: z.string(),
    parent_id: z.string().nullable(),
    reactions: z.array(ReactionSchema).default([]),
    attachments: z.array(AttachmentSchema).default([]),
    created_at: z.string(),
    updated_at: z.string(),
    source_task_id: z.string().nullable().optional(),
    // Set only on comments a quick action produced (MUL-5465). Server-only.
    quick_action_id: z.string().nullable().optional(),
  })
  .loose();

export const CommentsListSchema = z.array(CommentSchema);

// Degraded placeholder for a comment response that failed schema validation.
// The empty id is the caller's signal that nothing usable came back — the run
// UI treats it as "could not read the result" rather than a successful run.
export const EMPTY_COMMENT: Comment = {
  id: "",
  issue_id: "",
  author_type: "member",
  author_id: "",
  content: "",
  type: "comment",
  parent_id: null,
  reactions: [],
  attachments: [],
  created_at: "",
  updated_at: "",
  resolved_at: null,
  resolved_by_type: null,
  resolved_by_id: null,
};

const CommentTriggerPreviewAgentSchema = z
  .object({
    id: z.string(),
    name: z.string().default(""),
    avatar_url: z.string().optional(),
    source: z.string().default(""),
    reason: z.string().default(""),
  })
  .loose();

// Per-target outcome of an explicit @agent / @squad mention (MUL-4525 §2).
// target_id is required to correlate with the client's rendered mention; a
// malformed entry (missing id) is dropped rather than failing the whole payload.
export const CommentTriggerOutcomeSchema = z
  .object({
    target_type: z.string().default(""),
    target_id: z.string(),
    status: z.string().default(""),
    reason_code: z.string().default(""),
  })
  .loose();

export const CommentTriggerPreviewSchema = z
  .object({
    agents: z.array(CommentTriggerPreviewAgentSchema).default([]),
    // Drop malformed blocked entries INDIVIDUALLY (MUL-4525): a single bad item
    // must not discard the whole set of valid blocked mentions. A non-array
    // degrades to []; each valid entry is kept, each malformed one dropped.
    blocked: z
      .array(z.unknown())
      .catch([])
      .default([])
      .transform((items) =>
        items.flatMap((item) => {
          const parsed = CommentTriggerOutcomeSchema.safeParse(item);
          return parsed.success ? [parsed.data] : [];
        }),
      ),
  })
  .loose();

const IssueTriggerPreviewItemSchema = z
  .object({
    issue_id: z.string(),
    agent_id: z.string().default(""),
    source: z.string().default(""),
    handoff_supported: z.boolean().default(false),
  })
  .loose();

export const IssueTriggerPreviewSchema = z
  .object({
    triggers: z.array(IssueTriggerPreviewItemSchema).default([]),
    total_count: z.number().default(0),
  })
  .loose();

// Metadata is primitive-only by API/DB contract. Stay lenient on shape:
// unknown keys land as `unknown` to a caller, but the field itself defaults
// to {} so consumers never need to nil-guard `issue.metadata`.
const IssueMetadataSchema = z
  .record(z.string(), z.union([z.string(), z.number(), z.boolean()]))
  .default({});

export const IssueSchema = z
  .object({
    id: z.string(),
    workspace_id: z.string(),
    number: z.number(),
    identifier: z.string(),
    title: z.string(),
    description: z.string().nullable(),
    status: z.string(),
    priority: z.string(),
    assignee_type: z.string().nullable(),
    assignee_id: z.string().nullable(),
    creator_type: z.string(),
    creator_id: z.string(),
    parent_issue_id: z.string().nullable(),
    project_id: z.string().nullable(),
    position: z.number(),
    // Older backends predate `stage`; default to null so a missing field parses
    // cleanly into the non-optional Issue.stage (number | null).
    stage: z.number().nullable().default(null),
    start_date: z.string().nullable(),
    due_date: z.string().nullable(),
    metadata: IssueMetadataSchema,
    // Older backends predate custom properties; default {} so consumers never
    // nil-guard issue.properties.
    properties: IssuePropertyValuesSchema,
    reactions: z.array(z.unknown()).optional(),
    labels: z.array(z.unknown()).optional(),
    created_at: z.string(),
    updated_at: z.string(),
  })
  .loose();

export const ListIssuesResponseSchema = z
  .object({
    issues: z.array(IssueSchema).default([]),
    total: z.number().default(0),
  })
  .loose();

// Response schema for POST /api/issues. Two tightenings over IssueSchema:
//
//   - `id` must be non-empty. A created issue always carries a real id, so an
//     empty/absent id means the create effectively failed. createIssue turns a
//     schema failure into a rejection (not a fabricated success), so tightening
//     id here routes an id-less body to that same failure path.
//   - `labels` is the backend-compatibility signal the create modal reads to
//     decide whether the backend attached labels in the create transaction
//     (present) or predates that (absent → fall back to per-label attach).
//     Validate it strictly as Label[] and degrade a malformed value to
//     `undefined` — the same as an absent field — so a wrong shape (null,
//     object, a garbage array) can never masquerade as "handled" and suppress
//     the fallback. Unlike the loose IssueSchema.labels (z.array(z.unknown())),
//     the elements are fully validated. See packages/views/modals/create-issue.tsx.
export const CreateIssueResponseSchema = IssueSchema.extend({
  id: z.string().min(1),
  labels: z.array(LabelSchema).optional().catch(undefined),
}).loose();

export const EMPTY_LIST_ISSUES_RESPONSE: ListIssuesResponse = {
  issues: [],
  total: 0,
};

const SearchIssueResultSchema = IssueSchema.extend({
  match_source: z.string(),
  matched_snippet: z.string().optional(),
  matched_description_snippet: z.string().optional(),
  matched_comment_snippet: z.string().optional(),
}).loose();

export const SearchIssuesResponseSchema = z
  .object({
    issues: z.array(SearchIssueResultSchema).default([]),
    total: z.number().default(0),
  })
  .loose();

export const EMPTY_SEARCH_ISSUES_RESPONSE: SearchIssuesResponse = {
  issues: [],
  total: 0,
};

const ProjectSchema = z
  .object({
    id: z.string(),
    workspace_id: z.string(),
    title: z.string(),
    description: z.string().nullable(),
    icon: z.string().nullable(),
    status: z.string(),
    priority: z.string(),
    lead_type: z.string().nullable(),
    lead_id: z.string().nullable(),
    // .default(null) so a project from an older backend (frontend deploys before
    // backend) that omits these keys parses to null instead of failing the whole
    // object — which would degrade a search/list batch to the empty fallback.
    start_date: z.string().nullable().default(null),
    due_date: z.string().nullable().default(null),
    created_at: z.string(),
    updated_at: z.string(),
    issue_count: z.number().default(0),
    done_count: z.number().default(0),
    resource_count: z.number().default(0),
  })
  .loose();

const SearchProjectResultSchema = ProjectSchema.extend({
  match_source: z.string(),
  matched_snippet: z.string().optional(),
}).loose();

export const SearchProjectsResponseSchema = z
  .object({
    projects: z.array(SearchProjectResultSchema).default([]),
    total: z.number().default(0),
  })
  .loose();

export const EMPTY_SEARCH_PROJECTS_RESPONSE: SearchProjectsResponse = {
  projects: [],
  total: 0,
};

const IssueAssigneeGroupSchema = z
  .object({
    id: z.string(),
    assignee_type: z.string().nullable(),
    assignee_id: z.string().nullable(),
    issues: z.array(IssueSchema).default([]),
    total: z.number().default(0),
  })
  .loose();

export const GroupedIssuesResponseSchema = z
  .object({
    groups: z.array(IssueAssigneeGroupSchema).default([]),
  })
  .loose();

export const EMPTY_GROUPED_ISSUES_RESPONSE: GroupedIssuesResponse = {
  groups: [],
};

const IssueTableActorRefSchema = z
  .object({
    // Server-driven enums stay open so installed desktop clients survive a
    // backend that introduces another actor kind.
    type: z.string(),
    id: z.string(),
  })
  .loose();

const IssueTableParentRefSchema = z
  .object({
    id: z.string(),
    number: z.number(),
    identifier: z.string(),
    title: z.string(),
    status: z.string(),
  })
  .loose();

const IssueTableGroupValueSchema = z.discriminatedUnion("kind", [
  z
    .object({
      kind: z.literal("status"),
      status: z.string(),
    })
    .loose(),
  z
    .object({
      kind: z.literal("assignee"),
      actor: IssueTableActorRefSchema.nullable(),
    })
    .loose(),
  z
    .object({
      kind: z.literal("project"),
      project_id: z.string().nullable().optional().default(null),
    })
    .loose(),
  z
    .object({
      kind: z.literal("parent"),
      parent_id: z.string().nullable().optional().default(null),
      parent: IssueTableParentRefSchema.nullable().optional().default(null),
      value_state: z.enum(["value", "unavailable", "unset"]),
    })
    .loose(),
  z
    .object({
      kind: z.literal("property"),
      property_id: z.string(),
      value: z.union([z.string(), z.boolean(), z.null()]).optional(),
      value_state: z.enum(["value", "unavailable", "unset"]),
    })
    .loose(),
]);

const IssueTableGroupDescriptorSchema: z.ZodType<IssueTableGroupDescriptor> =
  z.lazy(() =>
    z
      .object({
        key: z.string(),
        value: IssueTableGroupValueSchema,
        count: z.number(),
        secondary_groups: z.array(IssueTableGroupDescriptorSchema).optional(),
      })
      .loose(),
  );

export const IssueTableGroupsResponseSchema = z
  .object({
    query_fingerprint: z.string(),
    total: z.number(),
    groups: z.array(IssueTableGroupDescriptorSchema).default([]),
    next_cursor: z.string().nullable().default(null),
  })
  .loose();

export const EMPTY_ISSUE_TABLE_GROUPS_RESPONSE: IssueTableGroupsResponse = {
  query_fingerprint: "",
  total: 0,
  groups: [],
  next_cursor: null,
};

const IssueTableRowSchema = z
  .object({
    issue: IssueSchema,
    direct_child_count: z.number().default(0),
  })
  .loose();

export const IssueTableRowsResponseSchema = z
  .object({
    query_fingerprint: z.string(),
    group_key: z.string().nullable().default(null),
    parent_id: z.string().nullable().default(null),
    total: z.number(),
    rows: z.array(IssueTableRowSchema).default([]),
    branch_total: z.number(),
    next_cursor: z.string().nullable().default(null),
  })
  .loose();

export const EMPTY_ISSUE_TABLE_ROWS_RESPONSE: IssueTableRowsResponse = {
  query_fingerprint: "",
  group_key: null,
  parent_id: null,
  total: 0,
  rows: [],
  branch_total: 0,
  next_cursor: null,
};

const IssueTableFacetValueSchema = z
  .object({
    key: z.string(),
    count: z.number(),
  })
  .loose();

const IssueTableFacetSchema = z
  .object({
    kind: z.enum([
      "status",
      "priority",
      "assignee",
      "creator",
      "project",
      "label",
      "property",
      "working_agents",
    ]),
    property_id: z.string().optional(),
    values: z.array(IssueTableFacetValueSchema).default([]),
  })
  .loose();

export const IssueTableFacetsResponseSchema = z
  .object({
    query_fingerprint: z.string(),
    total: z.number(),
    facets: z.array(IssueTableFacetSchema).default([]),
  })
  .loose();

export const EMPTY_ISSUE_TABLE_FACETS_RESPONSE: IssueTableFacetsResponse = {
  query_fingerprint: "",
  total: 0,
  facets: [],
};

const SubscriberSchema = z
  .object({
    issue_id: z.string(),
    user_type: z.string(),
    user_id: z.string(),
    reason: z.string(),
    created_at: z.string(),
  })
  .loose();

export const SubscribersListSchema = z.array(SubscriberSchema);

export const ChildIssuesResponseSchema = z
  .object({
    issues: z.array(IssueSchema).default([]),
  })
  .loose();

export const CloudRuntimeNodeSchema = z
  .object({
    id: z.string(),
    owner_id: z.string(),
    instance_id: z.string(),
    region: z.string(),
    instance_type: z.string(),
    image_id: z.string(),
    subnet_id: z.string(),
    name: z.string(),
    status: z.string(),
    tags: z.record(z.string(), z.string()).default({}),
    metadata: z.record(z.string(), z.unknown()).default({}),
    created_at: z.string(),
    updated_at: z.string(),
  })
  .loose();

export const CloudRuntimeNodeListSchema = z.array(CloudRuntimeNodeSchema);

export const FCE2BStableRolloutMilestoneSchema = z.object({
  batch: z.number(),
  percentage: z.number(),
  scheduled_at: z.string(),
  kind: z.enum(["rollout", "complete"]),
});

export const FCE2BStableReleaseSchema = z
  .object({
    sandbox_backend: z.enum(["aliyun_fc", "asb"]).default("aliyun_fc"),
    artifact_kind: z
      .enum(["e2b_template", "oci_image"])
      .default("e2b_template"),
    artifact_ref: z.string().default(""),
    artifact_build_id: z.string().default(""),
    artifact_built_at: z.string().optional(),
    artifact_alias: z.string().default(""),
    artifact_digest: z.string().default(""),
    id: z.string(),
		template_id: z.string(),
		template_alias: z.string(),
    source_revision: z.string().default(""),
    note: z.string(),
    actor_user_id: z.string(),
    bootstrap: z.boolean(),
    status: z.enum([
      "validating",
      "developer_rollout",
      "awaiting_rollout",
      "rolling_out",
      "observing",
      "completed",
      "paused",
      "rolling_back",
      "rolled_back",
      "terminated",
      "failed",
    ]),
    current_batch: z.number(),
    target_percentage: z.number(),
    stage_target_count: z.number().optional(),
		previous_template_id: z.string(),
		previous_template_alias: z.string(),
    previous_artifact_ref: z.string().default(""),
    previous_artifact_build_id: z.string().default(""),
    previous_artifact_digest: z.string().default(""),
    manifest: z.record(z.string(), z.unknown()).optional(),
    total_targets: z.number(),
    updated_targets: z.number(),
    failed_targets: z.number(),
    developer_targets: z.number().default(0),
    developer_updated_targets: z.number().default(0),
    developer_rollout_started_at: z.string().optional(),
    developer_rollout_completed_at: z.string().optional(),
    rollout_started_at: z.string().optional(),
    batch_started_at: z.string().optional(),
    next_batch_at: z.string().optional(),
    rollout_schedule: z.array(FCE2BStableRolloutMilestoneSchema).optional(),
    completed_at: z.string().optional(),
    validation_error: z.string().optional(),
    created_at: z.string(),
    updated_at: z.string(),
  })
  .loose();

export const FCE2BStableReleaseListSchema = z.array(FCE2BStableReleaseSchema);

export const FCE2BStableRuntimeOverviewSchema = z
  .object({
    runtime_id: z.string(),
    workspace_id: z.string(),
    workspace_name: z.string(),
    runtime_name: z.string(),
    sandbox_backend: z.enum(["aliyun_fc", "asb"]).default("aliyun_fc"),
    provider: z.string(),
    status: z.string(),
    artifact_channel: z.enum(["stable", "candidate"]).default("stable"),
    artifact_alias: z.string().default(""),
    artifact_ref: z.string().default(""),
    artifact_build_id: z.string().default(""),
    artifact_digest: z.string().default(""),
    template_channel: z.enum(["stable", "candidate"]),
		template_alias: z.string(),
		template_id: z.string(),
    matches_current_stable: z.boolean(),
    matches_active_release: z.boolean(),
    active_release_target_status: z.string(),
    updated_at: z.string(),
  })
  .loose();

export const FCE2BStableRuntimeOverviewListSchema = z.array(
  FCE2BStableRuntimeOverviewSchema,
);

export const FCE2BStableTemplateBindingSchema = z
  .object({
    sandbox_backend: z.enum(["aliyun_fc", "asb"]).default("aliyun_fc"),
    artifact_kind: z
      .enum(["e2b_template", "oci_image"])
      .default("e2b_template"),
    artifact_ref: z.string().default(""),
    artifact_build_id: z.string().default(""),
    artifact_alias: z.string().default(""),
    artifact_digest: z.string().default(""),
		template_id: z.string(),
		template_alias: z.string(),
    release_id: z.string(),
  })
  .loose();

export const FCE2BStableChannelSchema = z
  .object({
    current: FCE2BStableTemplateBindingSchema.nullable(),
    active_release: FCE2BStableReleaseSchema.nullable(),
    can_publish: z.boolean(),
  })
  .loose();

export const EMPTY_FC_E2B_STABLE_RELEASE: FCE2BStableRelease = {
  sandbox_backend: "aliyun_fc",
  artifact_kind: "e2b_template",
  artifact_ref: "",
  artifact_build_id: "",
  artifact_alias: "",
  artifact_digest: "",
  id: "",
	template_id: "",
	template_alias: "",
  source_revision: "",
  note: "",
  actor_user_id: "",
  bootstrap: false,
  status: "failed",
  current_batch: 0,
  target_percentage: 0,
	previous_template_id: "",
	previous_template_alias: "",
  previous_artifact_ref: "",
  previous_artifact_build_id: "",
  previous_artifact_digest: "",
  total_targets: 0,
  updated_targets: 0,
  failed_targets: 0,
  developer_targets: 0,
  developer_updated_targets: 0,
  created_at: "",
  updated_at: "",
};

export const EMPTY_FC_E2B_STABLE_CHANNEL: FCE2BStableChannel = {
  current: null,
  active_release: null,
  can_publish: false,
};

export const EMPTY_CLOUD_RUNTIME_NODE_LIST: CloudRuntimeNode[] = [];

export const EMPTY_CLOUD_RUNTIME_NODE: CloudRuntimeNode = {
  id: "",
  owner_id: "",
  instance_id: "",
  region: "",
  instance_type: "",
  image_id: "",
  subnet_id: "",
  name: "",
  status: "",
  tags: {},
  metadata: {},
  created_at: "",
  updated_at: "",
};

// ---------------------------------------------------------------------------
// Workspace dashboard schemas
//
// The dashboard hits three independent rollup endpoints. Each returns a flat
// array, and every field is consumed by chart / KPI math — a missing number
// silently degrades to NaN downstream, so we coerce missing numbers to 0.
// String fields default to "" (no enum narrowing) to survive future model /
// agent ID drift, and so a single null from tz-aware SQL bucketing fails
// only that row instead of dropping the whole array to the `[]` fallback.
// ---------------------------------------------------------------------------

// Cost split carried by every usage row. `cost_usd_ticks` is what the provider
// itself charged for the rows behind this aggregate (1e-10 USD); the
// `uncosted_*` counts are the tokens from rows the provider did NOT price, and
// so are the only ones the client should run through its rate table.
//
// The `uncosted_*` fields are deliberately `.optional()` rather than
// `.default(0)`: a backend that predates them sends nothing, and defaulting
// those rows to "0 tokens left to estimate" would silently zero their cost.
// `undefined` means "this backend doesn't split", and the consumer falls back
// to the full token counts — i.e. exactly the old behaviour. A real 0 from a
// current backend means "everything here is already priced", which is a
// different thing and must stay distinguishable.
const CostSplitShape = {
  cost_usd_ticks: z.number().optional(),
  uncosted_input_tokens: z.number().optional(),
  uncosted_output_tokens: z.number().optional(),
  uncosted_cache_read_tokens: z.number().optional(),
  uncosted_cache_write_tokens: z.number().optional(),
};

const DashboardUsageDailySchema = z
  .object({
    date: z.string().default(""),
    agent_id: z.string().default(""),
    provider: z.string().default(""),
    model: z.string().default(""),
    input_tokens: z.number().default(0),
    output_tokens: z.number().default(0),
    cache_read_tokens: z.number().default(0),
    cache_write_tokens: z.number().default(0),
    ...CostSplitShape,
    task_count: z.number().default(0),
  })
  .loose();

export const DashboardUsageDailyListSchema = z.array(DashboardUsageDailySchema);

const DashboardUsageByAgentSchema = z
  .object({
    agent_id: z.string().default(""),
    provider: z.string().default(""),
    model: z.string().default(""),
    input_tokens: z.number().default(0),
    output_tokens: z.number().default(0),
    cache_read_tokens: z.number().default(0),
    cache_write_tokens: z.number().default(0),
    ...CostSplitShape,
    task_count: z.number().default(0),
  })
  .loose();

export const DashboardUsageByAgentListSchema = z.array(
  DashboardUsageByAgentSchema,
);

// `cancelled_count` defaults to 0 so an installed client pointed at a
// backend that predates it still renders: those rows simply carry no
// cancelled segment, which is exactly what that backend measured.
const DashboardAgentRunTimeSchema = z
  .object({
    agent_id: z.string().default(""),
    total_seconds: z.number().default(0),
    task_count: z.number().default(0),
    failed_count: z.number().default(0),
    cancelled_count: z.number().default(0),
  })
  .loose();

export const DashboardAgentRunTimeListSchema = z.array(
  DashboardAgentRunTimeSchema,
);

const DashboardRunTimeDailySchema = z
  .object({
    date: z.string().default(""),
    agent_id: z.string().default(""),
    total_seconds: z.number().default(0),
    task_count: z.number().default(0),
    failed_count: z.number().default(0),
    cancelled_count: z.number().default(0),
  })
  .loose();

export const DashboardRunTimeDailyListSchema = z.array(
  DashboardRunTimeDailySchema,
);

// Failure rollups. `failure_reason` is an open string on purpose — it carries
// the backend's canonical taxonomy, which grows as new classifier rules land
// (server/pkg/taskfailure). Pinning it to a z.enum would make an installed
// desktop client drop rows for a reason its build predates; the client folds
// unrecognised reasons into an "other" display class instead. The empty
// string is the succeeded bucket, so `.default("")` is a meaningful default
// only for a row that already lost its reason — such a row lands in the
// denominator rather than inventing a failure that never happened.
const DashboardFailureDailySchema = z
  .object({
    date: z.string().default(""),
    failure_reason: z.string().default(""),
    task_count: z.number().default(0),
  })
  .loose();

export const DashboardFailureDailyListSchema = z.array(
  DashboardFailureDailySchema,
);

const DashboardFailureByAgentSchema = z
  .object({
    agent_id: z.string().default(""),
    failure_reason: z.string().default(""),
    task_count: z.number().default(0),
  })
  .loose();

export const DashboardFailureByAgentListSchema = z.array(
  DashboardFailureByAgentSchema,
);

// ---------------------------------------------------------------------------
// Runtime usage schemas — the runtime-detail page's four usage endpoints
// (`/api/runtimes/:id/usage*`). Same leniency rules as the dashboard
// schemas above: numbers default to 0, strings to "", `.loose()` passes
// unknown fields.
// ---------------------------------------------------------------------------

const RuntimeUsageSchema = z
  .object({
    runtime_id: z.string().default(""),
    date: z.string().default(""),
    provider: z.string().default(""),
    model: z.string().default(""),
    input_tokens: z.number().default(0),
    output_tokens: z.number().default(0),
    cache_read_tokens: z.number().default(0),
    cache_write_tokens: z.number().default(0),
    ...CostSplitShape,
  })
  .loose();

export const RuntimeUsageListSchema = z.array(RuntimeUsageSchema);

const RuntimeHourlyActivitySchema = z
  .object({
    hour: z.number().default(0),
    count: z.number().default(0),
  })
  .loose();

export const RuntimeHourlyActivityListSchema = z.array(
  RuntimeHourlyActivitySchema,
);

const RuntimeUsageByAgentSchema = z
  .object({
    agent_id: z.string().default(""),
    provider: z.string().default(""),
    model: z.string().default(""),
    input_tokens: z.number().default(0),
    output_tokens: z.number().default(0),
    cache_read_tokens: z.number().default(0),
    cache_write_tokens: z.number().default(0),
    ...CostSplitShape,
    task_count: z.number().default(0),
  })
  .loose();

export const RuntimeUsageByAgentListSchema = z.array(RuntimeUsageByAgentSchema);

const RuntimeUsageByHourSchema = z
  .object({
    hour: z.number().default(0),
    model: z.string().default(""),
    input_tokens: z.number().default(0),
    output_tokens: z.number().default(0),
    cache_read_tokens: z.number().default(0),
    cache_write_tokens: z.number().default(0),
    ...CostSplitShape,
    task_count: z.number().default(0),
  })
  .loose();

export const RuntimeUsageByHourListSchema = z.array(RuntimeUsageByHourSchema);

// ---------------------------------------------------------------------------
// Agent task responses. The base object stays loose so daemon/runtime fields
// can drift while task-list consumers still validate the fields they render.
// ---------------------------------------------------------------------------

const OptionalStringArraySchema = z.preprocess(
  (value) =>
    Array.isArray(value) && value.every((item) => typeof item === "string")
      ? value
      : undefined,
  z.array(z.string()).optional(),
);

// One (provider, model) slice of a run's token usage. Token counts default to
// 0 rather than failing the row: a slice missing one counter is still worth
// pricing on the counters it does have, and the "we have no usage at all" case
// is carried by the field's absence, not by a zeroed entry.
const TaskUsageSchema = z
  .object({
    provider: z.string().optional(),
    model: z.string().default(""),
    input_tokens: z.number().default(0),
    output_tokens: z.number().default(0),
    cache_read_tokens: z.number().default(0),
    cache_write_tokens: z.number().default(0),
    cost_usd_ticks: z.number().optional(),
  })
  .loose();

export const AgentTaskSchema = z
  .object({
    id: z.string(),
    agent_id: z.string().default(""),
    runtime_id: z.string().default(""),
    issue_id: z.string().default(""),
    status: z.string().default("cancelled"),
    priority: z.number().default(0),
    dispatched_at: z.string().nullable().default(null),
    started_at: z.string().nullable().default(null),
    completed_at: z.string().nullable().default(null),
    result: z.unknown().default(null),
    error: z.string().nullable().default(null),
    failure_reason: z.string().optional(),
    created_at: z.string().default(""),
    dsh_trajectory_available: z.boolean().optional(),
    chat_session_id: z.string().optional(),
    autopilot_run_id: z.string().optional(),
    parent_task_id: z.string().optional(),
    attempt: z.number().optional(),
    trigger_comment_id: z.string().optional(),
    // Coverage is additive display metadata. A mixed-version or partially
    // upgraded server must not make one malformed optional field erase the
    // entire execution log, so degrade that field to "absent" independently.
    coalesced_comment_ids: OptionalStringArraySchema,
    delivered_comment_ids: OptionalStringArraySchema,
    trigger_summary: z.string().optional(),
    handoff_note: z.string().optional(),
    kind: z.string().optional(),
    work_dir: z.string().optional(),
    relative_work_dir: z.string().optional(),
    attribution: TaskAttributionSchema.optional(),
    // Per-run token usage. Same independent-degradation rule as the coverage
    // arrays above: usage is additive display metadata, so one malformed entry
    // must cost the row its usage figure, not erase the whole execution log.
    // `.catch(undefined)` collapses a bad array to "no usage recorded", which
    // the UI already renders as an em dash.
    usage: z.array(TaskUsageSchema).optional().catch(undefined),
  })
  .loose();

export const AgentTaskListSchema = z.array(AgentTaskSchema);

export const AgentSceneMemorySchema = z
  .object({
    id: z.string(),
    workspace_id: z.string().default(""),
    agent_id: z.string().default(""),
    org_id: z.string().default(""),
    scene_key: z.string(),
    scene_kind: z.string().default(""),
    scene_title: z.string().default(""),
    memory_text: z.string().default(""),
    memory_revision: z.number().default(0),
    status: z.string().default(""),
    last_error: z.string().default(""),
    last_error_code: z.string().default(""),
    updated_at: z.string().default(""),
    bootstrapped_at: z.string().optional().default(""),
    last_flushed_at: z.string().optional().default(""),
  })
  .loose();

export const AgentSceneMemoryListSchema = z.array(AgentSceneMemorySchema);
export const EMPTY_AGENT_SCENE_MEMORY: AgentSceneMemory = {
  id: "",
  workspace_id: "",
  agent_id: "",
  org_id: "",
  scene_key: "",
  scene_kind: "",
  scene_title: "",
  memory_text: "",
  memory_revision: 0,
  status: "",
  last_error: "",
  last_error_code: "",
  updated_at: "",
};
export const EMPTY_AGENT_SCENE_MEMORY_LIST: AgentSceneMemory[] = [];

const AgentSceneRelationItemSchema = z
  .object({
    issue_id: z.string().optional().default(""),
    issue: z.string().optional().default(""),
    purpose: z.string().default(""),
    status: z.string().default(""),
    on_this_scene: z.boolean().optional().default(false),
  })
  .loose();

export const AgentSceneRelationListSchema = z
  .object({
    items: z.array(AgentSceneRelationItemSchema).catch([]),
  })
  .loose();
export const EMPTY_AGENT_SCENE_RELATION_LIST: { items: AgentSceneRelation[] } = {
  items: [],
};

// Task cancellation (`POST /api/tasks/:id/cancel`) is consumed directly by
// chat recovery. Its optional message payload must be well-formed before the
// UI deletes a message from cache or restores text into the input.
const CancelledChatMessageSchema = z
  .object({
    chat_session_id: z.string(),
    message_id: z.string(),
    content: z.string(),
    restore_to_input: z.boolean().default(false),
    // Attachments detached from the deleted message so a restored draft can
    // re-bind them on re-send. Absent on servers that predate the field.
    attachments: z.array(AttachmentSchema).optional(),
  })
  .loose();

export const CancelTaskResponseSchema = AgentTaskSchema.extend({
  cancelled_chat_message: CancelledChatMessageSchema.nullish().transform(
    (value) => value ?? undefined,
  ),
}).loose();

// Deferred-cancellation draft restores
// (`GET /api/chat/sessions/{id}/draft-restores`, #5219) feed the composer
// directly: `content` becomes the draft text, `attachments` re-bind on
// re-send, and `id` is the consume key. A malformed response falls back to
// an empty list — the durable row stays pending server-side, so nothing is
// lost by skipping a fetch.
const ChatDraftRestoreSchema = z
  .object({
    id: z.string(),
    chat_session_id: z.string(),
    task_id: z.string().optional(),
    content: z.string().default(""),
    attachments: z.array(AttachmentSchema).optional(),
    created_at: z.string().optional(),
  })
  .loose();

export const ChatDraftRestoresResponseSchema = z
  .object({
    restores: z.array(ChatDraftRestoreSchema).default([]),
  })
  .loose();

const ChatQueuedTaskSchema = z
  .object({
    task_id: z.string(),
    status: z.string().default("queued"),
    created_at: z.string().default(""),
    message_id: z.string().optional(),
    content: z.string().optional(),
  })
  .loose();

const ChatQueuedTasksSchema = z.array(z.unknown()).transform((tasks) =>
  tasks.flatMap((task) => {
    const parsed = ChatQueuedTaskSchema.safeParse(task);
    return parsed.success ? [parsed.data] : [];
  }),
);

// Root fields retain the legacy single-task response shape. Keep additive
// fields optional so callers can distinguish an older server from an empty
// queue. A malformed queue row is ignored without discarding a valid head.
export const ChatPendingTaskSchema: z.ZodType<ChatPendingTask> = z
  .object({
    task_id: z.string().optional(),
    status: z.string().optional(),
    created_at: z.string().optional(),
    supports_queue: z.boolean().optional(),
    queued_tasks: ChatQueuedTasksSchema.optional(),
  })
  .loose();

export const EMPTY_CHAT_PENDING_TASK: ChatPendingTask = {};

export const SendChatMessageResponseSchema: z.ZodType<SendChatMessageResponse> =
  z
    .object({
      message_id: z.string().min(1),
      task_id: z
        .string()
        .nullish()
        .transform((id) => id || undefined),
      supports_queue: z.boolean().optional(),
      queued: z.boolean().optional().catch(undefined),
      created_at: z.string().min(1),
      attachment_ids: z
        .array(z.string())
        .nullish()
        .transform((ids) => ids ?? undefined),
      assistant_message_id: z
        .string()
        .nullish()
        .transform((id) => id || undefined),
      assistant_content: z
        .string()
        .nullish()
        .transform((content) => content || undefined),
      assistant_created_at: z
        .string()
        .nullish()
        .transform((at) => at || undefined),
      assistant_message_kind: z
        .enum([
          "message",
          "no_response",
          "onboarding_kickoff",
          "onboarding_opening",
          "coordinator",
        ])
        .optional(),
      coordinator: ChatCoordinatorTraceSchema.optional(),
    })
    .loose();

// `started` is the only field the flow branches on, and a malformed response
// must not be read as "the opening landed" — parseWithFallback's fallback says
// it did not, which leaves the flow's own retry as the recovery path.
export const StartMikaOnboardingResponseSchema: z.ZodType<StartMikaOnboardingResponse> =
  z
    .object({
      started: z.boolean(),
      message_id: z
        .string()
        .nullish()
        .transform((id) => id ?? undefined),
      created_at: z
        .string()
        .nullish()
        .transform((at) => at ?? undefined),
    })
    .loose();

export const PrioritizeQueuedChatTaskResponseSchema: z.ZodType<PrioritizeQueuedChatTaskResponse> =
  z
    .object({
      task_id: z.string(),
      active_task_id: z.string().optional(),
    })
    .loose();

export const EMPTY_PRIORITIZE_QUEUED_CHAT_TASK_RESPONSE: PrioritizeQueuedChatTaskResponse =
  { task_id: "" };

export const EMPTY_CHAT_DRAFT_RESTORES: ChatDraftRestoresResponse = {
  restores: [],
};

export const EMPTY_CANCEL_TASK_RESPONSE: CancelTaskResponse = {
  id: "",
  agent_id: "",
  runtime_id: "",
  issue_id: "",
  status: "cancelled",
  priority: 0,
  dispatched_at: null,
  started_at: null,
  completed_at: null,
  result: null,
  error: null,
  created_at: "",
};

// ---------------------------------------------------------------------------
// Agent template catalog — `/api/agent-templates*` and the
// create-from-template response. The desktop app's create-agent picker
// reaches these endpoints, and a future server change to the template shape
// would white-screen older installed builds (#2192 pattern) without these
// parsers. Lenient by the same rules as IssueSchema above: arrays default to
// `[]`, optional fields stay optional, `.loose()` lets unknown fields pass
// through unchanged.
// ---------------------------------------------------------------------------

const AgentTemplateSkillRefSchema = z
  .object({
    source_url: z.string(),
    cached_name: z.string().default(""),
    cached_description: z.string().default(""),
  })
  .loose();

const AgentTemplateSummarySchemaBase = z
  .object({
    slug: z.string(),
    name: z.string(),
    description: z.string().default(""),
    category: z.string().optional(),
    icon: z.string().optional(),
    accent: z.string().optional(),
    // skills MUST default to [] — picker code reads `template.skills.length`
    // and `.map(...)`, both of which crash on `undefined`. The most common
    // future drift (field renamed / wrapped) lands here.
    skills: z.array(AgentTemplateSkillRefSchema).default([]),
  })
  .loose();

export const AgentTemplateSummarySchema = AgentTemplateSummarySchemaBase;

// List endpoint historically returns a bare array. Server could legitimately
// migrate to `{templates: [...]}` later — we accept either shape so an old
// desktop survives the upgrade.
export const AgentTemplateSummaryListSchema = z.union([
  z.array(AgentTemplateSummarySchemaBase),
  z
    .object({ templates: z.array(AgentTemplateSummarySchemaBase).default([]) })
    .loose()
    .transform((v) => v.templates),
]);

export const EMPTY_AGENT_TEMPLATE_SUMMARY_LIST: AgentTemplateSummary[] = [];

export const CoordinatorContractSchema = z
  .object({
    version: z.literal(1),
    scope: z.string().refine((value) => value.trim().length > 0),
    must_delegate: z.array(z.string().refine((value) => value.trim().length > 0)),
    constraints: z.array(z.string().refine((value) => value.trim().length > 0)),
    clarify_when: z.array(z.string().refine((value) => value.trim().length > 0)),
    source_instructions_sha256: z.string().regex(/^[a-f0-9]{64}$/).optional(),
  })
  .strict()
  .refine((value) => [...JSON.stringify(value)].length <= 1600);

export const AgentTemplateSchema = AgentTemplateSummarySchemaBase.extend({
  // Detail-only field. Default "" so a malformed detail still renders the
  // header + skill list; the user just sees an empty Instructions block.
  instructions: z.string().default(""),
  coordinator_contract: CoordinatorContractSchema.nullish().catch(null),
  system_key: z.string().optional(),
  system_instructions: z.string().optional(),
}).loose();

// Used as the parse fallback for `GET /api/agent-templates/:slug`. Slug comes
// from the URL, so we round-trip the requested one back into the fallback
// at the call site (see `getAgentTemplate` in client.ts).
export const EMPTY_AGENT_TEMPLATE_DETAIL: AgentTemplate = {
  slug: "",
  name: "",
  description: "",
  skills: [],
  instructions: "",
};

// ---------------------------------------------------------------------------
// Agent invocation permissions (MUL-3963)
//
/**
 * The composed inbound task instruction for one agent. Served per-agent and
 * manage-gated, so it is not public config. Every field defaults so a backend
 * that predates a segment cannot blank the whole preview.
 */
export const DispatchPromptSegmentSchema = z.object({
  id: z.string(),
  source: z.string().default("builtin"),
  delivery: z.string().default("per_turn"),
  customizable: z.boolean().default(false),
  overridden: z.boolean().default(false),
  condition: z.string().default(""),
  included: z.boolean().default(false),
  excluded_reason: z.string().optional(),
  managed_text: z.string().default(""),
  effective_text: z.string().default(""),
});

export const DispatchPromptRuntimeSectionSchema = z.object({
  id: z.string(),
  source: z.string().default("builtin"),
  customizable: z.boolean().default(false),
  origin: z.string().default(""),
});

export const DispatchPromptPreviewSchema = z.object({
  surface: z.string().default("auto"),
  segments: z.array(DispatchPromptSegmentSchema).default([]),
  instruction: z.string().default(""),
  runtime_sections: z.array(DispatchPromptRuntimeSectionSchema).default([]),
});
export type DispatchPromptPreviewPayload = z.infer<
  typeof DispatchPromptPreviewSchema
>;

export const ExtractAgentVoiceResponseSchema = z.object({
  persona: z.string().default(""),
  reply_tone: z.string().default(""),
});
export type ExtractAgentVoiceResponsePayload = z.infer<
  typeof ExtractAgentVoiceResponseSchema
>;
export const EMPTY_EXTRACT_AGENT_VOICE_RESPONSE: ExtractAgentVoiceResponsePayload =
  { persona: "", reply_tone: "" };

const AgentOKRSpendSchema = z.object({
  total_tokens: z.number().default(0),
  total_cost_usd_ticks: z.number().default(0),
  uncosted_tokens: z.number().default(0),
  task_count: z.number().default(0),
  unpriced_task_count: z.number().default(0),
});

export const AgentOKRSchema = z.object({
  id: z.string().default(""),
  label_id: z.string().default(""),
  position: z.number().int().default(0),
  objective: z.string().default(""),
  label: z.string().default(""),
  color: z.string().default(""),
  spend: AgentOKRSpendSchema.optional().catch(undefined),
  key_results: z
    .array(
      z.object({
        id: z.string().default(""),
        label_id: z.string().default(""),
        position: z.number().int().default(0),
        text: z.string().default(""),
        label: z.string().default(""),
        color: z.string().default(""),
        spend: AgentOKRSpendSchema.optional().catch(undefined),
      }),
    )
    .default([]),
});

export const AgentOKRResponseSchema = z.object({
  okrs: z.array(AgentOKRSchema).default([]),
  // Older servers returned spend without an availability bit. Treat that
  // legacy shape as available; the new server sends false explicitly when the
  // usage query failed and omits every spend object.
  usage_available: z.boolean().optional().catch(false).default(true),
});
export type AgentOKRResponsePayload = z.infer<typeof AgentOKRResponseSchema>;

// Permission fragments remain reusable for nested agent responses. Unknown
// permission modes degrade to the strict default rather than widening access.
// ---------------------------------------------------------------------------

export const AgentPermissionModeSchema = z
  .enum(["private", "public_to"])
  .catch("private");

export const AgentInvocationTargetSchema = z
  .object({
    target_type: z.string(),
    target_id: z
      .string()
      .nullable()
      .optional()
      .transform((v) => v ?? null),
  })
  .loose();

export const AgentInvocationTargetsSchema = z
  .array(AgentInvocationTargetSchema)
  .default([]);

// Agent payloads predate schema validation. Validate additive response policy
// fields without changing other existing fields or dropping future fields.
export const AgentResponseSchema = z
  .object({
    id: z.string(),
    coordinator_contract: CoordinatorContractSchema.nullish().catch(null),
    coordinator_contract_state: z.enum(["loaded", "not_configured", "stale", "unavailable"]).catch("unavailable").default("not_configured"),
    event_trigger_enabled: z.boolean().catch(false).default(false),
    dingtalk_response_enabled: z.boolean().catch(false).default(false),
    dingtalk_show_ai_tag: z.boolean().catch(false).default(false),
    dingtalk_response_policy_revision: z
      .number()
      .int()
      .positive()
      .safe()
      .catch(1)
      .default(1),
  })
  .loose();

export const AgentResponseListSchema = z.array(AgentResponseSchema);

export const EMPTY_AGENT_RESPONSE: Agent = {
  id: "",
  workspace_id: "",
  runtime_id: "",
  name: "",
  description: "",
  instructions: "",
  avatar_url: null,
  runtime_mode: "local",
  runtime_config: {},
  custom_args: [],
  visibility: "private",
  permission_mode: "private",
  invocation_targets: [],
  status: "offline",
  max_concurrent_tasks: 1,
  model: "",
  owner_id: null,
  skills: [],
  created_at: "",
  updated_at: "",
  archived_at: null,
  archived_by: null,
  dingtalk_response_enabled: false,
  dingtalk_show_ai_tag: false,
  dingtalk_response_policy_revision: 1,
};

// `agent` is a full Agent record — schematising every field would duplicate
// a 50-field interface and bit-rot fast. Keep it loose and require only `id`.
const MinimalAgentSchema = AgentResponseSchema.extend({
  permission_mode: AgentPermissionModeSchema.optional(),
  invocation_targets: AgentInvocationTargetsSchema.optional(),
});

export const CreateAgentFromTemplateResponseSchema = z
  .object({
    agent: MinimalAgentSchema,
    imported_skill_ids: z.array(z.string()).default([]),
    reused_skill_ids: z.array(z.string()).default([]),
  })
  .loose();

export const EMPTY_CREATE_AGENT_FROM_TEMPLATE_RESPONSE: CreateAgentFromTemplateResponse =
  {
    agent: { id: "" } as Agent,
    imported_skill_ids: [],
    reused_skill_ids: [],
  };

export const AgentBuilderSessionSchema = z
  .object({
    session_id: z.string(),
    builder_agent_id: z.string(),
    runtime_id: z.string(),
  })
  .loose();

export const EMPTY_AGENT_BUILDER_SESSION: AgentBuilderSession = {
  session_id: "",
  builder_agent_id: "",
  runtime_id: "",
};

export const GitHubAgentRepositorySchema = z
  .object({
    installation_id: z.string(),
    full_name: z.string(),
    private: z.boolean().default(false),
    default_branch: z.string().default(""),
    html_url: z.string().default(""),
  })
  .loose();

export const ListGitHubAgentRepositoriesResponseSchema = z
  .object({
    repositories: z.array(GitHubAgentRepositorySchema).default([]),
  })
  .loose();

export const EMPTY_GITHUB_AGENT_REPOSITORIES: ListGitHubAgentRepositoriesResponse =
  {
    repositories: [],
  };

export const GitHubInstallationSchema = z
  .object({
    id: z.string(),
    workspace_id: z.string(),
    installation_id: z.number().int().positive().optional(),
    account_login: z.string(),
    account_type: z.enum(["User", "Organization"]).default("User"),
    account_avatar_url: z.string().nullable().default(null),
    created_at: z.string(),
    connected_by: z.string().optional(),
  })
  .loose();

export const GitHubReusableInstallationSchema = z
  .object({
    id: z.string(),
    account_login: z.string(),
    account_type: z.enum(["User", "Organization"]).default("User"),
    account_avatar_url: z.string().nullable().default(null),
    source_workspace_id: z.string(),
    source_workspace_name: z.string(),
  })
  .loose();

export const ListGitHubInstallationsResponseSchema = z
  .object({
    installations: z.array(GitHubInstallationSchema).default([]),
    reusable_installations: z
      .array(GitHubReusableInstallationSchema)
      .default([]),
    configured: z.boolean().default(false),
    repository_browse_configured: z.boolean().optional().default(false),
    can_manage: z.boolean().optional(),
  })
  .loose();

export const EMPTY_GITHUB_INSTALLATION: GitHubInstallation = {
  id: "",
  workspace_id: "",
  account_login: "",
  account_type: "User",
  account_avatar_url: null,
  created_at: "",
};

export const EMPTY_GITHUB_INSTALLATIONS: ListGitHubInstallationsResponse = {
  installations: [],
  reusable_installations: [],
  configured: false,
  repository_browse_configured: false,
  can_manage: false,
};

const GitHubAgentSkillPreviewSchema = z
  .object({
    enabled: z.boolean().optional(),
    source_path: z.string(),
    name: z.string(),
    description: z.string().default(""),
    file_count: z.number().int().nonnegative().default(0),
  })
  .loose();

const NullableStringArraySchema = z
  .array(z.string())
  .nullish()
  .transform((value) => value ?? []);

export const AgentPackageRequirementsSchema = z.object({
  secrets: NullableStringArraySchema,
  deferred_bindings: NullableStringArraySchema,
  runtime_provider: z.string().default(""),
});

export const AgentPackagePreviewSchema = z.object({
  definition: z.record(z.string(), z.unknown()).optional(),
  preview_id: z.string().min(1),
  expires_at: z.string().min(1),
  package_hash: z.string().min(1),
  manifest_version: z.string().min(1),
  name: z.string().min(1),
  description: z.string().default(""),
  instructions: z.string().default(""),
  // A preview is a confirmation boundary: reject malformed authored constraints.
  coordinator_contract: CoordinatorContractSchema.nullish(),
  skills: z.array(GitHubAgentSkillPreviewSchema).default([]),
  manifest_fields: NullableStringArraySchema,
  configuration_fields: NullableStringArraySchema,
  warnings: NullableStringArraySchema,
  requirements: AgentPackageRequirementsSchema,
});

export const GitHubAgentPreviewSchema = z
  .object({
    requirements: AgentPackageRequirementsSchema.optional(),
    definition: z.record(z.string(), z.unknown()).optional(),
    preview_id: z.string().optional(),
    expires_at: z.string().optional(),
    repository_url: z.string().optional(),
    installation_id: z.string(),
    repository: z.string(),
    ref: z.string(),
    resolved_sha: z.string(),
    name: z.string(),
    description: z.string().default(""),
    instructions: z.string().default(""),
    coordinator_contract: CoordinatorContractSchema.nullish().catch(null),
    skills: z
      .array(GitHubAgentSkillPreviewSchema)
      .nullish()
      .transform((value) => value ?? []),
    compatible_providers: NullableStringArraySchema,
    warnings: NullableStringArraySchema,
    blockers: NullableStringArraySchema,
  })
  .loose();

export const EMPTY_GITHUB_AGENT_PREVIEW: GitHubAgentPreview = {
  installation_id: "",
  repository: "",
  ref: "",
  resolved_sha: "",
  name: "",
  description: "",
  instructions: "",
  skills: [],
  compatible_providers: [],
  warnings: [],
  blockers: [],
};

export const AgentManifestSchemaDownloadSchema = z.object({
  $schema: z.literal("https://json-schema.org/draft/2020-12/schema"),
  $defs: z.record(z.string(), z.record(z.string(), z.unknown())),
  oneOf: z.array(z.object({ $ref: z.string().min(1) })).min(1),
}).passthrough();

export const AgentSourceSchema = z
  .object({
    repository_url: z.string().optional(),
    can_sync: z.boolean().optional(),
    configuration_scope: NullableStringArraySchema,
    agent_id: z.string(),
    source_type: z.string(),
    installation_id: z.string().nullable().default(null),
    repository: z.string(),
    ref: z.string(),
    manifest_path: z.string().default("dingtalk-agent.json"),
    synced_commit_sha: z.string(),
    sync_status: z.string(),
    last_sync_error: z.string().nullable().default(null),
    last_sync_attempt_at: z.string().nullable().default(null),
    last_synced_at: z.string().default(""),
    github_connected: z.boolean().default(false),
  })
  .loose();

export const EMPTY_AGENT_SOURCE: AgentSource = {
  agent_id: "",
  source_type: "github",
  installation_id: null,
  repository: "",
  ref: "",
  manifest_path: "dingtalk-agent.json",
  synced_commit_sha: "",
  sync_status: "disconnected",
  last_sync_error: null,
  last_sync_attempt_at: null,
  last_synced_at: "",
  github_connected: false,
};

export const CreateGitHubAgentResponseSchema = z
  .object({
    agent: MinimalAgentSchema,
    source: AgentSourceSchema,
    warnings: NullableStringArraySchema,
  })
  .loose();

export const EMPTY_CREATE_GITHUB_AGENT_RESPONSE: CreateGitHubAgentResponse = {
  agent: { id: "" } as Agent,
  source: EMPTY_AGENT_SOURCE,
  warnings: [],
};

export const SyncAgentSourceResponseSchema = z
  .object({
    source: AgentSourceSchema,
    changed: z.boolean().default(false),
    warnings: NullableStringArraySchema,
  })
  .loose();

export const EMPTY_SYNC_AGENT_SOURCE_RESPONSE: SyncAgentSourceResponse = {
  source: EMPTY_AGENT_SOURCE,
  changed: false,
  warnings: [],
};

const AgentSourceFileChangeSchema = z.object({
  path: z.string(), status: z.string(),
  before: z.string().nullable(), after: z.string().nullable(),
  before_sha: z.string().optional(), after_sha: z.string().optional(),
  before_mode: z.string().optional(), after_mode: z.string().optional(),
}).loose();

const ImmutableGitCommitSchema = z.string().regex(/^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$/);
const AgentSourceChangesSchema = z.array(AgentSourceFileChangeSchema).nullish().transform((value) => value ?? []);

export const AgentSourceSyncPreviewSchema = z.object({
  requirements: AgentPackageRequirementsSchema.optional(),
  preview_id: z.string().uuid(),
  expires_at: z.string(),
  repository_url: z.string(),
  ref: z.string(),
  base_sha: ImmutableGitCommitSchema,
  resolved_sha: ImmutableGitCommitSchema,
  git_changes: AgentSourceChangesSchema,
  configuration_changes: AgentSourceChangesSchema,
  warnings: NullableStringArraySchema,
  changed: z.boolean(),
}).loose();

export const EMPTY_AGENT_SOURCE_SYNC_PREVIEW: AgentSourceSyncPreview = {
  preview_id: "", expires_at: "", repository_url: "", ref: "", base_sha: "", resolved_sha: "",
  git_changes: [], configuration_changes: [], warnings: [], changed: false,
};

export const AgentSourceBranchesSchema = z.object({
  repository: z.string(),
  repository_url: z.string(),
  default_branch: z.string(),
  branches: z.array(z.object({
    name: z.string(),
    commit: z.object({ sha: ImmutableGitCommitSchema }),
    protected: z.boolean().default(false),
  })).nullish().transform((value) => value ?? []),
}).loose();

export const EMPTY_AGENT_SOURCE_BRANCHES: AgentSourceBranches = {
  repository: "", repository_url: "", default_branch: "", branches: [],
};
/**
 * The stored configuration of a creation conversation. Every field falls back
 * to empty on its own: a draft written by a newer build (or truncated in
 * transit) must still restore the fields it does understand rather than
 * discarding the user's work wholesale.
 */
export const StoredAgentDraftSchema = z
  .object({
    package_text: z.string().optional().catch(undefined),
    name: z.string().catch(""),
    description: z.string().catch(""),
    instructions: z.string().catch(""),
    coordinator_contract: CoordinatorContractSchema.nullish().catch(null),
    avatar_url: z.string().nullable().catch(null),
    model: z.string().catch(""),
    thinking_level: z.string().catch(""),
    service_tier: z.string().catch(""),
    skill_ids: z.array(z.string()).catch([]),
    permission_scope: z
      .enum(["private", "workspace", "members"])
      .catch("private"),
    member_ids: z.array(z.string()).catch([]),
    team_ids: z.array(z.string()).catch([]),
    applied_message_id: z.string().nullable().catch(null),
  })
  .loose();

/**
 * One unfinished creation draft. Every field except the id has a safe empty
 * default: an older server that omits `runtime_id` must degrade to "let the
 * user pick" rather than dropping the whole row and losing the conversation.
 */
export const AgentBuilderSessionSummarySchema = z
  .object({
    session_id: z.string(),
    title: z.string().catch(""),
    runtime_id: z.string().catch(""),
    created_at: z.string().catch(""),
    updated_at: z.string().catch(""),
    last_message_content: z.string().catch(""),
    last_message_role: z.string().catch(""),
    last_message_at: z.string().catch(""),
    // Absent for a conversation the user has never hand-edited; the client then
    // replays the last <agent_draft> block instead of restoring a stored copy.
    draft: StoredAgentDraftSchema.nullish().catch(null),
  })
  .loose();

export const AgentBuilderSessionListSchema = z
  .object({
    sessions: z.array(AgentBuilderSessionSummarySchema).catch([]),
  })
  .loose();

export const EMPTY_AGENT_BUILDER_SESSION_LIST: {
  sessions: AgentBuilderSessionSummary[];
} = { sessions: [] };

export const AgentBuilderRuntimeSwitchSchema = z
  .object({
    runtime_id: z.string(),
  })
  .loose();

// This endpoint returns 2xx only after the carrier has been bound to the
// runtime the caller asked for; anything else is a thrown error and no commit.
// So the safe fallback for an unparseable SUCCESS body is the requested id, not
// an empty one: the rebind did happen, and reporting "unknown" would leave the
// picker showing a runtime that is no longer executing — the exact split this
// endpoint exists to close.
export const agentBuilderRuntimeSwitchFallback = (
  requestedRuntimeID: string,
): AgentBuilderRuntimeSwitch => ({ runtime_id: requestedRuntimeID });

// Squad list responses carry lightweight membership previews used by hover
// cards. The preview fields are additive API fields, so older backends default
// cleanly to no preview instead of breaking newer frontends.
const SquadMemberPreviewSchema = z
  .object({
    member_type: z.string(),
    member_id: z.string(),
    role: z.string().default(""),
  })
  .loose();

export const SquadSchema = z
  .object({
    id: z.string(),
    workspace_id: z.string(),
    name: z.string(),
    description: z.string().default(""),
    instructions: z.string().default(""),
    system_key: z.string().optional(),
    system_instructions: z.string().optional(),
    avatar_url: z
      .string()
      .nullable()
      .optional()
      .transform((v) => v ?? null),
    leader_id: z.string(),
    creator_id: z.string(),
    created_at: z.string(),
    updated_at: z.string(),
    archived_at: z
      .string()
      .nullable()
      .optional()
      .transform((v) => v ?? null),
    archived_by: z
      .string()
      .nullable()
      .optional()
      .transform((v) => v ?? null),
    member_count: z.number().default(0),
    member_preview: z.array(SquadMemberPreviewSchema).default([]),
  })
  .loose();

export const SquadListSchema = z.array(SquadSchema);
export const EMPTY_SQUAD_LIST: Squad[] = [];
export const EMPTY_SQUAD: Squad = {
  id: "",
  workspace_id: "",
  name: "",
  description: "",
  instructions: "",
  avatar_url: null,
  leader_id: "",
  creator_id: "",
  created_at: "",
  updated_at: "",
  archived_at: null,
  archived_by: null,
  member_count: 0,
  member_preview: [],
};

// Squad member status — backs the Squad detail page's Members tab. status
// is `string | null` (not the narrow `SquadMemberStatusValue` union) so a
// new server-side status doesn't fail the parse; the UI defaults to a
// neutral pill for unknown values.
const SquadActiveIssueBriefSchema = z
  .object({
    issue_id: z.string(),
    identifier: z.string(),
    title: z.string(),
    issue_status: z.string(),
  })
  .loose();

const SquadMemberStatusSchema = z
  .object({
    member_type: z.string(),
    member_id: z.string(),
    status: z
      .string()
      .nullable()
      .optional()
      .transform((v) => v ?? null),
    active_issues: z.array(SquadActiveIssueBriefSchema).default([]),
    last_active_at: z
      .string()
      .nullable()
      .optional()
      .transform((v) => v ?? null),
  })
  .loose();

export const SquadMemberStatusListResponseSchema = z
  .object({
    members: z.array(SquadMemberStatusSchema).default([]),
  })
  .loose();

export const EMPTY_SQUAD_MEMBER_STATUS_LIST = { members: [] };

// ---------------------------------------------------------------------------
// Structured error body — POST /api/workspaces/:wsId/issues 409 conflict.
//
// When the server detects an active issue with the same title in the same
// workspace, it returns `{ code: "active_duplicate_issue", error, issue }`
// instead of letting the create through. The UI uses the embedded issue ref
// to offer "view existing" rather than dropping the user into a generic
// "create failed" toast.
//
// Strict guarantees:
//   - `code` is a literal so a future server rename (e.g. `duplicate_issue`)
//     fails the parse and falls back to a normal error toast — drift never
//     ships as a broken duplicate UI.
//   - `issue` is required; without an id/identifier/title the "view existing"
//     button has nothing to point at, so we'd rather fall back than guess.
//   - `issue.status` is intentionally OMITTED: the duplicate toast doesn't
//     render a StatusIcon (which has no fallback for unknown enum values),
//     so a future server-side rename of `status` must not knock this branch
//     out. `.loose()` lets the field pass through unchanged for any other
//     consumer.
// ---------------------------------------------------------------------------

export const DuplicateIssueErrorBodySchema = z
  .object({
    code: z.literal("active_duplicate_issue"),
    error: z.string().optional(),
    issue: z
      .object({
        id: z.string(),
        identifier: z.string(),
        title: z.string(),
      })
      .loose(),
  })
  .loose();

export interface DuplicateIssueErrorBody {
  code: "active_duplicate_issue";
  error?: string;
  issue: {
    id: string;
    identifier: string;
    title: string;
  };
}

// ---------------------------------------------------------------------------
// Webhook delivery schemas — backing the Autopilot Deliveries section. Enums
// (`status`, `signature_status`, `provider`) are kept as `z.string()` so a
// future server-side value (e.g. a Stripe provider, a new dedupe state)
// degrades to a generic UI fallback rather than collapsing the list into
// the empty array. `.loose()` lets unknown fields pass through, matching
// the rule used by every other endpoint here.
// ---------------------------------------------------------------------------

const WebhookDeliverySchema = z
  .object({
    id: z.string(),
    workspace_id: z.string(),
    autopilot_id: z.string(),
    trigger_id: z.string(),
    provider: z.string(),
    event: z.string(),
    dedupe_key: z.string().nullable(),
    dedupe_source: z.string().nullable(),
    signature_status: z.string(),
    status: z.string(),
    attempt_count: z.number().default(0),
    // Older servers predate the durable dispatch queue. Defaults preserve
    // compatibility while the UI rolls out alongside the new worker.
    dispatch_attempts: z.number().default(0),
    available_at: z.string().default(""),
    content_type: z.string().nullable(),
    response_status: z.number().nullable(),
    autopilot_run_id: z.string().nullable(),
    replayed_from_delivery_id: z.string().nullable(),
    error: z.string().nullable(),
    received_at: z.string(),
    last_attempt_at: z.string(),
    created_at: z.string(),
    // Detail-only fields. The list endpoint omits them; the detail endpoint
    // populates raw_body / selected_headers / response_body.
    selected_headers: z.record(z.string(), z.unknown()).nullable().optional(),
    raw_body: z.string().nullable().optional(),
    response_body: z.string().nullable().optional(),
  })
  .loose();

export const ListWebhookDeliveriesResponseSchema = z
  .object({
    deliveries: z.array(WebhookDeliverySchema).default([]),
    total: z.number().default(0),
  })
  .loose();

export const WebhookDeliveryResponseSchema = WebhookDeliverySchema;

export const EMPTY_LIST_WEBHOOK_DELIVERIES_RESPONSE: ListWebhookDeliveriesResponse =
  {
    deliveries: [],
    total: 0,
  };

// ---------------------------------------------------------------------------
// Autopilot list schema. Enums (`status`, `execution_mode`, `trigger_kinds`,
// `last_run_status`) stay `z.string()` so future server-side values degrade
// to a generic UI fallback. The three derived fields (trigger_kinds /
// next_run_at / last_run_status) are list-endpoint-only and absent on older
// servers — optional by contract, the list renders "—" without them.
// ---------------------------------------------------------------------------

const AutopilotListItemSchema = z
  .object({
    id: z.string(),
    workspace_id: z.string(),
    title: z.string(),
    description: z.string().nullable().optional(),
    project_id: z.string().nullable().optional(),
    // Older servers (pre-MUL-2429) omit assignee_type; "agent" is the
    // documented default.
    assignee_type: z.string().default("agent"),
    assignee_id: z.string(),
    status: z.string(),
    execution_mode: z.string(),
    issue_title_template: z.string().nullable().optional(),
    created_by_type: z.string(),
    created_by_id: z.string(),
    last_run_at: z.string().nullable().optional(),
    created_at: z.string(),
    updated_at: z.string(),
    trigger_kinds: z.array(z.string()).optional(),
    next_run_at: z.string().nullable().optional(),
    last_run_status: z.string().nullable().optional(),
    // Per-caller write capability; absent on older servers (treated as unknown).
    can_write: z.boolean().optional(),
    // Narrower per-caller access-management capability (detail endpoint only).
    can_manage_access: z.boolean().optional(),
  })
  .loose();

export const AutopilotTriggerSchema = z.object({
  id: z.string().min(1), autopilot_id: z.string().min(1), kind: z.string(),
  enabled: z.boolean().default(false),
  merge_interval_minutes: z.number().int().min(1).max(1440).nullable().optional(),
  cron_expression: z.string().nullable().default(null), timezone: z.string().nullable().default(null),
  next_run_at: z.string().nullable().default(null), webhook_token: z.string().nullable().default(null),
  label: z.string().nullable().default(null), last_fired_at: z.string().nullable().default(null),
  created_at: z.string().default(""), updated_at: z.string().default(""),
}).loose();

export const GetAutopilotResponseSchema = z.object({
  autopilot: AutopilotListItemSchema,
  triggers: z.array(AutopilotTriggerSchema).default([]),
}).loose();

export const FALLBACK_AUTOPILOT_TRIGGER = {
  id: "", autopilot_id: "", kind: "api" as const, enabled: false,
  cron_expression: null, timezone: null, next_run_at: null, webhook_token: null,
  label: null, last_fired_at: null, created_at: "", updated_at: "",
};

export const FALLBACK_GET_AUTOPILOT_RESPONSE = {
  autopilot: {
    id: "", workspace_id: "", title: "", description: null, assignee_type: "agent" as const,
    assignee_id: "", status: "paused" as const, execution_mode: "run_only" as const,
    issue_title_template: null, created_by_type: "member", created_by_id: "", last_run_at: null,
    created_at: "", updated_at: "", can_write: false, can_manage_access: false,
  },
  triggers: [],
};

export const ListAutopilotsResponseSchema = z
  .object({
    autopilots: z.array(AutopilotListItemSchema).default([]),
    total: z.number().default(0),
  })
  .loose();

export const EMPTY_LIST_AUTOPILOTS_RESPONSE = {
  autopilots: [],
  total: 0,
};

// Autopilot run (POST /trigger, GET /runs). Consumed by the "run now" flow,
// which branches on `status` to avoid a false-success toast (MUL-4525), so the
// response must be schema-parsed. `reason_code` is an additive, stable
// classification of a non-success run the UI localizes; older servers omit it.
// Defaults are conservative: an unreadable run degrades to a non-success status
// so the UI never shows success it cannot confirm. .loose() tolerates new fields.
export const AutopilotRunSchema = z
  .object({
    id: z.string().default(""),
    autopilot_id: z.string().default(""),
    trigger_id: z.string().nullable().default(null),
    source: z.string().default("manual"),
    status: z.string().default("failed"),
    issue_id: z.string().nullable().default(null),
    task_id: z.string().nullable().default(null),
    triggered_at: z.string().default(""),
    completed_at: z.string().nullable().default(null),
    failure_reason: z.string().nullable().default(null),
    reason_code: z.string().optional(),
    trigger_payload: z.unknown().default(null),
    result: z.unknown().default(null),
    created_at: z.string().default(""),
  })
  .loose();

export const FALLBACK_AUTOPILOT_RUN: AutopilotRun = {
  id: "",
  autopilot_id: "",
  trigger_id: null,
  source: "manual",
  status: "failed",
  issue_id: null,
  task_id: null,
  triggered_at: "",
  completed_at: null,
  failure_reason: null,
  trigger_payload: null,
  result: null,
  created_at: "",
};

// Cron preview: the server is the authority on the next occurrences. No
// `.default([])` here — a missing or reshaped field must fail validation so it
// degrades to the `next_runs: null` fallback ("preview unreadable") instead of
// masquerading as a valid empty list ("this expression never fires").
export const CronPreviewResponseSchema = z
  .object({
    next_runs: z.array(z.string()),
  })
  .loose();

export const UNREADABLE_CRON_PREVIEW_RESPONSE: CronPreviewResponse = {
  next_runs: null,
};

export const EMPTY_WEBHOOK_DELIVERY: WebhookDelivery = {
  id: "",
  workspace_id: "",
  autopilot_id: "",
  trigger_id: "",
  provider: "",
  event: "",
  dedupe_key: null,
  dedupe_source: null,
  signature_status: "not_required",
  status: "queued",
  attempt_count: 0,
  dispatch_attempts: 0,
  available_at: "",
  content_type: null,
  response_status: null,
  autopilot_run_id: null,
  replayed_from_delivery_id: null,
  error: null,
  received_at: "",
  last_attempt_at: "",
  created_at: "",
};

// ---------------------------------------------------------------------------
// User (`/api/me` GET + PATCH). The auth store and Settings → Account both
// trust this shape — a drift here would knock both surfaces out. Kept
// lenient by the same rules as IssueSchema: enums stay `z.string()`,
// nullable fields are unioned with `null`, unknown server fields pass
// through via `.loose()`. `profile_description` is the field added in
// MUL-2406; the server emits `""` when unset (NOT NULL DEFAULT ''), so
// the schema defaults to `""` too — keeps the type tight without
// breaking older backends that don't return the column yet.
// ---------------------------------------------------------------------------

export const UserSchema = z
  .object({
    id: z.string(),
    name: z.string().default(""),
    email: z.string().default(""),
    avatar_url: z.string().nullable().default(null),
    onboarded_at: z.string().nullable().default(null),
    onboarding_questionnaire: z.record(z.string(), z.unknown()).default({}),
    starter_content_state: z.string().nullable().default(null),
    language: z.string().nullable().default(null),
    profile_description: z.string().default(""),
    timezone: z.string().nullable().default(null),
    created_at: z.string().default(""),
    updated_at: z.string().default(""),
  })
  .loose();

export const EMPTY_USER: User = {
  id: "",
  name: "",
  email: "",
  avatar_url: null,
  onboarded_at: null,
  onboarding_questionnaire: {},
  starter_content_state: null,
  language: null,
  profile_description: "",
  timezone: null,
  created_at: "",
  updated_at: "",
};

const DingTalkUserSchema = z
  .object({
    user_id: z.string(),
    union_id: z.string().optional(),
    name: z.string().default(""),
    avatar_url: z
      .string()
      .nullable()
      .optional()
      .transform((v) => v ?? null),
    mobile: z.string().optional(),
    title: z.string().optional(),
    email: z.string().optional(),
    department_ids: z.array(z.number()).default([]),
  })
  .loose();

export const DingTalkUserSearchResponseSchema = z
  .object({
    users: z.array(DingTalkUserSchema).default([]),
  })
  .loose();

export const EMPTY_DINGTALK_USER_SEARCH_RESPONSE: DingTalkUserSearchResponse = {
  users: [],
};

export const AddDingTalkGroupMembersResponseSchema = z
  .object({
    added_user_ids: z.array(z.string()).default([]),
  })
  .loose();

export const EMPTY_ADD_DINGTALK_GROUP_MEMBERS_RESPONSE: AddDingTalkGroupMembersResponse =
  {
    added_user_ids: [],
  };

const DingTalkWorkspaceMemberSchema = z
  .object({
    id: z.string(),
    workspace_id: z.string(),
    user_id: z.string(),
    role: z.enum(["owner", "admin", "member"]),
    created_at: z.string(),
    name: z.string().default(""),
    email: z.string().default(""),
    avatar_url: z.string().nullable().default(null),
  })
  .loose();

export const AddDingTalkWorkspaceMembersResponseSchema = z
  .object({
    members: z.array(DingTalkWorkspaceMemberSchema).default([]),
    added_count: z.number().default(0),
    already_member_count: z.number().default(0),
    unresolved_user_ids: z.array(z.string()).default([]),
  })
  .loose();

export const EMPTY_ADD_DINGTALK_WORKSPACE_MEMBERS_RESPONSE: AddDingTalkWorkspaceMembersResponse =
  {
    members: [],
    added_count: 0,
    already_member_count: 0,
    unresolved_user_ids: [],
  };

// ---------------------------------------------------------------------------
// Cross-workspace unread inbox summary (`/api/inbox/unread-summary` GET).
// One entry per workspace the user belongs to that has unread items; the
// sidebar derives the workspace-switcher dot from it. Lenient per the usual
// rules so a future field addition can't blank the dot — on malformed JSON
// parseWithFallback returns the empty list, which simply hides the dot.
// ---------------------------------------------------------------------------

export const InboxUnreadSummarySchema = z.array(
  z
    .object({
      workspace_id: z.string(),
      count: z.number(),
    })
    .loose(),
);

export const EMPTY_INBOX_UNREAD_SUMMARY: InboxWorkspaceUnread[] = [];

// ---------------------------------------------------------------------------
// Archived inbox items (`/api/inbox/archived` GET).
// Lenient per the usual rules: `severity` / `type` / `recipient_type` stay
// `z.string()` so a notification kind this client doesn't know yet still
// parses and renders (the UI's type-label lookup already tolerates unknown
// kinds). Nullable optional fields are declared optional as well, since older
// rows can omit them entirely. On malformed JSON parseWithFallback returns the
// empty list — the archived view then reads as empty rather than white-
// screening the inbox.
// ---------------------------------------------------------------------------

export const InboxItemListSchema = z.array(
  z
    .object({
      id: z.string(),
      workspace_id: z.string(),
      recipient_type: z.string(),
      recipient_id: z.string(),
      type: z.string(),
      severity: z.string(),
      issue_id: z.string().nullish(),
      title: z.string(),
      body: z.string().nullish(),
      read: z.boolean(),
      archived: z.boolean(),
      created_at: z.string(),
    })
    .loose(),
);

export const EMPTY_INBOX_ITEMS: InboxItem[] = [];

// ---------------------------------------------------------------------------
// Billing schemas (cloud-billing proxy surface)
//
// All billing JSON we receive comes from multica-cloud verbatim — we proxy
// the bytes without re-shaping. These schemas use `loose()` so a future
// non-breaking field addition on the cloud side doesn't crash us; required
// fields are still strictly enforced. EMPTY_* constants supply the
// fallback parseWithFallback uses when the upstream response is malformed
// or unparseable.

export const BillingBalanceSchema = z
  .object({
    owner_id: z.string(),
    balance_micro: z.number(),
    balance_credit: z.number(),
    updated_at: z.string(),
  })
  .loose();

export const EMPTY_BILLING_BALANCE: BillingBalance = {
  owner_id: "",
  balance_micro: 0,
  balance_credit: 0,
  updated_at: "",
};

// `tx_type` and `source` are kept as plain strings here; the cloud doc
// enumerates the canonical values but the frontend display tolerates
// unknown ones gracefully. Strict enums would crash the page on a future
// addition (e.g. a new `topup` source kind).
export const BillingTransactionSchema = z
  .object({
    id: z.string(),
    owner_id: z.string(),
    idempotency_key: z.string().default(""),
    tx_type: z.string(),
    source: z.string(),
    amount_micro: z.number(),
    balance_after: z.number(),
    reference_id: z.string().default(""),
    description: z.string().default(""),
    metadata: z.record(z.string(), z.unknown()).default({}),
    created_at: z.string(),
  })
  .loose();

export const BillingTransactionsPageSchema = z
  .object({
    items: z.array(BillingTransactionSchema).default([]),
    total: z.number().default(0),
    page: z.number().default(1),
    page_size: z.number().default(20),
  })
  .loose();

export const EMPTY_BILLING_TRANSACTIONS_PAGE: BillingTransactionsPage = {
  items: [],
  total: 0,
  page: 1,
  page_size: 20,
};

export const BillingBatchSchema = z
  .object({
    id: z.string(),
    owner_id: z.string(),
    source_tx_id: z.string().default(""),
    source_type: z.string(),
    total_micro: z.number(),
    remaining_micro: z.number(),
    // Cloud either omits the key (never expires) or sends a string
    // timestamp. Null is also tolerated since some serializers emit
    // explicit nulls for absent timestamps.
    expires_at: z.string().nullable().optional(),
    created_at: z.string(),
    updated_at: z.string(),
  })
  .loose();

export const BillingBatchesPageSchema = z
  .object({
    items: z.array(BillingBatchSchema).default([]),
    total: z.number().default(0),
    page: z.number().default(1),
    page_size: z.number().default(20),
  })
  .loose();

export const EMPTY_BILLING_BATCHES_PAGE: BillingBatchesPage = {
  items: [],
  total: 0,
  page: 1,
  page_size: 20,
};

export const BillingTopupSchema = z
  .object({
    id: z.string(),
    owner_id: z.string(),
    amount_cents: z.number(),
    currency: z.string().default("usd"),
    credits: z.number(),
    bonus_credits: z.number().default(0),
    status: z.string(),
    tier_id: z.string().default(""),
    stripe_checkout_id: z.string().default(""),
    // Only set after status reaches `credited` — leave optional rather
    // than coerce to "" so a UI can branch on existence.
    purchase_batch_id: z.string().optional(),
    created_at: z.string(),
    updated_at: z.string(),
  })
  .loose();

export const BillingTopupsPageSchema = z
  .object({
    items: z.array(BillingTopupSchema).default([]),
    total: z.number().default(0),
    page: z.number().default(1),
    page_size: z.number().default(20),
  })
  .loose();

export const EMPTY_BILLING_TOPUPS_PAGE: BillingTopupsPage = {
  items: [],
  total: 0,
  page: 1,
  page_size: 20,
};

export const BillingPriceTierSchema = z
  .object({
    id: z.string(),
    // Cloud doc says display_name falls back to id; tolerate empty too.
    display_name: z.string().default(""),
    amount_cents: z.number(),
    credits: z.number(),
    bonus_credits: z.number().optional(),
    bonus_expires_in: z.string().optional(),
  })
  .loose();

export const BillingPriceTierListSchema = z.array(BillingPriceTierSchema);

export const EMPTY_BILLING_PRICE_TIER_LIST: BillingPriceTier[] = [];

export const CreateBillingCheckoutSessionResponseSchema = z
  .object({
    order_id: z.string(),
    session_id: z.string(),
    url: z.string(),
  })
  .loose();

export const EMPTY_CREATE_BILLING_CHECKOUT_SESSION_RESPONSE: CreateBillingCheckoutSessionResponse =
  {
    order_id: "",
    session_id: "",
    url: "",
  };

export const BillingCheckoutSessionStatusSchema = z
  .object({
    order_id: z.string(),
    status: z.string(),
    amount_cents: z.number(),
    credits: z.number(),
    bonus_credits: z.number().default(0),
    currency: z.string().default("usd"),
    tier_id: z.string().default(""),
  })
  .loose();

export const EMPTY_BILLING_CHECKOUT_SESSION_STATUS: BillingCheckoutSessionStatus =
  {
    order_id: "",
    status: "pending",
    amount_cents: 0,
    credits: 0,
    bonus_credits: 0,
    currency: "usd",
    tier_id: "",
  };

export const CreateBillingPortalSessionResponseSchema = z
  .object({
    url: z.string(),
  })
  .loose();

export const EMPTY_CREATE_BILLING_PORTAL_SESSION_RESPONSE: CreateBillingPortalSessionResponse =
  {
    url: "",
  };

export const WorkspaceAccessTokenSchema = z
  .object({
    id: z.string(),
    workspace_id: z.string(),
    name: z.string(),
    version: z.number(),
    token_prefix: z.string(),
    expires_at: z.string().nullable(),
    last_used_at: z.string().nullable(),
    created_at: z.string(),
    updated_at: z.string(),
    revoked_at: z.string().nullable(),
  })
  .loose();

export const WorkspaceAccessTokenListSchema = z.array(
  WorkspaceAccessTokenSchema,
);
export const WorkspaceAccessTokenSecretResponseSchema =
  WorkspaceAccessTokenSchema.extend({
    token: z.string().min(1),
  });

export const EMPTY_WORKSPACE_ACCESS_TOKEN: WorkspaceAccessToken = {
  id: "",
  workspace_id: "",
  name: "",
  version: 0,
  token_prefix: "",
  expires_at: null,
  last_used_at: null,
  created_at: "",
  updated_at: "",
  revoked_at: null,
};

export const EMPTY_WORKSPACE_ACCESS_TOKEN_SECRET_RESPONSE: WorkspaceAccessTokenSecretResponse =
  {
    ...EMPTY_WORKSPACE_ACCESS_TOKEN,
    token: "",
  };
// ---------------------------------------------------------------------------
// Runtime model discovery (`POST /api/runtimes/:id/models`,
// `GET /api/runtimes/:id/models/:requestId`). Both endpoints return the same
// request record, and the UI drives a state machine off `status`, so the two
// fields that decide behaviour are pinned: `status` gates the polling loop and
// `supported` gates whether the picker is usable at all. Everything else stays
// lenient per the rules at the top of this file.
//
// `status` deliberately stays `z.string()` (a newer server may add a state);
// `resolveRuntimeModels` treats anything it does not recognise as an explicit
// failure rather than a completed-but-empty catalog. `supported` defaults to
// true so a server old enough to omit it keeps the picker enabled instead of
// rendering "managed by runtime" off an `undefined`.
//
// `cached` / `cached_at` are additive markers for a snapshot served from the
// server-side catalog cache (MUL-5444); an older backend omits them.
// ---------------------------------------------------------------------------

const RuntimeModelThinkingLevelSchema = z
  .object({
    value: z.string(),
    label: z.string().default(""),
    description: z.string().optional(),
  })
  .loose();

const RuntimeModelThinkingSchema = z
  .object({
    supported_levels: z.array(RuntimeModelThinkingLevelSchema).default([]),
    default_level: z.string().optional(),
  })
  .loose();

const RuntimeModelServiceTierSchema = z
  .object({
    id: z.string(),
    name: z.string().default(""),
    description: z.string().optional(),
  })
  .loose();

// A model entry with no `id` is unselectable — `onChange(m.id)` would persist
// an empty model — so `id` is required and a malformed entry drops the whole
// response to the fallback rather than rendering a dead row.
const RuntimeModelSchema = z
  .object({
    id: z.string(),
    label: z.string().default(""),
    provider: z.string().optional(),
    default: z.boolean().optional(),
    pricing: z
      .object({
        input: z.number().nonnegative(),
        output: z.number().nonnegative(),
        cache_read: z.number().nonnegative(),
        cache_write: z.number().nonnegative(),
        base_tier_max_input_tokens: z.number().int().positive().optional(),
      })
      .optional()
      .catch(undefined),
    thinking: RuntimeModelThinkingSchema.nullable()
      .optional()
      .transform((v) => v ?? undefined),
    service_tiers: z.array(RuntimeModelServiceTierSchema).optional(),
  })
  .loose();

export const RuntimeModelListRequestSchema = z
  .object({
    id: z.string().default(""),
    runtime_id: z.string().default(""),
    status: z.string(),
    models: z.array(RuntimeModelSchema).optional(),
    supported: z.boolean().default(true),
    error: z.string().optional(),
    created_at: z.string().default(""),
    updated_at: z.string().default(""),
    cached: z.boolean().optional(),
    cached_at: z.string().optional(),
  })
  .loose();

// Fallback for an unparseable model-discovery response. `failed` is the only
// honest choice: `completed` would fabricate an empty catalog (and silently
// clear a saved model when `supported` is read as false), while `pending`
// would spin the picker until the client-side poll timeout. `failed` surfaces
// "discovery failed" immediately and leaves the creatable manual-entry field
// working, which is the same degradation as a real discovery failure.
export const MALFORMED_RUNTIME_MODEL_LIST_REQUEST: RuntimeModelListRequest = {
  id: "",
  runtime_id: "",
  status: "failed",
  supported: true,
  error: "invalid model discovery response",
  created_at: "",
  updated_at: "",
};

export const AgentA2ACardSkillSchema = z.object({
  id: z.string(),
  name: z.string(),
  description: z.string(),
  tags: z.array(z.string()),
  examples: z.array(z.string()).optional(),
  inputModes: z.array(z.string()).optional(),
  outputModes: z.array(z.string()).optional(),
  securityRequirements: z.array(
    z.object({
      schemes: z.record(z.string(), z.array(z.string())),
    }).loose(),
  ).optional(),
}).loose();

const AgentA2ASupportedInterfaceSchema = z.object({
  url: z.string(),
  protocolBinding: z.string(),
  protocolVersion: z.string(),
  tenant: z.string().optional(),
}).loose();

const AgentA2ACapabilitiesSchema = z.object({
  streaming: z.boolean().optional(),
  pushNotifications: z.boolean().optional(),
  extendedAgentCard: z.boolean().optional(),
  extensions: z.array(z.unknown()).optional(),
}).loose();

export const AgentA2AAgentCardSchema = z.object({
  name: z.string(),
  description: z.string(),
  supportedInterfaces: z.array(AgentA2ASupportedInterfaceSchema),
  version: z.string(),
  capabilities: AgentA2ACapabilitiesSchema,
  securitySchemes: z.record(z.string(), z.unknown()).optional(),
  securityRequirements: z.array(
    z.object({
      schemes: z.record(z.string(), z.array(z.string())),
    }).loose(),
  ).optional(),
  defaultInputModes: z.array(z.string()),
  defaultOutputModes: z.array(z.string()),
  skills: z.array(AgentA2ACardSkillSchema),
}).loose();

export const AgentA2AEndpointSchema = z.object({
  id: z.string().optional(),
  agent_id: z.string().optional(),
  public_agent_id: z.string(),
  enabled: z.boolean(),
  delegated_by_user_id: z.string().optional(),
  card_name: z.string(),
  card_description: z.string(),
  card_version: z.string(),
  card_skills: z.array(AgentA2ACardSkillSchema),
  card_url: z.string(),
  rpc_url: z.string(),
  mcp_url: z.string().optional().default(""),
  protocol_version: z.string().optional().default("1.0"),
  created_at: z.string().optional(),
  updated_at: z.string().optional(),
}).loose()
  .transform((endpoint) => ({
    enabled: endpoint.enabled,
    publicAgentId: endpoint.public_agent_id,
    cardName: endpoint.card_name,
    cardDescription: endpoint.card_description,
    cardVersion: endpoint.card_version,
    cardSkills: endpoint.card_skills,
    cardUrl: endpoint.card_url,
    rpcUrl: endpoint.rpc_url,
    mcpUrl: endpoint.mcp_url,
    protocolVersion: endpoint.protocol_version,
    ...(endpoint.id !== undefined ? { id: endpoint.id } : {}),
    ...(endpoint.agent_id !== undefined ? { agentId: endpoint.agent_id } : {}),
    ...(endpoint.delegated_by_user_id !== undefined
      ? { delegatedByUserId: endpoint.delegated_by_user_id }
      : {}),
    ...(endpoint.created_at !== undefined ? { createdAt: endpoint.created_at } : {}),
    ...(endpoint.updated_at !== undefined ? { updatedAt: endpoint.updated_at } : {}),
  }));

const AgentA2ACredentialWireSchema = z.object({
  id: z.string(),
  key_id: z.string(),
  token_prefix: z.string(),
  status: z.enum(["active", "revoked"]),
  expires_at: z.string().nullable().optional().default(null),
  last_used_at: z.string().nullable().optional().default(null),
  revoked_at: z.string().nullable().optional().default(null),
  created_at: z.string(),
  updated_at: z.string().optional(),
});

function toAgentA2ACredential(
  credential: z.infer<typeof AgentA2ACredentialWireSchema>,
): AgentA2ACredential {
  return {
    id: credential.id,
    keyId: credential.key_id,
    tokenPrefix: credential.token_prefix,
    status: credential.status,
    expiresAt: credential.expires_at,
    lastUsedAt: credential.last_used_at,
    revokedAt: credential.revoked_at,
    createdAt: credential.created_at,
    updatedAt: credential.updated_at ?? credential.created_at,
  };
}

export const AgentA2ACredentialSchema = AgentA2ACredentialWireSchema
  .transform(toAgentA2ACredential);

export const AgentA2AClientSchema = z.object({
  id: z.string(),
  name: z.string(),
  status: z.enum(["active", "disabled", "revoked"]),
  scopes: z.array(z.enum(["send", "read", "list", "cancel"])),
  rate_limit_per_minute: z.number().int().positive().nullable().optional().default(null),
  max_concurrent_tasks: z.number().int().positive().nullable().optional().default(null),
  credentials: z.array(AgentA2ACredentialSchema).optional().default([]),
  created_at: z.string(),
  updated_at: z.string(),
  revoked_at: z.string().nullable().optional().default(null),
}).loose().transform((client) => ({
  id: client.id,
  name: client.name,
  status: client.status,
  scopes: client.scopes,
  rateLimitPerMinute: client.rate_limit_per_minute,
  maxConcurrentTasks: client.max_concurrent_tasks,
  credentials: client.credentials,
  createdAt: client.created_at,
  updatedAt: client.updated_at,
  revokedAt: client.revoked_at,
}));

export const AgentA2AConfigSchema = z.object({
  endpoint: AgentA2AEndpointSchema.nullable(),
  agent_card: AgentA2AAgentCardSchema.nullable(),
  clients: z.array(AgentA2AClientSchema),
}).loose().transform((config) => ({
  endpoint: config.endpoint,
  agentCard: config.agent_card,
  clients: config.clients,
}));

export const AgentA2ACredentialSecretResponseSchema = z.union([
  z.object({
    credential: AgentA2ACredentialWireSchema,
    token: z.string().min(1),
  }),
  AgentA2ACredentialWireSchema.extend({
    token: z.string().min(1),
  }),
]).transform((response): AgentA2ACredentialSecretResponse => {
  if ("credential" in response) {
    return {
      credential: toAgentA2ACredential(response.credential),
      token: response.token,
    };
  }
  return {
    credential: toAgentA2ACredential(response),
    token: response.token,
  };
});

export const EMPTY_AGENT_A2A_CREDENTIAL: AgentA2ACredential = {
  id: "",
  keyId: "",
  tokenPrefix: "",
  status: "revoked",
  expiresAt: null,
  lastUsedAt: null,
  revokedAt: null,
  createdAt: "",
  updatedAt: "",
};

export const EMPTY_AGENT_A2A_CLIENT: AgentA2AClient = {
  id: "",
  name: "",
  status: "revoked",
  scopes: [],
  rateLimitPerMinute: null,
  maxConcurrentTasks: null,
  credentials: [],
  createdAt: "",
  updatedAt: "",
  revokedAt: null,
};

export const EMPTY_AGENT_A2A_CONFIG: AgentA2AConfig = {
  endpoint: null,
  agentCard: null,
  clients: [],
};

export const EMPTY_AGENT_A2A_CREDENTIAL_SECRET_RESPONSE: AgentA2ACredentialSecretResponse = {
  credential: EMPTY_AGENT_A2A_CREDENTIAL,
  token: "",
};
export const DingTalkInstallationSchema = z
  .object({
    id: z.string(),
    workspace_id: z.string(),
    agent_id: z.string(),
    client_id: z.string(),
    robot_code: z.string().optional(),
    installer_user_id: z.string(),
    status: z.string(),
    installed_at: z.string(),
    created_at: z.string(),
    updated_at: z.string(),
    transport_mode: z.enum(["STREAM", "HTTP_CALLBACK"]).optional(),
    connection_managed: z.boolean().optional(),
    router_status: z.string().optional(),
    router_last_error: z.string().optional(),
    registration_status: z.string().optional(),
  })
  .loose();

export const ListDingTalkInstallationsResponseSchema = z
  .object({
    installations: z.array(DingTalkInstallationSchema),
    configured: z.boolean(),
    install_supported: z.boolean().optional(),
    capabilities: z
      .object({
        http_callback: z
          .object({
            available: z.boolean(),
            reason: z.string().optional(),
          })
          .loose(),
      })
      .loose()
      .optional(),
  })
  .loose();

// WeCom smart-bot ("智能机器人" / aibot) installation responses. `.loose()` so a
// newer backend field never fails the parse on an older desktop build (see
// CLAUDE.md → API Compatibility). Defaults are chosen so a malformed response
// degrades safely: `configured` defaults false (renders the "ask your operator"
// state rather than a Connect dialog whose submit is guaranteed to fail), and a
// missing `status` defaults to "revoked" rather than "active" so a broken read
// never shows a bot as connected when it may not be.
export const WecomInstallationSchema = z
  .object({
    id: z.string(),
    workspace_id: z.string().default(""),
    agent_id: z.string().default(""),
    bot_id: z.string().default(""),
    installer_user_id: z.string().default(""),
    status: z.string().default("revoked"),
  })
  .loose();

export const EMPTY_WECOM_INSTALLATION: WecomInstallation = {
  id: "",
  workspace_id: "",
  agent_id: "",
  bot_id: "",
  installer_user_id: "",
  status: "revoked",
};

export const ListWecomInstallationsResponseSchema = z
  .object({
    installations: z.array(WecomInstallationSchema).default([]),
    configured: z.boolean().default(false),
    install_supported: z.boolean().optional(),
  })
  .loose();

export const EMPTY_LIST_WECOM_INSTALLATIONS_RESPONSE: ListWecomInstallationsResponse =
  {
    installations: [],
    configured: false,
  };

export const RedeemWecomBindingTokenResponseSchema = z
  .object({
    workspace_id: z.string().default(""),
    installation_id: z.string().default(""),
    wecom_user_id: z.string().default(""),
  })
  .loose();

export const EMPTY_REDEEM_WECOM_BINDING_TOKEN_RESPONSE: RedeemWecomBindingTokenResponse =
  {
    workspace_id: "",
    installation_id: "",
    wecom_user_id: "",
  };

// ---------------------------------------------------------------------------
// DSH plugins
//
// The server speaks npm's vocabulary because DeepSeek Harness does: a plugin is
// a package reference plus the loader rows its own bundle patch declares.
// Enums stay `z.string()` so an unknown source kind still parses.
// ---------------------------------------------------------------------------

const DshPluginBaseSchema = z
  .object({
    id: z.string(),
    workspace_id: z.string(),
    package_name: z.string(),
    display_name: z.string().optional(),
    description: z.string().optional(),
    homepage: z.string().optional(),
    source_kind: z.string().optional(),
    source_spec: z.string().optional(),
    resolved_version: z.string().optional(),
    integrity: z.string().optional(),
    bundle_rows: z.array(z.string()).optional(),
    config_row: z.string().optional(),
    config: z.record(z.string(), z.unknown()).optional(),
    catalog: z.string().optional(),
    validated_dsh_version: z.string().optional(),
    created_by: z.string().nullable().optional(),
    created_at: z.string().optional(),
    updated_at: z.string().optional(),
    enabled: z.boolean().optional(),
  })
  .loose();

function toDshPlugin(row: z.infer<typeof DshPluginBaseSchema>): DshPlugin {
  return {
    id: row.id,
    workspaceId: row.workspace_id,
    packageName: row.package_name,
    displayName: row.display_name ?? row.package_name,
    description: row.description ?? "",
    homepage: row.homepage ?? "",
    sourceKind: (row.source_kind ?? "npm") as DshPluginSourceKind,
    sourceSpec: row.source_spec ?? "",
    resolvedVersion: row.resolved_version ?? "",
    integrity: row.integrity ?? "",
    bundleRows: row.bundle_rows ?? [],
    configRow: row.config_row ?? "",
    config: row.config ?? {},
    catalog: row.catalog ?? "",
    validatedDshVersion: row.validated_dsh_version ?? "",
    createdBy: row.created_by ?? null,
    createdAt: row.created_at ?? "",
    updatedAt: row.updated_at ?? "",
  };
}

export const DshPluginSchema = DshPluginBaseSchema.transform(toDshPlugin);
export const DshPluginListSchema = z.array(DshPluginSchema);

export const AgentDshPluginListSchema = z.array(
  DshPluginBaseSchema.transform(
    (row): AgentDshPlugin => ({ ...toDshPlugin(row), enabled: row.enabled !== false }),
  ),
);

export const DshPluginBindingListSchema = z.array(
  z
    .object({
      agent_id: z.string(),
      dsh_plugin_id: z.string(),
      enabled: z.boolean().optional(),
    })
    .loose()
    .transform(
      (row): DshPluginBinding => ({
        agentId: row.agent_id,
        pluginId: row.dsh_plugin_id,
        enabled: row.enabled !== false,
      }),
    ),
);

const DshPluginCatalogStateSchema = z
  .object({
    catalog: z.string().optional(),
    catalog_version: z.string().optional(),
    entry_count: z.number().optional(),
    refreshed_at: z.string().optional(),
    source_package: z.string().optional(),
    source_repo: z.string().optional(),
    source_site: z.string().optional(),
    license: z.string().optional(),
    official: z.boolean().optional(),
  })
  .loose()
  .transform(
    (row): DshPluginCatalogState => ({
      catalog: row.catalog ?? "",
      catalogVersion: row.catalog_version ?? "",
      entryCount: row.entry_count ?? 0,
      refreshedAt: row.refreshed_at ?? "",
      sourcePackage: row.source_package ?? "",
      sourceRepo: row.source_repo ?? "",
      sourceSite: row.source_site ?? "",
      license: row.license ?? "",
      // Default to false: never imply a community list is official.
      official: row.official === true,
    }),
  );

const DshPluginCatalogEntrySchema = z
  .object({
    name: z.string(),
    owner: z.string().optional(),
    url: z.string().optional(),
    page: z.string().optional(),
    category: z.string().optional(),
    description_en: z.string().optional(),
    description_zh: z.string().optional(),
    npm_package: z.string().optional(),
    npm_version: z.string().optional(),
    stars: z.number().optional(),
    downloads: z.number().optional(),
    added_on: z.string().optional(),
    source_spec: z.string().optional(),
  })
  .loose()
  .transform(
    (row): DshPluginCatalogEntry => ({
      name: row.name,
      owner: row.owner ?? "",
      url: row.url ?? "",
      page: row.page ?? "",
      category: row.category ?? "",
      descriptionEn: row.description_en ?? "",
      descriptionZh: row.description_zh ?? "",
      npmPackage: row.npm_package ?? "",
      npmVersion: row.npm_version ?? "",
      stars: row.stars ?? 0,
      downloads: row.downloads ?? 0,
      addedOn: row.added_on ?? "",
      sourceSpec: row.source_spec ?? "",
    }),
  );

export const DshPluginCatalogPageSchema = z
  .object({
    entries: z.array(DshPluginCatalogEntrySchema).optional(),
    total: z.number().optional(),
    limit: z.number().optional(),
    offset: z.number().optional(),
    state: DshPluginCatalogStateSchema.optional(),
  })
  .loose()
  .transform(
    (row): DshPluginCatalogPage => ({
      entries: row.entries ?? [],
      total: row.total ?? 0,
      limit: row.limit ?? 0,
      offset: row.offset ?? 0,
      state:
        row.state ??
        {
          catalog: "",
          catalogVersion: "",
          entryCount: 0,
          refreshedAt: "",
          sourcePackage: "",
          sourceRepo: "",
          sourceSite: "",
          license: "",
          official: false,
        },
    }),
  );

export const DshPluginCatalogCategoryListSchema = z.array(
  z
    .object({ category: z.string(), entry_count: z.number().optional() })
    .loose()
    .transform(
      (row): DshPluginCatalogCategory => ({
        category: row.category,
        entryCount: row.entry_count ?? 0,
      }),
    ),
);

export const DshPluginRegistrySearchSchema = z
  .object({
    results: z
      .array(
        z
          .object({
            name: z.string(),
            version: z.string().optional(),
            description: z.string().optional(),
            publisher: z.string().optional(),
            links: z.string().optional(),
          })
          .loose()
          .transform(
            (row): DshPluginRegistryResult => ({
              name: row.name,
              version: row.version ?? "",
              description: row.description ?? "",
              publisher: row.publisher ?? "",
              links: row.links ?? "",
            }),
          ),
      )
      .optional(),
  })
  .loose()
  .transform((row): DshPluginRegistryResult[] => row.results ?? []);

export const ImportDshPluginResultSchema = z
  .object({
    status: z.string().optional(),
    plugin: DshPluginSchema.optional(),
    warnings: z.array(z.string()).optional(),
    existing_plugin: DshPluginSchema.optional(),
    error: z.string().optional(),
  })
  .loose()
  .transform(
    (row): ImportDshPluginResult => ({
      status: row.status ?? "",
      plugin: row.plugin ?? null,
      warnings: row.warnings ?? [],
      existingPlugin: row.existing_plugin ?? null,
      error: row.error ?? "",
    }),
  );

export const DshPluginUpdateSchema = z
  .object({
    package_name: z.string().optional(),
    current_version: z.string().optional(),
    latest_version: z.string().optional(),
    update_available: z.boolean().optional(),
    checkable: z.boolean().optional(),
    reason: z.string().optional(),
    source_spec: z.string().optional(),
  })
  .loose()
  .transform(
    (row): DshPluginUpdate => ({
      packageName: row.package_name ?? "",
      currentVersion: row.current_version ?? "",
      latestVersion: row.latest_version ?? "",
      // Default to false: never claim an update exists on a drifted response.
      updateAvailable: row.update_available === true,
      checkable: row.checkable === true,
      reason: row.reason ?? "",
      sourceSpec: row.source_spec ?? "",
    }),
  );

export const DshPluginFileListingSchema = z
  .object({
    package_name: z.string().optional(),
    resolved_version: z.string().optional(),
    files: z
      .array(
        z
          .object({
            path: z.string(),
            size: z.number().optional(),
            viewable: z.boolean().optional(),
          })
          .loose()
          .transform(
            (row): DshPluginFile => ({
              path: row.path,
              size: row.size ?? 0,
              // Default to false: offering to open something the server would
              // refuse is worse than hiding a file that could have been shown.
              viewable: row.viewable === true,
            }),
          ),
      )
      .optional(),
    truncated: z.boolean().optional(),
  })
  .loose()
  .transform(
    (row): DshPluginFileListing => ({
      packageName: row.package_name ?? "",
      resolvedVersion: row.resolved_version ?? "",
      files: row.files ?? [],
      truncated: row.truncated === true,
    }),
  );

export const DshPluginFileContentSchema = z
  .object({
    path: z.string().optional(),
    size: z.number().optional(),
    content: z.string().optional(),
  })
  .loose()
  .transform(
    (row): DshPluginFileContent => ({
      path: row.path ?? "",
      size: row.size ?? 0,
      content: row.content ?? "",
    }),
  );

export const CoordinatorConversationsPageSchema = z
  .object({
    conversations: z
      .array(
        z
          .object({
            id: z.string(),
            session_id: z.string(),
            title: z.string().catch(""),
            conversation_type: z.string().catch(""),
            source: z.string().catch(""),
            session_count: z.number().int().nonnegative().catch(0),
            updated_at: z.string().catch(""),
          })
          .loose(),
      )
      .default([]),
    has_more: z.boolean().catch(false),
    next_offset: z.number().int().nonnegative().catch(0),
  })
  .loose();

// No account UID, organization ID or authorization material leaves the server.
export const ReusableDingTalkIdentitiesSchema = z.object({
  identities: z.array(z.object({
    source_agent_id: z.string().uuid(),
    source_agent_name: z.string(),
    account_display_name: z.string(),
    organization_name: z.string(),
  }).transform((item) => ({
    sourceAgentId: item.source_agent_id,
    sourceAgentName: item.source_agent_name,
    accountDisplayName: item.account_display_name,
    organizationName: item.organization_name,
  }))),
}).transform((response) => response.identities);

export const AgentPackageBindingReportSchema = z.object({
  revision: z.string().min(1),
  bindings: z.array(z.object({
    path: z.string().min(1), status: z.string().min(1),
    declaration: z.unknown().refine((value) => value !== undefined, "required"), current: z.unknown().refine((value) => value !== undefined, "required"),
    current_fingerprint: z.string(), config_tab: z.string().min(1), message: z.string().default(""),
  })),
  resources: z.array(z.object({ ref: z.string().min(1), kind: z.string(), label: z.string() })),
});
