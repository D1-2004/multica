// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type { Agent, ChatMessage } from "@multica/core/types";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import { CoordinatorSessionsTab } from "./coordinator-sessions-tab";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  messages: vi.fn(),
  compact: false,
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws" }));
vi.mock("@multica/core/api", () => ({
  api: {
    listAgentCoordinatorConversations: (...args: unknown[]) =>
      mocks.list(...args),
    listAgentCoordinatorConversationMessages: (...args: unknown[]) =>
      mocks.messages(...args),
  },
}));
vi.mock("@multica/ui/hooks/use-mobile", () => ({
  useIsCompact: () => mocks.compact,
}));
vi.mock("../../../chat/components/chat-message-list", () => ({
  ChatMessageSkeleton: () => <div>message skeleton</div>,
  ChatMessageList: ({
    messages,
    hasOlderMessages,
    onLoadOlderMessages,
  }: {
    messages: ChatMessage[];
    hasOlderMessages: boolean;
    onLoadOlderMessages: () => void;
  }) => (
    <div>
      <div data-testid="transcript">
        {messages.map((m) => (
          <p key={m.id}>{m.content}</p>
        ))}
      </div>
      {hasOlderMessages && (
        <button onClick={onLoadOlderMessages}>Older messages</button>
      )}
    </div>
  ),
}));
const group = (id: string) => ({
  id,
  session_id: `session-${id}`,
  title: `Conversation ${id}`,
  conversation_type: "group",
  source: "digital_employee",
  session_count: 20,
  updated_at: "2026-09-08T00:00:00Z",
});
const message = (id: string, content: string) => ({
  id,
  chat_session_id: id,
  role: "assistant",
  content,
});
function mount() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  return render(
    <QueryClientProvider client={client}>
      <I18nProvider
        locale="en"
        resources={{ en: { common: enCommon, agents: enAgents } }}
      >
        <CoordinatorSessionsTab agent={{ id: "agent" } as Agent} />
      </I18nProvider>
    </QueryClientProvider>,
  );
}
beforeEach(() => {
  mocks.compact = false;
  mocks.list.mockResolvedValue({
    conversations: [group("a")],
    has_more: false,
    next_offset: 1,
  });
  mocks.messages.mockResolvedValue({ messages: [], has_more: false });
});
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});

describe("CoordinatorSessionsTab", () => {
  it("loads groups on demand and opens a single continuous transcript across judgments", async () => {
    mocks.list.mockImplementation(async (_agent, offset) =>
      offset === 0
        ? { conversations: [group("a")], has_more: true, next_offset: 25 }
        : { conversations: [group("b")], has_more: false, next_offset: 26 },
    );
    mocks.messages.mockImplementation(async (_agent, _session, cursor) =>
      cursor
        ? { messages: [message("old", "Earlier judgment")], has_more: false }
        : {
            messages: [message("new", "Latest judgment")],
            has_more: true,
            next_cursor: { created_at: "2026-09-08T00:00:00Z", id: "new" },
          },
    );
    mount();
    await screen.findByText("Conversation a");
    expect(mocks.messages).not.toHaveBeenCalled();
    expect(screen.getByText("20 judgments")).toBeInTheDocument();
    fireEvent.click(screen.getByText("Load more conversations"));
    await screen.findByText("Conversation b");
    expect(mocks.list).toHaveBeenLastCalledWith("agent", 25);
    fireEvent.click(screen.getByText("Conversation a"));
    await screen.findByText("Latest judgment");
    expect(mocks.messages).toHaveBeenCalledTimes(1);
    expect(mocks.messages).toHaveBeenLastCalledWith("agent", "session-a", null);
    fireEvent.click(screen.getByText("Older messages"));
    await screen.findByText("Earlier judgment");
    expect(screen.getByTestId("transcript").textContent).toBe(
      "Earlier judgmentLatest judgment",
    );
  });
  it("shows message errors with retry and keeps compact back navigation", async () => {
    mocks.compact = true;
    mocks.messages.mockRejectedValueOnce(new Error("offline"));
    mount();
    fireEvent.click(await screen.findByText("Conversation a"));
    await screen.findByRole("alert");
    fireEvent.click(screen.getByText(enAgents.tab_body.inbound.retry));
    await screen.findByText(enAgents.tab_body.inbound.no_messages);
    fireEvent.click(screen.getByText(enAgents.tab_body.inbound.back));
    expect(screen.getByText("Conversation a")).toBeInTheDocument();
    await waitFor(() => expect(mocks.messages).toHaveBeenCalledTimes(2));
  });
  it("retains loaded groups when the next page fails", async () => {
    mocks.list
      .mockResolvedValueOnce({
        conversations: [group("a")],
        has_more: true,
        next_offset: 25,
      })
      .mockRejectedValueOnce(new Error("offline"));
    mount();
    await screen.findByText("Conversation a");
    fireEvent.click(screen.getByText("Load more conversations"));
    await screen.findByRole("alert");
    expect(screen.getByText("Conversation a")).toBeInTheDocument();
  });
});
