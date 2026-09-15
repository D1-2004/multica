export function githubConnectionErrorField(code: string) {
  switch (code) {
    case "user_authorization_not_configured":
      return "toast_authorization_not_configured";
    case "connection_in_progress":
      return "toast_connection_in_progress";
    default:
      return "toast_connect_failed";
  }
}
