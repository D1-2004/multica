import { afterEach, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());
const id = "11111111-1111-4111-8111-111111111111";
const publication = { id, source_type: "github", repository_url: "https://github.com/acme/agent", ref: "refs/tags/v1", commit_sha: "a".repeat(40), published_at: "2026-09-14T12:00:00Z", published_by: id, author_name: "Owner", changed: true, rollback_of: "", has_configuration_snapshot: true, initial_publication: true };

it("loads publication metadata and passes the pagination cursor", async () => {
  const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ publications: [publication], next_cursor: id })));
  vi.stubGlobal("fetch", fetch);
  const history = await new ApiClient("https://api.example.test").listAgentPublications("agent", id);
  expect(history.publications).toEqual([publication]);
  expect(history.next_cursor).toBe(id);
  expect(fetch).toHaveBeenCalledWith(`https://api.example.test/api/agents/agent/source/publications?before=${id}`, expect.anything());
});

it.each([{ publications: [{ ...publication, id: "invalid" }], next_cursor: null }, { publications: [{ ...publication, has_configuration_snapshot: "yes" }], next_cursor: null }, { publications: [] }])("rejects malformed publication history", async (body) => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(body))));
  await expect(new ApiClient("https://api.example.test").listAgentPublications("agent")).rejects.toThrow("Invalid Agent publication history response");
});

it("previews a historical publication through the shared preview endpoint", async () => {
  const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ rollback_of: id, preview_id: id, expires_at: "2026-09-14T12:30:00Z", repository_url: publication.repository_url, ref: publication.ref, base_sha: "b".repeat(40), resolved_sha: publication.commit_sha, changed: true })));
  vi.stubGlobal("fetch", fetch);
  const preview = await new ApiClient("https://api.example.test").previewAgentPublicationRollback("agent", id);
  expect(preview.resolved_sha).toBe(publication.commit_sha);
  expect(fetch).toHaveBeenCalledWith("https://api.example.test/api/agents/agent/source/preview", expect.objectContaining({ method: "POST", body: JSON.stringify({ publication_id: id }) }));
});

it("rejects a malformed rollback preview", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("{}")));
  await expect(new ApiClient("https://api.example.test").previewAgentPublicationRollback("agent", id)).rejects.toThrow("Invalid Agent rollback preview response");
});

it("rejects an older server that ignores the requested publication", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ preview_id: id, expires_at: "2026-09-14T12:30:00Z", repository_url: publication.repository_url, ref: "main", base_sha: "b".repeat(40), resolved_sha: "b".repeat(40), changed: false }))));
  await expect(new ApiClient("https://api.example.test").previewAgentPublicationRollback("agent", id)).rejects.toThrow("Invalid Agent rollback preview response");
});
