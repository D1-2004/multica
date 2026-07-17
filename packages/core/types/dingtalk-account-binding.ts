export interface DingTalkAccountBindingOutcome {
  status: string;
  organizationName?: string | null;
  accountDisplayName?: string | null;
  accountAvatarUrl?: string | null;
  boundAt?: string | null;
}

export type DingTalkMessageScope = "direct_only" | "custom" | "all";

export interface DingTalkConversationSummary {
  cid: string;
  name: string;
  avatarMediaId?: string | null;
  avatarUrl?: string | null;
}

export interface DingTalkMessageRouteOutcome
  extends DingTalkAccountBindingOutcome {
  messageScope: DingTalkMessageScope;
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
  installationId: string;
  qrCodeUrl: string;
  expiresAt: string;
}
