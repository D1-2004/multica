#!/usr/bin/env node

import { readFile } from "node:fs/promises";

const baseURL = process.env.MULTICA_MCP_BASE_URL?.replace(/\/$/, "");
const publicAgentID = process.env.MULTICA_MCP_PUBLIC_AGENT_ID;
const tokenFile = process.env.MULTICA_MCP_TOKEN_FILE;

if (!baseURL || !publicAgentID || !tokenFile) {
  throw new Error(
    "MULTICA_MCP_BASE_URL, MULTICA_MCP_PUBLIC_AGENT_ID, and MULTICA_MCP_TOKEN_FILE are required",
  );
}

const token = (await readFile(tokenFile, "utf8")).trim();
if (!token) throw new Error("MCP token file is empty");

const canonicalURL = `${baseURL}/api/mcp/agents/${encodeURIComponent(publicAgentID)}`;
const connectURL = `${baseURL}/api/mcp/connect/${encodeURIComponent(token)}`;
const protocolVersion = "2025-03-26";
let rpcID = 0;

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

async function post(url, payload, { auth = "header", origin } = {}) {
  const headers = { "Content-Type": "application/json" };
  if (auth === "header") headers["X-API-Key"] = token;
  if (auth === "invalid") headers["X-API-Key"] = "invalid-agent-token";
  if (origin) headers.Origin = origin;
  if (payload?.method !== "initialize") {
    headers["MCP-Protocol-Version"] = protocolVersion;
  }
  const response = await fetch(url, {
    method: "POST",
    headers,
    body: payload === undefined ? undefined : JSON.stringify(payload),
  });
  const text = await response.text();
  let body = null;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      body = { raw: text };
    }
  }
  return {
    status: response.status,
    body,
    headers: {
      cacheControl: response.headers.get("cache-control"),
      referrerPolicy: response.headers.get("referrer-policy"),
      authenticate: response.headers.get("www-authenticate"),
    },
  };
}

function request(method, params = {}, id = `deep-${++rpcID}`) {
  return { jsonrpc: "2.0", id, method, params };
}

async function rpc(method, params = {}, options = {}) {
  return post(options.url ?? canonicalURL, request(method, params), options);
}

async function callTool(name, args, id) {
  return post(
    canonicalURL,
    request("tools/call", { name, arguments: args }, id ?? `tool-${++rpcID}`),
  );
}

function structuredTask(response) {
  return response?.body?.result?.structuredContent;
}

function taskState(task) {
  return task?.status?.state ?? "UNKNOWN";
}

async function pollTask(taskID, timeoutMs = 240_000) {
  const startedAt = Date.now();
  const observations = [];
  let previousSnapshot = "";
  while (Date.now() - startedAt < timeoutMs) {
    const response = await callTool("get_task", { task_id: taskID });
    assert(response.status === 200, `get_task HTTP ${response.status}`);
    assert(!response.body?.result?.isError, `get_task failed: ${JSON.stringify(response.body)}`);
    const task = structuredTask(response);
    const snapshot = JSON.stringify({
      state: taskState(task),
      artifactCount: task?.artifacts?.length ?? 0,
      historyCount: task?.history?.length ?? 0,
      statusMessageParts: task?.status?.message?.parts?.map((part) => part.kind ?? part.type) ?? [],
    });
    if (snapshot !== previousSnapshot) {
      observations.push({ atMs: Date.now() - startedAt, ...JSON.parse(snapshot) });
      previousSnapshot = snapshot;
    }
    if (["TASK_STATE_COMPLETED", "TASK_STATE_FAILED", "TASK_STATE_CANCELED", "TASK_STATE_REJECTED"].includes(taskState(task))) {
      return { task, observations };
    }
    await new Promise((resolve) => setTimeout(resolve, 2_000));
  }
  throw new Error(`task ${taskID} did not reach a terminal state in ${timeoutMs}ms`);
}

const runID = `mcp-deep-${new Date().toISOString().replace(/[-:.TZ]/g, "")}`;
const report = { runID, protocol: {}, idempotency: {}, issue: {}, artifact: {}, followUp: {} };

const missingAuth = await post(canonicalURL, request("initialize", { protocolVersion }), { auth: "none" });
const invalidAuth = await post(canonicalURL, request("initialize", { protocolVersion }), { auth: "invalid" });
const untrustedOrigin = await post(canonicalURL, request("initialize", { protocolVersion }), {
  origin: "https://untrusted.example",
});
const initialize = await post(
  connectURL,
  request("initialize", {
    protocolVersion,
    capabilities: {},
    clientInfo: { name: "multica-agent-mcp-deep-e2e", version: "1.0.0" },
  }),
  { auth: "none" },
);
const toolsList = await rpc("tools/list");
const unknownMethod = await rpc("resources/list");
const invalidDelegate = await callTool("delegate_task", { instruction: "", unknown: true });
const unknownTask = await callTool("get_task", { task_id: `tsk_missing_${runID}` });

assert(missingAuth.status === 401, `missing auth status ${missingAuth.status}`);
assert(invalidAuth.status === 401, `invalid auth status ${invalidAuth.status}`);
assert(untrustedOrigin.status === 403, `untrusted origin status ${untrustedOrigin.status}`);
assert(initialize.status === 200, `initialize status ${initialize.status}`);
assert(toolsList.status === 200, `tools/list status ${toolsList.status}`);
assert(unknownMethod.body?.error?.code === -32601, "unknown method did not return -32601");
assert(invalidDelegate.body?.error?.code === -32602, "invalid delegate did not return -32602");
assert(unknownTask.body?.result?.isError === true, "unknown task did not return a tool error");

const tools = toolsList.body?.result?.tools ?? [];
const expectedTools = [
  "delegate_task",
  "get_task",
  "get_issue",
  "continue_issue",
  "list_artifacts",
  "read_artifact",
  "describe_agent",
];
assert(
  expectedTools.every((name) => tools.some((tool) => tool.name === name)),
  `missing Issue-backed tools: ${tools.map((tool) => tool.name).join(", ")}`,
);
report.protocol = {
  missingAuthStatus: missingAuth.status,
  invalidAuthStatus: invalidAuth.status,
  untrustedOriginStatus: untrustedOrigin.status,
  initializeProtocolVersion: initialize.body?.result?.protocolVersion,
  connectSecurityHeaders: initialize.headers,
  toolNames: tools.map((tool) => tool.name),
  toolSchemas: tools.map((tool) => ({ name: tool.name, inputSchema: tool.inputSchema })),
  unknownMethodCode: unknownMethod.body?.error?.code,
  invalidArgumentsCode: invalidDelegate.body?.error?.code,
  unknownTaskIsToolError: unknownTask.body?.result?.isError,
};

const marker = `REMOTE_FILE_${runID}`;
const instruction = [
  `Create a file named mcp-output/${runID}.txt in your current task workspace.`,
  `Its complete content must be exactly ${marker}.`,
  "Verify the file by reading it back.",
  `Run multica attachment upload mcp-output/${runID}.txt so the file becomes a durable Issue artifact.`,
  "Post one final Issue comment saying REMOTE_FILE_CREATED, without including the marker.",
].join(" ");
const requestID = `${runID}-same-request`;
const replayResponses = await Promise.all(
  Array.from({ length: 6 }, (_, index) =>
    callTool("delegate_task", { instruction, request_id: requestID }, `idem-${index}`),
  ),
);
const replayTasks = replayResponses.map(structuredTask);
const replayTaskIDs = [...new Set(replayTasks.map((task) => task?.id).filter(Boolean))];
assert(replayTaskIDs.length === 1, `idempotent replay created ${replayTaskIDs.length} tasks`);
const taskID = replayTaskIDs[0];
const conflict = await callTool("delegate_task", {
  instruction: `${instruction} but return DIFFERENT_RESULT`,
  request_id: requestID,
});
assert(conflict.body?.result?.isError === true, "request id conflict did not return a tool error");
const firstTerminal = await pollTask(taskID);
const issueID = firstTerminal.task?.issue?.id;
assert(issueID, `Issue-backed task did not expose issue_id: ${JSON.stringify(firstTerminal.task)}`);
const artifactParts = (firstTerminal.task?.artifacts ?? []).flatMap((artifact) => artifact.parts ?? []);
report.idempotency = {
  concurrentRequests: replayResponses.length,
  uniqueTaskIDs: replayTaskIDs,
  conflictIsToolError: conflict.body?.result?.isError,
  conflictMessage: conflict.body?.result?.content?.[0]?.text,
};
report.artifact = {
  taskID,
  terminalState: taskState(firstTerminal.task),
  observations: firstTerminal.observations,
  artifactCount: firstTerminal.task?.artifacts?.length ?? 0,
  partKinds: artifactParts.map((part) => part.kind ?? part.type),
  textContainsMarker: artifactParts.some((part) => typeof part.text === "string" && part.text.includes(marker)),
  finalTexts: artifactParts.map((part) => part.text).filter((value) => typeof value === "string"),
};

const issueResponse = await callTool("get_issue", { issue_id: issueID });
assert(!issueResponse.body?.result?.isError, `get_issue failed: ${JSON.stringify(issueResponse.body)}`);
const issue = structuredTask(issueResponse);
report.issue = {
  id: issueID,
  identifier: issue?.issue?.identifier,
  url: issue?.issue?.url,
  title: issue?.issue?.title,
  priority: issue?.issue?.priority,
  commentCount: issue?.comments?.length ?? 0,
};

const listedArtifactsResponse = await callTool("list_artifacts", { issue_id: issueID });
assert(!listedArtifactsResponse.body?.result?.isError, `list_artifacts failed: ${JSON.stringify(listedArtifactsResponse.body)}`);
const listedArtifacts = structuredTask(listedArtifactsResponse)?.artifacts ?? [];
const uploadedArtifact = listedArtifacts.find((artifact) => artifact.filename === `${runID}.txt`);
assert(uploadedArtifact?.id, `uploaded artifact was not listed: ${JSON.stringify(listedArtifacts)}`);
const readArtifactResponse = await callTool("read_artifact", { artifact_id: uploadedArtifact.id });
assert(!readArtifactResponse.body?.result?.isError, `read_artifact failed: ${JSON.stringify(readArtifactResponse.body)}`);
const embeddedResource = readArtifactResponse.body?.result?.content?.find((part) => part.type === "resource")?.resource;
assert(embeddedResource?.text === marker, `artifact body mismatch: ${JSON.stringify(embeddedResource)}`);
report.artifact.persistent = {
  metadata: uploadedArtifact,
  uri: embeddedResource.uri,
  mimeType: embeddedResource.mimeType,
  exactBodyMatch: embeddedResource.text === marker,
};

const followUpInstruction = [
  `Without recreating any file, find mcp-output/${runID}.txt from the previous delegated task.`,
  `If you can read it and its exact content is ${marker}, reply exactly FOUND_PREVIOUS_FILE.`,
  "Otherwise reply exactly MISSING_PREVIOUS_FILE.",
].join(" ");
const followUpResponse = await callTool("continue_issue", {
  issue_id: issueID,
  instruction: followUpInstruction,
  request_id: `${runID}-follow-up`,
});
const followUpTask = structuredTask(followUpResponse);
assert(followUpTask?.id, `follow-up task was not created: ${JSON.stringify(followUpResponse.body)}`);
const followUpTerminal = await pollTask(followUpTask.id);
const followUpTexts = (followUpTerminal.task?.artifacts ?? [])
  .flatMap((artifact) => artifact.parts ?? [])
  .map((part) => part.text)
  .filter((value) => typeof value === "string");
const followedIssueResponse = await callTool("get_issue", { issue_id: issueID });
const followedIssue = structuredTask(followedIssueResponse);
const followUpCommentTexts = (followedIssue?.comments ?? []).map((comment) => comment.content).filter(Boolean);
const foundPreviousFile = [...followUpTexts, ...followUpCommentTexts].some((text) => text.includes("FOUND_PREVIOUS_FILE"));
report.followUp = {
  taskID: followUpTask.id,
  issueID: followUpTask.issue?.id,
  terminalState: taskState(followUpTerminal.task),
  observations: followUpTerminal.observations,
  finalTexts: followUpTexts,
  commentTexts: followUpCommentTexts,
  foundPreviousFile,
  missingPreviousFile: [...followUpTexts, ...followUpCommentTexts].some((text) => text.includes("MISSING_PREVIOUS_FILE")),
};
assert(followUpTask.issue?.id === issueID, "follow-up created or returned a different Issue");
assert(foundPreviousFile, "follow-up did not recover the previous Issue task's file/context");

console.log(JSON.stringify(report, null, 2));
