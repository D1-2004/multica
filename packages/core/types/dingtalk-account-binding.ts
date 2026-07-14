export interface DingTalkAccountBinding {
  id: string;
  workspaceId: string;
  agentId: string;
  status: string;
  accountDisplayName?: string | null;
  accountAvatarUrl?: string | null;
  boundAt?: string | null;
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
