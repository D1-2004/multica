import type { HostedSite } from "./types";

export function hostedSiteDisplayTitle(site: HostedSite): string {
  return site.title.trim() || site.publicSiteId;
}
