export function canCreateFCE2BRuntime(role?: string | null): boolean {
  return role === "owner" || role === "admin" || role === "member";
}

export function canCreatePublicFCE2BRuntime(role?: string | null): boolean {
  return role === "owner" || role === "admin";
}
