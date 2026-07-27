export interface DingTalkBindingError {
  code: string;
  message: string;
  retryable: boolean;
}

export interface DingTalkAccountBindingOutcome {
  status: string;
  source?: "identity" | null;
  organizationName?: string | null;
  accountDisplayName?: string | null;
  accountAvatarUrl?: string | null;
  boundAt?: string | null;
  error?: DingTalkBindingError | null;
}

export type DingTalkMessageScope = "direct_only" | "custom" | "all";
export type DingTalkProcessingSurface = "issue" | "chat";

export interface DingTalkConversationSummary {
  cid: string;
  name: string;
  avatarMediaId?: string | null;
  avatarUrl?: string | null;
}

export interface DingTalkMessageRouteOutcome
  extends DingTalkAccountBindingOutcome {
  surfaceType?: DingTalkProcessingSurface | null;
  messageScope: DingTalkMessageScope;
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
