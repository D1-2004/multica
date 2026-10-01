export interface DingTalkBindingError {
  code: string;
  message: string;
  retryable: boolean;
}

export type DingTalkAccountBindingStatus =
  | "active"
  | "pending"
  | "failed"
  | "skipped"
  | "revoked"
  | "unbound"
  | "bound_to_other_agent"
  | "inconsistent"
  | "router_unavailable";

export interface DingTalkAccountBindingOutcome {
  status: DingTalkAccountBindingStatus;
  source?: "identity" | null;
  organizationName?: string | null;
  accountDisplayName?: string | null;
  accountAvatarUrl?: string | null;
  boundAt?: string | null;
  error?: DingTalkBindingError | null;
}

// The execution identity outcome additionally reports whether inbound DingTalk
// messages for that identity reach the agent through the DWS native
// subscription. It is mutually exclusive with the digital-employee message
// binding.
export interface DingTalkExecutionIdentityOutcome
  extends DingTalkAccountBindingOutcome {
  nativeSubscription: boolean;
}

export type DingTalkMessageScope = "direct_only" | "custom" | "all";
export type DingTalkProcessingSurface = "issue" | "chat" | "auto";

export interface DingTalkConversationSummary {
  cid: string;
  name: string;
  avatarMediaId?: string | null;
  avatarUrl?: string | null;
}

export interface DingTalkMessageScopeSubscription {
  directCids: string[];
  groupCids: string[];
  emojiReactionCids: string[];
}

export interface DingTalkMessageRouteOutcome
  extends DingTalkAccountBindingOutcome {
  surfaceType?: DingTalkProcessingSurface | null;
  messageScope: DingTalkMessageScope;
  messageScopeVersion?: number;
  subscription?: DingTalkMessageScopeSubscription | null;
  enabledDomains: string[];
  calendarStartEnabled?: boolean;
  conversations: DingTalkConversationSummary[];
  emojiConversations: DingTalkConversationSummary[];
}

export interface DingTalkAccountBinding {
  id: string;
  workspaceId: string;
  agentId: string;
  dwsIdentity: DingTalkExecutionIdentityOutcome;
  messageRoute: DingTalkMessageRouteOutcome;
}

export interface DingTalkAccountBindingsResponse {
  bindings: DingTalkAccountBinding[];
  configured: boolean;
  // True only for deployment operators, who may bind the message route by
  // DingTalk organization and account id instead of scanning.
  manualBindingAllowed: boolean;
}

export interface DingTalkNativeSubscriptionResponse {
  nativeSubscription: boolean;
}

// The state of a native subscription's DWS event stream (WebSocket), as every
// server replica sees it. "unknown" also covers states this client predates.
export type DingTalkNativeStreamState =
  | "connected"
  | "connecting"
  | "disconnected"
  | "unavailable"
  | "off"
  | "unknown";

export interface DingTalkNativeStream {
  state: DingTalkNativeStreamState;
  lastConnectedAt: string | null;
  lastEventAt: string | null;
  // Reported only while disconnected.
  lastError: string | null;
  failures: number;
}

export interface DingTalkNativeSubscriptionStatus {
  nativeSubscription: boolean;
  stream: DingTalkNativeStream;
}

export type DingTalkManualMessageScope = Extract<
  DingTalkMessageScope,
  "all" | "direct_only"
>;

export interface BindDingTalkMessageRouteManuallyRequest {
  // The organization's corpId (ding…), which the Router uses as tenant id.
  corpId: string;
  uid: string;
  messageScope: DingTalkManualMessageScope;
}

export interface BeginDingTalkAccountBindingResponse {
  bindingId: string;
  qrCodeUrl: string;
  expiresAt: string;
}

export type DingTalkBindingMode = "message" | "identity";

export interface ReusableDingTalkIdentity {
  sourceAgentId: string;
  sourceAgentName: string;
  accountDisplayName: string;
  organizationName: string;
}
