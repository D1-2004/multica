"use client";

import { HostedSitesTab } from "../settings/components/hosted-sites-tab";

export function HostedSitesPage() {
  return (
    <main className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto w-full max-w-5xl px-4 py-6 sm:px-6 sm:py-8">
        <HostedSitesTab />
      </div>
    </main>
  );
}
