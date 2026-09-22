import { describe, it, expect } from "vitest";
import { parseWithFallback } from "./schema";
import {
  GlobalModelsSchema,
  EMPTY_GLOBAL_MODELS,
  globalModelsWire,
} from "./global-models-schema";
describe("global model API boundary", () => {
  it("malformed settings cannot become a saveable empty configuration", () => {
    expect(
      parseWithFallback(
        { providers: "bad" },
        GlobalModelsSchema,
        EMPTY_GLOBAL_MODELS,
        { endpoint: "test", includeReceived: false },
      ).revision,
    ).toBe(-1);
  });
  it("strips returned credentials and preserves slash-containing model IDs", () => {
    const c = GlobalModelsSchema.parse({
      revision: 3,
      providers: [
        {
          id: "p",
          name: "P",
          base_url: "https://p.example/v1",
          models: ["org/m"],
          enabled: true,
          api_key: "never-render",
        },
      ],
      agent_models: [{ provider: "p", model: "org/m" }],
      default_model: { provider: "p", model: "org/m" },
      coordinator: [{ provider: "p", model: "org/m" }],
      diamond_fallback: false,
    });
    expect(c.providers[0]?.apiKey).toBe("");
    expect(globalModelsWire(c).default_model.model).toBe("org/m");
    expect(JSON.stringify(c)).not.toContain("never-render");
  });
});

import {ApiClient} from "./client";
import {vi,afterEach} from "vitest";
afterEach(()=>vi.unstubAllGlobals());
it("uses the authenticated /api namespace for developer capabilities",async()=>{
 const fetcher=vi.fn().mockResolvedValue(new Response(JSON.stringify({developer:true}),{status:200}));vi.stubGlobal("fetch",fetcher);
 const result=await new ApiClient("https://pre.example").getDeveloperCapabilities();
 expect(result.developer).toBe(true);expect(fetcher.mock.calls[0]?.[0]).toBe("https://pre.example/api/developer/capabilities");
});
