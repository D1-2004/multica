export interface HostedSite {
  siteId: string;
  publicSiteId: string;
  status: string;
  activeRevisionId: string | null;
  latestRevisionId: string;
  latestStatus: string;
  latestError: string;
  createdAt: string;
  updatedAt: string;
  siteUrl: string;
}
