export interface ConnectorBinding {
  scope_kind: string;
  scope_id: string;
}

export interface ConnectorAuthInstance {
  id: string;
  label: string;
  external_subject: string;
  external_login: string;
  status: string;
  enabled: boolean;
  token_hint: string;
  token_set: boolean;
  bindings: ConnectorBinding[];
}

export interface ConnectorApp {
  id: string;
  provider: string;
  display_name: string;
  client_id: string;
  client_secret_hint: string;
  client_secret_set: boolean;
  scopes: string;
  authorization_endpoint: string;
  token_endpoint: string;
  callback_mode: string;
  enabled: boolean;
  active_for_catalog: boolean;
  instances: ConnectorAuthInstance[];
}

export interface ConnectorAppList {
  apps: ConnectorApp[];
  priority: string[];
}

export interface ConnectorAppInput {
  provider: string;
  display_name?: string;
  client_id: string;
  client_secret?: string;
  clear_client_secret?: boolean;
  scopes?: string;
  authorization_endpoint?: string;
  token_endpoint?: string;
  callback_mode?: string;
  enabled?: boolean;
  public_client?: boolean;
}

export interface ConnectorInstanceInput {
  label: string;
  external_subject?: string;
  external_login?: string;
  status?: string;
  token?: string;
  clear_token?: boolean;
  enabled?: boolean;
}

export interface ConnectorResolveResult {
  matched: boolean;
  provider: string;
  app_id?: string;
  instance_id?: string;
  instance_label?: string;
  scope_kind?: string;
  scope_id?: string;
  token_ready: boolean;
  priority: string[];
  fallback?: string;
}
