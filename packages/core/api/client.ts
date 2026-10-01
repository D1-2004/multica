import { InternalConnectorListSchema, AvailableInternalConnectorListSchema, SavedInternalConnectorSchema, InternalConnectorTestSchema, type InternalConnector, type AvailableInternalConnector, type InternalConnectorInput, type InternalConnectorTest } from "./internal-connector-schema";
import {
  AddedCatalogConnectorSchema,
  ConnectorAuthorizeUrlSchema,
  InternalConnectorToolsRefreshSchema,
  type InternalConnectorToolsRefresh,
} from "./internal-connector-schema";
import {
  AgentContextCapabilitiesSchema,
  AgentScenesPageSchema,
  AgentTenantPersonsSchema,
  AgentTenantResponseSchema,
  AgentTenantsListSchema,
  ContextNodeDetailSchema,
  ContextNodeGrantsRevokedSchema,
  ContextNodeMcpConfigResponseSchema,
  ContextPromptComponentsResponseSchema,
  EMPTY_AGENT_SCENES_PAGE,
  EMPTY_AGENT_TENANTS,
  ConnectedAppDetailSchema,
  ConnectedAppsListSchema,
  ContextCapabilityBindingResponseSchema,
  ContextConfigAgentDetailSchema,
  ContextConfigAgentListSchema,
  ContextConfigRedeemSchema,
  ContextConfigSceneDetailSchema,
  ContextConfigSceneResolveSchema,
  ContextConnectorCredentialResponseSchema,
  DingTalkJsapiConfigSchema,
  EMPTY_CONTEXT_CONFIG_REDEEM,
} from "./context-capability-schema";
import type {
  AgentContextCapabilities,
  AgentScenesPage,
  AgentTenant,
  AgentTenantPerson,
  AgentTenantsList,
  ConnectedAppDetail,
  ConnectedAppsList,
  ContextCapabilityBinding,
  ContextConfigAgentDetail,
  ContextConfigAgentSummary,
  ContextConfigRedeemResult,
  ContextConfigSceneDetail,
  ContextConfigSceneGrant,
  ContextConfigScopeInput,
  ContextConnectorCredential,
  ContextNodeDetail,
  ContextNodeRef,
  ContextPromptComponent,
  ContextPromptComponentInput,
  CreateAgentTenantInput,
  DeleteContextConnectorCredentialInput,
  DingTalkJsapiConfig,
  ListAgentScenesParams,
  ResolveContextConfigSceneInput,
  SetAgentContextCapabilityOffersInput,
  SetContextCapabilityBindingInput,
  SetContextConnectorCredentialInput,
  SetContextNodeBindingInput,
  SetContextNodeCredentialInput,
  StartContextConnectorConnectionInput,
  StartContextNodeConnectionInput,
} from "../types/context-capability";
import { SemanticaMCPStatusSchema, EMPTY_SEMANTICA_MCP_STATUS, type SemanticaMCPStatus } from "./semantica-mcp-schema";
import { WorkspaceMCPConnectionsSchema, WorkspaceMCPLinkSchema, type WorkspaceMCPConnection, type CreateWorkspaceMCPConnection } from "./workspace-mcp-schema";
import {ModelProbeSchema, GlobalModelsSchema, EMPTY_GLOBAL_MODELS, DeveloperCapabilitiesSchema, DiscoveredModelsSchema, globalModelsWire, type GlobalModels, type ModelProvider} from "./global-models-schema";
import { DSHProfileSchema, type DSHProfileStatus } from "./dsh-profile-schema";
import { AgentDshPluginConfigSchema } from "./agent-dsh-plugin-config-schema";
import type { AgentDshPluginConfig, UpdateAgentDshPluginConfig } from "../dsh-plugins/types";
import { DSHHomeSchema, type DSHHomeStatus } from "./dsh-home-schema";
import {
  FilesystemEntriesSchema,
  FilesystemGrantsSchema,
  FilesystemGrantSchema,
  FilesystemRootsSchema,
  type FilesystemEntries,
  type FilesystemGrant,
  type FilesystemGrants,
  type FilesystemRoots,
} from "./filesystem-schema";
import type { GitRepositoryIdentity, GitConnections } from "../types/git-repo";
import { GitRepositoryIdentitySchema, GitConnectionsSchema } from "./schemas";
import { ASBNetworkPolicySchema, EMPTY_ASB_NETWORK_POLICY } from "./asb-network-policy-schema";
import { ASBRegionsSchema, EMPTY_ASB_REGIONS } from "./asb-regions-schema";
import type { ReusableDingTalkIdentity } from "../types/dingtalk-account-binding";
import type { AgentPackageBindingReport, ConfirmAgentPackageBindingRequest, AgentPackagePreview, CreateAgentPackageRequest } from "../types/agent-package";
import { AgentPackageBindingReportSchema, AgentPackagePreviewSchema } from "./schemas";
import type {
  Issue,
  IssuePriority,
  CreateIssueRequest,
  MoveIssueRequest,
  UpdateIssueRequest,
  GroupedIssuesResponse,
  ListIssuesResponse,
  SearchIssuesResponse,
  SearchProjectsResponse,
  UpdateMeRequest,
  CreateMemberRequest,
  UpdateMemberRequest,
  DingTalkUser,
  DingTalkProcessingSurface,
  AddDingTalkGroupMembersRequest,
  AddDingTalkGroupMembersResponse,
  AddDingTalkWorkspaceMembersRequest,
  AddDingTalkWorkspaceMembersResponse,
  ListIssuesParams,
  ListGroupedIssuesParams,
  IssueTableFacetsRequest,
  IssueTableFacetsResponse,
  IssueTableGroupsRequest,
  IssueTableGroupsResponse,
  IssueTableRowsRequest,
  IssueTableRowsResponse,
  Agent,
  AgentSceneMemory,
  AgentSceneRelation,
  MikaBootstrapResponse,
  CreateAgentRequest,
  AgentTemplate,
  AgentTemplateSummary,
  CreateAgentFromTemplateRequest,
  CreateAgentFromTemplateResponse,
  AgentBuilderRuntimeSwitch,
  AgentBuilderSession,
  AgentBuilderSessionSummary,
  StoredAgentDraft,
  UpdateAgentRequest,
  DispatchPromptPreview,
  AgentOKRResponse,
  AgentEnvResponse,
  UpdateAgentEnvRequest,
  AgentTask,
  DSHTrajectoryArtifact,
  AgentActivityBucket,
  AgentRunCount,
  WorkspaceWorkingAgent,
  WorkspaceWorkingAgentMineRelation,
  WorkspaceWorkingAgentType,
  AgentRuntime,
  RuntimeProfile,
  CreateRuntimeProfileRequest,
  UpdateRuntimeProfileRequest,
  InboxItem,
  InboxWorkspaceUnread,
  IssueSubscriber,
  Comment,
  CommentTriggerPreview,
  IssueTriggerPreview,
  IssueTriggerPreviewParams,
  Reaction,
  IssueReaction,
  Workspace,
  WorkspaceRepo,
  MemberWithUser,
  User,
  Skill,
  SkillSummary,
  CreateSkillRequest,
  UpdateSkillRequest,
  SetAgentSkillsRequest,
  SetAgentRuntimeSkillEnabledRequest,
  PersonalAccessToken,
  CreatePersonalAccessTokenRequest,
  CreatePersonalAccessTokenResponse,
  WorkspaceAccessToken,
  CreateWorkspaceAccessTokenRequest,
  UpdateWorkspaceAccessTokenRequest,
  RegenerateWorkspaceAccessTokenRequest,
  WorkspaceAccessTokenSecretResponse,
  AgentA2AConfig,
  AgentA2AClient,
  AgentA2ACredentialSecretResponse,
  UpdateAgentA2AConfigRequest,
  CreateAgentA2AClientRequest,
  UpdateAgentA2AClientRequest,
  CreateAgentA2ACredentialRequest,
  AgentA2AOperatorConfig,
  UpdateAgentA2AOperatorIdentityRequest,
  UpdateAgentA2AProdForwardRequest,
  RuntimeUsage,
  IssueUsageSummary,
  RuntimeHourlyActivity,
  RuntimeUsageByAgent,
  RuntimeUsageByHour,
  DashboardUsageDaily,
  DashboardUsageByAgent,
  DashboardAgentRunTime,
  DashboardRunTimeDaily,
  DashboardFailureDaily,
  DashboardFailureByAgent,
  RuntimeUpdate,
  RuntimeModelListRequest,
  RuntimeLocalSkillListRequest,
  CreateRuntimeLocalSkillImportRequest,
  RuntimeLocalSkillImportRequest,
  TimelineEntry,
  AssigneeFrequencyEntry,
  TaskMessagePayload,
  Attachment,
  ChatSession,
  ChatPinnedAgent,
  ChatMessage,
  ChatMessagesPage,
  CoordinatorConversationsPage,
  ChatDraftRestoresResponse,
  ChatPendingTask,
  PrioritizeQueuedChatTaskResponse,
  PendingChatTasksResponse,
  HasPendingChatTasksResponse,
  SendChatMessageResponse,
  ChatReplyReceivedRequest,
  StartMikaOnboardingResponse,
  CancelTaskResponse,
  Project,
  CreateProjectRequest,
  UpdateProjectRequest,
  ListProjectsResponse,
  ProjectResource,
  CreateProjectResourceRequest,
  UpdateProjectResourceRequest,
  ListProjectResourcesResponse,
  Label,
  IssueProperty,
  IssuePropertyValue,
  CreatePropertyRequest,
  QuickAction,
  CreateQuickActionRequest,
  UpdateQuickActionRequest,
  ListQuickActionsResponse,
  UpdatePropertyRequest,
  ListPropertiesResponse,
  IssuePropertiesResponse,
  CreateLabelRequest,
  UpdateLabelRequest,
  ListLabelsResponse,
  IssueLabelsResponse,
  LabelResourceType,
  ResourceLabelsResponse,
  LabelUsageParams,
  LabelUsageResponse,
  PinnedItem,
  CreatePinRequest,
  PinnedItemType,
  ReorderPinsRequest,
  Invitation,
  Autopilot,
  AutopilotTrigger,
  AutopilotRun,
  CreateAutopilotRequest,
  UpdateAutopilotRequest,
  CreateAutopilotTriggerRequest,
  UpdateAutopilotTriggerRequest,
  ListAutopilotsResponse,
  CronPreviewResponse,
  GetAutopilotResponse,
  AutopilotCollaboratorsResponse,
  ListAutopilotRunsResponse,
  ListWebhookDeliveriesResponse,
  WebhookDelivery,
  NotificationPreferenceResponse,
  NotificationPreferences,
  GitHubPullRequest,
  GitHubInstallation,
  ListGitHubInstallationsResponse,
  ListGitHubRepositoriesResponse,
  GitHubConnectResponse,
  GitAgentPreviewRequest,
  GitAgentPreview,

  CreateAgentPackageResponse,
  AgentSource,
  AgentSourceSyncPreview,
  AgentSourceBranches,
  AgentPublicationList,
  SyncAgentSourceResponse,
  ListVCSConnectionsResponse,
  ConnectVCSRequest,
  ConnectVCSResponse,
  ListLarkInstallationsResponse,
  BeginLarkInstallResponse,
  LarkInstallStatusResponse,
  RedeemLarkBindingTokenResponse,
  ComposioToolkit,
  ComposioConnection,
  ComposioConnectInitResponse,
  SlackInstallation,
  ListSlackInstallationsResponse,
  DingTalkInstallation,
  ListDingTalkInstallationsResponse,
  BeginDingTalkInstallResponse,
  DingTalkInstallStatusResponse,
  RedeemDingTalkBindingTokenResponse,
  DingTalkAccountBindingsResponse,
  BeginDingTalkAccountBindingResponse,
  AgentIdentityGitHubStatusResponse,
  BeginAgentIdentityGitHubOAuthResponse,
  DisconnectAgentIdentityGitHubConnectionResponse,
  TestAgentIdentityGitHubConnectionResponse,
  AgentEnterpriseIdentityStatusResponse,
  BeginAgentEnterpriseIdentityBindingResponse,
  RegisterSlackBYORequest,
  RedeemSlackBindingTokenResponse,
  WecomInstallation,
  ListWecomInstallationsResponse,
  RegisterWecomBYORequest,
  RedeemWecomBindingTokenResponse,
  Squad,
  SquadMember,
  SquadMemberStatusListResponse,
  BillingBalance,
  BillingTransactionsPage,
  BillingBatchesPage,
  BillingTopupsPage,
  BillingPriceTier,
  CreateBillingCheckoutSessionRequest,
  CreateBillingCheckoutSessionResponse,
  BillingCheckoutSessionStatus,
  CreateBillingPortalSessionResponse,
  FDEOnboardingState,
  ProvisionFDEOnboardingRequest,
  ProvisionFDEOnboardingResponse,
} from "../types";
import type { OnboardingCompletionPath } from "../onboarding/types";
import type { CreateFeedbackResponse, FeedbackKind } from "../feedback/types";
import type { HostedSite } from "../sitehosting/types";
import type {
  ListProductFeatureReleasesParams,
  ProductFeatureRelease,
  ProductFeatureReleasePage,
} from "../product-features/types";
import type {
  AccountRunnerBindingList,
  CreateRunnerPairingResponse,
  CreateRunnerReconnectCommandResponse,
  RunnerDeviceAuthorization,
  RunnerDeviceAuthorizationResult,
  RunnerMachineBindingList,
} from "../runner/types";
import {
  AccountRunnerBindingListSchema,
  CreateRunnerPairingResponseSchema,
  CreateRunnerReconnectCommandResponseSchema,
  RunnerDeviceAuthorizationResultSchema,
  RunnerDeviceAuthorizationSchema,
  RunnerMachineBindingListSchema,
} from "../runner/schemas";
import type {
  CloudRuntimeNode,
  CreateCloudSandboxRuntimeRequest,
  CreateCloudSandboxStableReleaseRequest,
  CreateFCE2BRuntimeRequest,
  CreateCloudRuntimeNodeRequest,
  FCE2BTemplate,
  FCE2BStableChannel,
  FCE2BStableRelease,
  FCE2BStableReleaseAction,
  FCE2BStableRuntimeOverview,
  CreateFCE2BStableReleaseRequest,
  ListCloudRuntimeNodesParams,
  SandboxBackend,
  ASBRuntimeCredentialResponse,
  ValidateASBRuntimeCredentialRequest,
  ValidateASBRuntimeCredentialResponse,
  UpdateCloudSandboxRuntimeArtifactRequest,
  UpdateASBRuntimeCredentialRequest,
  UpdateFCE2BRuntimeTemplateRequest,
} from "../runtimes/cloud-runtime";
import { type Logger, noopLogger } from "../logger";
import { createRequestId } from "../utils";
import { getCurrentSlug } from "../platform/workspace-storage";
import { parseWithFallback } from "./schema";
import {
  EMPTY_TAG_APPLY,
  EMPTY_TAG_STATE,
  EMPTY_TAG_TENANT_MUTATION,
  TagApplyResponseSchema,
  TagStateSchema,
  TagTenantMutationSchema,
} from "./tag-schema";
import type { CreateTagInput, TagApplyResponse, TagState, TagTenantMutationResult } from "../tag/types";
import type {
  AgentDshPlugin,
  DshPlugin,
  DshPluginBinding,
  DshPluginCatalogCategory,
  DshPluginCatalogPage,
  DshPluginRegistryResult,
  DshPluginFileContent,
  DshPluginFileListing,
  DshPluginUpdate,
  ImportDshPluginRequest,
  ImportDshPluginResult,
} from "../dsh-plugins/types";
import {
  AgentTaskListSchema,
  AgentSceneMemoryListSchema,
  AgentSceneMemorySchema,
  AgentSceneRelationListSchema,
  EMPTY_AGENT_SCENE_MEMORY,
  EMPTY_AGENT_SCENE_MEMORY_LIST,
  EMPTY_AGENT_SCENE_RELATION_LIST,
  HostedSiteListSchema,
  ProductFeatureReleasePageSchema,
  ProductFeatureReleaseSchema,
  EMPTY_PRODUCT_FEATURE_RELEASE,
  EMPTY_PRODUCT_FEATURE_RELEASE_PAGE,
  AgentDshPluginListSchema,
  DshPluginBindingListSchema,
  DshPluginCatalogCategoryListSchema,
  DshPluginCatalogPageSchema,
  DshPluginListSchema,
  DshPluginRegistrySearchSchema,
  DshPluginFileContentSchema,
  DshPluginFileListingSchema,
  DshPluginUpdateSchema,
  DshPluginSchema,
  ImportDshPluginResultSchema,
  AgentTemplateSchema,
  AgentTemplateSummaryListSchema,
  AttachmentResponseSchema,
  CancelTaskResponseSchema,
  ChatDraftRestoresResponseSchema,
  ChatMessageListSchema,
  ChatMessagesPageSchema,
  CoordinatorConversationsPageSchema,
  ChatPendingTaskSchema,
  PrioritizeQueuedChatTaskResponseSchema,
  SendChatMessageResponseSchema,
  StartMikaOnboardingResponseSchema,
  ChildIssuesResponseSchema,
  CommentsListSchema,
  CommentTriggerPreviewSchema,
  IssueTriggerPreviewSchema,
  CloudRuntimeNodeListSchema,
  CloudRuntimeNodeSchema,
  FCE2BStableChannelSchema,
  FCE2BStableReleaseSchema,
  FCE2BStableReleaseListSchema,
  FCE2BStableRuntimeOverviewListSchema,
  EMPTY_FC_E2B_STABLE_CHANNEL,
  EMPTY_FC_E2B_STABLE_RELEASE,
  AgentEnterpriseIdentityStatusResponseSchema,
  BeginAgentEnterpriseIdentityBindingResponseSchema,
  EMPTY_AGENT_ENTERPRISE_IDENTITY_STATUS_RESPONSE,
  EMPTY_BEGIN_AGENT_ENTERPRISE_IDENTITY_BINDING_RESPONSE,
  AddDingTalkGroupMembersResponseSchema,
  AddDingTalkWorkspaceMembersResponseSchema,
  DingTalkUserSearchResponseSchema,
  CreateAgentFromTemplateResponseSchema,
  AgentResponseSchema,
  AgentResponseListSchema,
  EMPTY_AGENT_RESPONSE,
  AgentBuilderRuntimeSwitchSchema,
  AgentBuilderSessionSchema,
  AgentBuilderSessionListSchema,
  EMPTY_AGENT_BUILDER_SESSION_LIST,
  agentBuilderRuntimeSwitchFallback,
  DashboardAgentRunTimeListSchema,
  DashboardRunTimeDailyListSchema,
  DashboardFailureDailyListSchema,
  DashboardFailureByAgentListSchema,
  DashboardUsageByAgentListSchema,
  DashboardUsageDailyListSchema,
  EMPTY_AGENT_TEMPLATE_DETAIL,
  EMPTY_AGENT_TEMPLATE_SUMMARY_LIST,
  EMPTY_APP_CONFIG,
  EMPTY_ATTACHMENT,
  EMPTY_CHAT_MESSAGE_LIST,
  EMPTY_CHAT_PENDING_TASK,
  EMPTY_PRIORITIZE_QUEUED_CHAT_TASK_RESPONSE,
  EMPTY_CLOUD_RUNTIME_NODE,
  EMPTY_CLOUD_RUNTIME_NODE_LIST,
  EMPTY_CREATE_AGENT_FROM_TEMPLATE_RESPONSE,
  EMPTY_ADD_DINGTALK_GROUP_MEMBERS_RESPONSE,
  EMPTY_ADD_DINGTALK_WORKSPACE_MEMBERS_RESPONSE,
  EMPTY_DINGTALK_USER_SEARCH_RESPONSE,
  EMPTY_AGENT_BUILDER_SESSION,
  EMPTY_GROUPED_ISSUES_RESPONSE,
  EMPTY_ISSUE_TABLE_FACETS_RESPONSE,
  EMPTY_ISSUE_TABLE_GROUPS_RESPONSE,
  EMPTY_ISSUE_TABLE_ROWS_RESPONSE,
  EMPTY_LIST_ISSUES_RESPONSE,
  EMPTY_SEARCH_ISSUES_RESPONSE,
  EMPTY_SEARCH_PROJECTS_RESPONSE,
  EMPTY_SQUAD,
  EMPTY_SQUAD_LIST,
  EMPTY_SQUAD_MEMBER_STATUS_LIST,
  EMPTY_TIMELINE_ENTRIES,
  EMPTY_USER,
  EMPTY_LIST_WEBHOOK_DELIVERIES_RESPONSE,
  EMPTY_WEBHOOK_DELIVERY,
  AppConfigSchema,
  type AppConfigResponse,
  GroupedIssuesResponseSchema,
  IssueTableFacetsResponseSchema,
  IssueTableGroupsResponseSchema,
  IssueTableRowsResponseSchema,
  ListAutopilotsResponseSchema,
  EMPTY_LIST_AUTOPILOTS_RESPONSE,
  AutopilotRunSchema,
  AutopilotTriggerSchema,
  GetAutopilotResponseSchema,
  FALLBACK_AUTOPILOT_TRIGGER,
  FALLBACK_GET_AUTOPILOT_RESPONSE,
  FALLBACK_AUTOPILOT_RUN,
  CronPreviewResponseSchema,
  UNREADABLE_CRON_PREVIEW_RESPONSE,
  ListIssuesResponseSchema,
  CreateIssueResponseSchema,
  ListWebhookDeliveriesResponseSchema,
  RuntimeHourlyActivityListSchema,
  RuntimeUsageByAgentListSchema,
  RuntimeUsageByHourListSchema,
  RuntimeUsageListSchema,
  SearchIssuesResponseSchema,
  SearchProjectsResponseSchema,
  SquadSchema,
  SquadListSchema,
  SquadMemberStatusListResponseSchema,
  SubscribersListSchema,
  TimelineEntriesSchema,
  UserSchema,
  DispatchPromptPreviewSchema,
  ExtractAgentVoiceResponseSchema,
  EMPTY_EXTRACT_AGENT_VOICE_RESPONSE,
  AgentOKRResponseSchema,
  WebhookDeliveryResponseSchema,
  BillingBalanceSchema,
  BillingTransactionsPageSchema,
  BillingBatchesPageSchema,
  BillingTopupsPageSchema,
  BillingPriceTierListSchema,
  CreateBillingCheckoutSessionResponseSchema,
  BillingCheckoutSessionStatusSchema,
  CreateBillingPortalSessionResponseSchema,
  WecomInstallationSchema,
  ListWecomInstallationsResponseSchema,
  ListDingTalkInstallationsResponseSchema,
  RedeemWecomBindingTokenResponseSchema,
  EMPTY_WECOM_INSTALLATION,
  EMPTY_LIST_WECOM_INSTALLATIONS_RESPONSE,
  EMPTY_REDEEM_WECOM_BINDING_TOKEN_RESPONSE,
  EMPTY_BILLING_BALANCE,
  EMPTY_BILLING_TRANSACTIONS_PAGE,
  EMPTY_BILLING_BATCHES_PAGE,
  EMPTY_BILLING_TOPUPS_PAGE,
  EMPTY_BILLING_PRICE_TIER_LIST,
  EMPTY_CREATE_BILLING_CHECKOUT_SESSION_RESPONSE,
  EMPTY_BILLING_CHECKOUT_SESSION_STATUS,
  EMPTY_CREATE_BILLING_PORTAL_SESSION_RESPONSE,
  EMPTY_CANCEL_TASK_RESPONSE,
  EMPTY_CHAT_DRAFT_RESTORES,
  CreateFeedbackResponseSchema,
  EMPTY_CREATE_FEEDBACK_RESPONSE,
  InboxUnreadSummarySchema,
  EMPTY_INBOX_UNREAD_SUMMARY,
  InboxItemListSchema,
  EMPTY_INBOX_ITEMS,
  NotificationPreferenceResponseSchema,
  EMPTY_NOTIFICATION_PREFERENCE_RESPONSE,
  LabelSchema,
  ListLabelsResponseSchema,
  IssuePropertySchema,
  ListPropertiesResponseSchema,
  IssuePropertiesResponseSchema,
  QuickActionSchema,
  ListQuickActionsResponseSchema,
  QuickActionRenderSchema,
  EMPTY_QUICK_ACTION,
  EMPTY_LIST_QUICK_ACTIONS_RESPONSE,
  CommentSchema,
  EMPTY_COMMENT,
  EMPTY_ISSUE_PROPERTY,
  EMPTY_LIST_PROPERTIES_RESPONSE,
  EMPTY_ISSUE_PROPERTIES_RESPONSE,
  EMPTY_ISSUE_PULL_REQUESTS_RESPONSE,
  IssuePullRequestsResponseSchema,
  ResourceLabelsResponseSchema,
  EMPTY_LABEL,
  EMPTY_LIST_LABELS_RESPONSE,
  EMPTY_RESOURCE_LABELS_RESPONSE,
  LabelUsageResponseSchema,
  EMPTY_LABEL_USAGE_RESPONSE,
  GitAgentPreviewSchema,
  GitHubInstallationSchema,
  ListGitHubInstallationsResponseSchema,

  AgentSourceSchema,
  AgentManifestSchemaDownloadSchema,
  AgentSourceSyncPreviewSchema,
  AgentSourceBranchesSchema,
  AgentPublicationListSchema,
  EMPTY_AGENT_SOURCE_SYNC_PREVIEW,
  EMPTY_AGENT_SOURCE_BRANCHES,
  CreateAgentPackageResponseSchema,
  SyncAgentSourceResponseSchema,
  EMPTY_GIT_AGENT_PREVIEW,
  EMPTY_GITHUB_INSTALLATION,
  EMPTY_GITHUB_INSTALLATIONS,

  EMPTY_AGENT_SOURCE,
  EMPTY_CREATE_AGENT_PACKAGE_RESPONSE,
  EMPTY_SYNC_AGENT_SOURCE_RESPONSE,
  BeginDingTalkAccountBindingResponseSchema,
  DingTalkAccountBindingsResponseSchema,
  ReusableDingTalkIdentitiesSchema,
  EMPTY_BEGIN_DINGTALK_ACCOUNT_BINDING_RESPONSE,
  EMPTY_DINGTALK_ACCOUNT_BINDINGS_RESPONSE,
  FDEOnboardingStateSchema,
  ProvisionFDEOnboardingResponseSchema,
  EMPTY_FDE_ONBOARDING_STATE,
  EMPTY_PROVISION_FDE_ONBOARDING_RESPONSE,
  AgentIdentityGitHubStatusResponseSchema,
  BeginAgentIdentityGitHubOAuthResponseSchema,
  DisconnectAgentIdentityGitHubConnectionResponseSchema,
  TestAgentIdentityGitHubConnectionResponseSchema,
  EMPTY_AGENT_IDENTITY_GITHUB_STATUS_RESPONSE,
  EMPTY_BEGIN_AGENT_IDENTITY_GITHUB_OAUTH_RESPONSE,
  EMPTY_DISCONNECT_AGENT_IDENTITY_GITHUB_CONNECTION_RESPONSE,
  EMPTY_TEST_AGENT_IDENTITY_GITHUB_CONNECTION_RESPONSE,
  WorkspaceAccessTokenSchema,
  WorkspaceAccessTokenListSchema,
  WorkspaceAccessTokenSecretResponseSchema,
  EMPTY_WORKSPACE_ACCESS_TOKEN,
  EMPTY_WORKSPACE_ACCESS_TOKEN_SECRET_RESPONSE,
  AgentA2AConfigSchema,
  AgentA2AClientSchema,
  AgentA2ACredentialSecretResponseSchema,
  EMPTY_AGENT_A2A_CONFIG,
  EMPTY_AGENT_A2A_CLIENT,
  EMPTY_AGENT_A2A_CREDENTIAL_SECRET_RESPONSE,
  AgentA2AOperatorConfigSchema,
  EMPTY_AGENT_A2A_OPERATOR_CONFIG,
  GitHubConnectResponseSchema,
  ListGitHubRepositoriesResponseSchema,
  EMPTY_GITHUB_CONNECT_RESPONSE,
  EMPTY_LIST_GITHUB_REPOSITORIES_RESPONSE,
  RuntimeModelListRequestSchema,
  MALFORMED_RUNTIME_MODEL_LIST_REQUEST,
  IssueViewSchema,
  IssueViewListSchema,
  IssueViewPreferenceSchema,
  EMPTY_ISSUE_VIEW_PREFERENCE,
  type IssueView,
  type IssueViewPreference,
  type CreateIssueViewRequest,
} from "./schemas";

/** Identifies the calling client to the server.
 *  Sent on every HTTP request as X-Client-Platform / X-Client-Version /
 *  X-Client-OS so the backend can log, gate, or split metrics by client.
 *  See server/internal/middleware/client.go for the receiving end. */
export interface ApiClientIdentity {
  /** Logical client kind. Server expects: "web" | "desktop" | "cli" | "daemon". */
  platform?: string;
  /** Client/app version string (e.g. "0.1.0", git tag, commit). */
  version?: string;
  /** Coarse operating-system bucket (for example "macos", "windows", or "linux"). */
  os?: string;
}

export interface ApiClientOptions {
  logger?: Logger;
  onUnauthorized?: () => void;
  /** Identifies the client to the server. Sent as X-Client-* headers. */
  identity?: ApiClientIdentity;
}

export interface ClientRuntimeSnapshot {
  probe_result: "success" | "error";
  runtime_count?: number;
  provider_summary?: Record<string, number>;
  online_count?: number;
  offline_count?: number;
}

export interface ClientUsageRequest {
  install_id: string;
  runtime?: ClientRuntimeSnapshot;
}

export interface LoginResponse {
  token: string;
  user: User;
}

export class ApiError extends Error {
  readonly status: number;
  readonly statusText: string;
  // Raw decoded JSON body (when the server returned one). Carries structured
  // error fields like `code` so callers can branch on machine-readable
  // identifiers instead of pattern-matching the human-readable message.
  readonly body?: unknown;

  constructor(
    message: string,
    status: number,
    statusText: string,
    body?: unknown,
  ) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.statusText = statusText;
    this.body = body;
  }
}

// errorCode extracts the stable `code` a handler attaches to a failure
// (writeErrorCode), so a caller can render its own localized sentence instead
// of toasting the server's English one. Returns undefined for a non-ApiError,
// or a server that did not send one — the caller then falls back to
// err.message, which is what every endpoint that has not adopted this yet
// produces.
export function errorCode(err: unknown): string | undefined {
  if (err instanceof ApiError && err.body && typeof err.body === "object") {
    const code = (err.body as { code?: unknown }).code;
    if (typeof code === "string" && code.length > 0) return code;
  }
  return undefined;
}

// dispatchReasonCode extracts the stable, machine-readable admission reason
// (MUL-4525) from a blocked-trigger error's structured body, when present. UI
// callers localize a blocked/partial trigger from this code instead of pattern
// matching the human-readable message. Returns undefined for non-ApiErrors or
// bodies without a reason_code (older servers), so callers fall back to their
// generic failure toast.
export function dispatchReasonCode(err: unknown): string | undefined {
  if (err instanceof ApiError && err.body && typeof err.body === "object") {
    const code = (err.body as { reason_code?: unknown }).reason_code;
    if (typeof code === "string" && code.length > 0) return code;
  }
  return undefined;
}

// Thrown by getAttachmentTextContent when the server refuses to inline a
// file because it exceeds the 2 MB cap. UI maps to a "too large, please
// download" affordance with the Download CTA still available.
export class PreviewTooLargeError extends Error {
  constructor() {
    super("attachment too large for inline preview");
    this.name = "PreviewTooLargeError";
  }
}

// Thrown by getAttachmentTextContent when the server's text whitelist
// rejects the content type. Normally the client's isPreviewable() guard
// catches this earlier, but the two whitelists can drift — surfacing the
// 415 as a typed error makes the drift visible.
export class PreviewUnsupportedError extends Error {
  constructor() {
    super("attachment type not supported for inline preview");
    this.name = "PreviewUnsupportedError";
  }
}

/**
 * Advertised in X-Client-Capabilities so the server knows this client can
 * recover a cancelled prompt from the durable draft-restore row (#5219).
 * Must stay in sync with protocol.AppCapabilityChatDraftRestoreV1.
 */
export const CHAT_DRAFT_RESTORE_CAPABILITY = "chat-draft-restore-v1";

/**
 * Body shared by both unsubscribe endpoints: an omitted target means "the
 * caller", which the server resolves from the request actor.
 */
function subscriberTarget(
  userId?: string,
  userType?: string,
): Record<string, string> {
  const body: Record<string, string> = {};
  if (userId) body.user_id = userId;
  if (userType) body.user_type = userType;
  return body;
}

/**
 * Per-call override for the workspace a request targets.
 *
 * `authHeaders()` normally stamps `X-Workspace-Slug` from the global
 * current-workspace singleton, and the server resolves the workspace from that
 * header BEFORE any `workspace_id` query param. Anything that acts on a
 * workspace the user is not currently "in" — notably the create-workspace flow,
 * which provisions a workspace it has not navigated to yet — must say so
 * explicitly, or a concurrent writer of that singleton silently redirects the
 * write to the wrong workspace.
 */
function workspaceHeader(slug?: string): Record<string, string> | undefined {
  return slug ? { "X-Workspace-Slug": slug } : undefined;
}

/**
 * Blanks the workspace header for routes that are not workspace-scoped
 * (context capability mobile configuration). Their callers are often not
 * members of any workspace, so a stale current-workspace slug must never
 * leak into the request.
 */
const NO_WORKSPACE_HEADER: Record<string, string> = { "X-Workspace-Slug": "" };

/** The scope fields of a configure-page write; `org_id` only for a tenant
 * other than the agent's own org (the server's default). */
function contextConfigScopeBody(scope: ContextConfigScopeInput): Record<string, string> {
  return {
    scope_type: scope.scopeType,
    scope_key: scope.scopeKey,
    ...(scope.orgId ? { org_id: scope.orgId } : {}),
  };
}

/** Prompt components as the list PUTs take them; a missing switch is on. */
function promptComponentsBody(prompts: ContextPromptComponentInput[]) {
  return prompts.map((prompt) => ({
    name: prompt.name,
    order: prompt.order,
    text: prompt.text,
    enabled: prompt.enabled !== false,
  }));
}

export class ApiClient {
  private baseUrl: string;
  private token: string | null = null;
  private logger: Logger;
  private options: ApiClientOptions;

  constructor(baseUrl: string, options?: ApiClientOptions) {
    this.baseUrl = baseUrl;
    this.options = options ?? {};
    this.logger = options?.logger ?? noopLogger;
  }

  getBaseUrl(): string {
    return this.baseUrl;
  }

  setToken(token: string | null) {
    this.token = token;
  }

  private readCsrfToken(): string | null {
    if (typeof document === "undefined") return null;
    const match = document.cookie
      .split("; ")
      .find((c) => c.startsWith("multica_csrf="));
    return match ? (match.split("=")[1] ?? null) : null;
  }

  private authHeaders(): Record<string, string> {
    const headers: Record<string, string> = {};
    if (this.token) headers["Authorization"] = `Bearer ${this.token}`;
    const slug = getCurrentSlug();
    if (slug) headers["X-Workspace-Slug"] = slug;
    const csrf = this.readCsrfToken();
    if (csrf) headers["X-CSRF-Token"] = csrf;
    const id = this.options.identity;
    if (id?.platform) headers["X-Client-Platform"] = id.platform;
    if (id?.version) headers["X-Client-Version"] = id.version;
    if (id?.os) headers["X-Client-OS"] = id.os;
    return headers;
  }

  private handleUnauthorized() {
    this.token = null;
    // Workspace id is owned by the URL-driven workspace-storage singleton
    // (set by [workspaceSlug]/layout.tsx). On 401, the auth flow navigates
    // to /login which leaves the workspace route, and the next workspace
    // entry will overwrite the id. No clear needed here.
    this.options.onUnauthorized?.();
  }

  private async parseErrorMessage(
    res: Response,
    fallback: string,
  ): Promise<string> {
    try {
      const data = (await res.json()) as { error?: string };
      if (typeof data.error === "string" && data.error) return data.error;
    } catch {
      // Ignore non-JSON error bodies.
    }
    return fallback;
  }

  // Reads the response body once for both human-readable error message and
  // structured fields. The Response stream can only be consumed once, so
  // both pieces have to come from a single read.
  private async parseErrorBody(
    res: Response,
    fallback: string,
  ): Promise<{ message: string; body: unknown }> {
    try {
      const data = (await res.json()) as { error?: string };
      const message =
        typeof data.error === "string" && data.error ? data.error : fallback;
      return { message, body: data };
    } catch {
      return { message: fallback, body: undefined };
    }
  }

  // Sends the request with the standard headers (auth, CSRF, request id,
  // client identity) and runs the shared error path (401 → handleUnauthorized,
  // structured ApiError, status-aware log level). Returns the raw Response so
  // callers can decide how to decode the body — JSON for the typed `fetch<T>`
  // path, plain text for the attachment-preview proxy, etc.
  private async fetchRaw(
    path: string,
    init?: RequestInit & { extraHeaders?: Record<string, string> },
  ): Promise<Response> {
    const rid = createRequestId();
    const start = Date.now();
    const method = init?.method ?? "GET";

    const headers: Record<string, string> = {
      "X-Request-ID": rid,
      ...this.authHeaders(),
      ...(init?.extraHeaders ?? {}),
      ...((init?.headers as Record<string, string>) ?? {}),
    };

    this.logger.info(`→ ${method} ${path}`, { rid });

    const res = await fetch(`${this.baseUrl}${path}`, {
      ...init,
      headers,
      credentials: "include",
    });

    if (!res.ok) {
      if (res.status === 401) this.handleUnauthorized();
      const { message, body } = await this.parseErrorBody(
        res,
        `API error: ${res.status} ${res.statusText}`,
      );
      const logLevel = res.status === 404 ? "warn" : "error";
      this.logger[logLevel](`← ${res.status} ${path}`, {
        rid,
        duration: `${Date.now() - start}ms`,
        // Validator messages may quote submitted values. Keep them in the UI,
        // never in client telemetry.
        error: body && typeof body === "object" && "code" in body && (body.code === "invalid_agent_manifest" || body.code === "invalid_agent_package") ? String(body.code) : message,
      });
      throw new ApiError(message, res.status, res.statusText, body);
    }

    this.logger.info(`← ${res.status} ${path}`, {
      rid,
      duration: `${Date.now() - start}ms`,
    });
    return res;
  }

  private async fetch<T>(path: string, init?: RequestInit): Promise<T> {
    const res = await this.fetchRaw(path, {
      ...init,
      extraHeaders: { "Content-Type": "application/json" },
    });
    // Handle 204 No Content
    if (res.status === 204) {
      return undefined as T;
    }
    return res.json() as Promise<T>;
  }

  async restoreGlobalModels(revision:number): Promise<GlobalModels> {
 return parseWithFallback(await this.fetch<unknown>("/api/developer/models/restore",{method:"POST",body:JSON.stringify({revision})}),GlobalModelsSchema,EMPTY_GLOBAL_MODELS,{endpoint:"developer/models/restore",includeReceived:false});
 }
  async testProviderModel(provider:ModelProvider, model:string) {
 return parseWithFallback(await this.fetch<unknown>("/api/developer/models/test",{method:"POST",body:JSON.stringify({provider:{id:provider.id,base_url:provider.baseUrl,api_key:provider.apiKey},model})}),ModelProbeSchema,{valid:false,status:0,elapsedMs:0},{endpoint:"developer/models/test",includeReceived:false});
 }
  async getDeveloperCapabilities() {
    return parseWithFallback(await this.fetch<unknown>("/api/developer/capabilities"), DeveloperCapabilitiesSchema, {developer:false}, {endpoint:"developer/capabilities"});
  }
  async getGlobalModels(): Promise<GlobalModels> {
    return parseWithFallback(await this.fetch<unknown>("/api/developer/models"), GlobalModelsSchema, EMPTY_GLOBAL_MODELS, {endpoint:"developer/models",includeReceived:false});
  }
  async saveGlobalModels(config: GlobalModels): Promise<GlobalModels> {
    return parseWithFallback(await this.fetch<unknown>("/api/developer/models", {method:"PUT", body:JSON.stringify(globalModelsWire(config))}), GlobalModelsSchema, EMPTY_GLOBAL_MODELS, {endpoint:"developer/models",includeReceived:false});
  }
  async discoverProviderModels(provider: ModelProvider) {
    return parseWithFallback(await this.fetch<unknown>("/api/developer/models/discover", {method:"POST",body:JSON.stringify({id:provider.id,base_url:provider.baseUrl,api_key:provider.apiKey})}), DiscoveredModelsSchema, {models:[] as string[], replace:false, checked:0, unavailable:0, unverified:0}, {endpoint:"developer/models/discover",includeReceived:false});
  }

  // Auth
  async sendCode(email: string): Promise<void> {
    await this.fetch("/auth/send-code", {
      method: "POST",
      body: JSON.stringify({ email }),
    });
  }

  async verifyCode(email: string, code: string): Promise<LoginResponse> {
    return this.fetch("/auth/verify-code", {
      method: "POST",
      body: JSON.stringify({ email, code }),
    });
  }

  async googleLogin(code: string, redirectUri: string): Promise<LoginResponse> {
    return this.fetch("/auth/google", {
      method: "POST",
      body: JSON.stringify({ code, redirect_uri: redirectUri }),
    });
  }

  // DingTalk's OAuth callback returns a single-use `authCode`; the backend
  // exchanges it server-side (no redirect_uri needed for the token step).
  async dingtalkLogin(code: string): Promise<LoginResponse> {
    return this.fetch("/auth/dingtalk", {
      method: "POST",
      body: JSON.stringify({ code }),
    });
  }

  async fdeDingtalkLogin(code: string): Promise<LoginResponse> {
    return this.fetch("/auth/fde/dingtalk", {
      method: "POST",
      body: JSON.stringify({ code }),
    });
  }

  // Feishu (Lark) returns the grant as `code` and, unlike DingTalk, the
  // backend must send the same redirect_uri again on the token exchange.
  async larkLogin(code: string, redirectUri: string): Promise<LoginResponse> {
    return this.fetch("/auth/lark", {
      method: "POST",
      body: JSON.stringify({ code, redirect_uri: redirectUri }),
    });
  }

  async logout(): Promise<void> {
    await this.fetch("/auth/logout", { method: "POST" });
  }

  async issueCliToken(): Promise<{ token: string }> {
    return this.fetch("/api/cli-token", { method: "POST" });
  }

  async getMe(): Promise<User> {
    const raw = await this.fetch<unknown>("/api/me");
    return parseWithFallback(raw, UserSchema, EMPTY_USER, {
      endpoint: "GET /api/me",
    });
  }

  async listHostedSites(): Promise<HostedSite[]> {
    const raw = await this.fetch<unknown>("/api/sitehosting/sites");
    return parseWithFallback(raw, HostedSiteListSchema, [], {
      endpoint: "GET /api/sitehosting/sites",
    });
  }

  async deleteHostedSite(siteId: string): Promise<void> {
    await this.fetch(
      `/api/sitehosting/sites/${encodeURIComponent(siteId)}`,
      { method: "DELETE" },
    );
  }

  async listProductFeatureReleases(
    params: ListProductFeatureReleasesParams = {},
  ): Promise<ProductFeatureReleasePage> {
    const search = new URLSearchParams();
    if (params.query?.trim()) search.set("q", params.query.trim());
    if (params.limit !== undefined) search.set("limit", String(params.limit));
    if (params.offset !== undefined) search.set("offset", String(params.offset));
    const query = search.toString();
    const raw = await this.fetch<unknown>(`/api/features${query ? `?${query}` : ""}`, {
      signal: params.signal,
    });
    return parseWithFallback(
      raw,
      ProductFeatureReleasePageSchema,
      EMPTY_PRODUCT_FEATURE_RELEASE_PAGE,
      { endpoint: "GET /api/features", includeReceived: false },
    );
  }

  async getProductFeatureRelease(id: string): Promise<ProductFeatureRelease> {
    const raw = await this.fetch<unknown>(
      `/api/features/${encodeURIComponent(id)}`,
    );
    return parseWithFallback(
      raw,
      ProductFeatureReleaseSchema,
      EMPTY_PRODUCT_FEATURE_RELEASE,
      { endpoint: "GET /api/features/:id", includeReceived: false },
    );
  }

  async markOnboardingComplete(payload?: {
    completion_path?: OnboardingCompletionPath;
    workspace_id?: string;
  }): Promise<User> {
    const raw = await this.fetch<unknown>("/api/me/onboarding/complete", {
      method: "POST",
      body: payload ? JSON.stringify(payload) : undefined,
    });
    return parseWithFallback(raw, UserSchema, EMPTY_USER, {
      endpoint: "POST /api/me/onboarding/complete",
    });
  }

  async joinCloudWaitlist(payload: {
    email: string;
    reason?: string;
  }): Promise<User> {
    const raw = await this.fetch<unknown>("/api/me/onboarding/cloud-waitlist", {
      method: "POST",
      body: JSON.stringify(payload),
    });
    return parseWithFallback(raw, UserSchema, EMPTY_USER, {
      endpoint: "POST /api/me/onboarding/cloud-waitlist",
    });
  }

  async patchOnboarding(payload: {
    questionnaire?: Record<string, unknown>;
  }): Promise<User> {
    const raw = await this.fetch<unknown>("/api/me/onboarding", {
      method: "PATCH",
      body: JSON.stringify(payload),
    });
    return parseWithFallback(raw, UserSchema, EMPTY_USER, {
      endpoint: "PATCH /api/me/onboarding",
    });
  }

  async updateMe(data: UpdateMeRequest): Promise<User> {
    const raw = await this.fetch<unknown>("/api/me", {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, UserSchema, EMPTY_USER, {
      endpoint: "PATCH /api/me",
    });
  }

  // Issues
  async listIssues(params?: ListIssuesParams): Promise<ListIssuesResponse> {
    const search = new URLSearchParams();
    if (params?.limit) search.set("limit", String(params.limit));
    if (params?.offset) search.set("offset", String(params.offset));
    if (params?.workspace_id) search.set("workspace_id", params.workspace_id);
    if (params?.q?.trim()) search.set("q", params.q.trim());
    if (params?.status) search.set("status", params.status);
    if (params?.statuses?.length)
      search.set("statuses", params.statuses.join(","));
    if (params?.priority) search.set("priority", params.priority);
    if (params?.priorities?.length)
      search.set("priorities", params.priorities.join(","));
    if (params?.assignee_id) search.set("assignee_id", params.assignee_id);
    if (params?.assignee_ids?.length)
      search.set("assignee_ids", params.assignee_ids.join(","));
    if (params?.assignee_types?.length)
      search.set("assignee_types", params.assignee_types.join(","));
    if (params?.creator_id) search.set("creator_id", params.creator_id);
    if (params?.project_id) search.set("project_id", params.project_id);
    if (params?.assignee_filters?.length) {
      search.set(
        "assignee_filters",
        params.assignee_filters.map((f) => `${f.type}:${f.id}`).join(","),
      );
    }
    if (params?.include_no_assignee) search.set("include_no_assignee", "true");
    if (params?.creator_filters?.length) {
      search.set(
        "creator_filters",
        params.creator_filters.map((f) => `${f.type}:${f.id}`).join(","),
      );
    }
    if (params?.project_ids?.length)
      search.set("project_ids", params.project_ids.join(","));
    if (params?.include_no_project) search.set("include_no_project", "true");
    if (params?.label_ids?.length)
      search.set("label_ids", params.label_ids.join(","));
    if (params?.top_level_only) search.set("top_level_only", "true");
    // No `.length` guard on purpose: an empty ids array must still send
    // `ids=` — the server treats a PRESENT-but-empty list as an empty window
    // (nothing running), while an absent param means no restriction.
    if (params?.ids) search.set("ids", params.ids.join(","));
    if (params?.involves_user_id)
      search.set("involves_user_id", params.involves_user_id);
    if (params?.metadata && Object.keys(params.metadata).length > 0) {
      search.set("metadata", JSON.stringify(params.metadata));
    }
    if (params?.properties && Object.keys(params.properties).length > 0) {
      search.set("properties", JSON.stringify(params.properties));
    }
    if (params?.open_only) search.set("open_only", "true");
    if (params?.scheduled) search.set("scheduled", "true");
    if (params?.date_field) search.set("date_field", params.date_field);
    if (params?.date_start) search.set("date_start", params.date_start);
    if (params?.date_end) search.set("date_end", params.date_end);
    if (params?.sort_by) search.set("sort", params.sort_by);
    if (params?.sort_direction) search.set("direction", params.sort_direction);
    // An ids facet can carry hundreds of UUIDs (agents-working filter) —
    // enough to blow the ~8 KB request-line cap of common reverse proxies.
    // Route those windows through the POST twin, which takes the SAME
    // key/value pairs as a JSON body.
    if (params?.ids) {
      const raw = await this.fetch<unknown>("/api/issues/query", {
        method: "POST",
        body: JSON.stringify(Object.fromEntries(search)),
      });
      return parseWithFallback(
        raw,
        ListIssuesResponseSchema,
        EMPTY_LIST_ISSUES_RESPONSE,
        {
          endpoint: "POST /api/issues/query",
        },
      );
    }
    const path = `/api/issues?${search}`;
    const raw = await this.fetch<unknown>(path);
    return parseWithFallback(
      raw,
      ListIssuesResponseSchema,
      EMPTY_LIST_ISSUES_RESPONSE,
      {
        endpoint: "GET /api/issues",
      },
    );
  }

  async listGroupedIssues(
    params: ListGroupedIssuesParams,
  ): Promise<GroupedIssuesResponse> {
    const search = new URLSearchParams({ group_by: params.group_by });
    if (params.limit) search.set("limit", String(params.limit));
    if (params.offset) search.set("offset", String(params.offset));
    if (params.workspace_id) search.set("workspace_id", params.workspace_id);
    if (params.statuses?.length)
      search.set("statuses", params.statuses.join(","));
    if (params.priorities?.length)
      search.set("priorities", params.priorities.join(","));
    if (params.assignee_types?.length)
      search.set("assignee_types", params.assignee_types.join(","));
    if (params.assignee_id) search.set("assignee_id", params.assignee_id);
    if (params.assignee_ids?.length)
      search.set("assignee_ids", params.assignee_ids.join(","));
    if (params.creator_id) search.set("creator_id", params.creator_id);
    if (params.project_id) search.set("project_id", params.project_id);
    if (params.involves_user_id)
      search.set("involves_user_id", params.involves_user_id);
    if (params.metadata && Object.keys(params.metadata).length > 0) {
      search.set("metadata", JSON.stringify(params.metadata));
    }
    if (params.properties && Object.keys(params.properties).length > 0) {
      search.set("properties", JSON.stringify(params.properties));
    }
    if (params.assignee_filters?.length) {
      search.set(
        "assignee_filters",
        params.assignee_filters.map((f) => `${f.type}:${f.id}`).join(","),
      );
    }
    if (params.include_no_assignee) search.set("include_no_assignee", "true");
    if (params.creator_filters?.length) {
      search.set(
        "creator_filters",
        params.creator_filters.map((f) => `${f.type}:${f.id}`).join(","),
      );
    }
    if (params.project_ids?.length)
      search.set("project_ids", params.project_ids.join(","));
    if (params.include_no_project) search.set("include_no_project", "true");
    if (params.label_ids?.length)
      search.set("label_ids", params.label_ids.join(","));
    if (params.group_assignee_type)
      search.set("group_assignee_type", params.group_assignee_type);
    if (params.group_assignee_id)
      search.set("group_assignee_id", params.group_assignee_id);
    if (params.date_field) search.set("date_field", params.date_field);
    if (params.date_start) search.set("date_start", params.date_start);
    if (params.date_end) search.set("date_end", params.date_end);
    if (params.sort_by) search.set("sort", params.sort_by);
    if (params.sort_direction) search.set("direction", params.sort_direction);
    const raw = await this.fetch<unknown>(`/api/issues/grouped?${search}`);
    return parseWithFallback(
      raw,
      GroupedIssuesResponseSchema,
      EMPTY_GROUPED_ISSUES_RESPONSE,
      {
        endpoint: "GET /api/issues/grouped",
      },
    );
  }

  async listIssueTableGroups(
    params: IssueTableGroupsRequest,
  ): Promise<IssueTableGroupsResponse> {
    const raw = await this.fetch<unknown>("/api/issues/table/groups", {
      method: "POST",
      body: JSON.stringify(params),
    });
    return parseWithFallback(
      raw,
      IssueTableGroupsResponseSchema,
      EMPTY_ISSUE_TABLE_GROUPS_RESPONSE,
      { endpoint: "POST /api/issues/table/groups" },
    );
  }

  async listIssueTableRows(
    params: IssueTableRowsRequest,
  ): Promise<IssueTableRowsResponse> {
    const raw = await this.fetch<unknown>("/api/issues/table/rows", {
      method: "POST",
      body: JSON.stringify(params),
    });
    return parseWithFallback(
      raw,
      IssueTableRowsResponseSchema,
      EMPTY_ISSUE_TABLE_ROWS_RESPONSE,
      { endpoint: "POST /api/issues/table/rows" },
    );
  }

  async listIssueTableFacets(
    params: IssueTableFacetsRequest,
  ): Promise<IssueTableFacetsResponse> {
    const raw = await this.fetch<unknown>("/api/issues/table/facets", {
      method: "POST",
      body: JSON.stringify(params),
    });
    return parseWithFallback(
      raw,
      IssueTableFacetsResponseSchema,
      EMPTY_ISSUE_TABLE_FACETS_RESPONSE,
      { endpoint: "POST /api/issues/table/facets" },
    );
  }

  async searchIssues(params: {
    q: string;
    limit?: number;
    offset?: number;
    include_closed?: boolean;
    signal?: AbortSignal;
  }): Promise<SearchIssuesResponse> {
    const search = new URLSearchParams({ q: params.q });
    if (params.limit !== undefined) search.set("limit", String(params.limit));
    if (params.offset !== undefined)
      search.set("offset", String(params.offset));
    if (params.include_closed) search.set("include_closed", "true");
    const raw = await this.fetch<unknown>(
      `/api/issues/search?${search}`,
      params.signal ? { signal: params.signal } : undefined,
    );
    return parseWithFallback(
      raw,
      SearchIssuesResponseSchema,
      EMPTY_SEARCH_ISSUES_RESPONSE,
      {
        endpoint: "GET /api/issues/search",
      },
    );
  }

  async searchProjects(params: {
    q: string;
    limit?: number;
    offset?: number;
    include_closed?: boolean;
    signal?: AbortSignal;
  }): Promise<SearchProjectsResponse> {
    const search = new URLSearchParams({ q: params.q });
    if (params.limit !== undefined) search.set("limit", String(params.limit));
    if (params.offset !== undefined)
      search.set("offset", String(params.offset));
    if (params.include_closed) search.set("include_closed", "true");
    const raw = await this.fetch<unknown>(
      `/api/projects/search?${search}`,
      params.signal ? { signal: params.signal } : undefined,
    );
    return parseWithFallback(
      raw,
      SearchProjectsResponseSchema,
      EMPTY_SEARCH_PROJECTS_RESPONSE,
      {
        endpoint: "GET /api/projects/search",
      },
    );
  }

  async getIssue(id: string): Promise<Issue> {
    return this.fetch(`/api/issues/${id}`);
  }

  async createIssue(data: CreateIssueRequest): Promise<Issue> {
    // Parse through a schema (not a raw cast): the create modal keys its
    // label-attach compatibility fallback off `labels` being absent vs a
    // validated Label[], so an unvalidated wrong shape must not slip through.
    // Unlike list endpoints, a create that returns an unusable body is a
    // FAILED mutation, not a safe-empty read: fall back to null and reject so
    // the modal keeps the draft and shows a failure toast instead of a blank
    // "created" card pointing at an empty issue id. parseWithFallback already
    // logged the schema issues + raw payload; the empty message lets the modal
    // render its localized "failed to create" toast.
    const raw = await this.fetch<unknown>("/api/issues", {
      method: "POST",
      body: JSON.stringify(data),
    });
    const issue = parseWithFallback<Issue | null>(
      raw,
      CreateIssueResponseSchema,
      null,
      {
        endpoint: "POST /api/issues",
      },
    );
    if (!issue) {
      throw new Error();
    }
    return issue;
  }

  async quickCreateIssue(data: {
    agent_id?: string;
    squad_id?: string;
    prompt: string;
    priority?: IssuePriority;
    due_date?: string;
    project_id?: string | null;
    parent_issue_id?: string | null;
    attachment_ids?: string[];
  }): Promise<{ task_id: string }> {
    return this.fetch("/api/issues/quick-create", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async createFeedback(data: {
    message: string;
    url?: string;
    workspace_id?: string;
    kind?: FeedbackKind;
  }): Promise<CreateFeedbackResponse> {
    const raw = await this.fetch<unknown>("/api/feedback", {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseWithFallback(
      raw,
      CreateFeedbackResponseSchema,
      EMPTY_CREATE_FEEDBACK_RESPONSE,
      {
        endpoint: "POST /api/feedback",
      },
    );
  }

  async upsertClientUsage(data: ClientUsageRequest): Promise<void> {
    await this.fetch("/api/client-usage", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async updateIssue(id: string, data: UpdateIssueRequest): Promise<Issue> {
    return this.fetch(`/api/issues/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

  async moveIssue(id: string, data: MoveIssueRequest): Promise<Issue> {
    return this.fetch(`/api/issues/${id}/move`, {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async listChildIssues(id: string): Promise<{ issues: Issue[] }> {
    const raw = await this.fetch<unknown>(`/api/issues/${id}/children`);
    return parseWithFallback(
      raw,
      ChildIssuesResponseSchema,
      { issues: [] },
      {
        endpoint: "GET /api/issues/:id/children",
      },
    );
  }

  /** Batched variant — returns children for multiple parents in one request.
   *  Avoids an N-request fan-out in Swimlane (one per visible parent lane).
   *  parentIds must be non-empty; pass a sorted, deduplicated list so the
   *  React Query cache key is stable across renders. */
  async listChildrenByParents(
    parentIds: string[],
  ): Promise<{ issues: Issue[] }> {
    const raw = await this.fetch<unknown>(
      `/api/issues/children?parent_ids=${parentIds.join(",")}`,
    );
    return parseWithFallback(
      raw,
      ChildIssuesResponseSchema,
      { issues: [] },
      {
        endpoint: "GET /api/issues/children",
      },
    );
  }

  async getChildIssueProgress(): Promise<{
    progress: { parent_issue_id: string; total: number; done: number }[];
  }> {
    return this.fetch("/api/issues/child-progress");
  }

  async deleteIssue(id: string): Promise<void> {
    await this.fetch(`/api/issues/${id}`, { method: "DELETE" });
  }

  async batchUpdateIssues(
    issueIds: string[],
    updates: UpdateIssueRequest,
  ): Promise<{ updated: number }> {
    return this.fetch("/api/issues/batch-update", {
      method: "POST",
      body: JSON.stringify({ issue_ids: issueIds, updates }),
    });
  }

  async batchDeleteIssues(issueIds: string[]): Promise<{ deleted: number }> {
    return this.fetch("/api/issues/batch-delete", {
      method: "POST",
      body: JSON.stringify({ issue_ids: issueIds }),
    });
  }

  // Comments
  async listComments(issueId: string): Promise<Comment[]> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/comments`);
    return parseWithFallback(raw, CommentsListSchema, [], {
      endpoint: "GET /api/issues/:id/comments",
    });
  }

  async createComment(
    issueId: string,
    content: string,
    type?: string,
    parentId?: string,
    attachmentIds?: string[],
    suppressAgentIds?: string[],
  ): Promise<Comment> {
    return this.fetch(`/api/issues/${issueId}/comments`, {
      method: "POST",
      body: JSON.stringify({
        content,
        type: type ?? "comment",
        ...(parentId ? { parent_id: parentId } : {}),
        ...(attachmentIds?.length ? { attachment_ids: attachmentIds } : {}),
        ...(suppressAgentIds?.length
          ? { suppress_agent_ids: suppressAgentIds }
          : {}),
      }),
    });
  }

  async previewCommentTriggers(
    issueId: string,
    content: string,
    parentId?: string,
    editingCommentId?: string,
  ): Promise<CommentTriggerPreview> {
    const raw = await this.fetch<unknown>(
      `/api/issues/${issueId}/comments/trigger-preview`,
      {
        method: "POST",
        body: JSON.stringify({
          content,
          ...(parentId ? { parent_id: parentId } : {}),
          ...(editingCommentId ? { editing_comment_id: editingCommentId } : {}),
        }),
      },
    );
    return parseWithFallback(
      raw,
      CommentTriggerPreviewSchema,
      { agents: [] },
      {
        endpoint: "POST /api/issues/:id/comments/trigger-preview",
      },
    );
  }

  /** Dry-run the unified run-enqueue predicate for a prospective issue write
   *  (create / single assign / single status / batch). Returns the runs that
   *  would start; no side effect. The four entry points consult this instead
   *  of re-implementing the rule (MUL-3375). */
  async previewIssueTrigger(
    params: IssueTriggerPreviewParams,
  ): Promise<IssueTriggerPreview> {
    const raw = await this.fetch<unknown>("/api/issues/preview-trigger", {
      method: "POST",
      body: JSON.stringify({
        ...(params.issueIds?.length ? { issue_ids: params.issueIds } : {}),
        ...(params.isCreate ? { is_create: true } : {}),
        ...(params.assigneeType ? { assignee_type: params.assigneeType } : {}),
        ...(params.assigneeId ? { assignee_id: params.assigneeId } : {}),
        ...(params.status ? { status: params.status } : {}),
      }),
    });
    return parseWithFallback(
      raw,
      IssueTriggerPreviewSchema,
      { triggers: [], total_count: 0 },
      {
        endpoint: "POST /api/issues/preview-trigger",
      },
    );
  }

  async listTimeline(issueId: string): Promise<TimelineEntry[]> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/timeline`);
    return parseWithFallback(
      raw,
      TimelineEntriesSchema,
      EMPTY_TIMELINE_ENTRIES,
      {
        endpoint: "GET /api/issues/:id/timeline",
      },
    );
  }

  async getAssigneeFrequency(): Promise<AssigneeFrequencyEntry[]> {
    return this.fetch("/api/assignee-frequency");
  }

  async updateComment(
    commentId: string,
    content: string,
    attachmentIds?: string[],
    suppressAgentIds?: string[],
  ): Promise<Comment> {
    return this.fetch(`/api/comments/${commentId}`, {
      method: "PUT",
      body: JSON.stringify({
        content,
        attachment_ids: attachmentIds,
        ...(suppressAgentIds?.length
          ? { suppress_agent_ids: suppressAgentIds }
          : {}),
      }),
    });
  }

  async deleteComment(commentId: string): Promise<void> {
    await this.fetch(`/api/comments/${commentId}`, { method: "DELETE" });
  }

  async resolveComment(commentId: string): Promise<Comment> {
    return this.fetch(`/api/comments/${commentId}/resolve`, { method: "POST" });
  }

  async unresolveComment(commentId: string): Promise<Comment> {
    return this.fetch(`/api/comments/${commentId}/resolve`, {
      method: "DELETE",
    });
  }

  async addReaction(commentId: string, emoji: string): Promise<Reaction> {
    return this.fetch(`/api/comments/${commentId}/reactions`, {
      method: "POST",
      body: JSON.stringify({ emoji }),
    });
  }

  async removeReaction(commentId: string, emoji: string): Promise<void> {
    await this.fetch(`/api/comments/${commentId}/reactions`, {
      method: "DELETE",
      body: JSON.stringify({ emoji }),
    });
  }

  async addIssueReaction(
    issueId: string,
    emoji: string,
  ): Promise<IssueReaction> {
    return this.fetch(`/api/issues/${issueId}/reactions`, {
      method: "POST",
      body: JSON.stringify({ emoji }),
    });
  }

  async removeIssueReaction(issueId: string, emoji: string): Promise<void> {
    await this.fetch(`/api/issues/${issueId}/reactions`, {
      method: "DELETE",
      body: JSON.stringify({ emoji }),
    });
  }

  // Subscribers
  async listIssueSubscribers(issueId: string): Promise<IssueSubscriber[]> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/subscribers`);
    return parseWithFallback(raw, SubscribersListSchema, [], {
      endpoint: "GET /api/issues/:id/subscribers",
    });
  }

  async subscribeToIssue(
    issueId: string,
    userId?: string,
    userType?: string,
  ): Promise<void> {
    const body: Record<string, string> = {};
    if (userId) body.user_id = userId;
    if (userType) body.user_type = userType;
    await this.fetch(`/api/issues/${issueId}/subscribe`, {
      method: "POST",
      body: JSON.stringify(body),
    });
  }

  async unsubscribeFromIssue(
    issueId: string,
    userId?: string,
    userType?: string,
  ): Promise<void> {
    await this.fetch(`/api/issues/${issueId}/unsubscribe`, {
      method: "POST",
      body: JSON.stringify(subscriberTarget(userId, userType)),
    });
  }

  /**
   * Leaves this issue and every descendant, and keeps future children of the
   * tree from re-subscribing the user — the escape hatch for an agent-built
   * tree that keeps growing (MUL-5483).
   *
   * Deliberately its own endpoint rather than a `subtree` flag on
   * `unsubscribeFromIssue`. Web/desktop staging ships on merge while the
   * backend is deployed by hand, so this client regularly runs against an
   * older server; one that predates the feature would ignore an unknown body
   * field, unsubscribe only the root, and still answer 200. A distinct path
   * 404s there, which surfaces as a failed mutation instead of a silent lie.
   */
  async unsubscribeFromIssueSubtree(
    issueId: string,
    userId?: string,
    userType?: string,
  ): Promise<void> {
    await this.fetch(`/api/issues/${issueId}/unsubscribe/subtree`, {
      method: "POST",
      body: JSON.stringify(subscriberTarget(userId, userType)),
    });
  }

  // Agents
  async listAgents(params?: {
    workspace_id?: string;
    include_archived?: boolean;
  }): Promise<Agent[]> {
    const search = new URLSearchParams();
    if (params?.workspace_id) search.set("workspace_id", params.workspace_id);
    if (params?.include_archived) search.set("include_archived", "true");
    const raw = await this.fetch<unknown>(`/api/agents?${search}`);
    return parseWithFallback<Agent[]>(raw, AgentResponseListSchema, [], {
      endpoint: "GET /api/agents",
    });
  }

  async getAgent(id: string): Promise<Agent> {
    const raw = await this.fetch<unknown>(`/api/agents/${id}`);
    return parseWithFallback(raw, AgentResponseSchema, EMPTY_AGENT_RESPONSE, {
      endpoint: "GET /api/agents/:id",
    });
  }

  async createAgent(data: CreateAgentRequest): Promise<Agent> {
    const raw = await this.fetch<unknown>("/api/agents", {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, AgentResponseSchema, EMPTY_AGENT_RESPONSE, {
      endpoint: "POST /api/agents",
    });
  }

  /**
   * Provisions the workspace's built-in Chief of Staff, or returns the
   * existing one.
   *
   * Only a runtime and a language are sent: name, description, avatar,
   * permissions, and the system instruction layer are server constants, so a
   * client cannot mint an agent that would claim them. The server is also the
   * idempotency boundary — calling twice yields the same agent.
   */
  async createMikaAgent(
    data: {
      runtime_id: string;
      language: "en" | "zh" | "ko" | "ja";
      /** Empty means "whatever the runtime defaults to". */
      model?: string;
      /** Label for the onboarding conversation, used only if this call is the
       *  one that creates it. The session's identity is the member and Mika,
       *  never this string — it is localized. */
      session_title?: string;
    },
    workspaceSlug?: string,
  ): Promise<MikaBootstrapResponse> {
    return this.fetch("/api/agents/mika", {
      method: "POST",
      headers: workspaceHeader(workspaceSlug),
      body: JSON.stringify(data),
    });
  }

  async createAgentBuilderSession(data: {
    runtime_id: string;
    model?: string;
  }): Promise<AgentBuilderSession> {
    const raw = await this.fetch<unknown>("/api/agent-builder/sessions", {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseWithFallback(
      raw,
      AgentBuilderSessionSchema,
      EMPTY_AGENT_BUILDER_SESSION,
      { endpoint: "POST /api/agent-builder/sessions" },
    );
  }

  /**
   * The caller's unfinished agent-creation conversations.
   *
   * Builder sessions are hidden from every chat list (their carrier agent is
   * `kind = 'system'`), so this is the only route back to one. A 404 means the
   * backend predates the endpoint: degrade to "no drafts" instead of erroring
   * the Agents page, exactly as listChatDraftRestores does.
   */
  async listAgentBuilderSessions(): Promise<AgentBuilderSessionSummary[]> {
    let raw: unknown;
    try {
      raw = await this.fetch<unknown>("/api/agent-builder/sessions");
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) return [];
      throw err;
    }
    return parseWithFallback(
      raw,
      AgentBuilderSessionListSchema,
      EMPTY_AGENT_BUILDER_SESSION_LIST,
      { endpoint: "GET /api/agent-builder/sessions" },
    ).sessions;
  }

  /**
   * Stores the configuration a creation conversation has arrived at, including
   * edits the user typed but has not sent. Whole-object last-write-wins: one
   * conversation has one editor, so a field-level merge could only reconstruct
   * a state nobody saw. Read back through `listAgentBuilderSessions`.
   */
  async saveAgentBuilderDraft(
    sessionId: string,
    draft: StoredAgentDraft,
  ): Promise<void> {
    await this.fetch(`/api/agent-builder/sessions/${sessionId}/draft`, {
      method: "PUT",
      body: JSON.stringify({ draft }),
    });
  }

  /** Rebinds a live builder conversation to another runtime. Callers must not
   *  show the new runtime as selected until this resolves — the whole point is
   *  that the UI's runtime and the executing runtime agree.
   *
   *  A non-2xx throws before we get here and nothing was committed. Reaching the
   *  parse means the server bound `data.runtime_id`, so that is the fallback for
   *  an unparseable body — see agentBuilderRuntimeSwitchFallback. */
  async switchAgentBuilderRuntime(
    sessionId: string,
    data: { runtime_id: string },
  ): Promise<AgentBuilderRuntimeSwitch> {
    const raw = await this.fetch<unknown>(
      `/api/agent-builder/sessions/${sessionId}/runtime`,
      { method: "PATCH", body: JSON.stringify(data) },
    );
    return parseWithFallback(
      raw,
      AgentBuilderRuntimeSwitchSchema,
      agentBuilderRuntimeSwitchFallback(data.runtime_id),
      { endpoint: "PATCH /api/agent-builder/sessions/{id}/runtime" },
    );
  }

  async listAgentTemplates(): Promise<AgentTemplateSummary[]> {
    const raw = await this.fetch<unknown>("/api/agent-templates");
    return parseWithFallback(
      raw,
      AgentTemplateSummaryListSchema,
      EMPTY_AGENT_TEMPLATE_SUMMARY_LIST,
      { endpoint: "GET /api/agent-templates" },
    );
  }

  async getAgentTemplate(slug: string): Promise<AgentTemplate> {
    const raw = await this.fetch<unknown>(
      `/api/agent-templates/${encodeURIComponent(slug)}`,
    );
    // Round-trip the requested slug into the fallback so a malformed
    // detail response still produces a navigable record matching the URL
    // the user clicked.
    return parseWithFallback(
      raw,
      AgentTemplateSchema,
      { ...EMPTY_AGENT_TEMPLATE_DETAIL, slug },
      { endpoint: "GET /api/agent-templates/:slug" },
    );
  }

  /** Creates an agent from a curated template. The server fetches every
   *  referenced skill URL in parallel, materializes them into the workspace
   *  (find-or-create by name), and writes the agent + skill bindings in a
   *  single transaction. On any upstream fetch failure, the entire write is
   *  rolled back and the API returns 422 with `failed_urls`. */
  async createAgentFromTemplate(
    data: CreateAgentFromTemplateRequest,
  ): Promise<CreateAgentFromTemplateResponse> {
    const raw = await this.fetch<unknown>("/api/agents/from-template", {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseWithFallback(
      raw,
      CreateAgentFromTemplateResponseSchema,
      EMPTY_CREATE_AGENT_FROM_TEMPLATE_RESPONSE,
      { endpoint: "POST /api/agents/from-template" },
    );
  }
  async updateAgent(id: string, data: UpdateAgentRequest): Promise<Agent> {
    const raw = await this.fetch<unknown>(`/api/agents/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, AgentResponseSchema, EMPTY_AGENT_RESPONSE, {
      endpoint: "PUT /api/agents/:id",
    });
  }

  async transferAgentOwner(id: string, ownerId: string): Promise<Agent> {
    const raw = await this.fetch<unknown>(`/api/agents/${id}/owner`, {
      method: "PUT",
      body: JSON.stringify({ owner_id: ownerId }),
    });
    return parseWithFallback(raw, AgentResponseSchema, EMPTY_AGENT_RESPONSE, {
      endpoint: "PUT /api/agents/:id/owner",
    });
  }

  async transferSkillOwner(id: string, ownerId: string): Promise<Skill> {
    return this.fetch(`/api/skills/${id}/owner`, {
      method: "PUT",
      body: JSON.stringify({ owner_id: ownerId }),
    });
  }

  async transferSquadOwner(id: string, ownerId: string): Promise<Squad> {
    const raw = await this.fetch<unknown>(`/api/squads/${id}/owner`, {
      method: "PUT",
      body: JSON.stringify({ owner_id: ownerId }),
    });
    return parseWithFallback(raw, SquadSchema, EMPTY_SQUAD, {
      endpoint: "PUT /api/squads/:id/owner",
    }) as Squad;
  }

  async transferAutopilotOwner(id: string, ownerId: string): Promise<Autopilot> {
    return this.fetch(`/api/autopilots/${id}/owner`, {
      method: "PUT",
      body: JSON.stringify({ owner_id: ownerId }),
    });
  }

  async transferRuntimeOwner(id: string, ownerId: string): Promise<AgentRuntime> {
    return this.fetch(`/api/runtimes/${id}/owner`, {
      method: "PUT",
      body: JSON.stringify({ owner_id: ownerId }),
    });
  }

  /**
   * The composed inbound task instruction for one agent, produced by the same
   * server function the claim path uses. Falls back to an empty preview so an
   * older backend renders an explanatory empty state rather than crashing the
   * settings dialog.
   */
  async getAgentDispatchPromptPreview(
    id: string,
    surface: string,
  ): Promise<DispatchPromptPreview> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${id}/dispatch-prompt-preview?surface=${encodeURIComponent(surface)}`,
    );
    return parseWithFallback(
      raw,
      DispatchPromptPreviewSchema,
      { surface, segments: [], instruction: "", runtime_sections: [] },
      { endpoint: "GET /api/agents/{id}/dispatch-prompt-preview" },
    );
  }

  /**
   * Draft persona and reply_tone from this agent's instructions. Does not
   * persist; the Instructions tab fills the fields so the owner can edit
   * and save. Falls back to empty strings when an older backend omits the
   * endpoint shape.
   */
  async extractAgentVoice(
    id: string,
    instructions?: string,
  ): Promise<{ persona: string; reply_tone: string }> {
    const raw = await this.fetch<unknown>(`/api/agents/${id}/extract-voice`, {
      method: "POST",
      body: JSON.stringify(
        instructions === undefined ? {} : { instructions },
      ),
    });
    return parseWithFallback(
      raw,
      ExtractAgentVoiceResponseSchema,
      EMPTY_EXTRACT_AGENT_VOICE_RESPONSE,
      { endpoint: "POST /api/agents/{id}/extract-voice" },
    );
  }

  async listAgentOKRs(id: string): Promise<AgentOKRResponse> {
    const raw = await this.fetch<unknown>(`/api/agents/${id}/okrs`);
    return parseWithFallback(
      raw,
      AgentOKRResponseSchema,
      { okrs: [], usage_available: false },
      { endpoint: "GET /api/agents/{id}/okrs" },
    );
  }

  async setAgentOKRs(
    id: string,
    okrs: { objective: string; key_results: string[] }[],
  ): Promise<AgentOKRResponse> {
    const raw = await this.fetch<unknown>(`/api/agents/${id}/okrs`, {
      method: "PUT",
      body: JSON.stringify({ okrs }),
    });
    return parseWithFallback(
      raw,
      AgentOKRResponseSchema,
      { okrs: [], usage_available: false },
      { endpoint: "PUT /api/agents/{id}/okrs" },
    );
  }

  async archiveAgent(id: string): Promise<Agent> {
    const raw = await this.fetch<unknown>(`/api/agents/${id}/archive`, { method: "POST" });
    return parseWithFallback(raw, AgentResponseSchema, EMPTY_AGENT_RESPONSE, {
      endpoint: "POST /api/agents/:id/archive",
    });
  }

  /**
   * Returns the plaintext `custom_env` map for an agent. Admits the
   * agent's owner or a workspace owner/admin (MUL-5438); calls from
   * agent-actor sessions get a 403. Every successful call writes an
   * `agent_env_revealed` activity_log row server-side. MUL-2600.
   */
  async getAgentEnv(id: string): Promise<AgentEnvResponse> {
    return this.fetch(`/api/agents/${id}/env`);
  }

  /**
   * Replaces an agent's `custom_env` wholesale. Values equal to
   * `"****"` are preserved server-side (the **** guard) so a partial
   * UI edit doesn't overwrite real secrets with the masked
   * placeholder. Admits the agent's owner or a workspace owner/admin
   * (MUL-5438); agent actors get a 403. Every successful call writes an
   * `agent_env_updated` activity_log row. MUL-2600.
   */
  async updateAgentEnv(
    id: string,
    data: UpdateAgentEnvRequest,
  ): Promise<AgentEnvResponse> {
    return this.fetch(`/api/agents/${id}/env`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

  async restoreAgent(id: string): Promise<Agent> {
    const raw = await this.fetch<unknown>(`/api/agents/${id}/restore`, { method: "POST" });
    return parseWithFallback(raw, AgentResponseSchema, EMPTY_AGENT_RESPONSE, {
      endpoint: "POST /api/agents/:id/restore",
    });
  }

  async listAgentRunnerBindings(
    agentId: string,
  ): Promise<RunnerMachineBindingList> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${agentId}/runner-bindings`,
    );
    return RunnerMachineBindingListSchema.parse(raw);
  }

  async listAccountRunnerBindings(): Promise<AccountRunnerBindingList | null> {
    const raw = await this.fetch<unknown>("/api/me/runner-bindings");
    return parseWithFallback<AccountRunnerBindingList | null>(
      raw,
      AccountRunnerBindingListSchema,
      null,
      {
        endpoint: "GET /api/me/runner-bindings",
        includeReceived: false,
      },
    );
  }

  async createAgentRunnerPairing(
    agentId: string,
  ): Promise<CreateRunnerPairingResponse> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${agentId}/runner-pairings`,
      { method: "POST" },
    );
    return CreateRunnerPairingResponseSchema.parse(raw);
  }

  async createAccountRunnerPairing(): Promise<CreateRunnerPairingResponse> {
    const raw = await this.fetch<unknown>("/api/me/runner-pairings", { method: "POST" });
    return CreateRunnerPairingResponseSchema.parse(raw);
  }

  async mountAgentRunnerMachine(agentId: string, machineId: string): Promise<void> {
    await this.fetch(`/api/agents/${agentId}/runner-mount`, {
      method: "PUT",
      body: JSON.stringify({ machine_id: machineId }),
    });
  }

  async renameAccountRunnerMachine(machineId: string, name: string): Promise<void> {
    await this.fetch(`/api/me/runner-machines/${machineId}`, { method: "PATCH", body: JSON.stringify({ name }) });
  }

  async revokeAccountRunnerMachine(machineId: string): Promise<void> {
    await this.fetch(`/api/me/runner-machines/${machineId}`, { method: "DELETE" });
  }

  async revokeAgentRunnerBinding(
    agentId: string,
    bindingId: string,
  ): Promise<void> {
    await this.fetch(`/api/agents/${agentId}/runner-bindings/${bindingId}`, {
      method: "DELETE",
    });
  }

  async disconnectAgentRunnerBinding(
    agentId: string,
    bindingId: string,
  ): Promise<void> {
    await this.fetch(
      `/api/agents/${agentId}/runner-bindings/${bindingId}/disconnect`,
      { method: "POST" },
    );
  }

  async createAgentRunnerReconnectCommand(
    agentId: string,
    bindingId: string,
  ): Promise<CreateRunnerReconnectCommandResponse> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${agentId}/runner-bindings/${bindingId}/reconnect-command`,
      { method: "POST" },
    );
    return CreateRunnerReconnectCommandResponseSchema.parse(raw);
  }

  async disconnectAccountRunnerBinding(bindingId: string): Promise<void> {
    await this.fetch(`/api/me/runner-bindings/${bindingId}/disconnect`, {
      method: "POST",
    });
  }

  async createAccountRunnerReconnectCommand(
    bindingId: string,
  ): Promise<CreateRunnerReconnectCommandResponse | null> {
    const raw = await this.fetch<unknown>(
      `/api/me/runner-bindings/${bindingId}/reconnect-command`,
      { method: "POST" },
    );
    return parseWithFallback<CreateRunnerReconnectCommandResponse | null>(
      raw,
      CreateRunnerReconnectCommandResponseSchema,
      null,
      {
        endpoint: "POST /api/me/runner-bindings/{bindingId}/reconnect-command",
        includeReceived: false,
      },
    );
  }

  async revokeAccountRunnerBinding(bindingId: string): Promise<void> {
    await this.fetch(`/api/me/runner-bindings/${bindingId}`, {
      method: "DELETE",
    });
  }

  async getRunnerDeviceAuthorization(
    userCode: string,
  ): Promise<RunnerDeviceAuthorization> {
    const raw = await this.fetch<unknown>(
      `/api/runner/device-authorizations/${encodeURIComponent(userCode)}`,
    );
    return RunnerDeviceAuthorizationSchema.parse(raw);
  }

  async finishRunnerDeviceAuthorization(
    userCode: string,
    action: "approve" | "deny",
  ): Promise<RunnerDeviceAuthorizationResult> {
    const raw = await this.fetch<unknown>(
      `/api/runner/device-authorizations/${encodeURIComponent(userCode)}/${action}`,
      { method: "POST" },
    );
    return RunnerDeviceAuthorizationResultSchema.parse(raw);
  }

  // Bulk-cancel every active task (queued/dispatched/running) for the agent.
  // Permission: agent owner or workspace admin/owner. Server returns the
  // count of cancelled rows; broadcasts task:cancelled for each so other
  // surfaces can clear their live cards.
  async cancelAgentTasks(id: string): Promise<{ cancelled: number }> {
    return this.fetch(`/api/agents/${id}/cancel-tasks`, { method: "POST" });
  }

  async listRuntimes(
    params?: { workspace_id?: string; owner?: "me" },
    workspaceSlug?: string,
  ): Promise<AgentRuntime[]> {
    const search = new URLSearchParams();
    if (params?.workspace_id) search.set("workspace_id", params.workspace_id);
    if (params?.owner) search.set("owner", params.owner);
    // workspace_id alone is not enough: the server resolves the workspace from
    // the slug header first, so a caller listing another workspace's runtimes
    // must override the header too.
    return this.fetch(`/api/runtimes?${search}`, {
      headers: workspaceHeader(workspaceSlug),
    });
  }

  async createFCE2BRuntime(
    data: CreateFCE2BRuntimeRequest,
  ): Promise<AgentRuntime> {
    return this.fetch("/api/runtimes/fc-e2b", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async createCloudSandboxRuntime(
    data: CreateCloudSandboxRuntimeRequest,
  ): Promise<AgentRuntime> {
    return this.fetch("/api/runtimes/cloud-sandbox", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async listFCE2BTemplates(): Promise<FCE2BTemplate[]> {
    return this.fetch("/api/runtimes/fc-e2b/templates");
  }

  async getFCE2BStableChannel(): Promise<FCE2BStableChannel> {
    const raw = await this.fetch<unknown>(
      "/api/runtimes/fc-e2b/stable-channel",
    );
    return parseWithFallback(
      raw,
      FCE2BStableChannelSchema,
      EMPTY_FC_E2B_STABLE_CHANNEL,
      { endpoint: "GET /api/runtimes/fc-e2b/stable-channel" },
    );
  }

  async getCloudSandboxStableChannel(
    backend: SandboxBackend,
    providerScope?: string,
  ): Promise<FCE2BStableChannel> {
    const search = new URLSearchParams({ sandbox_backend: backend });
    if (providerScope !== undefined) search.set("provider_scope", providerScope);
    const raw = await this.fetch<unknown>(
      `/api/runtimes/cloud-sandbox/stable-channel?${search.toString()}`,
    );
    return parseWithFallback(
      raw,
      FCE2BStableChannelSchema,
      EMPTY_FC_E2B_STABLE_CHANNEL,
      { endpoint: "GET /api/runtimes/cloud-sandbox/stable-channel" },
    );
  }

  async createFCE2BStableRelease(
    data: CreateFCE2BStableReleaseRequest,
    idempotencyKey: string,
  ): Promise<FCE2BStableRelease> {
    const raw = await this.fetch<unknown>(
      "/api/runtimes/fc-e2b/stable-releases",
      {
        method: "POST",
        body: JSON.stringify(data),
        headers: { "Idempotency-Key": idempotencyKey },
      },
    );
    return parseWithFallback(
      raw,
      FCE2BStableReleaseSchema,
      EMPTY_FC_E2B_STABLE_RELEASE,
      { endpoint: "POST /api/runtimes/fc-e2b/stable-releases" },
    );
  }

  async createCloudSandboxStableRelease(
    data: CreateCloudSandboxStableReleaseRequest,
    idempotencyKey: string,
  ): Promise<FCE2BStableRelease> {
    const raw = await this.fetch<unknown>(
      "/api/runtimes/cloud-sandbox/stable-releases",
      {
        method: "POST",
        body: JSON.stringify(data),
        headers: { "Idempotency-Key": idempotencyKey },
      },
    );
    return parseWithFallback(
      raw,
      FCE2BStableReleaseSchema,
      EMPTY_FC_E2B_STABLE_RELEASE,
      { endpoint: "POST /api/runtimes/cloud-sandbox/stable-releases" },
    );
  }

  async getFCE2BStableRelease(releaseId: string): Promise<FCE2BStableRelease> {
    const raw = await this.fetch<unknown>(
      `/api/runtimes/fc-e2b/stable-releases/${releaseId}`,
    );
    return parseWithFallback(
      raw,
      FCE2BStableReleaseSchema,
      EMPTY_FC_E2B_STABLE_RELEASE,
      { endpoint: "GET /api/runtimes/fc-e2b/stable-releases/:id" },
    );
  }

  async listCloudSandboxStableReleases(
    backend: SandboxBackend,
    limit = 20,
    providerScope?: string,
  ): Promise<FCE2BStableRelease[]> {
    const search = new URLSearchParams({
      sandbox_backend: backend,
      limit: String(limit),
    });
    if (providerScope !== undefined) search.set("provider_scope", providerScope);
    const raw = await this.fetch<unknown>(
      `/api/runtimes/cloud-sandbox/stable-releases?${search.toString()}`,
    );
    return parseWithFallback(raw, FCE2BStableReleaseListSchema, [], {
      endpoint: "GET /api/runtimes/cloud-sandbox/stable-releases",
    });
  }

  async listFCE2BStableRuntimes(): Promise<FCE2BStableRuntimeOverview[]> {
    const raw = await this.fetch<unknown>(
      "/api/runtimes/fc-e2b/stable-runtimes",
    );
    return parseWithFallback(raw, FCE2BStableRuntimeOverviewListSchema, [], {
      endpoint: "GET /api/runtimes/fc-e2b/stable-runtimes",
    });
  }

  async listCloudSandboxStableRuntimes(
    backend: SandboxBackend,
    providerScope?: string,
  ): Promise<FCE2BStableRuntimeOverview[]> {
    const search = new URLSearchParams({ sandbox_backend: backend });
    if (providerScope !== undefined) search.set("provider_scope", providerScope);
    const raw = await this.fetch<unknown>(
      `/api/runtimes/cloud-sandbox/stable-runtimes?${search.toString()}`,
    );
    return parseWithFallback(raw, FCE2BStableRuntimeOverviewListSchema, [], {
      endpoint: "GET /api/runtimes/cloud-sandbox/stable-runtimes",
    });
  }

  async mutateFCE2BStableRelease(
    releaseId: string,
    action: FCE2BStableReleaseAction,
  ): Promise<FCE2BStableRelease> {
    const raw = await this.fetch<unknown>(
      `/api/runtimes/fc-e2b/stable-releases/${releaseId}/${action}`,
      { method: "POST" },
    );
    return parseWithFallback(
      raw,
      FCE2BStableReleaseSchema,
      EMPTY_FC_E2B_STABLE_RELEASE,
      { endpoint: `POST /api/runtimes/fc-e2b/stable-releases/:id/${action}` },
    );
  }

  async mutateCloudSandboxStableRelease(
    releaseId: string,
    action: FCE2BStableReleaseAction,
  ): Promise<FCE2BStableRelease> {
    const raw = await this.fetch<unknown>(
      `/api/runtimes/cloud-sandbox/stable-releases/${encodeURIComponent(releaseId)}/${action}`,
      { method: "POST" },
    );
    return parseWithFallback(
      raw,
      FCE2BStableReleaseSchema,
      EMPTY_FC_E2B_STABLE_RELEASE,
      {
        endpoint: `POST /api/runtimes/cloud-sandbox/stable-releases/:id/${action}`,
      },
    );
  }

  async updateFCE2BRuntimeTemplate(
    runtimeId: string,
    data: UpdateFCE2BRuntimeTemplateRequest,
  ): Promise<AgentRuntime> {
    return this.fetch<AgentRuntime>(
      `/api/runtimes/${runtimeId}/fc-e2b-template`,
      {
        method: "PATCH",
        body: JSON.stringify(data),
      },
    );
  }

  async updateCloudSandboxRuntimeArtifact(
    runtimeId: string,
    data: UpdateCloudSandboxRuntimeArtifactRequest,
  ): Promise<AgentRuntime> {
    return this.fetch<AgentRuntime>(
      `/api/runtimes/${encodeURIComponent(runtimeId)}/cloud-sandbox-artifact`,
      {
        method: "PATCH",
        body: JSON.stringify(data),
      },
    );
  }

  async getASBNetworkPolicy(runtimeId: string) {
    const endpoint = `/api/runtimes/${encodeURIComponent(runtimeId)}/asb-network-policy`;
    const raw = await this.fetch<unknown>(endpoint);
    return parseWithFallback(raw, ASBNetworkPolicySchema, EMPTY_ASB_NETWORK_POLICY, { endpoint });
  }

  async updateASBNetworkPolicy(runtimeId: string, customTargets: string[]) {
    const endpoint = `/api/runtimes/${encodeURIComponent(runtimeId)}/asb-network-policy`;
    const raw = await this.fetch<unknown>(endpoint, { method: "PUT", body: JSON.stringify({ custom_targets: customTargets }) });
    return parseWithFallback(raw, ASBNetworkPolicySchema, EMPTY_ASB_NETWORK_POLICY, { endpoint });
  }

  async getASBRegions(runtimeId: string) {
    const endpoint = `/api/runtimes/${encodeURIComponent(runtimeId)}/asb-regions`;
    const raw = await this.fetch<unknown>(endpoint);
    return parseWithFallback(raw, ASBRegionsSchema, EMPTY_ASB_REGIONS, { endpoint });
  }

  async validateASBRuntimeCredential(
    data: ValidateASBRuntimeCredentialRequest,
  ): Promise<ValidateASBRuntimeCredentialResponse> {
    return this.fetch<ValidateASBRuntimeCredentialResponse>(
      "/api/runtimes/asb-credential/validate",
      { method: "POST", body: JSON.stringify(data) },
    );
  }

  async updateASBRuntimeCredential(
    runtimeId: string,
    data: UpdateASBRuntimeCredentialRequest,
  ): Promise<ASBRuntimeCredentialResponse> {
    return this.fetch<ASBRuntimeCredentialResponse>(
      `/api/runtimes/${encodeURIComponent(runtimeId)}/asb-credential`,
      {
        method: "PATCH",
        body: JSON.stringify(data),
      },
    );
  }

  async getASBRuntimeCredential(
    runtimeId: string,
  ): Promise<ASBRuntimeCredentialResponse> {
    return this.fetch<ASBRuntimeCredentialResponse>(
      `/api/runtimes/${encodeURIComponent(runtimeId)}/asb-credential`,
    );
  }

  async listCloudRuntimeNodes(
    params?: ListCloudRuntimeNodesParams,
  ): Promise<CloudRuntimeNode[]> {
    const search = new URLSearchParams();
    if (params?.limit !== undefined) search.set("limit", String(params.limit));
    if (params?.offset !== undefined)
      search.set("offset", String(params.offset));
    const query = search.toString();
    const raw = await this.fetch<unknown>(
      `/api/cloud-runtime/nodes${query ? `?${query}` : ""}`,
    );
    return parseWithFallback(
      raw,
      CloudRuntimeNodeListSchema,
      EMPTY_CLOUD_RUNTIME_NODE_LIST,
      { endpoint: "GET /api/cloud-runtime/nodes" },
    );
  }

  async createCloudRuntimeNode(
    data: CreateCloudRuntimeNodeRequest,
  ): Promise<CloudRuntimeNode> {
    const res = await this.fetchRaw("/api/cloud-runtime/nodes", {
      method: "POST",
      body: JSON.stringify(data),
      extraHeaders: { "Content-Type": "application/json" },
    });
    const raw = (await res.json()) as unknown;
    return parseWithFallback(
      raw,
      CloudRuntimeNodeSchema,
      EMPTY_CLOUD_RUNTIME_NODE,
      { endpoint: "POST /api/cloud-runtime/nodes" },
    );
  }

  async deleteCloudRuntimeNode(instanceId: string): Promise<void> {
    await this.fetchRaw("/api/cloud-runtime/nodes", {
      method: "DELETE",
      body: JSON.stringify({ instance_id: instanceId }),
      extraHeaders: { "Content-Type": "application/json" },
    });
  }

  // ---------------------------------------------------------------------
  // Cloud Billing — proxies to multica-cloud /api/v1/billing/*. The
  // multica-api server stamps X-User-ID and forwards bytes; everything
  // here is upstream-shaped. See packages/core/types/billing.ts for the
  // response field documentation.
  // ---------------------------------------------------------------------

  async getCloudBillingBalance(): Promise<BillingBalance> {
    const raw = await this.fetch<unknown>("/api/cloud-billing/balance");
    return parseWithFallback(raw, BillingBalanceSchema, EMPTY_BILLING_BALANCE, {
      endpoint: "GET /api/cloud-billing/balance",
    });
  }

  async listCloudBillingTransactions(params?: {
    page?: number;
    page_size?: number;
  }): Promise<BillingTransactionsPage> {
    const search = new URLSearchParams();
    if (params?.page !== undefined) search.set("page", String(params.page));
    if (params?.page_size !== undefined)
      search.set("page_size", String(params.page_size));
    const query = search.toString();
    const raw = await this.fetch<unknown>(
      `/api/cloud-billing/transactions${query ? `?${query}` : ""}`,
    );
    return parseWithFallback(
      raw,
      BillingTransactionsPageSchema,
      EMPTY_BILLING_TRANSACTIONS_PAGE,
      { endpoint: "GET /api/cloud-billing/transactions" },
    );
  }

  async listCloudBillingBatches(params?: {
    page?: number;
    page_size?: number;
  }): Promise<BillingBatchesPage> {
    const search = new URLSearchParams();
    if (params?.page !== undefined) search.set("page", String(params.page));
    if (params?.page_size !== undefined)
      search.set("page_size", String(params.page_size));
    const query = search.toString();
    const raw = await this.fetch<unknown>(
      `/api/cloud-billing/batches${query ? `?${query}` : ""}`,
    );
    return parseWithFallback(
      raw,
      BillingBatchesPageSchema,
      EMPTY_BILLING_BATCHES_PAGE,
      { endpoint: "GET /api/cloud-billing/batches" },
    );
  }

  async listCloudBillingTopups(params?: {
    page?: number;
    page_size?: number;
  }): Promise<BillingTopupsPage> {
    const search = new URLSearchParams();
    if (params?.page !== undefined) search.set("page", String(params.page));
    if (params?.page_size !== undefined)
      search.set("page_size", String(params.page_size));
    const query = search.toString();
    const raw = await this.fetch<unknown>(
      `/api/cloud-billing/topups${query ? `?${query}` : ""}`,
    );
    return parseWithFallback(
      raw,
      BillingTopupsPageSchema,
      EMPTY_BILLING_TOPUPS_PAGE,
      { endpoint: "GET /api/cloud-billing/topups" },
    );
  }

  async listCloudBillingPriceTiers(): Promise<BillingPriceTier[]> {
    const raw = await this.fetch<unknown>("/api/cloud-billing/price-tiers");
    return parseWithFallback(
      raw,
      BillingPriceTierListSchema,
      EMPTY_BILLING_PRICE_TIER_LIST,
      { endpoint: "GET /api/cloud-billing/price-tiers" },
    );
  }

  async createCloudBillingCheckoutSession(
    data: CreateBillingCheckoutSessionRequest,
  ): Promise<CreateBillingCheckoutSessionResponse> {
    const res = await this.fetchRaw("/api/cloud-billing/checkout-sessions", {
      method: "POST",
      body: JSON.stringify(data),
      extraHeaders: { "Content-Type": "application/json" },
    });
    const raw = (await res.json()) as unknown;
    return parseWithFallback(
      raw,
      CreateBillingCheckoutSessionResponseSchema,
      EMPTY_CREATE_BILLING_CHECKOUT_SESSION_RESPONSE,
      { endpoint: "POST /api/cloud-billing/checkout-sessions" },
    );
  }

  async getCloudBillingCheckoutSession(
    sessionId: string,
  ): Promise<BillingCheckoutSessionStatus> {
    // Stripe session ids are `cs_<base62>` so they're URL-safe by
    // construction; encodeURIComponent is paranoia for the case where a
    // future Stripe format change adds a non-alphanumeric character. The
    // server has its own allow-list rejection for unsafe ids.
    const raw = await this.fetch<unknown>(
      `/api/cloud-billing/checkout-sessions/${encodeURIComponent(sessionId)}`,
    );
    return parseWithFallback(
      raw,
      BillingCheckoutSessionStatusSchema,
      EMPTY_BILLING_CHECKOUT_SESSION_STATUS,
      { endpoint: "GET /api/cloud-billing/checkout-sessions/{sessionId}" },
    );
  }

  async createCloudBillingPortalSession(): Promise<CreateBillingPortalSessionResponse> {
    const res = await this.fetchRaw("/api/cloud-billing/portal-sessions", {
      method: "POST",
      // Body is intentionally absent — the upstream endpoint requires no
      // payload today. fetchRaw with no body skips the Content-Type
      // default; that's fine because there's nothing to declare.
    });
    const raw = (await res.json()) as unknown;
    return parseWithFallback(
      raw,
      CreateBillingPortalSessionResponseSchema,
      EMPTY_CREATE_BILLING_PORTAL_SESSION_RESPONSE,
      { endpoint: "POST /api/cloud-billing/portal-sessions" },
    );
  }

  async deleteRuntime(runtimeId: string): Promise<void> {
    await this.fetch(`/api/runtimes/${runtimeId}`, { method: "DELETE" });
  }

  // Confirmed variant of deleteRuntime. The strict DELETE refuses with
  // structured 409 (`code: "runtime_has_active_agents"`, body carries the
  // blocking agents) when active agents are bound; the front-end then opens
  // the confirmation dialog and submits the user-confirmed active agent set
  // here. Server compares the snapshot to the live set inside the transaction
  // and refuses with `code: "runtime_delete_plan_changed"` (same shape, fresh
  // `active_agents`) if they don't match — caller should re-render the agent
  // list and force the user to re-confirm.
  //
  // The agents are UNBOUND, not archived or deleted (MUL-5559): they keep their
  // configuration, chats and task history and need a new runtime to run again.
  // `agents_archived` is the server's deprecated mirror of `agents_unbound`,
  // kept because installed clients read it; prefer `agents_unbound`.
  async unbindAgentsAndDeleteRuntime(
    runtimeId: string,
    expectedActiveAgentIds: string[],
  ): Promise<{
    status: string;
    agents_unbound?: number;
    agents_archived?: number;
    tasks_cancelled: number;
    autopilots_paused?: number;
  }> {
    return this.fetch(`/api/runtimes/${runtimeId}/unbind-agents-and-delete`, {
      method: "POST",
      body: JSON.stringify({
        expected_active_agent_ids: expectedActiveAgentIds,
      }),
    });
  }

  async updateRuntime(
    runtimeId: string,
    patch: {
      visibility?: "private" | "public";
      /**
       * Custom display name. Pass an empty string to clear it (the server
       * reverts to the default name). Omit to leave it unchanged — a JSON
       * `null` is treated as "unchanged", not "clear". See MUL-4217.
       */
      custom_name?: string;
      /** Apply custom_name to every runtime on the same machine. */
      apply_to_machine?: boolean;
    },
  ): Promise<AgentRuntime> {
    return this.fetch(`/api/runtimes/${runtimeId}`, {
      method: "PATCH",
      body: JSON.stringify(patch),
    });
  }

  // ---------------------------------------------------------------------
  // Custom runtime profiles (MUL-3284). All workspace-scoped: the caller
  // passes the workspace id the same way the runtimes list resolves it.
  // ---------------------------------------------------------------------

  async listRuntimeProfiles(workspaceId: string): Promise<RuntimeProfile[]> {
    const res = await this.fetch<{ runtime_profiles?: RuntimeProfile[] }>(
      `/api/workspaces/${workspaceId}/runtime-profiles`,
    );
    return res.runtime_profiles ?? [];
  }

  async getRuntimeProfile(
    workspaceId: string,
    profileId: string,
  ): Promise<RuntimeProfile> {
    return this.fetch(
      `/api/workspaces/${workspaceId}/runtime-profiles/${profileId}`,
    );
  }

  async createRuntimeProfile(
    workspaceId: string,
    body: CreateRuntimeProfileRequest,
  ): Promise<RuntimeProfile> {
    return this.fetch(`/api/workspaces/${workspaceId}/runtime-profiles`, {
      method: "POST",
      body: JSON.stringify(body),
    });
  }

  async updateRuntimeProfile(
    workspaceId: string,
    profileId: string,
    patch: UpdateRuntimeProfileRequest,
  ): Promise<RuntimeProfile> {
    return this.fetch(
      `/api/workspaces/${workspaceId}/runtime-profiles/${profileId}`,
      {
        method: "PATCH",
        body: JSON.stringify(patch),
      },
    );
  }

  async deleteRuntimeProfile(
    workspaceId: string,
    profileId: string,
  ): Promise<void> {
    await this.fetch(
      `/api/workspaces/${workspaceId}/runtime-profiles/${profileId}`,
      { method: "DELETE" },
    );
  }

  async getRuntimeUsage(
    runtimeId: string,
    params?: { days?: number; tz?: string },
  ): Promise<RuntimeUsage[]> {
    const search = new URLSearchParams();
    if (params?.days) search.set("days", String(params.days));
    // `tz` drives the calendar-day boundary for the trend chart (Viewing
    // layer). Caller-supplied; the backend falls back to user.timezone /
    // UTC if omitted.
    if (params?.tz) search.set("tz", params.tz);
    const raw = await this.fetch<unknown>(
      `/api/runtimes/${runtimeId}/usage?${search}`,
    );
    return parseWithFallback<RuntimeUsage[]>(raw, RuntimeUsageListSchema, [], {
      endpoint: "GET /api/runtimes/:id/usage",
    });
  }

  async getRuntimeTaskActivity(
    runtimeId: string,
    params?: { tz?: string },
  ): Promise<RuntimeHourlyActivity[]> {
    // Hour-of-day heatmap follows the viewer's tz, like the other reports on
    // this page. Pass the viewer's IANA zone so the server buckets correctly.
    const search = new URLSearchParams();
    if (params?.tz) search.set("tz", params.tz);
    const raw = await this.fetch<unknown>(
      `/api/runtimes/${runtimeId}/activity?${search}`,
    );
    return parseWithFallback<RuntimeHourlyActivity[]>(
      raw,
      RuntimeHourlyActivityListSchema,
      [],
      { endpoint: "GET /api/runtimes/:id/activity" },
    );
  }

  async getRuntimeUsageByAgent(
    runtimeId: string,
    params?: { days?: number; tz?: string },
  ): Promise<RuntimeUsageByAgent[]> {
    const search = new URLSearchParams();
    if (params?.days) search.set("days", String(params.days));
    if (params?.tz) search.set("tz", params.tz);
    const raw = await this.fetch<unknown>(
      `/api/runtimes/${runtimeId}/usage/by-agent?${search}`,
    );
    return parseWithFallback<RuntimeUsageByAgent[]>(
      raw,
      RuntimeUsageByAgentListSchema,
      [],
      { endpoint: "GET /api/runtimes/:id/usage/by-agent" },
    );
  }

  async getRuntimeUsageByHour(
    runtimeId: string,
    params?: { days?: number; tz?: string },
  ): Promise<RuntimeUsageByHour[]> {
    const search = new URLSearchParams();
    if (params?.days) search.set("days", String(params.days));
    if (params?.tz) search.set("tz", params.tz);
    const raw = await this.fetch<unknown>(
      `/api/runtimes/${runtimeId}/usage/by-hour?${search}`,
    );
    return parseWithFallback<RuntimeUsageByHour[]>(
      raw,
      RuntimeUsageByHourListSchema,
      [],
      { endpoint: "GET /api/runtimes/:id/usage/by-hour" },
    );
  }

  // ---------------------------------------------------------------------------
  // Workspace dashboard — three independent rollups for `/{slug}/dashboard`.
  // Each accepts an optional `project_id` to narrow the scope to one project.
  // Cost is computed client-side from the model pricing table (same contract
  // as the per-runtime endpoints above).
  // ---------------------------------------------------------------------------

  async getDashboardUsageDaily(params: {
    days?: number;
    project_id?: string | null;
    tz?: string;
  }): Promise<DashboardUsageDaily[]> {
    const search = new URLSearchParams();
    if (params.days) search.set("days", String(params.days));
    if (params.project_id) search.set("project_id", params.project_id);
    if (params.tz) search.set("tz", params.tz);
    const raw = await this.fetch<unknown>(
      `/api/dashboard/usage/daily?${search}`,
    );
    return parseWithFallback<DashboardUsageDaily[]>(
      raw,
      DashboardUsageDailyListSchema,
      [],
      { endpoint: "GET /api/dashboard/usage/daily" },
    );
  }

  async getDashboardUsageByAgent(params: {
    days?: number;
    project_id?: string | null;
    tz?: string;
  }): Promise<DashboardUsageByAgent[]> {
    const search = new URLSearchParams();
    if (params.days) search.set("days", String(params.days));
    if (params.project_id) search.set("project_id", params.project_id);
    if (params.tz) search.set("tz", params.tz);
    const raw = await this.fetch<unknown>(
      `/api/dashboard/usage/by-agent?${search}`,
    );
    return parseWithFallback<DashboardUsageByAgent[]>(
      raw,
      DashboardUsageByAgentListSchema,
      [],
      { endpoint: "GET /api/dashboard/usage/by-agent" },
    );
  }

  async getDashboardAgentRunTime(params: {
    days?: number;
    project_id?: string | null;
    tz?: string;
  }): Promise<DashboardAgentRunTime[]> {
    const search = new URLSearchParams();
    if (params.days) search.set("days", String(params.days));
    if (params.project_id) search.set("project_id", params.project_id);
    // `tz` aligns the "last N days" cutoff with the viewer's calendar,
    // matching the per-agent token card.
    if (params.tz) search.set("tz", params.tz);
    const raw = await this.fetch<unknown>(
      `/api/dashboard/agent-runtime?${search}`,
    );
    return parseWithFallback<DashboardAgentRunTime[]>(
      raw,
      DashboardAgentRunTimeListSchema,
      [],
      { endpoint: "GET /api/dashboard/agent-runtime" },
    );
  }

  async getDashboardRunTimeDaily(params: {
    days?: number;
    project_id?: string | null;
    tz?: string;
  }): Promise<DashboardRunTimeDaily[]> {
    const search = new URLSearchParams();
    if (params.days) search.set("days", String(params.days));
    if (params.project_id) search.set("project_id", params.project_id);
    // `tz` cuts the day buckets in the viewer's calendar so Time / Tasks
    // align with the Cost / Tokens charts.
    if (params.tz) search.set("tz", params.tz);
    const raw = await this.fetch<unknown>(
      `/api/dashboard/runtime/daily?${search}`,
    );
    return parseWithFallback<DashboardRunTimeDaily[]>(
      raw,
      DashboardRunTimeDailyListSchema,
      [],
      { endpoint: "GET /api/dashboard/runtime/daily" },
    );
  }

  async getDashboardFailuresDaily(params: {
    days?: number;
    project_id?: string | null;
    tz?: string;
  }): Promise<DashboardFailureDaily[]> {
    const search = new URLSearchParams();
    if (params.days) search.set("days", String(params.days));
    if (params.project_id) search.set("project_id", params.project_id);
    // `tz` cuts the day buckets in the viewer's calendar so the Errors chart
    // shares an x-axis with the other four metrics.
    if (params.tz) search.set("tz", params.tz);
    const raw = await this.fetch<unknown>(
      `/api/dashboard/failures/daily?${search}`,
    );
    return parseWithFallback<DashboardFailureDaily[]>(
      raw,
      DashboardFailureDailyListSchema,
      [],
      { endpoint: "GET /api/dashboard/failures/daily" },
    );
  }

  async getDashboardFailuresByAgent(params: {
    days?: number;
    project_id?: string | null;
    tz?: string;
  }): Promise<DashboardFailureByAgent[]> {
    const search = new URLSearchParams();
    if (params.days) search.set("days", String(params.days));
    if (params.project_id) search.set("project_id", params.project_id);
    if (params.tz) search.set("tz", params.tz);
    const raw = await this.fetch<unknown>(
      `/api/dashboard/failures/by-agent?${search}`,
    );
    return parseWithFallback<DashboardFailureByAgent[]>(
      raw,
      DashboardFailureByAgentListSchema,
      [],
      { endpoint: "GET /api/dashboard/failures/by-agent" },
    );
  }

  async initiateUpdate(
    runtimeId: string,
    targetVersion: string,
  ): Promise<RuntimeUpdate> {
    return this.fetch(`/api/runtimes/${runtimeId}/update`, {
      method: "POST",
      body: JSON.stringify({ target_version: targetVersion }),
    });
  }

  async getUpdateResult(
    runtimeId: string,
    updateId: string,
  ): Promise<RuntimeUpdate> {
    return this.fetch(`/api/runtimes/${runtimeId}/update/${updateId}`);
  }

  // Both discovery endpoints feed a UI state machine (poll while
  // pending/running, then render or fail), so the response is validated rather
  // than cast: an unparseable body degrades to an explicit "failed" record that
  // shows the discovery error and keeps manual model entry usable, instead of a
  // fabricated empty catalog or an endless spinner (MUL-5444).
  async initiateListModels(
    runtimeId: string,
  ): Promise<RuntimeModelListRequest> {
    const raw = await this.fetch<unknown>(`/api/runtimes/${runtimeId}/models`, {
      method: "POST",
    });
    return parseWithFallback<RuntimeModelListRequest>(
      raw,
      RuntimeModelListRequestSchema,
      { ...MALFORMED_RUNTIME_MODEL_LIST_REQUEST, runtime_id: runtimeId },
      { endpoint: "POST /api/runtimes/{id}/models" },
    );
  }

  async getListModelsResult(
    runtimeId: string,
    requestId: string,
  ): Promise<RuntimeModelListRequest> {
    const raw = await this.fetch<unknown>(
      `/api/runtimes/${runtimeId}/models/${requestId}`,
    );
    return parseWithFallback<RuntimeModelListRequest>(
      raw,
      RuntimeModelListRequestSchema,
      {
        ...MALFORMED_RUNTIME_MODEL_LIST_REQUEST,
        id: requestId,
        runtime_id: runtimeId,
      },
      { endpoint: "GET /api/runtimes/{id}/models/{requestId}" },
    );
  }

  async initiateListLocalSkills(
    runtimeId: string,
  ): Promise<RuntimeLocalSkillListRequest> {
    return this.fetch(`/api/runtimes/${runtimeId}/local-skills`, {
      method: "POST",
    });
  }

  async getListLocalSkillsResult(
    runtimeId: string,
    requestId: string,
  ): Promise<RuntimeLocalSkillListRequest> {
    return this.fetch(`/api/runtimes/${runtimeId}/local-skills/${requestId}`);
  }

  async initiateImportLocalSkill(
    runtimeId: string,
    data: CreateRuntimeLocalSkillImportRequest,
  ): Promise<RuntimeLocalSkillImportRequest> {
    return this.fetch(`/api/runtimes/${runtimeId}/local-skills/import`, {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async getImportLocalSkillResult(
    runtimeId: string,
    requestId: string,
  ): Promise<RuntimeLocalSkillImportRequest> {
    return this.fetch(
      `/api/runtimes/${runtimeId}/local-skills/import/${requestId}`,
    );
  }

  async listAgentTasks(agentId: string, signal?: AbortSignal): Promise<AgentTask[]> {
    return this.fetch(`/api/agents/${agentId}/tasks`, { signal });
  }

  async listAgentCoordinatorConversations(
    agentId: string,
    offset = 0,
  ): Promise<CoordinatorConversationsPage> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${encodeURIComponent(agentId)}/coordinator-conversations?offset=${offset}`,
    );
    return parseWithFallback(
      raw,
      CoordinatorConversationsPageSchema,
      { conversations: [], has_more: false, next_offset: 0 },
      { endpoint: "GET /api/agents/{id}/coordinator-conversations" },
    );
  }

  async listAgentCoordinatorConversationMessages(
    agentId: string,
    sessionId: string,
    cursor?: { created_at: string; id: string } | null,
  ): Promise<ChatMessagesPage> {
    const params = new URLSearchParams({ limit: "50" });
    if (cursor) {
      params.set("before_created_at", cursor.created_at);
      params.set("before_id", cursor.id);
    }
    const raw = await this.fetch<unknown>(
      `/api/agents/${encodeURIComponent(agentId)}/coordinator-conversations/${encodeURIComponent(sessionId)}/messages?${params}`,
    );
    return parseWithFallback(
      raw,
      ChatMessagesPageSchema,
      { messages: [], limit: 50, has_more: false, next_cursor: null },
      {
        endpoint:
          "GET /api/agents/{id}/coordinator-conversations/{sessionId}/messages",
      },
    );
  }

  async listAgentCoordinatorSessions(agentId: string): Promise<ChatSession[]> {
    return this.fetch(`/api/agents/${agentId}/coordinator-sessions`);
  }

  async listAgentSceneMemory(agentId: string): Promise<AgentSceneMemory[]> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${encodeURIComponent(agentId)}/scene-memory`,
    );
    return parseWithFallback(
      raw,
      AgentSceneMemoryListSchema,
      EMPTY_AGENT_SCENE_MEMORY_LIST,
      { endpoint: "GET /api/agents/{id}/scene-memory" },
    );
  }

  /** One scene memory row by id (the scene detail's 记忆 sub-tab). A
   * malformed response parses to a row with an empty id. */
  async getAgentSceneMemory(agentId: string, memoryId: string): Promise<AgentSceneMemory> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${encodeURIComponent(agentId)}/scene-memory/${encodeURIComponent(memoryId)}`,
    );
    return parseWithFallback(
      raw,
      AgentSceneMemorySchema,
      EMPTY_AGENT_SCENE_MEMORY,
      { endpoint: "GET /api/agents/{id}/scene-memory/{memoryId}" },
    );
  }

  async updateAgentSceneMemory(
    agentId: string,
    memoryId: string,
    body: { memory_text: string; expected_revision: number },
  ): Promise<AgentSceneMemory> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${encodeURIComponent(agentId)}/scene-memory/${encodeURIComponent(memoryId)}`,
      { method: "PUT", body: JSON.stringify(body) },
    );
    return parseWithFallback(
      raw,
      AgentSceneMemorySchema,
      EMPTY_AGENT_SCENE_MEMORY,
      { endpoint: "PUT /api/agents/{id}/scene-memory/{memoryId}" },
    );
  }

  async resetAgentSceneMemory(
    agentId: string,
    memoryId: string,
  ): Promise<AgentSceneMemory> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${encodeURIComponent(agentId)}/scene-memory/${encodeURIComponent(memoryId)}/reset`,
      { method: "POST" },
    );
    return parseWithFallback(
      raw,
      AgentSceneMemorySchema,
      EMPTY_AGENT_SCENE_MEMORY,
      { endpoint: "POST /api/agents/{id}/scene-memory/{memoryId}/reset" },
    );
  }

  async clearAgentSceneRelations(
    agentId: string,
    memoryId: string,
  ): Promise<void> {
    await this.fetch(
      `/api/agents/${encodeURIComponent(agentId)}/scene-memory/${encodeURIComponent(memoryId)}/relations/clear`,
      { method: "POST" },
    );
  }

  async listAgentSceneRelations(
    agentId: string,
    conversationId: string,
  ): Promise<AgentSceneRelation[]> {
    const search = new URLSearchParams({
      agent_id: agentId,
      conversation_id: conversationId,
      since: "7d",
    });
    const raw = await this.fetch<unknown>(`/api/assoc/recall?${search.toString()}`);
    const parsed = parseWithFallback(
      raw,
      AgentSceneRelationListSchema,
      EMPTY_AGENT_SCENE_RELATION_LIST,
      { endpoint: "GET /api/assoc/recall" },
    );
    return parsed.items.map((item) => ({
      issue_id: item.issue_id || item.issue || "",
      issue: item.issue || item.issue_id || "",
      purpose: item.purpose,
      status: item.status,
      on_this_scene: item.on_this_scene === true,
    }));
  }

  // Workspace-scoped agent task snapshot: every active task
  // (queued/dispatched/running) plus each agent's most recent terminal task.
  // Powers the front-end's "active wins, else latest terminal" presence
  // derivation; one fetch backs every per-agent presence read in the app.
  // Workspace is resolved server-side from the X-Workspace-Slug header.
  async getAgentTaskSnapshot(): Promise<AgentTask[]> {
    return this.fetch(`/api/agent-task-snapshot`);
  }

  // Independent workspace-level projection. Unlike the task snapshot, this
  // already deduplicates running agents and returns only the display fields
  // consumers need. Callers may narrow the projection by task source and, for
  // issue work, the authenticated member's My Issues relation.
  // `parentIssueId` narrows the projection to that issue's direct children,
  // which is how the sub-issue header on issue detail reads the same source
  // as the Issues list header. The server rejects combining it with `scope`,
  // so callers pass one or the other.
  async getWorkspaceWorkingAgents(
    type?: WorkspaceWorkingAgentType,
    mineRelation?: WorkspaceWorkingAgentMineRelation,
    parentIssueId?: string,
  ): Promise<WorkspaceWorkingAgent[]> {
    const search = new URLSearchParams();
    if (type) search.set("type", type);
    if (mineRelation) {
      search.set("scope", "mine");
      search.set("relation", mineRelation);
    } else if (parentIssueId) {
      search.set("parent", parentIssueId);
    }
    const query = search.toString();
    return this.fetch(`/api/working-agents${query ? `?${query}` : ""}`);
  }

  // Per-agent daily activity for the last 30 days, anchored on
  // completed_at. One workspace-wide fetch backs both the Agents-list
  // sparkline (uses trailing 7 buckets) and the agent detail "Last 30
  // days" panel (uses all 30).
  async getWorkspaceAgentActivity30d(): Promise<AgentActivityBucket[]> {
    return this.fetch(`/api/agent-activity-30d`);
  }

  // Per-agent 30-day total run count for the Agents-list RUNS column.
  async getWorkspaceAgentRunCounts(): Promise<AgentRunCount[]> {
    return this.fetch(`/api/agent-run-counts`);
  }

  async getActiveTasksForIssue(
    issueId: string,
  ): Promise<{ tasks: AgentTask[] }> {
    return this.fetch(`/api/issues/${issueId}/active-task`);
  }

  async listTaskMessages(taskId: string): Promise<TaskMessagePayload[]> {
    return this.fetch(`/api/tasks/${taskId}/messages`);
  }

  async getDSHTrajectory(taskId: string): Promise<DSHTrajectoryArtifact> {
    const res = await this.fetchRaw(`/api/tasks/${taskId}/dsh-trajectory`);
    const contentType = res.headers
      .get("Content-Type")
      ?.split(";", 1)[0]
      ?.trim()
      .toLowerCase();
    const sessionId = res.headers.get("X-DSH-Session-ID");
    const sha256 = res.headers.get("X-Content-SHA256");
    const rawEventCount = res.headers.get("X-DSH-Event-Count");
    const eventCount = rawEventCount === null ? Number.NaN : Number(rawEventCount);
    if (
      contentType !== "application/x-ndjson" ||
      !sessionId ||
      !sha256?.match(/^[0-9a-f]{64}$/) ||
      !Number.isSafeInteger(eventCount) ||
      eventCount < 0
    ) {
      throw new Error("Invalid DSH trajectory metadata");
    }
    return {
      session_id: sessionId,
      sha256,
      event_count: eventCount,
      jsonl: await res.text(),
    };
  }

  async listTasksByIssue(issueId: string): Promise<AgentTask[]> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/task-runs`);
    return parseWithFallback<AgentTask[]>(raw, AgentTaskListSchema, [], {
      endpoint: "GET /api/issues/:id/task-runs",
    });
  }

  async getIssueUsage(issueId: string): Promise<IssueUsageSummary> {
    return this.fetch(`/api/issues/${issueId}/usage`);
  }

  async cancelTask(issueId: string, taskId: string): Promise<AgentTask> {
    return this.fetch(`/api/issues/${issueId}/tasks/${taskId}/cancel`, {
      method: "POST",
    });
  }

  async rerunIssue(issueId: string, taskId?: string): Promise<AgentTask> {
    return this.fetch(`/api/issues/${issueId}/rerun`, {
      method: "POST",
      body: JSON.stringify(taskId ? { task_id: taskId } : {}),
    });
  }

  // Inbox
  async listInbox(): Promise<InboxItem[]> {
    return this.fetch("/api/inbox");
  }

  async markInboxRead(id: string): Promise<InboxItem> {
    return this.fetch(`/api/inbox/${id}/read`, { method: "POST" });
  }

  async markInboxUnread(id: string): Promise<InboxItem> {
    return this.fetch(`/api/inbox/${id}/unread`, { method: "POST" });
  }

  async archiveInbox(id: string): Promise<InboxItem> {
    return this.fetch(`/api/inbox/${id}/archive`, { method: "POST" });
  }

  // Archived notifications, backing the inbox's "Archived" sub-view. Capped
  // server-side (no pagination in v1). Schema-guarded so a contract drift
  // renders an empty archive instead of taking the inbox down with it.
  async listArchivedInbox(): Promise<InboxItem[]> {
    const raw = await this.fetch<unknown>("/api/inbox/archived");
    return parseWithFallback(raw, InboxItemListSchema, EMPTY_INBOX_ITEMS, {
      endpoint: "GET /api/inbox/archived",
    });
  }

  async unarchiveInbox(id: string): Promise<InboxItem> {
    return this.fetch(`/api/inbox/${id}/unarchive`, { method: "POST" });
  }

  async getUnreadInboxCount(): Promise<{ count: number }> {
    return this.fetch("/api/inbox/unread-count");
  }

  // Cross-workspace unread summary: one entry per workspace the user belongs
  // to that has unread inbox items. Backs the workspace-switcher dot for
  // OTHER workspaces. Schema-guarded so a contract drift hides the dot rather
  // than crashing the sidebar.
  async getInboxUnreadSummary(): Promise<InboxWorkspaceUnread[]> {
    const raw = await this.fetch<unknown>("/api/inbox/unread-summary");
    return parseWithFallback(
      raw,
      InboxUnreadSummarySchema,
      EMPTY_INBOX_UNREAD_SUMMARY,
      {
        endpoint: "GET /api/inbox/unread-summary",
      },
    );
  }

  async markAllInboxRead(): Promise<{ count: number }> {
    return this.fetch("/api/inbox/mark-all-read", { method: "POST" });
  }

  async archiveAllInbox(): Promise<{ count: number }> {
    return this.fetch("/api/inbox/archive-all", { method: "POST" });
  }

  async archiveAllReadInbox(): Promise<{ count: number }> {
    return this.fetch("/api/inbox/archive-all-read", { method: "POST" });
  }

  async archiveCompletedInbox(): Promise<{ count: number }> {
    return this.fetch("/api/inbox/archive-completed", { method: "POST" });
  }

  // Notification preferences
  //
  // `workspaceSlug` overrides the default `X-Workspace-Slug` header (which
  // follows the active workspace) so a caller can read a SPECIFIC workspace's
  // preferences — e.g. honoring the mute setting of the workspace an inbox
  // notification came from while the user is viewing a different one (#3766).
  async getNotificationPreferences(
    workspaceSlug?: string,
  ): Promise<NotificationPreferenceResponse> {
    const raw = await this.fetch<unknown>(
      "/api/notification-preferences",
      workspaceSlug
        ? { headers: { "X-Workspace-Slug": workspaceSlug } }
        : undefined,
    );
    return parseWithFallback(
      raw,
      NotificationPreferenceResponseSchema,
      EMPTY_NOTIFICATION_PREFERENCE_RESPONSE,
      { endpoint: "GET /api/notification-preferences" },
    );
  }

  async updateNotificationPreferences(
    preferences: NotificationPreferences,
    workspaceSlug?: string,
  ): Promise<NotificationPreferenceResponse> {
    const raw = await this.fetch<unknown>("/api/notification-preferences", {
      method: "PATCH",
      headers: workspaceHeader(workspaceSlug),
      body: JSON.stringify({ preferences }),
    });
    return parseWithFallback(
      raw,
      NotificationPreferenceResponseSchema,
      EMPTY_NOTIFICATION_PREFERENCE_RESPONSE,
      { endpoint: "PATCH /api/notification-preferences" },
    );
  }

  // App Config
  async getConfig(): Promise<AppConfigResponse> {
    const raw = await this.fetch<unknown>("/api/config");
    return parseWithFallback<AppConfigResponse>(
      raw,
      AppConfigSchema,
      EMPTY_APP_CONFIG,
      {
        endpoint: "GET /api/config",
      },
    );
  }

  // Workspaces
  async listWorkspaces(): Promise<Workspace[]> {
    return this.fetch("/api/workspaces");
  }

  async getWorkspace(id: string): Promise<Workspace> {
    return this.fetch(`/api/workspaces/${id}`);
  }

  async createWorkspace(data: {
    name: string;
    slug: string;
    description?: string;
    context?: string;
  }): Promise<Workspace> {
    return this.fetch("/api/workspaces", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async getFDEOnboarding(): Promise<FDEOnboardingState> {
    const raw = await this.fetch<unknown>("/api/fde/onboarding");
    return parseWithFallback(
      raw,
      FDEOnboardingStateSchema,
      EMPTY_FDE_ONBOARDING_STATE,
      {
        endpoint: "GET /api/fde/onboarding",
      },
    );
  }

  async provisionFDEOnboarding(
    data: ProvisionFDEOnboardingRequest,
  ): Promise<ProvisionFDEOnboardingResponse> {
    const raw = await this.fetch<unknown>("/api/fde/onboarding", {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseWithFallback(
      raw,
      ProvisionFDEOnboardingResponseSchema,
      EMPTY_PROVISION_FDE_ONBOARDING_RESPONSE,
      { endpoint: "POST /api/fde/onboarding" },
    );
  }

  async updateWorkspace(
    id: string,
    data: {
      name?: string;
      description?: string;
      context?: string;
      settings?: Record<string, unknown>;
      repos?: WorkspaceRepo[];
      issue_prefix?: string;
      avatar_url?: string;
    },
  ): Promise<Workspace> {
    return this.fetch(`/api/workspaces/${id}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
  }

  // Members
  async listMembers(workspaceId: string): Promise<MemberWithUser[]> {
    return this.fetch(`/api/workspaces/${workspaceId}/members`);
  }

  async createMember(
    workspaceId: string,
    data: CreateMemberRequest,
  ): Promise<Invitation> {
    return this.fetch(`/api/workspaces/${workspaceId}/members`, {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async updateMember(
    workspaceId: string,
    memberId: string,
    data: UpdateMemberRequest,
  ): Promise<MemberWithUser> {
    return this.fetch(`/api/workspaces/${workspaceId}/members/${memberId}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
  }

  async deleteMember(workspaceId: string, memberId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/members/${memberId}`, {
      method: "DELETE",
    });
  }

  async searchDingTalkUsers(
    workspaceId: string,
    query: string,
    limit = 10,
  ): Promise<DingTalkUser[]> {
    const params = new URLSearchParams({
      q: query,
      limit: String(limit),
    });
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/dingtalk/users/search?${params.toString()}`,
    );
    return parseWithFallback(
      raw,
      DingTalkUserSearchResponseSchema,
      EMPTY_DINGTALK_USER_SEARCH_RESPONSE,
      { endpoint: "GET /api/workspaces/:id/dingtalk/users/search" },
    ).users;
  }

  async addDingTalkWorkspaceMembers(
    workspaceId: string,
    data: AddDingTalkWorkspaceMembersRequest,
  ): Promise<AddDingTalkWorkspaceMembersResponse> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/dingtalk/members`,
      {
        method: "POST",
        body: JSON.stringify(data),
      },
    );
    return parseWithFallback(
      raw,
      AddDingTalkWorkspaceMembersResponseSchema,
      EMPTY_ADD_DINGTALK_WORKSPACE_MEMBERS_RESPONSE,
      { endpoint: "POST /api/workspaces/:id/dingtalk/members" },
    );
  }

  async addDingTalkGroupMembers(
    workspaceId: string,
    data: AddDingTalkGroupMembersRequest,
  ): Promise<AddDingTalkGroupMembersResponse> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/dingtalk/group-members`,
      {
        method: "POST",
        body: JSON.stringify(data),
      },
    );
    return parseWithFallback(
      raw,
      AddDingTalkGroupMembersResponseSchema,
      EMPTY_ADD_DINGTALK_GROUP_MEMBERS_RESPONSE,
      { endpoint: "POST /api/workspaces/:id/dingtalk/group-members" },
    );
  }

  async leaveWorkspace(workspaceId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/leave`, {
      method: "POST",
    });
  }

  // Invitations
  async listWorkspaceInvitations(workspaceId: string): Promise<Invitation[]> {
    return this.fetch(`/api/workspaces/${workspaceId}/invitations`);
  }

  async revokeInvitation(
    workspaceId: string,
    invitationId: string,
  ): Promise<void> {
    await this.fetch(
      `/api/workspaces/${workspaceId}/invitations/${invitationId}`,
      {
        method: "DELETE",
      },
    );
  }

  async listMyInvitations(): Promise<Invitation[]> {
    return this.fetch("/api/invitations");
  }

  async getInvitation(invitationId: string): Promise<Invitation> {
    return this.fetch(`/api/invitations/${invitationId}`);
  }

  async acceptInvitation(invitationId: string): Promise<MemberWithUser> {
    return this.fetch(`/api/invitations/${invitationId}/accept`, {
      method: "POST",
    });
  }

  async declineInvitation(invitationId: string): Promise<void> {
    await this.fetch(`/api/invitations/${invitationId}/decline`, {
      method: "POST",
    });
  }

  async deleteWorkspace(workspaceId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}`, {
      method: "DELETE",
    });
  }

  // DSH plugins
  //
  // DeepSeek Harness ships no plugin registry, so browse reads a cached
  // community index plus the npm registry's own search endpoint, and import
  // records a pinned package reference.

  async listDshPlugins(): Promise<DshPlugin[]> {
    const raw = await this.fetch<unknown>("/api/dsh-plugins");
    return parseWithFallback(raw, DshPluginListSchema, [] as DshPlugin[], {
      endpoint: "GET /api/dsh-plugins",
    });
  }

  async getDshPlugin(id: string): Promise<DshPlugin | null> {
    const raw = await this.fetch<unknown>(`/api/dsh-plugins/${id}`);
    return parseWithFallback(raw, DshPluginSchema, null as DshPlugin | null, {
      endpoint: "GET /api/dsh-plugins/{id}",
    });
  }

  async importDshPlugin(
    data: ImportDshPluginRequest,
  ): Promise<ImportDshPluginResult> {
    const raw = await this.fetch<unknown>("/api/dsh-plugins", {
      method: "POST",
      body: JSON.stringify({
        source: data.source,
        display_name: data.displayName,
        config_row: data.configRow,
        config: data.config,
        catalog: data.catalog,
        on_conflict: data.onConflict,
      }),
    });
    return parseWithFallback(
      raw,
      ImportDshPluginResultSchema,
      {
        status: "",
        plugin: null,
        warnings: [],
        existingPlugin: null,
        error: "",
      } as ImportDshPluginResult,
      { endpoint: "POST /api/dsh-plugins" },
    );
  }

  async updateDshPlugin(
    id: string,
    data: {
      displayName?: string;
      configRow?: string;
      config?: Record<string, unknown>;
      source?: string;
    },
  ): Promise<ImportDshPluginResult> {
    const raw = await this.fetch<unknown>(`/api/dsh-plugins/${id}`, {
      method: "PUT",
      body: JSON.stringify({
        display_name: data.displayName,
        config_row: data.configRow,
        config: data.config,
        source: data.source,
      }),
    });
    return parseWithFallback(
      raw,
      ImportDshPluginResultSchema,
      {
        status: "",
        plugin: null,
        warnings: [],
        existingPlugin: null,
        error: "",
      } as ImportDshPluginResult,
      { endpoint: "PUT /api/dsh-plugins/{id}" },
    );
  }

  async deleteDshPlugin(id: string): Promise<void> {
    await this.fetch(`/api/dsh-plugins/${id}`, { method: "DELETE" });
  }

  async uploadDshPlugin(
    file: File,
    opts?: { onConflict?: "fail" | "overwrite" | "skip"; displayName?: string },
  ): Promise<ImportDshPluginResult> {
    const formData = new FormData();
    formData.append("file", file);
    if (opts?.onConflict) formData.append("on_conflict", opts.onConflict);
    if (opts?.displayName) formData.append("display_name", opts.displayName);

    // Multipart, so the body must not carry a JSON content type — the browser
    // sets the boundary itself.
    const res = await fetch(`${this.baseUrl}/api/dsh-plugins/upload`, {
      method: "POST",
      headers: this.authHeaders(),
      body: formData,
      credentials: "include",
    });
    if (!res.ok) {
      if (res.status === 401) this.handleUnauthorized();
      let message = `Upload failed (${res.status})`;
      try {
        const parsed: unknown = await res.json();
        if (
          parsed &&
          typeof parsed === "object" &&
          typeof (parsed as { error?: unknown }).error === "string"
        ) {
          message = (parsed as { error: string }).error;
        }
      } catch {
        // Keep the status-based message.
      }
      throw new ApiError(message, res.status, res.statusText);
    }
    const raw: unknown = await res.json();
    return parseWithFallback(
      raw,
      ImportDshPluginResultSchema,
      {
        status: "",
        plugin: null,
        warnings: [],
        existingPlugin: null,
        error: "",
      } as ImportDshPluginResult,
      { endpoint: "POST /api/dsh-plugins/upload" },
    );
  }

  async checkDshPluginUpdate(id: string): Promise<DshPluginUpdate> {
    const raw = await this.fetch<unknown>(`/api/dsh-plugins/${id}/update`);
    return parseWithFallback(
      raw,
      DshPluginUpdateSchema,
      {
        packageName: "",
        currentVersion: "",
        latestVersion: "",
        updateAvailable: false,
        checkable: false,
        reason: "",
        sourceSpec: "",
      } as DshPluginUpdate,
      { endpoint: "GET /api/dsh-plugins/{id}/update" },
    );
  }

  async listDshPluginFiles(id: string): Promise<DshPluginFileListing> {
    const raw = await this.fetch<unknown>(`/api/dsh-plugins/${id}/files`);
    return parseWithFallback(
      raw,
      DshPluginFileListingSchema,
      {
        packageName: "",
        resolvedVersion: "",
        files: [],
        truncated: false,
      } as DshPluginFileListing,
      { endpoint: "GET /api/dsh-plugins/{id}/files" },
    );
  }

  async getDshPluginFile(id: string, path: string): Promise<DshPluginFileContent> {
    const raw = await this.fetch<unknown>(
      `/api/dsh-plugins/${id}/file?path=${encodeURIComponent(path)}`,
    );
    return parseWithFallback(
      raw,
      DshPluginFileContentSchema,
      { path, size: 0, content: "" } as DshPluginFileContent,
      { endpoint: "GET /api/dsh-plugins/{id}/file" },
    );
  }

  async listDshPluginBindings(): Promise<DshPluginBinding[]> {
    const raw = await this.fetch<unknown>("/api/dsh-plugins/bindings");
    return parseWithFallback(
      raw,
      DshPluginBindingListSchema,
      [] as DshPluginBinding[],
      { endpoint: "GET /api/dsh-plugins/bindings" },
    );
  }

  async browseDshPluginCatalog(params: {
    query?: string;
    category?: string;
    limit?: number;
    offset?: number;
    installableOnly?: boolean;
  }): Promise<DshPluginCatalogPage> {
    const search = new URLSearchParams();
    if (params.query) search.set("q", params.query);
    if (params.category) search.set("category", params.category);
    if (params.limit) search.set("limit", String(params.limit));
    if (params.offset) search.set("offset", String(params.offset + 1));
    if (params.installableOnly === false) search.set("installable_only", "false");
    const suffix = search.toString() ? `?${search.toString()}` : "";
    const raw = await this.fetch<unknown>(`/api/dsh-plugins/catalog${suffix}`);
    return parseWithFallback(
      raw,
      DshPluginCatalogPageSchema,
      {
        entries: [],
        total: 0,
        limit: 0,
        offset: 0,
        state: {
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
      } as DshPluginCatalogPage,
      { endpoint: "GET /api/dsh-plugins/catalog" },
    );
  }

  async listDshPluginCatalogCategories(): Promise<DshPluginCatalogCategory[]> {
    const raw = await this.fetch<unknown>("/api/dsh-plugins/catalog/categories");
    return parseWithFallback(
      raw,
      DshPluginCatalogCategoryListSchema,
      [] as DshPluginCatalogCategory[],
      { endpoint: "GET /api/dsh-plugins/catalog/categories" },
    );
  }

  async refreshDshPluginCatalog(): Promise<void> {
    await this.fetch("/api/dsh-plugins/catalog/refresh", { method: "POST" });
  }

  async searchDshPluginRegistry(
    query: string,
  ): Promise<DshPluginRegistryResult[]> {
    const raw = await this.fetch<unknown>(
      `/api/dsh-plugins/registry-search?q=${encodeURIComponent(query)}`,
    );
    return parseWithFallback(
      raw,
      DshPluginRegistrySearchSchema,
      [] as DshPluginRegistryResult[],
      { endpoint: "GET /api/dsh-plugins/registry-search" },
    );
  }

  async getDSHProfile(workspaceId: string, agentId: string, signal?: AbortSignal): Promise<DSHProfileStatus | null> {
    const raw = await this.fetch<unknown>(`/api/agents/${encodeURIComponent(agentId)}/dsh-profile`, {
      signal,
      headers: { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId },
    });
    return parseWithFallback<DSHProfileStatus | null>(raw, DSHProfileSchema, null, {
      endpoint: "GET /api/agents/{id}/dsh-profile", includeReceived: false,
    });
  }

  async prepareDSHProfile(workspaceId: string, agentId: string): Promise<DSHProfileStatus | null> {
    const raw = await this.fetch<unknown>(`/api/agents/${encodeURIComponent(agentId)}/dsh-profile`, {
      method: "POST", body: "{}",
      headers: { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId },
    });
    return parseWithFallback<DSHProfileStatus | null>(raw, DSHProfileSchema, null, {
      endpoint: "POST /api/agents/{id}/dsh-profile", includeReceived: false,
    });
  }

  async retryDSHProfileBuild(workspaceId: string, agentId: string, revision: string, buildId: string): Promise<void> {
    await this.fetch<void>(`/api/agents/${encodeURIComponent(agentId)}/dsh-profile/retry`, {
      method: "POST", body: JSON.stringify({ revision, build_id: buildId }),
      headers: { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId },
    });
  }

  async getDSHHome(agentId: string, signal?: AbortSignal): Promise<DSHHomeStatus | null> {
    const raw = await this.fetch<unknown>(`/api/agents/${encodeURIComponent(agentId)}/filesystem`, { signal });
    return parseWithFallback<DSHHomeStatus | null>(raw, DSHHomeSchema, null, {
      endpoint: "GET /api/agents/{id}/filesystem",
      includeReceived: false,
    });
  }

  async listFilesystemRoots(signal?: AbortSignal): Promise<FilesystemRoots> {
    const raw = await this.fetch<unknown>(`/api/filesystem/roots`, { signal });
    return parseWithFallback<FilesystemRoots>(raw, FilesystemRootsSchema, { roots: [] }, {
      endpoint: "GET /api/filesystem/roots",
      includeReceived: false,
    });
  }

  async listFilesystemEntries(
    params: { root: string; path?: string; offset?: number; limit?: number; recursive?: boolean },
    signal?: AbortSignal,
  ): Promise<FilesystemEntries> {
    const query = new URLSearchParams({ root: params.root });
    if (params.path) query.set("path", params.path);
    if (params.offset != null) query.set("offset", String(params.offset));
    if (params.limit != null) query.set("limit", String(params.limit));
    if (params.recursive) query.set("recursive", "1");
    const raw = await this.fetch<unknown>(`/api/filesystem/entries?${query}`, { signal });
    return parseWithFallback<FilesystemEntries>(raw, FilesystemEntriesSchema, {
      root: params.root,
      path: params.path ?? ".",
      offset: params.offset ?? 0,
      limit: params.limit ?? 200,
      entries: [],
      count: 0,
      truncated: false,
      next_offset: null,
    }, {
      endpoint: "GET /api/filesystem/entries",
      includeReceived: false,
    });
  }

  async listFilesystemGrants(signal?: AbortSignal): Promise<FilesystemGrants> {
    const raw = await this.fetch<unknown>(`/api/filesystem/grants`, { signal });
    return parseWithFallback<FilesystemGrants>(raw, FilesystemGrantsSchema, { grants: [] }, {
      endpoint: "GET /api/filesystem/grants",
      includeReceived: false,
    });
  }

  async putFilesystemGrant(body: { agent_id: string; access: string }): Promise<FilesystemGrant | null> {
    const raw = await this.fetch<unknown>(`/api/filesystem/grants`, {
      method: "PUT",
      body: JSON.stringify(body),
    });
    return parseWithFallback<FilesystemGrant | null>(raw, FilesystemGrantSchema, null, {
      endpoint: "PUT /api/filesystem/grants",
      includeReceived: false,
    });
  }

  async mkdirFilesystem(body: { root: string; path: string }): Promise<void> {
    await this.fetch("/api/filesystem/mkdir", {
      method: "POST",
      body: JSON.stringify(body),
    });
  }

  async renameFilesystem(body: { root: string; path: string; name: string }): Promise<void> {
    await this.fetch("/api/filesystem/rename", {
      method: "POST",
      body: JSON.stringify(body),
    });
  }

  async deleteFilesystemEntry(params: { root: string; path: string }): Promise<void> {
    const query = new URLSearchParams({ root: params.root, path: params.path });
    await this.fetch(`/api/filesystem/entries?${query}`, { method: "DELETE" });
  }

  async uploadFilesystemFile(input: {
    root: string;
    path: string;
    file: File;
    filename?: string;
  }): Promise<void> {
    const form = new FormData();
    form.set("root", input.root);
    form.set("path", input.path);
    form.set("file", input.file);
    if (input.filename) form.set("filename", input.filename);
    await this.fetchRaw("/api/filesystem/upload", { method: "POST", body: form });
  }

  async downloadFilesystemFile(params: { root: string; path: string }): Promise<Blob> {
    const query = new URLSearchParams({ root: params.root, path: params.path });
    const res = await this.fetchRaw(`/api/filesystem/content?${query}`);
    return res.blob();
  }

  async ensureDSHHome(agentId: string): Promise<DSHHomeStatus | null> {
    const raw = await this.fetch<unknown>(`/api/agents/${encodeURIComponent(agentId)}/filesystem`, {
      method: "POST",
      body: "{}",
    });
    return parseWithFallback<DSHHomeStatus | null>(raw, DSHHomeSchema, null, {
      endpoint: "POST /api/agents/{id}/filesystem",
      includeReceived: false,
    });
  }

  async getAgentDshPluginConfig(agentId: string, pluginId: string, workspaceId?: string): Promise<AgentDshPluginConfig | null> {
    const raw = await this.fetch<unknown>(`/api/agents/${encodeURIComponent(agentId)}/dsh-plugins/${encodeURIComponent(pluginId)}/config`, {
      headers: workspaceId ? { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId } : undefined,
    });
    const value = parseWithFallback<AgentDshPluginConfig | null>(raw, AgentDshPluginConfigSchema, null, {
      endpoint: "GET /api/agents/{id}/dsh-plugins/{pluginId}/config", includeReceived: false,
    });
    return value?.agentId === agentId && value.pluginId === pluginId ? value : null;
  }

  async updateAgentDshPluginConfig(agentId: string, pluginId: string, input: UpdateAgentDshPluginConfig, workspaceId?: string): Promise<AgentDshPluginConfig | null> {
    const raw = await this.fetch<unknown>(`/api/agents/${encodeURIComponent(agentId)}/dsh-plugins/${encodeURIComponent(pluginId)}/config`, {
      method: "PUT",
      headers: workspaceId ? { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId } : undefined,
      body: JSON.stringify({ expected_revision: input.expectedRevision, config_override: input.override === null ? null : {
        row_id: input.override.rowId, config: input.override.config,
      } }),
    });
    const value = parseWithFallback<AgentDshPluginConfig | null>(raw, AgentDshPluginConfigSchema, null, {
      endpoint: "PUT /api/agents/{id}/dsh-plugins/{pluginId}/config", includeReceived: false,
    });
    return value?.agentId === agentId && value.pluginId === pluginId ? value : null;
  }

  async listAgentDshPlugins(agentId: string): Promise<AgentDshPlugin[]> {
    const raw = await this.fetch<unknown>(`/api/agents/${agentId}/dsh-plugins`);
    return parseWithFallback(
      raw,
      AgentDshPluginListSchema,
      [] as AgentDshPlugin[],
      { endpoint: "GET /api/agents/{id}/dsh-plugins" },
    );
  }

  async setAgentDshPlugins(
    agentId: string,
    plugins: { id: string; enabled?: boolean; configChange?: UpdateAgentDshPluginConfig }[],
    expectedPlugins?: AgentDshPlugin[],
  ): Promise<void> {
    await this.fetch(`/api/agents/${agentId}/dsh-plugins`, {
      method: "PUT",
      body: JSON.stringify({ plugins: plugins.map(({ configChange, ...plugin }) => ({
        ...plugin,
        ...(configChange ? { config_change: { expected_revision: configChange.expectedRevision, config_override: configChange.override === null ? null : { row_id: configChange.override.rowId, config: configChange.override.config } } } : {}),
      })), ...(expectedPlugins ? { expected_plugins: expectedPlugins.map(row => ({id:row.id,enabled:row.enabled,config_revision:row.configRevision ?? 0})) } : {}) }),
    });
  }

  async removeAgentDshPlugin(agentId: string, pluginId: string): Promise<void> {
    await this.fetch(`/api/agents/${agentId}/dsh-plugins/${pluginId}`, {
      method: "DELETE",
    });
  }

  // Skills
  async listSkills(): Promise<SkillSummary[]> {
    return this.fetch("/api/skills");
  }

  async getSkill(id: string): Promise<Skill> {
    return this.fetch(`/api/skills/${id}`);
  }

  async createSkill(data: CreateSkillRequest): Promise<Skill> {
    return this.fetch("/api/skills", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async updateSkill(id: string, data: UpdateSkillRequest): Promise<Skill> {
    return this.fetch(`/api/skills/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

  async deleteSkill(id: string): Promise<void> {
    await this.fetch(`/api/skills/${id}`, { method: "DELETE" });
  }

  async importSkill(data: { url: string; connection_id?: string; ref?: string; path?: string }): Promise<Skill> {
    return this.fetch("/api/skills/import", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async listAgentSkills(agentId: string): Promise<SkillSummary[]> {
    return this.fetch(`/api/agents/${agentId}/skills`);
  }

  async setAgentSkills(
    agentId: string,
    data: SetAgentSkillsRequest,
  ): Promise<void> {
    await this.fetch(`/api/agents/${agentId}/skills`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

  // Incremental attach: POST /skills/add only inserts the given ids (the
  // server upserts with ON CONFLICT DO NOTHING), so callers don't need to
  // read the agent's current skill set first.
  async addAgentSkills(
    agentId: string,
    data: SetAgentSkillsRequest,
  ): Promise<void> {
    await this.fetch(`/api/agents/${agentId}/skills/add`, {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async setAgentSkillEnabled(
    agentId: string,
    skillId: string,
    enabled: boolean,
  ): Promise<void> {
    await this.fetch(`/api/agents/${agentId}/skills/${skillId}/enabled`, {
      method: "PUT",
      body: JSON.stringify({ enabled }),
    });
  }

  async setAgentRuntimeSkillEnabled(
    agentId: string,
    data: SetAgentRuntimeSkillEnabledRequest,
  ): Promise<void> {
    await this.fetch(`/api/agents/${agentId}/runtime-skills/enabled`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

  async removeAgentSkill(agentId: string, skillId: string): Promise<void> {
    await this.fetch(`/api/agents/${agentId}/skills/${skillId}`, {
      method: "DELETE",
    });
  }

  async listInternalConnectors(workspaceId: string): Promise<InternalConnector[]> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/internal-connectors`);
    return parseWithFallback(raw, InternalConnectorListSchema, [], {
      endpoint: "GET /api/workspaces/:id/internal-connectors", includeReceived: false,
    });
  }

  async listAvailableInternalConnectors(workspaceId: string): Promise<AvailableInternalConnector[]> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/internal-connectors/available`);
    return parseWithFallback(raw, AvailableInternalConnectorListSchema, [], {
      endpoint: "GET /api/workspaces/:id/internal-connectors/available", includeReceived: false,
    });
  }

  async createInternalConnector(workspaceId: string, data: InternalConnectorInput): Promise<{id:string;credential_ref:string}> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/internal-connectors`, {method:"POST",body:JSON.stringify(data)});
    const result = parseWithFallback(raw,SavedInternalConnectorSchema,{id:"",credential_ref:""},{endpoint:"POST /api/workspaces/:id/internal-connectors",includeReceived:false});
    if (!result.id) throw new Error("Invalid connector response");
    return result;
  }

  async updateInternalConnector(workspaceId: string, id: string, data: InternalConnectorInput): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/internal-connectors/${encodeURIComponent(id)}`, {method:"PATCH",body:JSON.stringify(data)});
  }

  async setInternalConnectorCredential(workspaceId: string, id: string, bearerToken: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/internal-connectors/${encodeURIComponent(id)}/credential`, {
      method: "PUT", body: JSON.stringify({bearer_token: bearerToken}),
    });
  }

  /** Disconnects the workspace shared credential of a connector (OAuth
   * account or pasted token). Scene and personal credentials stay. */
  async deleteInternalConnectorCredential(workspaceId: string, id: string): Promise<void> {
    await this.fetch<void>(
      `/api/workspaces/${workspaceId}/internal-connectors/${encodeURIComponent(id)}/credential`,
      { method: "DELETE" },
    );
  }

  // Official app catalog (official remote MCP servers + OAuth). Admin only,
  // same guard as the connector library.

  /** Creates (or returns the existing) workspace connector for a catalog
   * app. Returns null when the echo is malformed; callers refetch the list. */
  async addCatalogConnector(workspaceId: string, slug: string): Promise<InternalConnector | null> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/connector-catalog/${encodeURIComponent(slug)}`,
      { method: "POST" },
    );
    return parseWithFallback<InternalConnector | null>(raw, AddedCatalogConnectorSchema, null, {
      endpoint: "POST /api/workspaces/:id/connector-catalog/:slug",
      includeReceived: false,
    });
  }

  /** Starts the provider OAuth flow for the workspace-wide shared account.
   * Resolves to "" when the server did not return a navigable https URL.
   * The response also sets the cookie that binds the flow to this browser,
   * so only a browser page that then navigates to the URL may call it. */
  async startInternalConnectorOAuth(
    workspaceId: string,
    id: string,
    returnTo?: string,
  ): Promise<string> {
    const body: Record<string, string> = {};
    if (returnTo) body.return_to = returnTo;
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/internal-connectors/${encodeURIComponent(id)}/oauth/start`,
      { method: "POST", body: JSON.stringify(body) },
    );
    return parseWithFallback<string>(raw, ConnectorAuthorizeUrlSchema, "", {
      endpoint: "POST /api/workspaces/:id/internal-connectors/:connectorId/oauth/start",
      includeReceived: false,
    });
  }

  /** Re-discovers a catalog connector's tools with its workspace credential
   * and re-pins them. null when the response is malformed. */
  async refreshInternalConnectorTools(workspaceId: string, id: string): Promise<InternalConnectorToolsRefresh | null> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/internal-connectors/${encodeURIComponent(id)}/tools/refresh`,
      { method: "POST" },
    );
    return parseWithFallback<InternalConnectorToolsRefresh | null>(raw, InternalConnectorToolsRefreshSchema, null, {
      endpoint: "POST /api/workspaces/:id/internal-connectors/:connectorId/tools/refresh",
    });
  }

  async testInternalConnector(workspaceId: string, id: string): Promise<InternalConnectorTest> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/internal-connectors/${encodeURIComponent(id)}/test`, {method:"POST"});
    return parseWithFallback(raw, InternalConnectorTestSchema, {reachable:false,ready:false,missing_tools:[],message:"Invalid connection test response"}, {
      endpoint:"POST /api/workspaces/:id/internal-connectors/:connectorId/test",includeReceived:false,
    });
  }

  // Context capabilities — mobile configuration (DingTalk H5). These routes
  // are keyed by agent and authorized by the caller's scene/person grants,
  // not by workspace membership, so no workspace header is sent.

  async redeemContextConfigLink(token: string): Promise<ContextConfigRedeemResult> {
    const raw = await this.fetch<unknown>("/api/context-capabilities/links/redeem", {
      method: "POST",
      body: JSON.stringify({ token }),
      headers: NO_WORKSPACE_HEADER,
    });
    return parseWithFallback(raw, ContextConfigRedeemSchema, EMPTY_CONTEXT_CONFIG_REDEEM, {
      endpoint: "POST /api/context-capabilities/links/redeem",
    });
  }

  async listContextConfigAgents(): Promise<ContextConfigAgentSummary[]> {
    const raw = await this.fetch<unknown>("/api/context-capabilities/agents", {
      headers: NO_WORKSPACE_HEADER,
    });
    return parseWithFallback<ContextConfigAgentSummary[]>(raw, ContextConfigAgentListSchema, [], {
      endpoint: "GET /api/context-capabilities/agents",
    });
  }

  /** What the caller may configure for one agent in one tenant: `orgId`,
   * else the server's default (the agent's own org, or the tenant of the
   * caller's newest grant). */
  async getContextConfigAgent(agentId: string, orgId = ""): Promise<ContextConfigAgentDetail | null> {
    const query = orgId ? `?${new URLSearchParams({ org_id: orgId }).toString()}` : "";
    const raw = await this.fetch<unknown>(
      `/api/context-capabilities/agents/${encodeURIComponent(agentId)}${query}`,
      { headers: NO_WORKSPACE_HEADER },
    );
    return parseWithFallback<ContextConfigAgentDetail | null>(raw, ContextConfigAgentDetailSchema, null, {
      endpoint: "GET /api/context-capabilities/agents/{agentId}",
      includeReceived: false,
    });
  }

  /** One scene the caller may configure. `orgId` names the scene's tenant
   * when it is not the agent's own org. */
  async getContextConfigScene(
    agentId: string,
    sceneKey: string,
    orgId = "",
  ): Promise<ContextConfigSceneDetail | null> {
    const query = orgId ? `?${new URLSearchParams({ org_id: orgId }).toString()}` : "";
    const raw = await this.fetch<unknown>(
      `/api/context-capabilities/agents/${encodeURIComponent(agentId)}/scenes/${encodeURIComponent(sceneKey)}${query}`,
      { headers: NO_WORKSPACE_HEADER },
    );
    return parseWithFallback<ContextConfigSceneDetail | null>(raw, ContextConfigSceneDetailSchema, null, {
      endpoint: "GET /api/context-capabilities/agents/{agentId}/scenes/{sceneKey}",
      includeReceived: false,
    });
  }

  async setContextCapabilityBinding(
    agentId: string,
    input: SetContextCapabilityBindingInput,
  ): Promise<ContextCapabilityBinding> {
    const body: Record<string, string | boolean> = {
      scope_type: input.scopeType,
      scope_key: input.scopeKey,
      resource_type: input.resourceType,
      resource_id: input.resourceId,
      enabled: input.enabled,
    };
    if (input.orgId) body.org_id = input.orgId;
    // Only sent when the caller changes it; the server rejects it outside
    // person connector bindings.
    if (input.shareInGroups !== undefined) body.share_in_groups = input.shareInGroups;
    const raw = await this.fetch<unknown>(
      `/api/context-capabilities/agents/${encodeURIComponent(agentId)}/bindings`,
      {
        method: "PUT",
        body: JSON.stringify(body),
        headers: NO_WORKSPACE_HEADER,
      },
    );
    // The write succeeded; a malformed echo falls back to what was sent and
    // the caller's invalidation refetches the authoritative state.
    const binding = parseWithFallback<ContextCapabilityBinding | null>(
      raw,
      ContextCapabilityBindingResponseSchema,
      null,
      { endpoint: "PUT /api/context-capabilities/agents/{agentId}/bindings" },
    );
    return binding ?? {
      resourceType: input.resourceType,
      resourceId: input.resourceId,
      enabled: input.enabled,
      shareInGroups: input.resourceType === "connector" && input.shareInGroups === true,
    };
  }

  async setContextConnectorCredential(
    agentId: string,
    input: SetContextConnectorCredentialInput,
  ): Promise<ContextConnectorCredential> {
    const raw = await this.fetch<unknown>(
      `/api/context-capabilities/agents/${encodeURIComponent(agentId)}/credentials`,
      {
        method: "PUT",
        body: JSON.stringify({
          scope_type: input.scopeType,
          scope_key: input.scopeKey,
          ...(input.orgId ? { org_id: input.orgId } : {}),
          connector_id: input.connectorId,
          bearer: input.bearer,
        }),
        headers: NO_WORKSPACE_HEADER,
      },
    );
    return parseWithFallback<ContextConnectorCredential>(
      raw,
      ContextConnectorCredentialResponseSchema,
      { connectorId: input.connectorId, hint: "", updatedAt: "", kind: "bearer" },
      {
        endpoint: "PUT /api/context-capabilities/agents/{agentId}/credentials",
        includeReceived: false,
      },
    );
  }

  async deleteContextConnectorCredential(
    agentId: string,
    input: DeleteContextConnectorCredentialInput,
  ): Promise<void> {
    const params = new URLSearchParams({
      scope_type: input.scopeType,
      scope_key: input.scopeKey,
      connector_id: input.connectorId,
    });
    if (input.orgId) params.set("org_id", input.orgId);
    await this.fetch<void>(
      `/api/context-capabilities/agents/${encodeURIComponent(agentId)}/credentials?${params.toString()}`,
      { method: "DELETE", headers: NO_WORKSPACE_HEADER },
    );
  }

  /** Starts the provider OAuth flow that connects the caller's own account
   * (person scope) or the group's account (scene scope) to an offered
   * connector. Resolves to "" when the server did not return a navigable
   * https URL. */
  async startContextConnectorConnection(
    agentId: string,
    input: StartContextConnectorConnectionInput,
  ): Promise<string> {
    const body: Record<string, string> = {
      scope_type: input.scopeType,
      scope_key: input.scopeKey,
      connector_id: input.connectorId,
    };
    if (input.orgId) body.org_id = input.orgId;
    if (input.returnTo) body.return_to = input.returnTo;
    const raw = await this.fetch<unknown>(
      `/api/context-capabilities/agents/${encodeURIComponent(agentId)}/connections/start`,
      { method: "POST", body: JSON.stringify(body), headers: NO_WORKSPACE_HEADER },
    );
    return parseWithFallback<string>(raw, ConnectorAuthorizeUrlSchema, "", {
      endpoint: "POST /api/context-capabilities/agents/{agentId}/connections/start",
      includeReceived: false,
    });
  }

  /** Replaces a configure-page scope's prompt components (the whole list).
   * Resolves to the stored list, or null when the echo is malformed (the
   * caller refetches). */
  async setContextConfigPrompts(
    agentId: string,
    scope: ContextConfigScopeInput,
    prompts: ContextPromptComponentInput[],
  ): Promise<ContextPromptComponent[] | null> {
    const raw = await this.fetch<unknown>(
      `/api/context-capabilities/agents/${encodeURIComponent(agentId)}/prompts`,
      {
        method: "PUT",
        body: JSON.stringify({ ...contextConfigScopeBody(scope), prompts: promptComponentsBody(prompts) }),
        headers: NO_WORKSPACE_HEADER,
      },
    );
    return parseWithFallback<ContextPromptComponent[] | null>(raw, ContextPromptComponentsResponseSchema, null, {
      endpoint: "PUT /api/context-capabilities/agents/{agentId}/prompts",
      includeReceived: false,
    });
  }

  /** Replaces a configure-page scope's MCP servers (remote URL servers only;
   * the server rejects local commands). Resolves to the stored document, or
   * to what was sent when the echo is malformed. */
  async setContextConfigMcpConfig(
    agentId: string,
    scope: ContextConfigScopeInput,
    mcpConfig: Record<string, unknown> | null,
  ): Promise<Record<string, unknown> | null> {
    const raw = await this.fetch<unknown>(
      `/api/context-capabilities/agents/${encodeURIComponent(agentId)}/mcp-config`,
      {
        method: "PUT",
        body: JSON.stringify({ ...contextConfigScopeBody(scope), mcp_config: mcpConfig }),
        headers: NO_WORKSPACE_HEADER,
      },
    );
    return parseWithFallback<Record<string, unknown> | null>(raw, ContextNodeMcpConfigResponseSchema, mcpConfig, {
      endpoint: "PUT /api/context-capabilities/agents/{agentId}/mcp-config",
      // Remote MCP servers can carry headers with tokens.
      includeReceived: false,
    });
  }

  async resolveContextConfigScene(
    agentId: string,
    input: ResolveContextConfigSceneInput,
  ): Promise<ContextConfigSceneGrant | null> {
    const body: Record<string, string> = {};
    if (input.chatId) body.chat_id = input.chatId;
    if (input.openConversationId) body.open_conversation_id = input.openConversationId;
    // The tenant goes in the query: the body takes only the picker's ids.
    const query = input.orgId ? `?${new URLSearchParams({ org_id: input.orgId }).toString()}` : "";
    const raw = await this.fetch<unknown>(
      `/api/context-capabilities/agents/${encodeURIComponent(agentId)}/scenes/resolve${query}`,
      { method: "POST", body: JSON.stringify(body), headers: NO_WORKSPACE_HEADER },
    );
    return parseWithFallback<ContextConfigSceneGrant | null>(raw, ContextConfigSceneResolveSchema, null, {
      endpoint: "POST /api/context-capabilities/agents/{agentId}/scenes/resolve",
    });
  }

  /** `dd.config` signature for `url` (the page URL without its fragment).
   * Returns null when the response is malformed so callers treat the JSAPI
   * as unavailable instead of signing with partial data. */
  async getDingTalkJsapiConfig(url: string): Promise<DingTalkJsapiConfig | null> {
    const params = new URLSearchParams({ url });
    const raw = await this.fetch<unknown>(`/api/dingtalk/jsapi-config?${params.toString()}`, {
      headers: NO_WORKSPACE_HEADER,
    });
    return parseWithFallback<DingTalkJsapiConfig | null>(raw, DingTalkJsapiConfigSchema, null, {
      endpoint: "GET /api/dingtalk/jsapi-config",
      includeReceived: false,
    });
  }

  // Context capabilities — admin (agent detail tab). Workspace-scoped: the
  // workspace is pinned explicitly so the query key's wsId and the request
  // always agree.

  async getAgentContextCapabilities(
    workspaceId: string,
    agentId: string,
  ): Promise<AgentContextCapabilities | null> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${encodeURIComponent(agentId)}/context-capabilities`,
      { headers: { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId } },
    );
    return parseWithFallback<AgentContextCapabilities | null>(raw, AgentContextCapabilitiesSchema, null, {
      endpoint: "GET /api/agents/{id}/context-capabilities",
    });
  }

  async setAgentContextCapabilityOffers(
    workspaceId: string,
    agentId: string,
    input: SetAgentContextCapabilityOffersInput,
  ): Promise<AgentContextCapabilities | null> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${encodeURIComponent(agentId)}/context-capabilities/offers`,
      {
        method: "PUT",
        body: JSON.stringify({
          connector_ids: input.connectorIds,
          skill_ids: input.skillIds,
        }),
        headers: { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId },
      },
    );
    return parseWithFallback<AgentContextCapabilities | null>(raw, AgentContextCapabilitiesSchema, null, {
      endpoint: "PUT /api/agents/{id}/context-capabilities/offers",
    });
  }

  // The workspace Tag (one multi-tenant digital employee per workspace).
  // Workspace-scoped: the workspace is pinned explicitly so the query key's
  // wsId and the request always agree.

  private tagHeaders(workspaceId: string): Record<string, string> {
    return { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId };
  }

  async getTag(workspaceId: string): Promise<TagState> {
    const raw = await this.fetch<unknown>("/api/tag", { headers: this.tagHeaders(workspaceId) });
    return parseWithFallback<TagState>(raw, TagStateSchema, EMPTY_TAG_STATE, { endpoint: "GET /api/tag" });
  }

  async createTag(workspaceId: string, input: CreateTagInput): Promise<TagState> {
    const raw = await this.fetch<unknown>("/api/tag", {
      method: "POST",
      body: JSON.stringify({
        name: input.name,
        description: input.description ?? "",
        runtime_id: input.runtimeId,
        model: input.model ?? "",
        copy_from_agent_id: input.copyFromAgentId ?? "",
      }),
      headers: this.tagHeaders(workspaceId),
    });
    return parseWithFallback<TagState>(raw, TagStateSchema, EMPTY_TAG_STATE, { endpoint: "POST /api/tag" });
  }

  async setTagSidebarVisible(workspaceId: string, visible: boolean): Promise<TagState> {
    const raw = await this.fetch<unknown>("/api/tag", {
      method: "PATCH",
      body: JSON.stringify({ sidebar_visible: visible }),
      headers: this.tagHeaders(workspaceId),
    });
    return parseWithFallback<TagState>(raw, TagStateSchema, EMPTY_TAG_STATE, { endpoint: "PATCH /api/tag" });
  }

  async deleteTag(workspaceId: string): Promise<void> {
    await this.fetch<unknown>("/api/tag", { method: "DELETE", headers: this.tagHeaders(workspaceId) });
  }

  async createTagTenant(workspaceId: string, name: string): Promise<TagTenantMutationResult> {
    const raw = await this.fetch<unknown>("/api/tag/tenants", {
      method: "POST",
      body: JSON.stringify({ name }),
      headers: this.tagHeaders(workspaceId),
    });
    return parseWithFallback<TagTenantMutationResult>(raw, TagTenantMutationSchema, EMPTY_TAG_TENANT_MUTATION, {
      endpoint: "POST /api/tag/tenants",
    });
  }

  async adoptTagTenant(workspaceId: string, agentId: string, name: string): Promise<TagTenantMutationResult> {
    const raw = await this.fetch<unknown>("/api/tag/tenants/adopt", {
      method: "POST",
      body: JSON.stringify({ agent_id: agentId, name }),
      headers: this.tagHeaders(workspaceId),
    });
    return parseWithFallback<TagTenantMutationResult>(raw, TagTenantMutationSchema, EMPTY_TAG_TENANT_MUTATION, {
      endpoint: "POST /api/tag/tenants/adopt",
    });
  }

  async renameTagTenant(workspaceId: string, tenantId: string, name: string): Promise<TagTenantMutationResult> {
    const raw = await this.fetch<unknown>(`/api/tag/tenants/${encodeURIComponent(tenantId)}`, {
      method: "PATCH",
      body: JSON.stringify({ name }),
      headers: this.tagHeaders(workspaceId),
    });
    return parseWithFallback<TagTenantMutationResult>(raw, TagTenantMutationSchema, EMPTY_TAG_TENANT_MUTATION, {
      endpoint: "PATCH /api/tag/tenants/{id}",
    });
  }

  async deleteTagTenant(workspaceId: string, tenantId: string): Promise<void> {
    await this.fetch<unknown>(`/api/tag/tenants/${encodeURIComponent(tenantId)}`, {
      method: "DELETE",
      headers: this.tagHeaders(workspaceId),
    });
  }

  async applyTag(workspaceId: string, tenantIds: string[], note = ""): Promise<TagApplyResponse> {
    const raw = await this.fetch<unknown>("/api/tag/apply", {
      method: "POST",
      body: JSON.stringify({ tenant_ids: tenantIds, note }),
      headers: this.tagHeaders(workspaceId),
    });
    return parseWithFallback<TagApplyResponse>(raw, TagApplyResponseSchema, EMPTY_TAG_APPLY, {
      endpoint: "POST /api/tag/apply",
    });
  }

  // Admin 场域 (agent detail → 场域): tenants, their group chats and people,
  // and the Context Builder of each level. Workspace-scoped like the context
  // capability admin routes: the workspace is pinned explicitly so the query
  // key's wsId and the request always agree.

  async listAgentTenants(workspaceId: string, agentId: string): Promise<AgentTenantsList> {
    const raw = await this.fetch<unknown>(`/api/agents/${encodeURIComponent(agentId)}/tenants`, {
      headers: { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId },
    });
    return parseWithFallback<AgentTenantsList>(raw, AgentTenantsListSchema, EMPTY_AGENT_TENANTS, {
      endpoint: "GET /api/agents/{id}/tenants",
    });
  }

  /** Creates a tenant (企业) for a DingTalk OrgId. Resolves to the echoed
   * tenant, or to what was sent when the echo is malformed. */
  async createAgentTenant(
    workspaceId: string,
    agentId: string,
    input: CreateAgentTenantInput,
  ): Promise<AgentTenant> {
    const raw = await this.fetch<unknown>(`/api/agents/${encodeURIComponent(agentId)}/tenants`, {
      method: "POST",
      body: JSON.stringify({ org_id: input.orgId, name: input.name }),
      headers: { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId },
    });
    return (
      parseWithFallback<AgentTenant | null>(raw, AgentTenantResponseSchema, null, {
        endpoint: "POST /api/agents/{id}/tenants",
      }) ?? { orgId: input.orgId, name: input.name, source: "created", groupCount: 0, personCount: 0 }
    );
  }

  async renameAgentTenant(
    workspaceId: string,
    agentId: string,
    orgId: string,
    name: string,
  ): Promise<AgentTenant | null> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${encodeURIComponent(agentId)}/tenants/${encodeURIComponent(orgId)}`,
      {
        method: "PATCH",
        body: JSON.stringify({ name }),
        headers: { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId },
      },
    );
    return parseWithFallback<AgentTenant | null>(raw, AgentTenantResponseSchema, null, {
      endpoint: "PATCH /api/agents/{id}/tenants/{orgId}",
    });
  }

  /** Deletes a tenant and its enterprise-level configuration. Its group and
   * person data stay (the org then shows as unassigned). */
  async deleteAgentTenant(workspaceId: string, agentId: string, orgId: string): Promise<void> {
    await this.fetch<void>(
      `/api/agents/${encodeURIComponent(agentId)}/tenants/${encodeURIComponent(orgId)}`,
      { method: "DELETE", headers: { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId } },
    );
  }

  /** Group chats of one tenant, newest activity first. */
  async listAgentTenantGroups(
    workspaceId: string,
    agentId: string,
    orgId: string,
    params: ListAgentScenesParams = {},
  ): Promise<AgentScenesPage> {
    const search = new URLSearchParams();
    if (params.limit !== undefined) search.set("limit", String(params.limit));
    if (params.offset !== undefined) search.set("offset", String(params.offset));
    const query = search.toString();
    const raw = await this.fetch<unknown>(
      `/api/agents/${encodeURIComponent(agentId)}/tenants/${encodeURIComponent(orgId)}/groups${query ? `?${query}` : ""}`,
      { headers: { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId } },
    );
    return parseWithFallback<AgentScenesPage>(raw, AgentScenesPageSchema, EMPTY_AGENT_SCENES_PAGE, {
      endpoint: "GET /api/agents/{id}/tenants/{orgId}/groups",
    });
  }

  /** People known under one tenant (a 1:1 chat is its person). */
  async listAgentTenantPersons(
    workspaceId: string,
    agentId: string,
    orgId: string,
  ): Promise<AgentTenantPerson[]> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${encodeURIComponent(agentId)}/tenants/${encodeURIComponent(orgId)}/persons`,
      { headers: { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId } },
    );
    return parseWithFallback<AgentTenantPerson[]>(raw, AgentTenantPersonsSchema, [], {
      endpoint: "GET /api/agents/{id}/tenants/{orgId}/persons",
    });
  }

  private contextNodePath(agentId: string, node: ContextNodeRef): string {
    return (
      `/api/agents/${encodeURIComponent(agentId)}/tenants/${encodeURIComponent(node.orgId)}` +
      `/context/${encodeURIComponent(node.scopeType)}/${encodeURIComponent(node.scopeKey)}`
    );
  }

  /** One level's Context Builder: its prompt components, connector and
   * skill switches, custom MCP servers and the effective preview. */
  async getContextNode(
    workspaceId: string,
    agentId: string,
    node: ContextNodeRef,
  ): Promise<ContextNodeDetail | null> {
    const raw = await this.fetch<unknown>(this.contextNodePath(agentId, node), {
      headers: { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId },
    });
    return parseWithFallback<ContextNodeDetail | null>(raw, ContextNodeDetailSchema, null, {
      endpoint: "GET /api/agents/{id}/tenants/{orgId}/context/{scopeType}/{scopeKey}",
      // Custom MCP servers can carry headers and environment values.
      includeReceived: false,
    });
  }

  /** Switches one offered connector or skill on or off at a level. */
  async setContextNodeBinding(
    workspaceId: string,
    agentId: string,
    node: ContextNodeRef,
    input: SetContextNodeBindingInput,
  ): Promise<void> {
    await this.fetch<unknown>(`${this.contextNodePath(agentId, node)}/bindings`, {
      method: "PUT",
      body: JSON.stringify({
        resource_type: input.resourceType,
        resource_id: input.resourceId,
        enabled: input.enabled,
      }),
      headers: { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId },
    });
  }

  /** Replaces a level's prompt components. Resolves to the stored list, or
   * null when the echo is malformed (the caller refetches). */
  async setContextNodePrompts(
    workspaceId: string,
    agentId: string,
    node: ContextNodeRef,
    prompts: ContextPromptComponentInput[],
  ): Promise<ContextPromptComponent[] | null> {
    const raw = await this.fetch<unknown>(`${this.contextNodePath(agentId, node)}/prompts`, {
      method: "PUT",
      body: JSON.stringify({ prompts: promptComponentsBody(prompts) }),
      headers: { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId },
    });
    return parseWithFallback<ContextPromptComponent[] | null>(raw, ContextPromptComponentsResponseSchema, null, {
      endpoint: "PUT /api/agents/{id}/tenants/{orgId}/context/{scopeType}/{scopeKey}/prompts",
      includeReceived: false,
    });
  }

  /** Saves a level's custom MCP servers; null clears them. Resolves to the
   * stored document, or to what was sent when the echo is malformed. */
  async setContextNodeMcpConfig(
    workspaceId: string,
    agentId: string,
    node: ContextNodeRef,
    mcpConfig: Record<string, unknown> | null,
  ): Promise<Record<string, unknown> | null> {
    const raw = await this.fetch<unknown>(`${this.contextNodePath(agentId, node)}/mcp-config`, {
      method: "PUT",
      body: JSON.stringify({ mcp_config: mcpConfig }),
      headers: { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId },
    });
    return parseWithFallback<Record<string, unknown> | null>(raw, ContextNodeMcpConfigResponseSchema, mcpConfig, {
      endpoint: "PUT /api/agents/{id}/tenants/{orgId}/context/{scopeType}/{scopeKey}/mcp-config",
      // MCP server configs can carry headers and environment values.
      includeReceived: false,
    });
  }

  /** Stores a level's token for a connector (a Bearer, or a Personal Access
   * Token for an app that allows one). */
  async setContextNodeCredential(
    workspaceId: string,
    agentId: string,
    node: ContextNodeRef,
    input: SetContextNodeCredentialInput,
  ): Promise<void> {
    await this.fetch<unknown>(`${this.contextNodePath(agentId, node)}/credentials`, {
      method: "PUT",
      body: JSON.stringify({ connector_id: input.connectorId, bearer: input.bearer }),
      headers: { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId },
    });
  }

  /** Removes a level's token or disconnects its OAuth account. */
  async deleteContextNodeCredential(
    workspaceId: string,
    agentId: string,
    node: ContextNodeRef,
    connectorId: string,
  ): Promise<void> {
    const params = new URLSearchParams({ connector_id: connectorId });
    await this.fetch<void>(`${this.contextNodePath(agentId, node)}/credentials?${params.toString()}`, {
      method: "DELETE",
      headers: { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId },
    });
  }

  /** Revokes every configure-page grant (from configuration links or the
   * DingTalk group picker) on a group or person level; the level's
   * configuration stays. Resolves to how many grants were removed, null when
   * the echo is malformed. */
  async revokeContextNodeGrants(
    workspaceId: string,
    agentId: string,
    node: ContextNodeRef,
  ): Promise<number | null> {
    const raw = await this.fetch<unknown>(`${this.contextNodePath(agentId, node)}/grants`, {
      method: "DELETE",
      headers: { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId },
    });
    return parseWithFallback<number | null>(raw, ContextNodeGrantsRevokedSchema, null, {
      endpoint: "DELETE /api/agents/{id}/tenants/{orgId}/context/{scopeType}/{scopeKey}/grants",
    });
  }

  /** Starts connecting an official app account for a level. Resolves to the
   * provider authorization URL, "" when the server sent no navigable URL. */
  async startContextNodeConnection(
    workspaceId: string,
    agentId: string,
    node: ContextNodeRef,
    input: StartContextNodeConnectionInput,
  ): Promise<string> {
    const body: Record<string, string> = { connector_id: input.connectorId };
    if (input.returnTo) body.return_to = input.returnTo;
    const raw = await this.fetch<unknown>(`${this.contextNodePath(agentId, node)}/connections/start`, {
      method: "POST",
      body: JSON.stringify(body),
      headers: { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId },
    });
    return parseWithFallback<string>(raw, ConnectorAuthorizeUrlSchema, "", {
      endpoint: "POST /api/agents/{id}/tenants/{orgId}/context/{scopeType}/{scopeKey}/connections/start",
      includeReceived: false,
    });
  }

  // Admin connected apps (agent detail → 连接器 → 连接应用). Workspace-scoped
  // like the other agent admin routes: the workspace is pinned explicitly so
  // the query key's wsId and the request always agree.

  /** Every official app with this agent's status. null when the body is
   * malformed, so the page shows a load error instead of an empty gallery. */
  async listAgentConnectedApps(
    workspaceId: string,
    agentId: string,
  ): Promise<ConnectedAppsList | null> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${encodeURIComponent(agentId)}/connected-apps`,
      { headers: { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId } },
    );
    return parseWithFallback<ConnectedAppsList | null>(raw, ConnectedAppsListSchema, null, {
      endpoint: "GET /api/agents/{id}/connected-apps",
      includeReceived: false,
    });
  }

  async getAgentConnectedApp(
    workspaceId: string,
    agentId: string,
    slug: string,
  ): Promise<ConnectedAppDetail | null> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${encodeURIComponent(agentId)}/connected-apps/${encodeURIComponent(slug)}`,
      { headers: { "X-Workspace-Slug": "", "X-Workspace-ID": workspaceId } },
    );
    return parseWithFallback<ConnectedAppDetail | null>(raw, ConnectedAppDetailSchema, null, {
      endpoint: "GET /api/agents/{id}/connected-apps/{slug}",
      includeReceived: false,
    });
  }

  async getSemanticaMCPStatus(workspaceId: string): Promise<SemanticaMCPStatus> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/semantica-mcp-relay`);
    return parseWithFallback(raw, SemanticaMCPStatusSchema, EMPTY_SEMANTICA_MCP_STATUS, {
      endpoint: "GET /api/workspaces/:id/semantica-mcp-relay", includeReceived: false,
    });
  }

  async listWorkspaceMCPConnections(workspaceId: string): Promise<WorkspaceMCPConnection[]> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/mcp-tokens/`);
    return parseWithFallback(raw, WorkspaceMCPConnectionsSchema, [], {
      endpoint: "GET /api/workspaces/:id/mcp-tokens", includeReceived: false,
    });
  }

  async createWorkspaceMCPConnection(workspaceId: string, data: CreateWorkspaceMCPConnection): Promise<{ id: string; url: string }> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/mcp-tokens/`, {
      method: "POST", body: JSON.stringify(data),
    });
    const result = parseWithFallback(raw, WorkspaceMCPLinkSchema, { id: "", url: "" }, {
      endpoint: "POST /api/workspaces/:id/mcp-tokens", includeReceived: false,
    });
    if (!result.id || !result.url) throw new Error("Invalid MCP connection response");
    return result;
  }

  async revokeWorkspaceMCPConnection(workspaceId: string, id: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/mcp-tokens/${id}/revoke`, { method: "POST" });
  }

  // Personal Access Tokens
  async listPersonalAccessTokens(): Promise<PersonalAccessToken[]> {
    return this.fetch("/api/tokens");
  }

  async createPersonalAccessToken(
    data: CreatePersonalAccessTokenRequest,
  ): Promise<CreatePersonalAccessTokenResponse> {
    return this.fetch("/api/tokens", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async revokePersonalAccessToken(id: string): Promise<void> {
    await this.fetch(`/api/tokens/${id}`, { method: "DELETE" });
  }

  // Owner-managed DTA workspace access. These credentials are intentionally
  // separate from personal PATs and are never used by the Multica CLI.
  async listWorkspaceAccessTokens(
    workspaceId: string,
  ): Promise<WorkspaceAccessToken[]> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/access-tokens`,
    );
    return parseWithFallback(raw, WorkspaceAccessTokenListSchema, [], {
      endpoint: "GET /api/workspaces/:id/access-tokens",
      includeReceived: false,
    });
  }

  async createWorkspaceAccessToken(
    workspaceId: string,
    data: CreateWorkspaceAccessTokenRequest,
  ): Promise<WorkspaceAccessTokenSecretResponse> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/access-tokens`,
      {
        method: "POST",
        body: JSON.stringify(data),
      },
    );
    return parseWithFallback(
      raw,
      WorkspaceAccessTokenSecretResponseSchema,
      EMPTY_WORKSPACE_ACCESS_TOKEN_SECRET_RESPONSE,
      {
        endpoint: "POST /api/workspaces/:id/access-tokens",
        includeReceived: false,
      },
    );
  }

  async updateWorkspaceAccessToken(
    workspaceId: string,
    tokenId: string,
    data: UpdateWorkspaceAccessTokenRequest,
  ): Promise<WorkspaceAccessToken> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/access-tokens/${tokenId}`,
      {
        method: "PATCH",
        body: JSON.stringify(data),
      },
    );
    return parseWithFallback(
      raw,
      WorkspaceAccessTokenSchema,
      EMPTY_WORKSPACE_ACCESS_TOKEN,
      {
        endpoint: "PATCH /api/workspaces/:id/access-tokens/:tokenId",
        includeReceived: false,
      },
    );
  }

  async regenerateWorkspaceAccessToken(
    workspaceId: string,
    tokenId: string,
    data: RegenerateWorkspaceAccessTokenRequest,
  ): Promise<WorkspaceAccessTokenSecretResponse> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/access-tokens/${tokenId}/regenerate`,
      {
        method: "POST",
        body: JSON.stringify(data),
      },
    );
    return parseWithFallback(
      raw,
      WorkspaceAccessTokenSecretResponseSchema,
      EMPTY_WORKSPACE_ACCESS_TOKEN_SECRET_RESPONSE,
      {
        endpoint: "POST /api/workspaces/:id/access-tokens/:tokenId/regenerate",
        includeReceived: false,
      },
    );
  }

  async revokeWorkspaceAccessToken(
    workspaceId: string,
    tokenId: string,
  ): Promise<void> {
    await this.fetch(
      `/api/workspaces/${workspaceId}/access-tokens/${tokenId}/revoke`,
      {
        method: "POST",
      },
    );
  }

  async deleteWorkspaceAccessToken(
    workspaceId: string,
    tokenId: string,
  ): Promise<void> {
    await this.fetch(
      `/api/workspaces/${workspaceId}/access-tokens/${tokenId}`,
      {
        method: "DELETE",
      },
    );
  }

  // Owner-managed inbound A2A exposure for one Agent. The raw credential
  // returned by createAgentA2ACredential must be consumed by the mutation
  // layer and never retained in TanStack Query state.
  async getAgentA2AConfig(agentId: string): Promise<AgentA2AConfig> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${encodeURIComponent(agentId)}/a2a`,
    );
    const parsed = parseWithFallback(
      raw,
      AgentA2AConfigSchema,
      EMPTY_AGENT_A2A_CONFIG,
      {
        endpoint: "GET /api/agents/:id/a2a",
        includeReceived: false,
      },
    );
    if (parsed === EMPTY_AGENT_A2A_CONFIG) {
      throw new Error("Invalid A2A configuration response");
    }
    return parsed;
  }

  // Deployment operator settings. Non-operators receive operator=false.
  async getAgentA2AOperatorConfig(agentId: string): Promise<AgentA2AOperatorConfig> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${encodeURIComponent(agentId)}/a2a/operator`,
    );
    return parseWithFallback(
      raw,
      AgentA2AOperatorConfigSchema,
      EMPTY_AGENT_A2A_OPERATOR_CONFIG,
      { endpoint: "GET /api/agents/:id/a2a/operator", includeReceived: false },
    );
  }

  async updateAgentA2AOperatorIdentity(
    agentId: string,
    data: UpdateAgentA2AOperatorIdentityRequest,
  ): Promise<AgentA2AOperatorConfig> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${encodeURIComponent(agentId)}/a2a/operator/dws-identity`,
      {
        method: "PUT",
        body: JSON.stringify({
          uid: data.uid,
          org_id: data.orgId,
          display_name: data.displayName ?? "",
          organization_name: data.organizationName ?? "",
          deap_agent_uuid: data.deapAgentUuid ?? "",
        }),
      },
    );
    return parseWithFallback(
      raw,
      AgentA2AOperatorConfigSchema,
      EMPTY_AGENT_A2A_OPERATOR_CONFIG,
      { endpoint: "PUT /api/agents/:id/a2a/operator/dws-identity", includeReceived: false },
    );
  }

  async deleteAgentA2AOperatorIdentity(agentId: string): Promise<AgentA2AOperatorConfig> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${encodeURIComponent(agentId)}/a2a/operator/dws-identity`,
      { method: "DELETE" },
    );
    return parseWithFallback(
      raw,
      AgentA2AOperatorConfigSchema,
      EMPTY_AGENT_A2A_OPERATOR_CONFIG,
      { endpoint: "DELETE /api/agents/:id/a2a/operator/dws-identity", includeReceived: false },
    );
  }

  async updateAgentA2AProdForward(
    agentId: string,
    data: UpdateAgentA2AProdForwardRequest,
  ): Promise<AgentA2AOperatorConfig> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${encodeURIComponent(agentId)}/a2a/operator/prod-forward`,
      { method: "PUT", body: JSON.stringify({ accept: data.accept }) },
    );
    return parseWithFallback(
      raw,
      AgentA2AOperatorConfigSchema,
      EMPTY_AGENT_A2A_OPERATOR_CONFIG,
      { endpoint: "PUT /api/agents/:id/a2a/operator/prod-forward", includeReceived: false },
    );
  }

  async updateAgentA2AConfig(
    agentId: string,
    data: UpdateAgentA2AConfigRequest,
  ): Promise<AgentA2AConfig> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${encodeURIComponent(agentId)}/a2a`,
      {
        method: "PUT",
        body: JSON.stringify({
          enabled: data.enabled,
          card_name: data.cardName,
          card_description: data.cardDescription,
          card_version: data.cardVersion,
          card_skills: data.cardSkills,
        }),
      },
    );
    const parsed = parseWithFallback(
      raw,
      AgentA2AConfigSchema,
      EMPTY_AGENT_A2A_CONFIG,
      {
        endpoint: "PUT /api/agents/:id/a2a",
        includeReceived: false,
      },
    );
    if (parsed === EMPTY_AGENT_A2A_CONFIG) {
      throw new Error("Invalid A2A configuration response");
    }
    return parsed;
  }

  async createAgentA2AClient(
    agentId: string,
    data: CreateAgentA2AClientRequest,
  ): Promise<AgentA2AClient> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${encodeURIComponent(agentId)}/a2a/clients`,
      {
        method: "POST",
        body: JSON.stringify({
          name: data.name,
          scopes: data.scopes,
          rate_limit_per_minute: data.rateLimitPerMinute,
          max_concurrent_tasks: data.maxConcurrentTasks,
        }),
      },
    );
    return parseWithFallback(raw, AgentA2AClientSchema, EMPTY_AGENT_A2A_CLIENT, {
      endpoint: "POST /api/agents/:id/a2a/clients",
      includeReceived: false,
    });
  }

  async updateAgentA2AClient(
    agentId: string,
    clientId: string,
    data: UpdateAgentA2AClientRequest,
  ): Promise<AgentA2AClient> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${encodeURIComponent(agentId)}/a2a/clients/${encodeURIComponent(clientId)}`,
      {
        method: "PATCH",
        body: JSON.stringify({
          name: data.name,
          status: data.status,
          scopes: data.scopes,
          rate_limit_per_minute: data.rateLimitPerMinute,
          max_concurrent_tasks: data.maxConcurrentTasks,
        }),
      },
    );
    return parseWithFallback(raw, AgentA2AClientSchema, EMPTY_AGENT_A2A_CLIENT, {
      endpoint: "PATCH /api/agents/:id/a2a/clients/:clientId",
      includeReceived: false,
    });
  }

  async createAgentA2ACredential(
    agentId: string,
    clientId: string,
    data: CreateAgentA2ACredentialRequest,
  ): Promise<AgentA2ACredentialSecretResponse> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${encodeURIComponent(agentId)}/a2a/clients/${encodeURIComponent(clientId)}/credentials`,
      {
        method: "POST",
        body: JSON.stringify({ expires_at: data.expiresAt }),
      },
    );
    return parseWithFallback(
      raw,
      AgentA2ACredentialSecretResponseSchema,
      EMPTY_AGENT_A2A_CREDENTIAL_SECRET_RESPONSE,
      {
        endpoint: "POST /api/agents/:id/a2a/clients/:clientId/credentials",
        includeReceived: false,
      },
    );
  }

  async deleteAgentA2ACredential(
    agentId: string,
    clientId: string,
    credentialId: string,
  ): Promise<void> {
    await this.fetch(
      `/api/agents/${encodeURIComponent(agentId)}/a2a/clients/${encodeURIComponent(clientId)}/credentials/${encodeURIComponent(credentialId)}`,
      { method: "DELETE" },
    );
  }

  // File Upload & Attachments
  async uploadFile(
    file: File,
    opts?: { issueId?: string; commentId?: string; chatSessionId?: string },
    // Optional abort signal so a module-level upload coordinator (MUL-5181)
    // can cancel an in-flight upload on logout. When aborted, `fetch` rejects
    // with an AbortError, which the coordinator distinguishes from a real
    // failure via `signal.aborted` / `err.name === "AbortError"`.
    signal?: AbortSignal,
  ): Promise<Attachment> {
    const formData = new FormData();
    formData.append("file", file);
    if (opts?.issueId) formData.append("issue_id", opts.issueId);
    if (opts?.commentId) formData.append("comment_id", opts.commentId);
    if (opts?.chatSessionId)
      formData.append("chat_session_id", opts.chatSessionId);

    const rid = createRequestId();
    const start = Date.now();
    this.logger.info("→ POST /api/upload-file", { rid });

    const res = await fetch(`${this.baseUrl}/api/upload-file`, {
      method: "POST",
      headers: this.authHeaders(),
      body: formData,
      credentials: "include",
      signal,
    });

    if (!res.ok) {
      if (res.status === 401) this.handleUnauthorized();
      const message = await this.parseErrorMessage(
        res,
        `Upload failed: ${res.status}`,
      );
      this.logger.error(`← ${res.status} /api/upload-file`, {
        rid,
        duration: `${Date.now() - start}ms`,
        error: message,
      });
      throw new Error(message);
    }

    this.logger.info(`← ${res.status} /api/upload-file`, {
      rid,
      duration: `${Date.now() - start}ms`,
    });
    const raw = (await res.json()) as unknown;
    return parseWithFallback(raw, AttachmentResponseSchema, EMPTY_ATTACHMENT, {
      endpoint: "POST /api/upload-file",
    });
  }

  // Chat Sessions
  async listChatSessions(
    params?: { status?: string },
    workspaceSlug?: string,
  ): Promise<ChatSession[]> {
    const query = params?.status ? `?status=${params.status}` : "";
    return this.fetch(`/api/chat/sessions${query}`, {
      headers: workspaceHeader(workspaceSlug),
    });
  }

  async getChatSession(id: string): Promise<ChatSession> {
    return this.fetch(`/api/chat/sessions/${id}`);
  }

  async createChatSession(
    data: {
      agent_id: string;
      title?: string;
      project_id?: string | null;
    },
    workspaceSlug?: string,
  ): Promise<ChatSession> {
    return this.fetch("/api/chat/sessions", {
      method: "POST",
      headers: workspaceHeader(workspaceSlug),
      body: JSON.stringify(data),
    });
  }

  async deleteChatSession(id: string): Promise<void> {
    await this.fetch(`/api/chat/sessions/${id}`, { method: "DELETE" });
  }

  // Refresh the quick-action suggestions for a session's latest assistant turn.
  // Fire-and-forget: the server enqueues a background regeneration pass and the
  // refreshed pills arrive over the chat:quick_actions realtime event, so the
  // caller anchors its pending placeholder on the turn it already knows rather
  // than on this response.
  // Refresh the quick actions for the given assistant turn. Sends the message
  // id the caller is refreshing so the server can atomically confirm it is still
  // the session's latest turn (409 otherwise) — that keeps the client's pending
  // marker aligned with the turn chat:quick_actions will resolve, with no
  // response reconciliation needed even under a WS-before-HTTP race (MUL-5149).
  async regenerateChatQuickActions(
    sessionId: string,
    messageId: string,
  ): Promise<void> {
    await this.fetch(
      `/api/chat/sessions/${sessionId}/quick-actions/regenerate`,
      {
        method: "POST",
        body: JSON.stringify({ message_id: messageId }),
      },
    );
  }

  async updateChatSession(
    id: string,
    data: { title: string } | { project_id: string | null },
  ): Promise<ChatSession> {
    return this.fetch(`/api/chat/sessions/${id}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
  }

  async setChatSessionPinned(
    id: string,
    pinned: boolean,
  ): Promise<ChatSession> {
    return this.fetch(`/api/chat/sessions/${id}/pin`, {
      method: "PATCH",
      body: JSON.stringify({ pinned }),
    });
  }

  async setChatSessionArchived(
    id: string,
    archived: boolean,
  ): Promise<ChatSession> {
    return this.fetch(`/api/chat/sessions/${id}/archive`, {
      method: "PATCH",
      body: JSON.stringify({ archived }),
    });
  }

  // Quick-agent bar: per-user pinned agents.
  async listChatPinnedAgents(): Promise<ChatPinnedAgent[]> {
    return this.fetch("/api/chat/pinned-agents");
  }

  async pinChatAgent(agentId: string): Promise<ChatPinnedAgent> {
    return this.fetch("/api/chat/pinned-agents", {
      method: "POST",
      body: JSON.stringify({ agent_id: agentId }),
    });
  }

  async unpinChatAgent(agentId: string): Promise<void> {
    await this.fetch(`/api/chat/pinned-agents/${agentId}`, {
      method: "DELETE",
    });
  }

  async listChatMessages(sessionId: string): Promise<ChatMessage[]> {
    const raw: unknown = await this.fetch(
      `/api/chat/sessions/${sessionId}/messages`,
    );
    return parseWithFallback(
      raw,
      ChatMessageListSchema,
      EMPTY_CHAT_MESSAGE_LIST,
      {
        endpoint: "GET /api/chat/sessions/:id/messages",
      },
    );
  }

  async listChatMessagesPage(
    sessionId: string,
    params: {
      before?: { created_at: string; id: string } | null;
      limit?: number;
    } = {},
  ): Promise<ChatMessagesPage> {
    const limit = params.limit ?? 50;
    const query = new URLSearchParams({ limit: String(limit) });
    if (params.before) {
      query.set("before_created_at", params.before.created_at);
      query.set("before_id", params.before.id);
    }
    try {
      const raw: unknown = await this.fetch(
        `/api/chat/sessions/${sessionId}/messages/page?${query.toString()}`,
      );
      return parseWithFallback(
        raw,
        ChatMessagesPageSchema,
        { messages: [], limit, has_more: false, next_cursor: null },
        {
          endpoint: "GET /api/chat/sessions/:id/messages/page",
        },
      );
    } catch (err) {
      // Deployment-order compatibility: a backend deployed before this endpoint
      // existed returns 404 for the unknown route. Fall back to the legacy
      // full-list endpoint so chat never white-screens regardless of whether
      // the server or the client deploys first. Only the initial (cursorless)
      // page falls back — the legacy endpoint returns every message at once, so
      // the fallback page reports has_more: false and there is no follow-up
      // request to translate. A 404 on a cursor request is an unexpected state
      // and propagates instead of duplicating the whole list.
      if (err instanceof ApiError && err.status === 404 && !params.before) {
        const messages = await this.listChatMessages(sessionId);
        return { messages, limit, has_more: false, next_cursor: null };
      }
      throw err;
    }
  }

  async sendChatMessage(
    sessionId: string,
    content: string,
    attachmentIds?: string[],
  ): Promise<SendChatMessageResponse> {
    const body: {
      content: string;
      attachment_ids?: string[];
    } = { content };
    if (attachmentIds && attachmentIds.length > 0) {
      body.attachment_ids = attachmentIds;
    }
    const raw = await this.fetch<unknown>(
      `/api/chat/sessions/${sessionId}/messages`,
      {
        method: "POST",
        body: JSON.stringify(body),
      },
    );
    const response = parseWithFallback<SendChatMessageResponse | null>(
      raw,
      SendChatMessageResponseSchema,
      null,
      { endpoint: "POST /api/chat/sessions/:id/messages" },
    );
    if (!response) throw new Error("invalid send chat message response");
    return response;
  }

  async startMikaOnboarding(
    sessionId: string,
    data: {
      language: "en" | "zh" | "ko" | "ja";
    },
    workspaceSlug?: string,
  ): Promise<StartMikaOnboardingResponse> {
    const raw = await this.fetch<unknown>(
      `/api/chat/sessions/${sessionId}/onboarding`,
      {
        method: "POST",
        headers: workspaceHeader(workspaceSlug),
        body: JSON.stringify(data),
      },
    );
    return parseWithFallback(
      raw,
      StartMikaOnboardingResponseSchema,
      { started: false },
      { endpoint: "POST /api/chat/sessions/:id/onboarding" },
    );
  }

  async getPendingChatTask(sessionId: string): Promise<ChatPendingTask> {
    const raw = await this.fetch<unknown>(
      `/api/chat/sessions/${sessionId}/pending-task`,
    );
    return parseWithFallback(
      raw,
      ChatPendingTaskSchema,
      EMPTY_CHAT_PENDING_TASK,
      {
        endpoint: "GET /api/chat/sessions/:id/pending-task",
      },
    );
  }

  async prioritizeQueuedChatTask(
    sessionId: string,
    taskId: string,
  ): Promise<PrioritizeQueuedChatTaskResponse> {
    const raw = await this.fetch<unknown>(
      `/api/chat/sessions/${sessionId}/queued-tasks/${taskId}/prioritize`,
      { method: "POST" },
    );
    return parseWithFallback(
      raw,
      PrioritizeQueuedChatTaskResponseSchema,
      EMPTY_PRIORITIZE_QUEUED_CHAT_TASK_RESPONSE,
      {
        endpoint: "POST /api/chat/sessions/:id/queued-tasks/:taskId/prioritize",
      },
    );
  }

  async clearQueuedChatTasks(sessionId: string): Promise<void> {
    await this.fetch(`/api/chat/sessions/${sessionId}/queued-tasks`, {
      method: "DELETE",
    });
  }

  /**
   * Pending deferred-cancellation draft restores for a session (#5219).
   * A 404 means the backend predates the endpoint — treat as "nothing
   * pending" so older servers never error the composer.
   */
  async listChatDraftRestores(
    sessionId: string,
  ): Promise<ChatDraftRestoresResponse> {
    let raw: unknown;
    try {
      raw = await this.fetch<unknown>(
        `/api/chat/sessions/${sessionId}/draft-restores`,
      );
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        return { restores: [] };
      }
      throw err;
    }
    return parseWithFallback(
      raw,
      ChatDraftRestoresResponseSchema,
      EMPTY_CHAT_DRAFT_RESTORES,
      {
        endpoint: "GET /api/chat/sessions/{id}/draft-restores",
      },
    );
  }

  /** Idempotent consume — deleting an already-consumed restore is a 204 no-op. */
  async consumeChatDraftRestore(
    sessionId: string,
    restoreId: string,
  ): Promise<void> {
    await this.fetch(
      `/api/chat/sessions/${sessionId}/draft-restores/${restoreId}`,
      {
        method: "DELETE",
      },
    );
  }

  async listPendingChatTasks(): Promise<PendingChatTasksResponse> {
    return this.fetch(`/api/chat/pending-tasks`);
  }

  async hasAnyPendingChatTasks(): Promise<HasPendingChatTasksResponse> {
    return this.fetch(`/api/chat/pending-tasks/has-any`);
  }

  async markChatSessionRead(sessionId: string): Promise<void> {
    await this.fetch(`/api/chat/sessions/${sessionId}/read`, {
      method: "POST",
    });
  }

  async reportChatReplyReceived(
    sessionId: string,
    messageId: string,
    data: ChatReplyReceivedRequest,
  ): Promise<void> {
    await this.fetch(
      `/api/chat/sessions/${sessionId}/messages/${messageId}/received`,
      {
        method: "POST",
        body: JSON.stringify(data),
      },
    );
  }

  // Advertises the durable draft-restore capability (#5219). The server only
  // defers the empty-transcript judgment — and therefore only withholds the
  // synchronous restore from the response — for clients that send this; without
  // it we would be treated as a pre-#5219 client and get the legacy behaviour.
  async cancelTaskById(
    taskId: string,
    options?: { queuedAction?: "edit" | "remove"; sessionId?: string },
  ): Promise<CancelTaskResponse> {
    const params = new URLSearchParams();
    if (options?.queuedAction) {
      if (!options.sessionId)
        throw new Error("sessionId is required for queued-only cancellation");
      params.set("expected_status", "queued");
      params.set("chat_session_id", options.sessionId);
      params.set("queue_action", options.queuedAction);
    }
    const query = params.size > 0 ? `?${params}` : "";
    const raw = await this.fetch<unknown>(
      `/api/tasks/${taskId}/cancel${query}`,
      {
        method: "POST",
        headers: { "X-Client-Capabilities": CHAT_DRAFT_RESTORE_CAPABILITY },
      },
    );
    return parseWithFallback(
      raw,
      CancelTaskResponseSchema,
      EMPTY_CANCEL_TASK_RESPONSE,
      {
        endpoint: "POST /api/tasks/{taskId}/cancel",
      },
    );
  }

  async listAttachments(issueId: string): Promise<Attachment[]> {
    return this.fetch(`/api/issues/${issueId}/attachments`);
  }

  // Fetches a fresh attachment metadata record. The server re-signs
  // `download_url` on every call (30 min expiry), so the click-time
  // download flow uses this endpoint to avoid handing the user a stale
  // signed URL cached in TanStack Query.
  async getAttachment(id: string): Promise<Attachment> {
    const raw = await this.fetch<unknown>(`/api/attachments/${id}`);
    return parseWithFallback(raw, AttachmentResponseSchema, EMPTY_ATTACHMENT, {
      endpoint: "GET /api/attachments/{id}",
    });
  }

  async deleteAttachment(id: string): Promise<void> {
    await this.fetch(`/api/attachments/${id}`, { method: "DELETE" });
  }

  // Fetches the raw bytes of a text-previewable attachment.
  //
  // The endpoint sidesteps CloudFront CORS (not configured on the CDN) and
  // bypasses Content-Disposition: attachment for the `text/*` family, both
  // of which would otherwise prevent the renderer from getting the body.
  // The server always replies with `text/plain; charset=utf-8` for safety;
  // the original MIME ships back in the `X-Original-Content-Type` header so
  // the preview dispatcher can choose between markdown / html / plain code.
  //
  // Routes through `fetchRaw` so it inherits the standard auth headers,
  // 401 → handleUnauthorized recovery, request-id logging, and ApiError
  // shape. 413 / 415 are translated to typed `Preview*Error` instances so
  // the modal can render specific fallbacks instead of generic failure.
  async getAttachmentTextContent(
    id: string,
  ): Promise<{ text: string; originalContentType: string }> {
    let res: Response;
    try {
      res = await this.fetchRaw(`/api/attachments/${id}/content`);
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 413) throw new PreviewTooLargeError();
        if (err.status === 415) throw new PreviewUnsupportedError();
      }
      throw err;
    }
    return {
      text: await res.text(),
      originalContentType: res.headers.get("X-Original-Content-Type") ?? "",
    };
  }

  // Fetches the raw bytes of an attachment through the unified download
  // endpoint.
  //
  // This is the last-resort inline-media path for deployments where the
  // server has no natively-loadable URL to offer. `GET /api/attachments/{id}`
  // only upgrades `download_url` to a signed storage URL under CloudFront
  // signing or presign mode; in **proxy** mode (self-hosted MinIO or any
  // storage endpoint on an internal host, which the default `auto` mode
  // classifies as proxy) it returns the auth-gated API path again. Clients
  // that cannot ride the session cookie on a native `<img>` resource fetch —
  // Desktop's file:// renderer, the mobile webview, split-origin web — get
  // the bytes here and render them from an object URL instead.
  //
  // Routes through `fetchRaw` so it inherits the standard auth headers,
  // 401 → handleUnauthorized recovery, request-id logging and ApiError shape.
  // Callers must only reach for this once the metadata refresh has shown
  // there is no signed URL: in the other modes the endpoint 302s to storage,
  // where CORS is not configured for a JS fetch.
  async getAttachmentBlob(id: string): Promise<Blob> {
    const res = await this.fetchRaw(`/api/attachments/${id}/download`);
    return res.blob();
  }

  // Projects
  async listProjects(params?: {
    status?: string;
  }): Promise<ListProjectsResponse> {
    const search = new URLSearchParams();
    if (params?.status) search.set("status", params.status);
    return this.fetch(`/api/projects?${search}`);
  }

  async getProject(id: string): Promise<Project> {
    return this.fetch(`/api/projects/${id}`);
  }

  async createProject(data: CreateProjectRequest): Promise<Project> {
    return this.fetch("/api/projects", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async updateProject(
    id: string,
    data: UpdateProjectRequest,
  ): Promise<Project> {
    return this.fetch(`/api/projects/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

  async deleteProject(id: string): Promise<void> {
    await this.fetch(`/api/projects/${id}`, { method: "DELETE" });
  }

  // Project resources
  async listProjectResources(
    projectId: string,
  ): Promise<ListProjectResourcesResponse> {
    return this.fetch(`/api/projects/${projectId}/resources`);
  }

  async createProjectResource(
    projectId: string,
    data: CreateProjectResourceRequest,
  ): Promise<ProjectResource> {
    return this.fetch(`/api/projects/${projectId}/resources`, {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async updateProjectResource(
    projectId: string,
    resourceId: string,
    data: UpdateProjectResourceRequest,
  ): Promise<ProjectResource> {
    return this.fetch(`/api/projects/${projectId}/resources/${resourceId}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

  async deleteProjectResource(
    projectId: string,
    resourceId: string,
  ): Promise<void> {
    await this.fetch(`/api/projects/${projectId}/resources/${resourceId}`, {
      method: "DELETE",
    });
  }

  // Labels
  async listLabels(
    resourceType: LabelResourceType = "issue",
    options?: { includeUsage?: boolean },
  ): Promise<ListLabelsResponse> {
    const search = new URLSearchParams({ resource_type: resourceType });
    if (options?.includeUsage === true) search.set("include_usage", "true");
    const raw = await this.fetch<unknown>(
      `/api/labels?${search.toString()}`,
    );
    return parseWithFallback(
      raw,
      ListLabelsResponseSchema,
      EMPTY_LIST_LABELS_RESPONSE,
      {
        endpoint: "GET /api/labels",
      },
    );
  }

  async getLabel(id: string): Promise<Label> {
    const raw = await this.fetch<unknown>(`/api/labels/${id}`);
    return parseWithFallback(raw, LabelSchema, EMPTY_LABEL, {
      endpoint: "GET /api/labels/{id}",
    });
  }

  async createLabel(data: CreateLabelRequest): Promise<Label> {
    const raw = await this.fetch<unknown>(`/api/labels`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, LabelSchema, EMPTY_LABEL, {
      endpoint: "POST /api/labels",
    });
  }

  async updateLabel(id: string, data: UpdateLabelRequest): Promise<Label> {
    const raw = await this.fetch<unknown>(`/api/labels/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, LabelSchema, EMPTY_LABEL, {
      endpoint: "PUT /api/labels/{id}",
    });
  }

  async deleteLabel(id: string): Promise<void> {
    await this.fetch(`/api/labels/${id}`, { method: "DELETE" });
  }

  async getLabelUsage(
    id: string,
    params: LabelUsageParams,
  ): Promise<LabelUsageResponse> {
    const search = new URLSearchParams({
      period: params.period,
      sort: params.sort,
      direction: params.direction,
      tz: params.tz,
      page: String(params.page),
      page_size: String(params.page_size),
    });
    const raw = await this.fetch<unknown>(
      `/api/labels/${encodeURIComponent(id)}/usage?${search.toString()}`,
    );
    return parseWithFallback(
      raw,
      LabelUsageResponseSchema,
      EMPTY_LABEL_USAGE_RESPONSE,
      { endpoint: "GET /api/labels/{id}/usage" },
    );
  }

  // Custom issue properties
  async listProperties(
    includeArchived = false,
  ): Promise<ListPropertiesResponse> {
    const suffix = includeArchived ? "?include_archived=true" : "";
    let raw: unknown;
    try {
      raw = await this.fetch<unknown>(`/api/properties${suffix}`);
    } catch (error) {
      // A backend predating custom properties 404s here (e.g. after a
      // server-only rollback). Treat it as an empty catalog: the property
      // UI sections disappear and the active-catalog reconciliation strips
      // persisted property sorts/filters, so no property params ever reach
      // the old server. Other errors keep normal query-error semantics.
      if (
        error instanceof Error &&
        "status" in error &&
        (error as { status?: number }).status === 404
      ) {
        return EMPTY_LIST_PROPERTIES_RESPONSE;
      }
      throw error;
    }
    return parseWithFallback(
      raw,
      ListPropertiesResponseSchema,
      EMPTY_LIST_PROPERTIES_RESPONSE,
      {
        endpoint: "GET /api/properties",
      },
    );
  }

  /**
   * Quick actions catalog — one projection for every caller.
   *
   * The server hides nothing beyond `private` ownership; whether the caller
   * may RUN an action is answered by runQuickAction, not here. There is
   * deliberately no "runnable only" mode: filtering the sidebar by permission
   * made two people looking at one issue see different sidebars with no
   * explanation.
   *
   * A backend predating quick actions 404s here; treat that as an empty
   * catalog so the sidebar section and settings tab simply do not render.
   */
  async listQuickActions(opts?: {
    includeArchived?: boolean;
  }): Promise<ListQuickActionsResponse> {
    const suffix =
      opts?.includeArchived === true ? "?include_archived=true" : "";
    let raw: unknown;
    try {
      raw = await this.fetch<unknown>(`/api/quick-actions${suffix}`);
    } catch (error) {
      if (
        error instanceof Error &&
        "status" in error &&
        (error as { status?: number }).status === 404
      ) {
        return EMPTY_LIST_QUICK_ACTIONS_RESPONSE;
      }
      throw error;
    }
    return parseWithFallback(
      raw,
      ListQuickActionsResponseSchema,
      EMPTY_LIST_QUICK_ACTIONS_RESPONSE,
      {
        endpoint: "GET /api/quick-actions",
      },
    );
  }

  async createQuickAction(
    data: CreateQuickActionRequest,
  ): Promise<QuickAction> {
    const raw = await this.fetch<unknown>(`/api/quick-actions`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, QuickActionSchema, EMPTY_QUICK_ACTION, {
      endpoint: "POST /api/quick-actions",
    });
  }

  async updateQuickAction(
    id: string,
    data: UpdateQuickActionRequest,
  ): Promise<QuickAction> {
    const raw = await this.fetch<unknown>(`/api/quick-actions/${id}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, QuickActionSchema, EMPTY_QUICK_ACTION, {
      endpoint: "PATCH /api/quick-actions/{id}",
    });
  }

  async deleteQuickAction(id: string): Promise<void> {
    await this.fetch<void>(`/api/quick-actions/${id}`, { method: "DELETE" });
  }

  /**
   * Run a quick action against one issue. The response is a Comment carrying
   * `trigger_outcomes` — the same shape POST /comments returns — so callers
   * reuse one result handler and inherit `queued` / `coalesced` / `deferred` /
   * `blocked` instead of a parallel vocabulary that would drift.
   */
  async runQuickAction(
    issueId: string,
    quickActionId: string,
  ): Promise<Comment> {
    const raw = await this.fetch<unknown>(
      `/api/issues/${issueId}/quick-actions/${quickActionId}/run`,
      {
        method: "POST",
      },
    );
    return parseWithFallback(raw, CommentSchema, EMPTY_COMMENT, {
      endpoint: "POST /api/issues/{id}/quick-actions/{quickActionId}/run",
    });
  }

  /**
   * What a quick action WOULD post, without posting it. Backs the composer
   * hand-off (⌥-click and the `/` menu) so the user can edit before sending.
   * Returns "" when the response cannot be read — callers must treat an empty
   * string as "insert nothing" rather than clearing the composer.
   */
  async renderQuickAction(
    issueId: string,
    quickActionId: string,
  ): Promise<string> {
    const raw = await this.fetch<unknown>(
      `/api/issues/${issueId}/quick-actions/${quickActionId}/render`,
      {
        method: "POST",
      },
    );
    const parsed = parseWithFallback(
      raw,
      QuickActionRenderSchema,
      { content: "" },
      {
        endpoint: "POST /api/issues/{id}/quick-actions/{quickActionId}/render",
      },
    );
    return parsed.content;
  }

  async createProperty(data: CreatePropertyRequest): Promise<IssueProperty> {
    const raw = await this.fetch<unknown>(`/api/properties`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, IssuePropertySchema, EMPTY_ISSUE_PROPERTY, {
      endpoint: "POST /api/properties",
    });
  }

  async updateProperty(
    id: string,
    data: UpdatePropertyRequest,
  ): Promise<IssueProperty> {
    const raw = await this.fetch<unknown>(`/api/properties/${id}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, IssuePropertySchema, EMPTY_ISSUE_PROPERTY, {
      endpoint: "PATCH /api/properties/{id}",
    });
  }

  async setIssueProperty(
    issueId: string,
    propertyId: string,
    value: IssuePropertyValue,
  ): Promise<IssuePropertiesResponse> {
    const raw = await this.fetch<unknown>(
      `/api/issues/${issueId}/properties/${propertyId}`,
      {
        method: "PUT",
        body: JSON.stringify({ value }),
      },
    );
    return parseWithFallback(
      raw,
      IssuePropertiesResponseSchema,
      EMPTY_ISSUE_PROPERTIES_RESPONSE,
      {
        endpoint: "PUT /api/issues/{id}/properties/{propertyId}",
      },
    );
  }

  async unsetIssueProperty(
    issueId: string,
    propertyId: string,
  ): Promise<IssuePropertiesResponse> {
    const raw = await this.fetch<unknown>(
      `/api/issues/${issueId}/properties/${propertyId}`,
      {
        method: "DELETE",
      },
    );
    return parseWithFallback(
      raw,
      IssuePropertiesResponseSchema,
      EMPTY_ISSUE_PROPERTIES_RESPONSE,
      {
        endpoint: "DELETE /api/issues/{id}/properties/{propertyId}",
      },
    );
  }

  async listLabelsForIssue(issueId: string): Promise<IssueLabelsResponse> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/labels`);
    return parseWithFallback(
      raw,
      ResourceLabelsResponseSchema,
      EMPTY_RESOURCE_LABELS_RESPONSE,
      {
        endpoint: "GET /api/issues/{id}/labels",
      },
    );
  }

  async attachLabel(
    issueId: string,
    labelId: string,
  ): Promise<IssueLabelsResponse> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/labels`, {
      method: "POST",
      body: JSON.stringify({ label_id: labelId }),
    });
    return parseWithFallback(
      raw,
      ResourceLabelsResponseSchema,
      EMPTY_RESOURCE_LABELS_RESPONSE,
      {
        endpoint: "POST /api/issues/{id}/labels",
      },
    );
  }

  async detachLabel(
    issueId: string,
    labelId: string,
  ): Promise<IssueLabelsResponse> {
    const raw = await this.fetch<unknown>(
      `/api/issues/${issueId}/labels/${labelId}`,
      {
        method: "DELETE",
      },
    );
    return parseWithFallback(
      raw,
      ResourceLabelsResponseSchema,
      EMPTY_RESOURCE_LABELS_RESPONSE,
      {
        endpoint: "DELETE /api/issues/{id}/labels/{labelId}",
      },
    );
  }

  async listLabelsForResource(
    resourceType: "agent" | "skill",
    resourceId: string,
  ): Promise<ResourceLabelsResponse> {
    const raw = await this.fetch<unknown>(
      `/api/${resourceType === "agent" ? "agents" : "skills"}/${resourceId}/labels`,
    );
    return parseWithFallback(
      raw,
      ResourceLabelsResponseSchema,
      EMPTY_RESOURCE_LABELS_RESPONSE,
      {
        endpoint: `GET /api/${resourceType === "agent" ? "agents" : "skills"}/{id}/labels`,
      },
    );
  }

  async attachLabelToResource(
    resourceType: "agent" | "skill",
    resourceId: string,
    labelId: string,
  ): Promise<ResourceLabelsResponse> {
    const raw = await this.fetch<unknown>(
      `/api/${resourceType === "agent" ? "agents" : "skills"}/${resourceId}/labels`,
      {
        method: "POST",
        body: JSON.stringify({ label_id: labelId }),
      },
    );
    return parseWithFallback(
      raw,
      ResourceLabelsResponseSchema,
      EMPTY_RESOURCE_LABELS_RESPONSE,
      {
        endpoint: `POST /api/${resourceType === "agent" ? "agents" : "skills"}/{id}/labels`,
      },
    );
  }

  async detachLabelFromResource(
    resourceType: "agent" | "skill",
    resourceId: string,
    labelId: string,
  ): Promise<ResourceLabelsResponse> {
    const raw = await this.fetch<unknown>(
      `/api/${resourceType === "agent" ? "agents" : "skills"}/${resourceId}/labels/${labelId}`,
      {
        method: "DELETE",
      },
    );
    return parseWithFallback(
      raw,
      ResourceLabelsResponseSchema,
      EMPTY_RESOURCE_LABELS_RESPONSE,
      {
        endpoint: `DELETE /api/${resourceType === "agent" ? "agents" : "skills"}/{id}/labels/{labelId}`,
      },
    );
  }

  // Saved issue views (MUL-4796). Responses go through zod so installed
  // desktop builds survive backend drift; a malformed list degrades to []
  // (selector shows only built-ins) rather than blanking the page.
  async listIssueViews(params: {
    scope_type: string;
    scope_id?: string | null;
  }): Promise<IssueView[]> {
    const qs = new URLSearchParams({ scope_type: params.scope_type });
    if (params.scope_id) qs.set("scope_id", params.scope_id);
    const raw = await this.fetch<unknown>(`/api/issue-views?${qs.toString()}`);
    return parseWithFallback(raw, IssueViewListSchema, [], {
      endpoint: "GET /api/issue-views",
    });
  }

  async createIssueView(
    data: CreateIssueViewRequest,
  ): Promise<IssueView | null> {
    const raw = await this.fetch<unknown>("/api/issue-views", {
      method: "POST",
      body: JSON.stringify(data),
    });
    // null fallback: the create itself succeeded server-side; a response we
    // cannot parse must not crash the dialog — callers refetch the list.
    return parseWithFallback(raw, IssueViewSchema.nullable(), null, {
      endpoint: "POST /api/issue-views",
    });
  }

  async updateIssueView(
    id: string,
    data: {
      name?: string;
      visibility?: "private" | "workspace";
      scope_variant?: string | null;
      query?: Record<string, unknown>;
      display?: Record<string, unknown>;
      expected_revision: number;
    },
  ): Promise<IssueView | null> {
    const raw = await this.fetch<unknown>(`/api/issue-views/${id}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, IssueViewSchema.nullable(), null, {
      endpoint: "PATCH /api/issue-views/{id}",
    });
  }

  async getIssueView(id: string): Promise<IssueView | null> {
    const raw = await this.fetch<unknown>(`/api/issue-views/${id}`);
    return parseWithFallback(raw, IssueViewSchema.nullable(), null, {
      endpoint: "GET /api/issue-views/{id}",
    });
  }

  async deleteIssueView(id: string): Promise<void> {
    await this.fetch(`/api/issue-views/${id}`, { method: "DELETE" });
  }

  async getIssueViewPreference(params: {
    scope_type: string;
    scope_id?: string | null;
  }): Promise<IssueViewPreference> {
    const qs = new URLSearchParams({ scope_type: params.scope_type });
    if (params.scope_id) qs.set("scope_id", params.scope_id);
    const raw = await this.fetch<unknown>(
      `/api/issue-view-preferences?${qs.toString()}`,
    );
    return parseWithFallback(
      raw,
      IssueViewPreferenceSchema,
      EMPTY_ISSUE_VIEW_PREFERENCE,
      {
        endpoint: "GET /api/issue-view-preferences",
      },
    );
  }

  async putIssueViewPreference(data: {
    scope_type: string;
    scope_id?: string | null;
    prefs: { hidden: string[]; order: string[] };
  }): Promise<IssueViewPreference> {
    const raw = await this.fetch<unknown>("/api/issue-view-preferences", {
      method: "PUT",
      body: JSON.stringify(data),
    });
    return parseWithFallback(
      raw,
      IssueViewPreferenceSchema,
      EMPTY_ISSUE_VIEW_PREFERENCE,
      {
        endpoint: "PUT /api/issue-view-preferences",
      },
    );
  }

  // Pins
  async listPins(): Promise<PinnedItem[]> {
    // include=view is the capability opt-in: the server withholds view pins
    // from clients that don't declare support (old builds treated any
    // non-issue pin as a project pin and auto-deleted it on 404).
    return this.fetch("/api/pins?include=view");
  }

  async createPin(data: CreatePinRequest): Promise<PinnedItem> {
    return this.fetch("/api/pins", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async deletePin(itemType: PinnedItemType, itemId: string): Promise<void> {
    await this.fetch(`/api/pins/${itemType}/${itemId}`, { method: "DELETE" });
  }

  async reorderPins(data: ReorderPinsRequest): Promise<void> {
    await this.fetch("/api/pins/reorder", {
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

  // Squads
  async listSquads(): Promise<Squad[]> {
    const raw = await this.fetch<unknown>(`/api/squads`);
    return parseWithFallback(raw, SquadListSchema, EMPTY_SQUAD_LIST, {
      endpoint: "GET /api/squads",
    }) as Squad[];
  }

  async getSquad(id: string): Promise<Squad> {
    const raw = await this.fetch<unknown>(`/api/squads/${id}`);
    return parseWithFallback(raw, SquadSchema, EMPTY_SQUAD, {
      endpoint: "GET /api/squads/:id",
    }) as Squad;
  }

  async createSquad(data: {
    name: string;
    description?: string;
    leader_id: string;
    avatar_url?: string;
  }): Promise<Squad> {
    const raw = await this.fetch<unknown>("/api/squads", {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, SquadSchema, EMPTY_SQUAD, {
      endpoint: "POST /api/squads",
    }) as Squad;
  }

  async updateSquad(
    id: string,
    data: {
      name?: string;
      description?: string;
      instructions?: string;
      leader_id?: string;
      avatar_url?: string;
    },
  ): Promise<Squad> {
    const raw = await this.fetch<unknown>(`/api/squads/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, SquadSchema, EMPTY_SQUAD, {
      endpoint: "PUT /api/squads/:id",
    }) as Squad;
  }

  async deleteSquad(id: string): Promise<void> {
    await this.fetch(`/api/squads/${id}`, { method: "DELETE" });
  }

  async listSquadMembers(squadId: string): Promise<SquadMember[]> {
    return this.fetch(`/api/squads/${squadId}/members`);
  }

  async addSquadMember(
    squadId: string,
    data: { member_type: string; member_id: string; role?: string },
  ): Promise<SquadMember> {
    return this.fetch(`/api/squads/${squadId}/members`, {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async removeSquadMember(
    squadId: string,
    data: { member_type: string; member_id: string },
  ): Promise<void> {
    await this.fetch(`/api/squads/${squadId}/members`, {
      method: "DELETE",
      body: JSON.stringify(data),
    });
  }

  async updateSquadMemberRole(
    squadId: string,
    data: { member_type: string; member_id: string; role: string },
  ): Promise<SquadMember> {
    return this.fetch(`/api/squads/${squadId}/members/role`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
  }

  // Per-squad members status snapshot: one row per member with derived
  // working/idle/offline/unstable plus the issues each agent is currently
  // running. Parsed with a lenient schema so a new server-side status
  // value or extra field can't white-screen the Squad page (#2143).
  async getSquadMemberStatus(
    squadId: string,
  ): Promise<SquadMemberStatusListResponse> {
    const raw = await this.fetch<unknown>(
      `/api/squads/${squadId}/members/status`,
    );
    return parseWithFallback(
      raw,
      SquadMemberStatusListResponseSchema,
      EMPTY_SQUAD_MEMBER_STATUS_LIST,
      {
        endpoint: "GET /api/squads/:id/members/status",
      },
    ) as SquadMemberStatusListResponse;
  }

  // Autopilots
  async listAutopilots(params?: {
    status?: string;
  }): Promise<ListAutopilotsResponse> {
    const search = new URLSearchParams();
    if (params?.status) search.set("status", params.status);
    const raw = await this.fetch<unknown>(`/api/autopilots?${search}`);
    return parseWithFallback(
      raw,
      ListAutopilotsResponseSchema,
      EMPTY_LIST_AUTOPILOTS_RESPONSE as ListAutopilotsResponse,
      { endpoint: "GET /api/autopilots" },
    );
  }

  async getAutopilot(id: string): Promise<GetAutopilotResponse> {
    const raw = await this.fetch<unknown>(`/api/autopilots/${id}`);
    return parseWithFallback(raw, GetAutopilotResponseSchema, FALLBACK_GET_AUTOPILOT_RESPONSE, { endpoint: "GET /api/autopilots/:id", includeReceived: false });
  }

  async createAutopilot(data: CreateAutopilotRequest): Promise<Autopilot> {
    return this.fetch("/api/autopilots", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async updateAutopilot(
    id: string,
    data: UpdateAutopilotRequest,
  ): Promise<Autopilot> {
    return this.fetch(`/api/autopilots/${id}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
  }

  async deleteAutopilot(id: string): Promise<void> {
    await this.fetch(`/api/autopilots/${id}`, { method: "DELETE" });
  }

  // Grant a workspace member explicit write access to the autopilot. Both
  // grant and revoke return the full updated collaborator list so callers can
  // refresh without a second round-trip.
  async grantAutopilotAccess(
    id: string,
    userId: string,
  ): Promise<AutopilotCollaboratorsResponse> {
    return this.fetch(`/api/autopilots/${id}/collaborators`, {
      method: "POST",
      body: JSON.stringify({ user_id: userId }),
    });
  }

  async revokeAutopilotAccess(
    id: string,
    userId: string,
  ): Promise<AutopilotCollaboratorsResponse> {
    return this.fetch(`/api/autopilots/${id}/collaborators/${userId}`, {
      method: "DELETE",
    });
  }

  async triggerAutopilot(id: string): Promise<AutopilotRun> {
    // Manual "run now" returns 200 even when admission blocks the run (status
    // skipped/failed). The UI branches on status/reason_code to avoid a
    // false-success toast (MUL-4525), so parse defensively rather than casting.
    const raw = await this.fetch<unknown>(`/api/autopilots/${id}/trigger`, {
      method: "POST",
    });
    return parseWithFallback(raw, AutopilotRunSchema, FALLBACK_AUTOPILOT_RUN, {
      endpoint: "POST /api/autopilots/:id/trigger",
    });
  }

  async listAutopilotRuns(
    id: string,
    params?: { limit?: number; offset?: number },
  ): Promise<ListAutopilotRunsResponse> {
    const search = new URLSearchParams();
    if (params?.limit) search.set("limit", params.limit.toString());
    if (params?.offset) search.set("offset", params.offset.toString());
    return this.fetch(`/api/autopilots/${id}/runs?${search}`);
  }

  // Returns a single run including its full trigger_payload. List responses
  // omit trigger_payload to keep them small (a webhook envelope can be
  // up to 256 KiB × limit rows), so the detail view fetches via this route.
  async getAutopilotRun(
    autopilotId: string,
    runId: string,
  ): Promise<AutopilotRun> {
    return this.fetch(`/api/autopilots/${autopilotId}/runs/${runId}`);
  }

  async createAutopilotTrigger(
    autopilotId: string,
    data: CreateAutopilotTriggerRequest,
  ): Promise<AutopilotTrigger> {
    const raw = await this.fetch<unknown>(`/api/autopilots/${autopilotId}/triggers`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    const parsed = parseWithFallback(raw, AutopilotTriggerSchema, FALLBACK_AUTOPILOT_TRIGGER, { endpoint: "automation trigger write", includeReceived: false });
    if (!parsed.id) throw new Error("The trigger response could not be read. Reload to verify the saved configuration.");
    return parsed;
  }

  async updateAutopilotTrigger(
    autopilotId: string,
    triggerId: string,
    data: UpdateAutopilotTriggerRequest,
  ): Promise<AutopilotTrigger> {
    const raw = await this.fetch<unknown>(`/api/autopilots/${autopilotId}/triggers/${triggerId}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    const parsed = parseWithFallback(raw, AutopilotTriggerSchema, FALLBACK_AUTOPILOT_TRIGGER, { endpoint: "automation trigger write", includeReceived: false });
    if (!parsed.id) throw new Error("The trigger response could not be read. Reload to verify the saved configuration.");
    return parsed;
  }

  async deleteAutopilotTrigger(
    autopilotId: string,
    triggerId: string,
  ): Promise<void> {
    await this.fetch(`/api/autopilots/${autopilotId}/triggers/${triggerId}`, {
      method: "DELETE",
    });
  }

  async cronPreview(params: {
    expr: string;
    tz: string;
  }): Promise<CronPreviewResponse> {
    const search = new URLSearchParams();
    search.set("expr", params.expr);
    search.set("tz", params.tz);
    const raw = await this.fetch<unknown>(
      `/api/autopilots/cron-preview?${search}`,
    );
    return parseWithFallback(
      raw,
      CronPreviewResponseSchema,
      UNREADABLE_CRON_PREVIEW_RESPONSE,
      { endpoint: "GET /api/autopilots/cron-preview" },
    );
  }

  async rotateAutopilotTriggerWebhookToken(
    autopilotId: string,
    triggerId: string,
  ): Promise<AutopilotTrigger> {
    return this.fetch(
      `/api/autopilots/${autopilotId}/triggers/${triggerId}/rotate-webhook-token`,
      { method: "POST" },
    );
  }

  // Webhook deliveries — list is slim (no raw_body / selected_headers /
  // response_body); detail returns the full row. Both responses are parsed
  // through a lenient schema so an unknown server-side `status` /
  // `signature_status` value degrades to a generic row instead of dropping
  // the whole list.
  async listAutopilotDeliveries(
    autopilotId: string,
    params?: { limit?: number; offset?: number },
  ): Promise<ListWebhookDeliveriesResponse> {
    const search = new URLSearchParams();
    if (params?.limit) search.set("limit", params.limit.toString());
    if (params?.offset) search.set("offset", params.offset.toString());
    const raw = await this.fetch<unknown>(
      `/api/autopilots/${autopilotId}/deliveries?${search}`,
    );
    return parseWithFallback(
      raw,
      ListWebhookDeliveriesResponseSchema,
      EMPTY_LIST_WEBHOOK_DELIVERIES_RESPONSE,
      { endpoint: "GET /api/autopilots/:id/deliveries" },
    );
  }

  async getAutopilotDelivery(
    autopilotId: string,
    deliveryId: string,
  ): Promise<WebhookDelivery> {
    const raw = await this.fetch<unknown>(
      `/api/autopilots/${autopilotId}/deliveries/${deliveryId}`,
    );
    return parseWithFallback(
      raw,
      WebhookDeliveryResponseSchema,
      { ...EMPTY_WEBHOOK_DELIVERY, id: deliveryId, autopilot_id: autopilotId },
      { endpoint: "GET /api/autopilots/:id/deliveries/:deliveryId" },
    );
  }

  // Replay creates a NEW delivery row referencing the original via
  // `replayed_from_delivery_id`. Server rejects replays of
  // signature-invalid / rejected deliveries with 400 — the UI keeps the
  // button disabled for those rows, but the server is the source of truth.
  async replayAutopilotDelivery(
    autopilotId: string,
    deliveryId: string,
  ): Promise<WebhookDelivery> {
    const raw = await this.fetch<unknown>(
      `/api/autopilots/${autopilotId}/deliveries/${deliveryId}/replay`,
      { method: "POST" },
    );
    return parseWithFallback(
      raw,
      WebhookDeliveryResponseSchema,
      { ...EMPTY_WEBHOOK_DELIVERY, autopilot_id: autopilotId },
      { endpoint: "POST /api/autopilots/:id/deliveries/:deliveryId/replay" },
    );
  }

  // GitHub integration
  async getGitHubConnectURL(
    workspaceId: string,
    returnTo?: "github" | "repositories",
  ): Promise<GitHubConnectResponse> {
    const search = new URLSearchParams();
    if (returnTo) search.set("return_to", returnTo);
    const suffix = search.size > 0 ? `?${search.toString()}` : "";
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/github/connect${suffix}`,
    );
    return parseWithFallback(
      raw,
      GitHubConnectResponseSchema,
      EMPTY_GITHUB_CONNECT_RESPONSE,
      { endpoint: "GET /api/workspaces/:id/github/connect" },
    );
  }

  async listGitHubInstallations(
    workspaceId: string,
  ): Promise<ListGitHubInstallationsResponse> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/github/installations`,
    );
    return parseWithFallback(
      raw,
      ListGitHubInstallationsResponseSchema,
      EMPTY_GITHUB_INSTALLATIONS,
      { endpoint: "GET /api/workspaces/:id/github/installations" },
    );
  }

  async reuseGitHubInstallation(
    workspaceId: string,
    sourceInstallationId: string,
  ): Promise<GitHubInstallation> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/github/installations/reuse`,
      {
        method: "POST",
        body: JSON.stringify({ source_installation_id: sourceInstallationId }),
      },
    );
    return parseWithFallback(
      raw,
      GitHubInstallationSchema,
      EMPTY_GITHUB_INSTALLATION,
      { endpoint: "POST /api/workspaces/:id/github/installations/reuse" },
    );
  }

  async listGitHubInstallationRepositories(
    workspaceId: string,
    installationId: string,
    params: { page?: number; per_page?: number } = {},
  ): Promise<ListGitHubRepositoriesResponse> {
    const search = new URLSearchParams();
    if (params.page !== undefined) search.set("page", String(params.page));
    if (params.per_page !== undefined)
      search.set("per_page", String(params.per_page));
    const suffix = search.size > 0 ? `?${search.toString()}` : "";
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/github/installations/${installationId}/repositories${suffix}`,
    );
    return parseWithFallback(
      raw,
      ListGitHubRepositoriesResponseSchema,
      EMPTY_LIST_GITHUB_REPOSITORIES_RESPONSE,
      {
        endpoint:
          "GET /api/workspaces/:id/github/installations/:installationId/repositories",
      },
    );
  }

  async deleteGitHubInstallation(
    workspaceId: string,
    installationId: string,
  ): Promise<void> {
    await this.fetch(
      `/api/workspaces/${workspaceId}/github/installations/${installationId}`,
      {
        method: "DELETE",
      },
    );
  }

  async resolveGitRepository(wsId: string, repository: string): Promise<GitRepositoryIdentity> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${wsId}/git/repository?repository=${encodeURIComponent(repository)}`);
    const result = parseWithFallback<GitRepositoryIdentity | null>(raw, GitRepositoryIdentitySchema, null, { endpoint: "GET /git/repository", includeReceived: false });
    if (!result) throw new Error("Invalid Git repository response");
    return result;
  }

  async listGitConnections(wsId: string): Promise<GitConnections> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${wsId}/git/connections`);
    const result = parseWithFallback<GitConnections | null>(raw, GitConnectionsSchema, null, { endpoint: "GET /git/connections", includeReceived: false });
    if (!result) throw new Error("Invalid Git connections response");
    return result;
  }

  async deleteGitConnection(wsId: string, id: string): Promise<void> {
    await this.fetch(`/api/workspaces/${wsId}/git/connections/${encodeURIComponent(id)}`, { method: "DELETE" });
  }

  async previewGitAgent(
    workspaceId: string,
    data: GitAgentPreviewRequest,
  ): Promise<GitAgentPreview> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/git/agent-preview`,
      { method: "POST", body: JSON.stringify(data) },
    );
    return parseWithFallback(
      raw,
      GitAgentPreviewSchema,
      EMPTY_GIT_AGENT_PREVIEW,
      {
        endpoint: "POST /api/workspaces/:id/git/agent-preview",
      },
    );
  }

  async getAgentPackageBindings(agentId: string): Promise<AgentPackageBindingReport> {
    const raw = await this.fetch<unknown>(`/api/agents/${agentId}/package-bindings`);
    const result = parseWithFallback<AgentPackageBindingReport | null>(raw, AgentPackageBindingReportSchema, null, { endpoint: "GET /api/agents/:id/package-bindings", includeReceived: false });
    if (!result) throw new Error("Invalid Agent package binding response");
    return result;
  }

  async confirmAgentPackageBinding(agentId: string, request: ConfirmAgentPackageBindingRequest): Promise<AgentPackageBindingReport> {
    const raw = await this.fetch<unknown>(`/api/agents/${agentId}/package-bindings/confirm`, { method: "POST", body: JSON.stringify(request) });
    const result = parseWithFallback<AgentPackageBindingReport | null>(raw, AgentPackageBindingReportSchema, null, { endpoint: "POST /api/agents/:id/package-bindings/confirm", includeReceived: false });
    if (!result) throw new Error("Invalid Agent package binding response");
    return result;
  }

  async previewAgentPackage(workspaceId: string, file: Blob): Promise<AgentPackagePreview> {
    if (!file.size || file.size > 40 * 1024 * 1024) throw new Error("Agent ZIP must be between 1 byte and 40 MiB");
    const response = await this.fetchRaw(`/api/workspaces/${workspaceId}/agent-packages/preview`, {
      method: "POST", headers: { "Content-Type": "application/zip" }, body: file,
    });
    const raw: unknown = await response.json();
    const result = parseWithFallback<AgentPackagePreview | null>(raw, AgentPackagePreviewSchema, null, { endpoint: "POST /api/workspaces/:id/agent-packages/preview", includeReceived: false });
    if (!result) throw new Error("Invalid Agent package preview response");
    return result;
  }

  getAgentSchemaUrl(): string {
    return `${this.baseUrl}/api/agent-schema`;
  }

  async getAgentManifestSchema(): Promise<Record<string, unknown>> {
    const blob = await this.downloadAgentSchema();
    const raw: unknown = JSON.parse(await blob.text());
    const result = parseWithFallback<Record<string, unknown> | null>(raw, AgentManifestSchemaDownloadSchema, null, { endpoint: "GET /api/agent-schema", includeReceived: false });
    if (!result) throw new Error("Invalid agent schema response");
    return result;
  }

  async prepareAgentPackage(workspaceId: string, content: string): Promise<AgentPackagePreview> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/agent-packages/prepare`, { method: "POST", body: content });
    const result = parseWithFallback<AgentPackagePreview | null>(raw, AgentPackagePreviewSchema, null, { endpoint: "POST /api/workspaces/:id/agent-packages/prepare", includeReceived: false });
    if (!result) throw new Error("Invalid Agent package preview response");
    return result;
  }

  async downloadPreparedAgentPackage(workspaceId: string, previewId: string): Promise<Blob> {
    const response = await this.fetchRaw(`/api/workspaces/${workspaceId}/agent-packages/${encodeURIComponent(previewId)}/download`);
    if (response.headers.get("content-type")?.split(";")[0] !== "application/zip") throw new Error("Invalid Agent package download response");
    const blob = await response.blob();
    if (!blob.size) throw new Error("Empty Agent package download response");
    return blob;
  }

  async previewAgentPackagePublication(agentId: string, file: Blob): Promise<AgentSourceSyncPreview> {
    if (!file.size || file.size > 40 * 1024 * 1024) throw new Error("Agent ZIP must be between 1 byte and 40 MiB");
    const response = await this.fetchRaw(`/api/agents/${encodeURIComponent(agentId)}/source/preview`, { method: "POST", headers: { "Content-Type": "application/zip" }, body: file });
    const raw: unknown = await response.json();
    const result = parseWithFallback<AgentSourceSyncPreview | null>(raw, AgentSourceSyncPreviewSchema, null, { endpoint: "POST /api/agents/:id/source/preview", includeReceived: false });
    if (!result?.preview_id || !result.resolved_sha) throw new Error("Invalid Agent package publication preview");
    return result;
  }

  async createAgentFromPackage(workspaceId: string, data: CreateAgentPackageRequest): Promise<CreateAgentPackageResponse> {
    if (!data.preview_id) throw new Error("Preview the Agent package before creating it");
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/agent-packages`, { method: "POST", body: JSON.stringify(data) });
    const result = parseWithFallback(raw, CreateAgentPackageResponseSchema, EMPTY_CREATE_AGENT_PACKAGE_RESPONSE, { endpoint: "POST /api/workspaces/:id/agent-packages", includeReceived: false });
    if (!result.agent.id || !result.source.synced_commit_sha) throw new Error("Invalid Agent package creation response");
    return result;
  }

  async exportAgent(agentId: string): Promise<Blob> {
    const response = await this.fetchRaw(`/api/agents/${encodeURIComponent(agentId)}/export`);
    if (response.headers.get("content-type")?.split(";")[0] !== "application/zip") {
      throw new Error("Invalid agent export response");
    }
    const blob = await response.blob();
    if (blob.size === 0) throw new Error("Empty agent export response");
    return blob;
  }

  async downloadAgentSchema(): Promise<Blob> {
    const response = await this.fetchRaw("/api/agent-schema");
    if (response.headers.get("content-type")?.split(";")[0] !== "application/schema+json") {
      throw new Error("Invalid agent schema response");
    }
    const content = await response.text();
    let raw: unknown;
    try { raw = JSON.parse(content); } catch { throw new Error("Invalid agent schema response"); }
    const schema = parseWithFallback<Record<string, unknown> | null>(raw, AgentManifestSchemaDownloadSchema, null, { endpoint: "GET /api/agent-schema", includeReceived: false });
    if (!schema) throw new Error("Invalid agent schema response");
    return new Blob([content], { type: "application/schema+json" });
  }

  async getAgentSource(agentId: string): Promise<AgentSource> {
    const raw = await this.fetch<unknown>(`/api/agents/${agentId}/source`);
    return parseWithFallback(raw, AgentSourceSchema, EMPTY_AGENT_SOURCE, {
      endpoint: "GET /api/agents/:id/source",
    });
  }

  async listAgentPublications(agentId: string, before?: string): Promise<AgentPublicationList> {
    const query = before ? `?before=${encodeURIComponent(before)}` : "";
    const raw = await this.fetch<unknown>(`/api/agents/${agentId}/source/publications${query}`);
    const result = parseWithFallback<AgentPublicationList | null>(raw, AgentPublicationListSchema, null, { endpoint: "GET /api/agents/:id/source/publications", includeReceived: false });
    if (!result) throw new Error("Invalid Agent publication history response");
    return result;
  }

  async previewAgentPublicationRollback(agentId: string, publicationId: string): Promise<AgentSourceSyncPreview> {
    const raw = await this.fetch<unknown>(`/api/agents/${agentId}/source/preview`, { method: "POST", body: JSON.stringify({ publication_id: publicationId }) });
    const result = parseWithFallback<AgentSourceSyncPreview | null>(raw, AgentSourceSyncPreviewSchema, null, { endpoint: "POST /api/agents/:id/source/preview", includeReceived: false });
    if (!result || result.rollback_of !== publicationId) throw new Error("Invalid Agent rollback preview response");
    return result;
  }

  async listAgentSourceBranches(agentId: string): Promise<AgentSourceBranches> {
    const raw = await this.fetch<unknown>(`/api/agents/${agentId}/source/branches`);
    return parseWithFallback(raw, AgentSourceBranchesSchema, EMPTY_AGENT_SOURCE_BRANCHES, {
      endpoint: "GET /api/agents/:id/source/branches",
    });
  }

  async listGitAgentBranches(workspaceId: string, connectionId: string, repository: string): Promise<AgentSourceBranches> {
    const params = new URLSearchParams({ connection_id: connectionId, repository });
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/git/refs?${params}`);
    return parseWithFallback(raw, AgentSourceBranchesSchema, EMPTY_AGENT_SOURCE_BRANCHES, {
      endpoint: "GET /api/workspaces/:id/git/refs",
    });
  }

  async previewAgentSourceSync(agentId: string, ref: string): Promise<AgentSourceSyncPreview> {
    const raw = await this.fetch<unknown>(`/api/agents/${agentId}/source/preview`, {
      method: "POST", body: JSON.stringify({ ref }),
    });
    return parseWithFallback(raw, AgentSourceSyncPreviewSchema, EMPTY_AGENT_SOURCE_SYNC_PREVIEW, {
      endpoint: "POST /api/agents/:id/source/preview",
    });
  }

  async syncAgentSource(agentId: string, previewId: string, bindings?: { secrets?: Record<string, string>; deferred_bindings?: string[]; dsh_plugin_bindings?: Record<string,string> }): Promise<SyncAgentSourceResponse> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${agentId}/source/sync`,
      {
        method: "POST",
        body: JSON.stringify({ preview_id: previewId, ...bindings }),
      },
    );
    return parseWithFallback(
      raw,
      SyncAgentSourceResponseSchema,
      EMPTY_SYNC_AGENT_SOURCE_RESPONSE,
      { endpoint: "POST /api/agents/:id/source/sync" },
    );
  }

  async listIssuePullRequests(
    issueId: string,
  ): Promise<{ pull_requests: GitHubPullRequest[] }> {
    const raw = await this.fetch<unknown>(
      `/api/issues/${issueId}/pull-requests`,
    );
    return parseWithFallback(
      raw,
      IssuePullRequestsResponseSchema,
      EMPTY_ISSUE_PULL_REQUESTS_RESPONSE,
      { endpoint: "GET /api/issues/:id/pull-requests" },
    );
  }

  // VCS integration (Forgejo / Gitea / GitLab)
  async listVCSConnections(
    workspaceId: string,
  ): Promise<ListVCSConnectionsResponse> {
    return this.fetch(`/api/workspaces/${workspaceId}/vcs/connections`);
  }

  async connectVCS(
    workspaceId: string,
    body: ConnectVCSRequest,
  ): Promise<ConnectVCSResponse> {
    return this.fetch(`/api/workspaces/${workspaceId}/vcs/connections`, {
      method: "POST",
      body: JSON.stringify(body),
    });
  }

  async deleteVCSConnection(
    workspaceId: string,
    connectionId: string,
  ): Promise<void> {
    await this.fetch(
      `/api/workspaces/${workspaceId}/vcs/connections/${connectionId}`,
      {
        method: "DELETE",
      },
    );
  }

  async rotateVCSWebhook(
    workspaceId: string,
    connectionId: string,
  ): Promise<ConnectVCSResponse> {
    return this.fetch(
      `/api/workspaces/${workspaceId}/vcs/connections/${connectionId}/rotate-webhook`,
      { method: "POST" },
    );
  }

  // Lark integration
  async listLarkInstallations(
    workspaceId: string,
  ): Promise<ListLarkInstallationsResponse> {
    return this.fetch(`/api/workspaces/${workspaceId}/lark/installations`);
  }

  async beginLarkInstall(
    workspaceId: string,
    agentId: string,
    region: "feishu" | "lark",
  ): Promise<BeginLarkInstallResponse> {
    // The user picks the cloud explicitly in the UI ("Bind to Feishu"
    // vs "Bind to Lark"), and the backend POSTs the device-flow `begin`
    // against the corresponding accounts host (accounts.feishu.cn vs
    // accounts.larksuite.com) so the QR renders against the right
    // cloud up front. Empty / omitted region still resolves to Feishu
    // server-side (RegionOrDefault) — we surface region as a required
    // arg here so every call site is forced to make a deliberate
    // choice rather than silently defaulting to mainland.
    const search = new URLSearchParams({ agent_id: agentId, region });
    return this.fetch(
      `/api/workspaces/${workspaceId}/lark/install/begin?${search.toString()}`,
      {
        method: "POST",
      },
    );
  }

  async getLarkInstallStatus(
    workspaceId: string,
    sessionId: string,
  ): Promise<LarkInstallStatusResponse> {
    return this.fetch(
      `/api/workspaces/${workspaceId}/lark/install/${sessionId}/status`,
    );
  }

  async deleteLarkInstallation(
    workspaceId: string,
    installationId: string,
  ): Promise<void> {
    await this.fetch(
      `/api/workspaces/${workspaceId}/lark/installations/${installationId}`,
      {
        method: "DELETE",
      },
    );
  }

  async redeemLarkBindingToken(
    token: string,
  ): Promise<RedeemLarkBindingTokenResponse> {
    return this.fetch(`/api/lark/binding/redeem`, {
      method: "POST",
      body: JSON.stringify({ token }),
    });
  }

  // DingTalk bot integration (scan-to-create device flow)
  async listDingTalkInstallations(
    workspaceId: string,
  ): Promise<ListDingTalkInstallationsResponse> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/dingtalk/installations`,
    );
    return ListDingTalkInstallationsResponseSchema.parse(raw);
  }

  async beginDingTalkInstall(
    workspaceId: string,
    agentId: string,
    allowUnbound = false,
    transportMode: "STREAM" | "HTTP_CALLBACK" = "STREAM",
  ): Promise<BeginDingTalkInstallResponse> {
    const search = new URLSearchParams({ agent_id: agentId });
    if (allowUnbound) search.set("allow_unbound", "true");
    search.set("transport_mode", transportMode);
    return this.fetch(
      `/api/workspaces/${workspaceId}/dingtalk/install/begin?${search.toString()}`,
      {
        method: "POST",
      },
    );
  }

  async getDingTalkInstallStatus(
    workspaceId: string,
    sessionId: string,
  ): Promise<DingTalkInstallStatusResponse> {
    return this.fetch(
      `/api/workspaces/${workspaceId}/dingtalk/install/${sessionId}/status`,
    );
  }

  // Manual install: create the DingTalk bot installation directly from an
  // operator-supplied AppKey/AppSecret, the fallback for when the
  // scan-to-create device flow is unavailable. Available whenever DingTalk
  // is configured (independent of install_supported).
  async manualInstallDingTalk(
    workspaceId: string,
    agentId: string,
    params: {
      clientId: string;
      clientSecret: string;
      robotCode: string;
      allowUnbound?: boolean;
    },
  ): Promise<DingTalkInstallation> {
    return this.fetch(
      `/api/workspaces/${workspaceId}/dingtalk/install/manual`,
      {
        method: "POST",
        body: JSON.stringify({
          agent_id: agentId,
          client_id: params.clientId,
          client_secret: params.clientSecret,
          robot_code: params.robotCode,
          allow_unbound: params.allowUnbound ?? false,
        }),
      },
    );
  }

  async deleteDingTalkInstallation(
    workspaceId: string,
    installationId: string,
  ): Promise<void> {
    await this.fetch(
      `/api/workspaces/${workspaceId}/dingtalk/installations/${installationId}`,
      {
        method: "DELETE",
      },
    );
  }

  async retryDingTalkRouterRegistration(
    workspaceId: string,
    installationId: string,
  ): Promise<DingTalkInstallation> {
    return this.fetch(
      `/api/workspaces/${workspaceId}/dingtalk/installations/${installationId}/router/retry`,
      { method: "POST" },
    );
  }

  async redeemDingTalkBindingToken(
    token: string,
  ): Promise<RedeemDingTalkBindingTokenResponse> {
    return this.fetch(`/api/dingtalk/binding/redeem`, {
      method: "POST",
      body: JSON.stringify({ token }),
    });
  }

  // DingTalk account binding (independent from the DingTalk bot installation)
  async listReusableDingTalkIdentities(workspaceId: string, agentId: string): Promise<ReusableDingTalkIdentity[]> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/dingtalk/execution-identities?agent_id=${encodeURIComponent(agentId)}`,
    );
    return parseWithFallback(raw, ReusableDingTalkIdentitiesSchema, [], { endpoint: "listReusableDingTalkIdentities", includeReceived: false });
  }

  async reuseDingTalkIdentity(workspaceId: string, agentId: string, sourceAgentId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/dingtalk/execution-identities/reuse`, {
      method: "POST",
      body: JSON.stringify({ agent_id: agentId, source_agent_id: sourceAgentId }),
    });
  }

  async listDingTalkAccountBindings(
    workspaceId: string,
  ): Promise<DingTalkAccountBindingsResponse> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/dingtalk/account-bindings`,
    );
    return parseWithFallback(
      raw,
      DingTalkAccountBindingsResponseSchema,
      EMPTY_DINGTALK_ACCOUNT_BINDINGS_RESPONSE,
      { endpoint: "GET /api/workspaces/:id/dingtalk/account-bindings" },
    );
  }

  async beginDingTalkAccountBinding(
    workspaceId: string,
    agentId: string,
    bindingMode: "message" | "identity",
  ): Promise<BeginDingTalkAccountBindingResponse> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/dingtalk/account-bindings/begin`,
      {
        method: "POST",
        body: JSON.stringify({ agent_id: agentId, binding_mode: bindingMode }),
      },
    );
    return parseWithFallback(
      raw,
      BeginDingTalkAccountBindingResponseSchema,
      EMPTY_BEGIN_DINGTALK_ACCOUNT_BINDING_RESPONSE,
      {
        endpoint: "POST /api/workspaces/:id/dingtalk/account-bindings/begin",
        includeReceived: false,
      },
    );
  }

  async deleteDingTalkAccountBinding(
    workspaceId: string,
    agentId: string,
    bindingMode: "message" | "identity",
  ): Promise<void> {
    await this.fetch(
      `/api/workspaces/${workspaceId}/dingtalk/account-bindings/${agentId}?binding_mode=${bindingMode}`,
      { method: "DELETE" },
    );
  }

  async updateDingTalkAccountBindingSurface(
    workspaceId: string,
    agentId: string,
    surfaceType: DingTalkProcessingSurface,
  ): Promise<void> {
    await this.fetch(
      `/api/workspaces/${workspaceId}/dingtalk/account-bindings/${agentId}/surface`,
      {
        method: "PATCH",
        body: JSON.stringify({ surface_type: surfaceType }),
      },
    );
  }

  // Agent Identity GitHub user identity for sandbox credentials.
  async getAgentIdentityGitHubStatus(
    workspaceId: string,
    agentId: string,
  ): Promise<AgentIdentityGitHubStatusResponse> {
    const search = new URLSearchParams({ agent_id: agentId });
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/agent-identity/github/status?${search.toString()}`,
    );
    return parseWithFallback(
      raw,
      AgentIdentityGitHubStatusResponseSchema,
      EMPTY_AGENT_IDENTITY_GITHUB_STATUS_RESPONSE,
      { endpoint: "GET /api/workspaces/:id/agent-identity/github/status" },
    );
  }

  async beginAgentIdentityGitHubOAuth(
    workspaceId: string,
    agentId: string,
    returnUrl: string,
  ): Promise<BeginAgentIdentityGitHubOAuthResponse> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/agent-identity/github/oauth/start`,
      {
        method: "POST",
        body: JSON.stringify({ agent_id: agentId, return_url: returnUrl }),
      },
    );
    return parseWithFallback(
      raw,
      BeginAgentIdentityGitHubOAuthResponseSchema,
      EMPTY_BEGIN_AGENT_IDENTITY_GITHUB_OAUTH_RESPONSE,
      {
        endpoint: "POST /api/workspaces/:id/agent-identity/github/oauth/start",
        includeReceived: false,
      },
    );
  }

  async testAgentIdentityGitHubConnection(
    workspaceId: string,
    agentId: string,
    connectionId: string,
  ): Promise<TestAgentIdentityGitHubConnectionResponse> {
    const search = new URLSearchParams({ agent_id: agentId });
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/agent-identity/github/${encodeURIComponent(connectionId)}/test?${search.toString()}`,
      { method: "POST" },
    );
    return parseWithFallback(
      raw,
      TestAgentIdentityGitHubConnectionResponseSchema,
      EMPTY_TEST_AGENT_IDENTITY_GITHUB_CONNECTION_RESPONSE,
      {
        endpoint:
          "POST /api/workspaces/:id/agent-identity/github/:connectionId/test",
      },
    );
  }

  async disconnectAgentIdentityGitHubConnection(
    workspaceId: string,
    agentId: string,
    connectionId: string,
  ): Promise<DisconnectAgentIdentityGitHubConnectionResponse> {
    const search = new URLSearchParams({ agent_id: agentId });
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/agent-identity/github/${encodeURIComponent(connectionId)}?${search.toString()}`,
      { method: "DELETE" },
    );
    return parseWithFallback(
      raw,
      DisconnectAgentIdentityGitHubConnectionResponseSchema,
      EMPTY_DISCONNECT_AGENT_IDENTITY_GITHUB_CONNECTION_RESPONSE,
      {
        endpoint:
          "DELETE /api/workspaces/:id/agent-identity/github/:connectionId",
      },
    );
  }

  async getAgentEnterpriseIdentityStatus(
    workspaceId: string,
    agentId: string,
  ): Promise<AgentEnterpriseIdentityStatusResponse> {
    const search = new URLSearchParams({ agent_id: agentId });
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/agent-identity/enterprise/status?${search.toString()}`,
    );
    return parseWithFallback(
      raw,
      AgentEnterpriseIdentityStatusResponseSchema,
      EMPTY_AGENT_ENTERPRISE_IDENTITY_STATUS_RESPONSE,
      {
        endpoint: "GET /api/workspaces/:id/agent-identity/enterprise/status",
        includeReceived: false,
      },
    );
  }

  async beginAgentEnterpriseIdentityBinding(
    workspaceId: string,
    agentId: string,
    redirectPath: string,
  ): Promise<BeginAgentEnterpriseIdentityBindingResponse> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/agent-identity/enterprise/oauth/start`,
      {
        method: "POST",
        body: JSON.stringify({
          agent_id: agentId,
          redirect_path: redirectPath,
        }),
      },
    );
    return parseWithFallback(
      raw,
      BeginAgentEnterpriseIdentityBindingResponseSchema,
      EMPTY_BEGIN_AGENT_ENTERPRISE_IDENTITY_BINDING_RESPONSE,
      {
        endpoint:
          "POST /api/workspaces/:id/agent-identity/enterprise/oauth/start",
        includeReceived: false,
      },
    );
  }

  async revokeAgentEnterpriseIdentity(
    workspaceId: string,
    agentId: string,
  ): Promise<void> {
    const search = new URLSearchParams({ agent_id: agentId });
    await this.fetch(
      `/api/workspaces/${workspaceId}/agent-identity/enterprise?${search.toString()}`,
      { method: "DELETE" },
    );
  }

  // Composio integration (MUL-3720). All routes are user-scoped (a connection
  // belongs to a user, not a workspace), so none take a workspaceId.

  /** The project's connectable Composio toolkits (those with an enabled auth
   * config). Since MUL-4009 the backend filters out non-connectable toolkits,
   * so every entry has `connectable: true`. A resolver/upstream failure is a
   * 502 rather than an empty list. */
  async listComposioToolkits(): Promise<ComposioToolkit[]> {
    return this.fetch(`/api/integrations/composio/toolkits`);
  }

  /** The caller's active Composio connections. */
  async listComposioConnections(): Promise<ComposioConnection[]> {
    return this.fetch(`/api/integrations/composio/connections`);
  }

  /** Starts a hosted Composio connect flow for a toolkit and returns the
   * redirect URL the browser should be sent to. */
  async beginComposioConnect(
    toolkitSlug: string,
  ): Promise<ComposioConnectInitResponse> {
    return this.fetch(`/api/integrations/composio/connect/init`, {
      method: "POST",
      body: JSON.stringify({ toolkit_slug: toolkitSlug }),
    });
  }

  /** Disconnects a Composio connection the caller owns. */
  async deleteComposioConnection(connectionId: string): Promise<void> {
    await this.fetch(`/api/integrations/composio/connections/${connectionId}`, {
      method: "DELETE",
    });
  }

  // Slack integration (MUL-3666)
  async listSlackInstallations(
    workspaceId: string,
  ): Promise<ListSlackInstallationsResponse> {
    return this.fetch(`/api/workspaces/${workspaceId}/slack/installations`);
  }

  // registerSlackBYO performs a bring-your-own-app install: the admin pastes the
  // bot token (xoxb-) + app-level token (xapp-) of the Slack app they created,
  // and the backend validates + persists it, returning the new installation.
  async registerSlackBYO(
    workspaceId: string,
    agentId: string,
    body: RegisterSlackBYORequest,
  ): Promise<SlackInstallation> {
    const search = new URLSearchParams({ agent_id: agentId });
    return this.fetch(
      `/api/workspaces/${workspaceId}/slack/install/byo?${search.toString()}`,
      {
        method: "POST",
        body: JSON.stringify(body),
      },
    );
  }

  async deleteSlackInstallation(
    workspaceId: string,
    installationId: string,
  ): Promise<void> {
    await this.fetch(
      `/api/workspaces/${workspaceId}/slack/installations/${installationId}`,
      {
        method: "DELETE",
      },
    );
  }

  async redeemSlackBindingToken(
    token: string,
  ): Promise<RedeemSlackBindingTokenResponse> {
    return this.fetch(`/api/slack/binding/redeem`, {
      method: "POST",
      body: JSON.stringify({ token }),
    });
  }

  // WeCom smart-bot ("智能机器人" / aibot) integration. The bot dials a
  // WebSocket long connection to wss://openws.work.weixin.qq.com and stays
  // authenticated with (bot_id, secret); no public callback URL is required.
  // These three methods drive the Settings-page BYO Connect dialog + list +
  // disconnect only — the inbound WebSocket loop runs entirely server-side.
  async listWecomInstallations(
    workspaceId: string,
  ): Promise<ListWecomInstallationsResponse> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/wecom/installations`,
    );
    return parseWithFallback(
      raw,
      ListWecomInstallationsResponseSchema,
      EMPTY_LIST_WECOM_INSTALLATIONS_RESPONSE,
      { endpoint: "GET /api/workspaces/:id/wecom/installations" },
    );
  }

  // registerWecomBYO performs a bring-your-own-app install: the admin pastes
  // the bot id and long-connection secret from the WeCom admin console,
  // and the backend seals the secret with MULTICA_WECOM_SECRET_KEY before
  // persisting, returning the new installation.
  async registerWecomBYO(
    workspaceId: string,
    agentId: string,
    body: RegisterWecomBYORequest,
  ): Promise<WecomInstallation> {
    const search = new URLSearchParams({ agent_id: agentId });
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/wecom/install/byo?${search.toString()}`,
      {
        method: "POST",
        body: JSON.stringify(body),
      },
    );
    return parseWithFallback(
      raw,
      WecomInstallationSchema,
      EMPTY_WECOM_INSTALLATION,
      {
        endpoint: "POST /api/workspaces/:id/wecom/install/byo",
      },
    );
  }

  async deleteWecomInstallation(
    workspaceId: string,
    installationId: string,
  ): Promise<void> {
    await this.fetch(
      `/api/workspaces/${workspaceId}/wecom/installations/${installationId}`,
      {
        method: "DELETE",
      },
    );
  }

  // redeemWecomBindingToken binds the WeCom aibot userid carried by the
  // token to the logged-in Multica user. Called by the /wecom/bind redeem
  // page after the user clicks through the "link your Multica account"
  // prompt the bot sent in WeCom. Status codes:
  //   410 Gone      → invalid / expired / already consumed
  //   409 Conflict  → the WeCom user is already bound to a different user
  //   403 Forbidden → the logged-in user is not a workspace member
  async redeemWecomBindingToken(
    token: string,
  ): Promise<RedeemWecomBindingTokenResponse> {
    const raw = await this.fetch<unknown>(`/api/wecom/binding/redeem`, {
      method: "POST",
      body: JSON.stringify({ token }),
    });
    return parseWithFallback(
      raw,
      RedeemWecomBindingTokenResponseSchema,
      EMPTY_REDEEM_WECOM_BINDING_TOKEN_RESPONSE,
      { endpoint: "POST /api/wecom/binding/redeem" },
    );
  }
}
