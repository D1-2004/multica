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
  app_identifier: string;
  install_slug: string;
  private_key_set: boolean;
  private_key_hint: string;
  optional_secret_set: boolean;
  optional_secret_hint: string;
  instances: ConnectorAuthInstance[];
}

export interface SettingsConnectorField {
  key: string;
  optional: boolean;
  file: boolean;
}

export interface SettingsConnectorSpec {
  slug: string;
  name: string;
  mode: string;
  later: boolean;
  fields: SettingsConnectorField[];
  docs_url: string;
  authorization_endpoint?: string;
  token_endpoint?: string;
  scopes?: string;
  mcp_url?: string;
  known_client_id?: string;
  oauth_connect: boolean;
  env_configured: boolean;
  connector_id?: string;
}

export interface ConnectorAppList {
  apps: ConnectorApp[];
  priority: string[];
  catalog?: SettingsConnectorSpec[];
  callback_url?: string;
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
  app_identifier?: string;
  install_slug?: string;
  private_key?: string;
  clear_private_key?: boolean;
  optional_secret?: string;
  clear_optional_secret?: boolean;
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
