export interface DingTalkAccountBindingOutcome {
  status: string;
  organizationName?: string | null;
  accountDisplayName?: string | null;
  accountAvatarUrl?: string | null;
  boundAt?: string | null;
}

export interface DingTalkAccountBinding {
  id: string;
  workspaceId: string;
  agentId: string;
  dwsIdentity: DingTalkAccountBindingOutcome;
  messageRoute: DingTalkAccountBindingOutcome;
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
