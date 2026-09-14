import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import enProductFeatures from "../locales/en/product-features.json";
import { ProductFeatureDetailPage, ProductFeatureListPage } from "./index";

const { detailResult, listResult, push, seenQueryKeys } = vi.hoisted(() => ({
  push: vi.fn(),
  seenQueryKeys: [] as (readonly unknown[])[],
  listResult: {
    current: {
      data: undefined as unknown,
      isPending: false,
      isError: false,
      refetch: vi.fn(),
    },
  },
  detailResult: {
    current: {
      data: undefined as unknown,
      isPending: false,
      isError: false,
      refetch: vi.fn(),
    },
  },
}));

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: (options: { queryKey: readonly unknown[] }) => {
    seenQueryKeys.push(options.queryKey);
    return options.queryKey.includes("detail") ? detailResult.current : listResult.current;
  },
}));

vi.mock("../i18n", () => ({
  useT: () => ({
    t: (
      selector: (resources: typeof enProductFeatures) => string,
      options?: Record<string, string>,
    ) => {
      let value = selector(enProductFeatures);
      for (const [key, replacement] of Object.entries(options ?? {})) {
        value = value.replace(`{{${key}}}`, replacement);
      }
      return value;
    },
  }),
}));

vi.mock("../navigation", () => ({
  useNavigation: () => ({ push }),
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    featureUpdates: () => "/acme/features",
    featureUpdateDetail: (id: string) => `/acme/features/${id}`,
  }),
}));

vi.mock("../rich-content", () => ({
  RichContent: ({ content }: { content: string }) => <div>{content}</div>,
}));

const older = {
  id: "release-1",
  featureId: "feature-1",
  featureSlug: "agent-events",
  releaseType: "new" as const,
  title: "Agent events",
  description: "Subscribe to events.",
  useCases: "Automate follow-up work.",
  usageGuide: "Open Events.",
  versionLabel: "2026.09.01",
  imageRequirement: "none" as const,
  requiredImageVersion: null,
  requiresImageUpgrade: false,
  previousReleaseId: null,
  previousRelease: null,
  publishedAt: "2026-09-01T08:00:00Z",
  createdAt: "2026-09-01T08:00:00Z",
};

const newer = {
  ...older,
  id: "release-2",
  releaseType: "improvement" as const,
  title: "Agent event filters",
  description: "Filter event subscriptions.",
  useCases: "Reduce irrelevant triggers.",
  usageGuide: "Add a source filter.",
  versionLabel: "2026.09.14",
  imageRequirement: "min_version" as const,
  requiredImageVersion: "multica-runtime:2026.09.14",
  requiresImageUpgrade: true,
  previousReleaseId: "release-1",
  previousRelease: {
    id: "release-1",
    title: "Agent events",
    versionLabel: "2026.09.01",
    publishedAt: "2026-09-01T08:00:00Z",
  },
  publishedAt: "2026-09-14T08:00:00Z",
  createdAt: "2026-09-14T08:00:00Z",
};

describe("product feature pages", () => {
  beforeEach(() => {
    push.mockReset();
    seenQueryKeys.length = 0;
    listResult.current = {
      data: { releases: [older, newer], total: 2, limit: 30, offset: 0 },
      isPending: false,
      isError: false,
      refetch: vi.fn(),
    };
    detailResult.current = {
      data: newer,
      isPending: false,
      isError: false,
      refetch: vi.fn(),
    };
  });

  it("keeps the newest release first and opens its detail", () => {
    render(<ProductFeatureListPage />);

    const entries = screen.getAllByRole("button", { name: /Agent/ });
    expect(entries[0]).toHaveTextContent("Agent event filters");
    fireEvent.click(entries[0]!);
    expect(push).toHaveBeenCalledWith("/acme/features/release-2");
  });

  it("shows usage, image requirement, and the previous version", () => {
    render(<ProductFeatureDetailPage releaseId="release-2" />);

    expect(screen.getByText("Filter event subscriptions.")).toBeInTheDocument();
    expect(screen.getByText("Reduce irrelevant triggers.")).toBeInTheDocument();
    expect(screen.getByText("Add a source filter.")).toBeInTheDocument();
    expect(screen.getByText("multica-runtime:2026.09.14")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Agent events/ }));
    expect(push).toHaveBeenCalledWith("/acme/features/release-1");
  });

  it("sends the debounced keyword to the server query", async () => {
    render(<ProductFeatureListPage />);

    fireEvent.change(screen.getByRole("textbox", { name: "Search features" }), {
      target: { value: "runtime image" },
    });

    await waitFor(() => {
      expect(seenQueryKeys).toContainEqual([
        "product-feature-releases",
        "list",
        "runtime image",
      ]);
    });
  });
});
