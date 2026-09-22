/**
 * @vitest-environment jsdom
 */
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import type { WSClient } from "../api/ws-client";
import { chatKeys } from "../chat/queries";
import { defaultStorage } from "../platform/storage";
import { issueKeys } from "../issues/queries";
import { labelKeys } from "../labels/queries";
import { workspaceWorkingAgentsKeys, agentTasksOptions, agentTaskSnapshotKeys } from "../agents/queries";
import { ApiClient, setApiInstance } from "../api";
import { workspaceKeys } from "../workspace/queries";
import {
  markWorkspaceDeletePending,
  unmarkWorkspaceDeletePending,
} from "../workspace/pending-delete";
import { useRealtimeSync, type RealtimeSyncStores } from "./use-realtime-sync";

vi.mock("../platform/workspace-storage", () => ({
  getCurrentWsId: () => "ws-1",
  getCurrentSlug: () => "test-ws",
  // Draft stores are now loaded transitively (storage-cleanup → register-all-drafts)
  // so their persist wiring must resolve against this mock.
  createWorkspaceAwareStorage: (adapter: unknown) => adapter,
  registerForWorkspaceRehydration: () => {},
}));

vi.mock("../paths", () => ({
  useHasOnboarded: () => true,
  resolvePostAuthDestination: () => "/",
}));

function createMockWs(): WSClient {
  return {
    on: vi.fn(() => () => {}),
    onAny: vi.fn(() => () => {}),
    onReconnect: vi.fn(() => () => {}),
  } as unknown as WSClient;
}

function createStores(): RealtimeSyncStores {
  return {
    authStore: Object.assign(() => ({}), {
      getState: () => ({ user: { id: "u1" } }),
      subscribe: () => () => {},
      setState: () => {},
      destroy: () => {},
    }),
  } as unknown as RealtimeSyncStores;
}

function createWrapper(qc: QueryClient) {
  // Named function (not arrow) so react/display-name lint rule passes —
  // anonymous render-fn components break that rule even in test files.
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  };
}

describe("useRealtimeSync — ws instance change", () => {
  let qc: QueryClient;
  let stores: RealtimeSyncStores;
  let invalidateSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    stores = createStores();
    invalidateSpy = vi.spyOn(qc, "invalidateQueries");
  });

  it.each([
    "dingtalk_account_binding:activated",
    "dingtalk_account_binding:revoked",
  ])("invalidates only the account binding query for %s", (eventType) => {
    vi.useFakeTimers();
    const ws = createMockWs();
    const setQueryDataSpy = vi.spyOn(qc, "setQueryData");
    renderHook(() => useRealtimeSync(ws, stores), {
      wrapper: createWrapper(qc),
    });
    invalidateSpy.mockClear();
    setQueryDataSpy.mockClear();

    const anyHandler = vi.mocked(ws.onAny).mock.calls[0]?.[0];
    expect(anyHandler).toBeDefined();
    act(() => {
      anyHandler?.({ type: eventType, payload: {} } as never);
      vi.advanceTimersByTime(101);
    });

    expect(invalidateSpy).toHaveBeenCalledTimes(1);
    expect(invalidateSpy).toHaveBeenCalledWith({
      queryKey: ["dingtalk-account-bindings", "ws-1", "list"],
    });
    expect(setQueryDataSpy).not.toHaveBeenCalled();
    vi.useRealTimers();
  });

  it("refreshes the chat thread list when an externally-created task is queued", () => {
    vi.useFakeTimers();
    const ws = createMockWs();
    renderHook(() => useRealtimeSync(ws, stores), {
      wrapper: createWrapper(qc),
    });
    invalidateSpy.mockClear();

    const queuedCall = vi
      .mocked(ws.on)
      .mock.calls.find(([event]) => event === "task:queued");
    expect(queuedCall).toBeDefined();

    act(() => {
      queuedCall?.[1]({
        task_id: "task-a2a",
        chat_session_id: "session-a2a",
      });
    });

    expect(invalidateSpy).toHaveBeenCalledWith({
      queryKey: chatKeys.sessions("ws-1"),
    });
    vi.clearAllTimers();
    vi.useRealTimers();
  });

  it("skips invalidation on first non-null ws instance", () => {
    const ws = createMockWs();
    renderHook(() => useRealtimeSync(ws, stores), {
      wrapper: createWrapper(qc),
    });

    // The main effect calls invalidateQueries for its own setup, but the
    // ws-instance-change effect should NOT have fired invalidation.
    // The only invalidateQueries calls should come from the main effect's
    // event handlers, not from the instance-change effect.
    // We verify by checking that no call was made with workspaceKeys.list()
    // pattern from the instance-change path (it logs a specific message).
    // Simpler: count calls — first mount with a ws should not trigger the
    // workspace-scoped bulk invalidation.
    expect(invalidateSpy).not.toHaveBeenCalled();
  });

  it("does not invalidate when ws goes from instance to null", () => {
    const ws1 = createMockWs();
    const { rerender } = renderHook(
      ({ ws }) => useRealtimeSync(ws, stores),
      { initialProps: { ws: ws1 as WSClient | null }, wrapper: createWrapper(qc) },
    );

    invalidateSpy.mockClear();
    rerender({ ws: null });

    expect(invalidateSpy).not.toHaveBeenCalled();
  });

  it("invalidates exactly once when a new ws instance appears after null gap", () => {
    const ws1 = createMockWs();
    const { rerender } = renderHook(
      ({ ws }) => useRealtimeSync(ws, stores),
      { initialProps: { ws: ws1 as WSClient | null }, wrapper: createWrapper(qc) },
    );

    // Simulate workspace switch: ws -> null -> new ws
    invalidateSpy.mockClear();
    rerender({ ws: null });
    expect(invalidateSpy).not.toHaveBeenCalled();

    const ws2 = createMockWs();
    rerender({ ws: ws2 });

    // Should have called invalidateQueries for all workspace-scoped keys
    // Upstream workspace/property/working-agent caches plus the internal
    // agent-source and DingTalk account-binding caches all refresh together.
    expect(invalidateSpy).toHaveBeenCalledTimes(32);
  });

  it("does not re-invalidate when rerendered with the same ws instance", () => {
    const ws1 = createMockWs();
    const { rerender } = renderHook(
      ({ ws }) => useRealtimeSync(ws, stores),
      { initialProps: { ws: ws1 as WSClient | null }, wrapper: createWrapper(qc) },
    );

    invalidateSpy.mockClear();
    // Rerender with same instance
    rerender({ ws: ws1 });

    expect(invalidateSpy).not.toHaveBeenCalled();
  });

  it("invalidates chat, pins, labels, and invitations queries on ws instance change", () => {
    const ws1 = createMockWs();
    const { rerender } = renderHook(
      ({ ws }) => useRealtimeSync(ws, stores),
      { initialProps: { ws: ws1 as WSClient | null }, wrapper: createWrapper(qc) },
    );

    invalidateSpy.mockClear();
    rerender({ ws: null });

    const ws2 = createMockWs();
    rerender({ ws: ws2 });

    const calls = invalidateSpy.mock.calls.map((call: [{ queryKey?: unknown }, ...unknown[]]) => call[0].queryKey);
    expect(calls).toContainEqual(["chat", "ws-1"]);
    expect(calls).toContainEqual(["labels", "ws-1"]);
    expect(calls).toContainEqual(["workspaces", "ws-1", "invitations"]);
    expect(calls).toContainEqual(["dingtalk-account-bindings", "ws-1", "list"]);
  });

  it("invalidates per-issue caches (no wsId in key) on ws instance change", () => {
    // These keys are not under the ["issues", wsId] prefix, so they need
    // their own invalidation on recovery — otherwise events missed while
    // disconnected leave them stale forever (staleTime: Infinity, #3953).
    const ws1 = createMockWs();
    const { rerender } = renderHook(
      ({ ws }) => useRealtimeSync(ws, stores),
      { initialProps: { ws: ws1 as WSClient | null }, wrapper: createWrapper(qc) },
    );

    invalidateSpy.mockClear();
    rerender({ ws: null });

    const ws2 = createMockWs();
    rerender({ ws: ws2 });

    const calls = invalidateSpy.mock.calls.map((call: [{ queryKey?: unknown }, ...unknown[]]) => call[0].queryKey);
    expect(calls).toContainEqual(["issues", "timeline"]);
    expect(calls).toContainEqual(["issues", "reactions"]);
    expect(calls).toContainEqual(["issues", "subscribers"]);
    expect(calls).toContainEqual(["issues", "usage"]);
    expect(calls).toContainEqual(["issues", "attachments"]);
    expect(calls).toContainEqual(["issues", "tasks"]);
  });

  it("invalidates per-chat-session caches (no wsId in key) on ws instance change", () => {
    // These keys are not under the ["chat", wsId] prefix, so they need their
    // own recovery invalidation when reconnecting after missed chat/task events.
    const ws1 = createMockWs();
    const { rerender } = renderHook(
      ({ ws }) => useRealtimeSync(ws, stores),
      { initialProps: { ws: ws1 as WSClient | null }, wrapper: createWrapper(qc) },
    );

    invalidateSpy.mockClear();
    rerender({ ws: null });

    const ws2 = createMockWs();
    rerender({ ws: ws2 });

    const calls = invalidateSpy.mock.calls.map((call: [{ queryKey?: unknown }, ...unknown[]]) => call[0].queryKey);
    expect(calls).toContainEqual(["chat", "messages"]);
    expect(calls).toContainEqual(["chat", "messages-page"]);
    expect(calls).toContainEqual(["chat", "pending-task"]);
    expect(calls).toContainEqual(["task-messages"]);
  });

  it("invalidates per-chat-session caches after an established ws reconnects", () => {
    const ws = createMockWs();
    renderHook(() => useRealtimeSync(ws, stores), {
      wrapper: createWrapper(qc),
    });
    const reconnect = vi.mocked(ws.onReconnect).mock.calls[0]?.[0];
    expect(reconnect).toBeDefined();

    invalidateSpy.mockClear();
    reconnect!();

    const calls = invalidateSpy.mock.calls.map((call: [{ queryKey?: unknown }, ...unknown[]]) => call[0].queryKey);
    expect(calls).toContainEqual(chatKeys.messagesAll());
    expect(calls).toContainEqual(chatKeys.messagesPageAll());
    expect(calls).toContainEqual(chatKeys.pendingTaskAll());
  });

  it("invalidates one issue attachment cache after detached channel media binds", () => {
    const ws = createMockWs();
    renderHook(() => useRealtimeSync(ws, stores), {
      wrapper: createWrapper(qc),
    });
    const attachmentChanged = vi
      .mocked(ws.on)
      .mock.calls.find(([event]) => event === "issue_attachments:changed")?.[1];
    expect(attachmentChanged).toBeDefined();

    (attachmentChanged as (payload: unknown) => void)({ issue_id: "issue-1" });

    expect(invalidateSpy).toHaveBeenCalledWith({
      queryKey: issueKeys.attachments("issue-1"),
    });
  });
});

describe("useRealtimeSync — queued chat promotion", () => {
  it("refetches the transcript when a queued prompt starts running", () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const ws = createMockWs();
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    renderHook(() => useRealtimeSync(ws, createStores()), {
      wrapper: createWrapper(qc),
    });
    const dispatch = vi
      .mocked(ws.on)
      .mock.calls.find(([event]) => event === "task:dispatch")?.[1];
    expect(dispatch).toBeDefined();

    invalidate.mockClear();
    (dispatch as (payload: unknown) => void)({
      task_id: "task-follow-up",
      chat_session_id: "session-1",
    });

    expect(invalidate).toHaveBeenCalledWith({
      queryKey: chatKeys.messages("session-1"),
    });
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: chatKeys.messagesPage("session-1"),
    });
  });
});

describe("useRealtimeSync — Table server membership invalidation", () => {
  let qc: QueryClient;
  let stores: RealtimeSyncStores;

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    stores = createStores();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("invalidates Table queries after a task lifecycle event", () => {
    vi.useFakeTimers();
    const ws = createMockWs();
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    renderHook(() => useRealtimeSync(ws, stores), {
      wrapper: createWrapper(qc),
    });
    const onAny = vi.mocked(ws.onAny).mock.calls[0]?.[0];
    expect(onAny).toBeDefined();

    onAny!({ type: "task:completed", payload: {} } as never);
    vi.advanceTimersByTime(100);

    expect(invalidate).toHaveBeenCalledWith({
      queryKey: issueKeys.tableAll("ws-1"),
    });
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: workspaceWorkingAgentsKeys.all("ws-1"),
    });
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: labelKeys.usageRoot("ws-1"),
    });
  });

  it("invalidates Table queries after a property definition changes", () => {
    const ws = createMockWs();
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    renderHook(() => useRealtimeSync(ws, stores), {
      wrapper: createWrapper(qc),
    });
    const propertyUpdated = vi
      .mocked(ws.on)
      .mock.calls.find(([event]) => event === "property:updated")?.[1];
    expect(propertyUpdated).toBeDefined();

    (propertyUpdated as (payload: unknown) => void)({});

    expect(invalidate).toHaveBeenCalledWith({
      queryKey: issueKeys.tableAll("ws-1"),
    });
  });
});

describe("useRealtimeSync — workspace:deleted self-initiated suppression", () => {
  let qc: QueryClient;
  let stores: RealtimeSyncStores;

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    stores = createStores();
  });

  afterEach(() => {
    unmarkWorkspaceDeletePending("ws-2");
    localStorage.clear();
  });

  // getCurrentWsId is mocked to "ws-1" at module level, so deleting "ws-2"
  // never enters the relocate branch — these tests only exercise the
  // storage-cleanup path, which is the observable difference between a
  // handled and a suppressed event.
  const dispatchWorkspaceDeleted = (ws: WSClient, workspaceId: string) => {
    const call = vi
      .mocked(ws.on)
      .mock.calls.find(([event]) => event === "workspace:deleted");
    expect(call).toBeDefined();
    (call![1] as (p: unknown) => void)({ workspace_id: workspaceId });
  };

  it("ignores the event for a delete this client initiated", () => {
    const ws = createMockWs();
    renderHook(() => useRealtimeSync(ws, stores), {
      wrapper: createWrapper(qc),
    });
    qc.setQueryData(workspaceKeys.list(), [{ id: "ws-2", slug: "delete-me" }]);
    defaultStorage.setItem("multica_issue_draft:delete-me", "draft");

    markWorkspaceDeletePending("ws-2");
    dispatchWorkspaceDeleted(ws, "ws-2");

    // useDeleteWorkspace.onSuccess owns cleanup for self-initiated deletes;
    // the handler must not have touched storage.
    expect(defaultStorage.getItem("multica_issue_draft:delete-me")).toBe("draft");
  });

  it("still cleans up for a delete initiated elsewhere", () => {
    const ws = createMockWs();
    renderHook(() => useRealtimeSync(ws, stores), {
      wrapper: createWrapper(qc),
    });
    qc.setQueryData(workspaceKeys.list(), [{ id: "ws-2", slug: "delete-me" }]);
    defaultStorage.setItem("multica_issue_draft:delete-me", "draft");

    dispatchWorkspaceDeleted(ws, "ws-2");

    expect(defaultStorage.getItem("multica_issue_draft:delete-me")).toBeNull();
  });
});


describe("agent history refresh pressure", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("coalesces repeated task events into periodic history reads while invalidating presence", async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn().mockImplementation(async () => new Response("[]"));
    vi.stubGlobal("fetch", fetchMock);
    setApiInstance(new ApiClient("https://api.example.test"));
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const ws = createMockWs();
    qc.setQueryData(agentTaskSnapshotKeys.list("ws-1"), []);
    const hook = renderHook(() => {
      useRealtimeSync(ws, createStores());
      return useQuery(agentTasksOptions("ws-1", "agent-1"));
    }, { wrapper: createWrapper(qc) });
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const onAny = vi.mocked(ws.onAny).mock.calls[0]![0];
    await act(async () => {
      for (let i = 0; i < 50; i++) {
        onAny({ type: "task:completed", payload: {} } as never);
        await vi.advanceTimersByTimeAsync(200);
      }
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(qc.getQueryState(agentTaskSnapshotKeys.list("ws-1"))?.isInvalidated).toBe(true);
    await act(async () => { await vi.advanceTimersByTimeAsync(30_100); });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    hook.unmount();
    qc.clear();
  });

  it("aborts the actual history HTTP request when its last observer leaves", async () => {
    let requestSignal: AbortSignal | undefined;
    vi.stubGlobal("fetch", vi.fn((_url: string, init: RequestInit) => {
      requestSignal = init.signal as AbortSignal;
      return new Promise<Response>((_resolve, reject) => {
        requestSignal!.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")));
      });
    }));
    setApiInstance(new ApiClient("https://api.example.test"));
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const hook = renderHook(() => useQuery(agentTasksOptions("ws-1", "agent-1")), {
      wrapper: createWrapper(qc),
    });
    expect(requestSignal?.aborted).toBe(false);
    hook.unmount();
    expect(requestSignal?.aborted).toBe(true);
    qc.clear();
  });
});
