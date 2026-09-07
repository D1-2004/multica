import { describe, expect, it } from "vitest";
import { ChatMessageSchema, SendChatMessageResponseSchema } from "./schemas";

const issueId = "9275c40d-7309-4f48-9a1e-5f751636b28a";
const result = { action: "issue_created", issue_id: issueId, issue_identifier: "MUL-17" };

describe.each([
  ["history", (coordinator: unknown) => ChatMessageSchema.parse({ id: "message", chat_session_id: "chat", role: "assistant", content: "Reply", coordinator })],
  ["send", (coordinator: unknown) => SendChatMessageResponseSchema.parse({ message_id: "message", created_at: "now", coordinator })],
] as const)("Coordinator %s schema", (_, parse) => {
  it("retains valid results when another result or optional field is malformed", () => {
    const parsed = parse({
      action: "issue",
      issue_results: [result, null, { issue_id: "../../settings" }, { ...result, action: "future_action", issue_title: 123, task_id: false }],
    });
    expect(parsed.coordinator?.issue_results).toEqual([
      result,
      { ...result, action: "future_action", issue_title: "", task_id: "" },
    ]);
  });

  it("preserves old replies and discards malformed result containers", () => {
    expect(parse({ action: "issue" }).coordinator?.issue_results).toBeUndefined();
    expect(parse({ action: "issue", issue_results: "invalid" }).coordinator?.issue_results).toEqual([]);
  });
});
