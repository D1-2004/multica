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
}

export interface DingTalkAccountBinding {
  id: string;
  workspaceId: string;
  agentId: string;
  dwsIdentity: DingTalkAccountBindingOutcome;
  messageRoute: DingTalkMessageRouteOutcome;
}

export interface DingTalkAccountBindingsResponse {
  bindings: DingTalkAccountBinding[];
  configured: boolean;
}

export interface BeginDingTalkAccountBindingResponse {
  bindingId: string;
  qrCodeUrl: string;
  expiresAt: string;
}

export type DingTalkBindingMode = "message" | "identity";
