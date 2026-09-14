import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "../api/client";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("product feature release client", () => {
  it("encodes search parameters and parses the global release page", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          releases: [
            {
              id: "release-1",
              feature_id: "feature-1",
              feature_slug: "agent-events",
              release_type: "new",
              title: "Agent events",
              description: "Subscribe to events.",
              use_cases: "Automate work.",
              usage_guide: "Open Events.",
              version_label: "2026.09",
              image_requirement: "none",
              required_image_version: null,
              requires_image_upgrade: false,
              previous_release_id: null,
              published_at: "2026-09-14T08:00:00Z",
              created_at: "2026-09-14T08:00:00Z"
            }
          ],
          total: 1,
          limit: 10,
          offset: 20
        }),
        { status: 200 },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const result = await new ApiClient("https://api.example.test")
      .listProductFeatureReleases({ query: "agent event", limit: 10, offset: 20 });

    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "https://api.example.test/api/features?q=agent+event&limit=10&offset=20",
    );
    expect(result.releases[0]).toMatchObject({
      featureSlug: "agent-events",
      releaseType: "new",
      previousRelease: null,
    });
  });
});
