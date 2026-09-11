import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());
function respond(body: unknown) {
  const fetch = vi.fn(
    async (_input: RequestInfo | URL, _init?: RequestInit) =>
      new Response(JSON.stringify(body), {
        headers: { "Content-Type": "application/json" },
      }),
  );
  vi.stubGlobal("fetch", fetch);
  return fetch;
}
const api = new ApiClient("https://api.example.test");

describe("coordinator conversations API", () => {
  it("requests a bounded group page and defaults optional display fields", async () => {
    const fetch = respond({
      conversations: [
        { id: "group", session_id: "session", session_count: 12 },
      ],
      has_more: true,
      next_offset: 25,
    });
    const page = await api.listAgentCoordinatorConversations("agent/id", 25);
    expect(page.conversations[0]).toMatchObject({
      id: "group",
      title: "",
      session_count: 12,
    });
    expect(fetch).toHaveBeenCalledWith(
      "https://api.example.test/api/agents/agent%2Fid/coordinator-conversations?offset=25",
      expect.any(Object),
    );
  });
  it("falls back safely on malformed groups", async () => {
    respond({ conversations: [{ id: "broken" }], has_more: true });
    expect(await api.listAgentCoordinatorConversations("agent")).toEqual({
      conversations: [],
      has_more: false,
      next_offset: 0,
    });
  });
  it("retains the source session and judgment trace across a merged message page", async () => {
    const fetch = respond({
      messages: [
        {
          id: "message",
          chat_session_id: "older-session",
          role: "assistant",
          message_kind: "coordinator",
          coordinator: {
            action: "reply",
            steps: [{ seq: 1, type: "thinking", content: "reason" }],
          },
        },
      ],
      has_more: false,
    });
    const page = await api.listAgentCoordinatorConversationMessages(
      "agent",
      "anchor/session",
      { created_at: "2026-09-08T01:00:00Z", id: "cursor" },
    );
    expect(page.messages[0]).toMatchObject({
      chat_session_id: "older-session",
      coordinator: { action: "reply" },
    });
    expect(fetch.mock.calls[0]?.[0]).toContain(
      "anchor%2Fsession/messages?limit=50&before_created_at=2026-09-08T01%3A00%3A00Z&before_id=cursor",
    );
  });
  it("falls back safely on malformed merged messages", async () => {
    respond({ messages: [{ id: 123 }], has_more: true });
    expect(
      await api.listAgentCoordinatorConversationMessages("agent", "session"),
    ).toEqual({ messages: [], limit: 50, has_more: false, next_cursor: null });
  });
});
