const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

const source = fs.readFileSync(path.join(__dirname, "fetch_proxy_runtime.js"), "utf8");

function loadRuntime(nativeFetch, allowlist) {
  const location = new URL("https://sites.example.test/sites/public-id/");
  const window = {
    fetch: nativeFetch,
    location,
    __MULTICA_FETCH_PROXY_ALLOWLIST__: allowlist,
  };
  const context = vm.createContext({
    window,
    document: { currentScript: { dataset: { publicSiteId: "public-id" } } },
    URL,
    Request,
    Response,
    Headers,
    Uint8Array,
    btoa,
    TypeError,
  });
  vm.runInContext(source, context);
  return window;
}

test("without a page allowlist the runtime defines no default target", async () => {
  const calls = [];
  const nativeFetch = async (...args) => {
    calls.push(args);
    return new Response("native");
  };
  const window = loadRuntime(nativeFetch, undefined);
  const target = "https://connector.dingtalk.com/webhook/flow/not-declared";

  await window.fetch(target, { method: "POST", body: "once" });

  assert.equal(window.__MULTICA_FETCH_PROXY_ALLOWLIST__, undefined);
  assert.equal(calls.length, 1);
  assert.equal(calls[0][0], target);
});

test("same-origin and unmatched requests use native fetch", async () => {
  const calls = [];
  const nativeFetch = async (...args) => {
    calls.push(args);
    return new Response("native");
  };
  const window = loadRuntime(nativeFetch, [
    "https://connector.dingtalk.com/webhook/flow/exact",
  ]);

  await window.fetch("/api/local");
  await window.fetch("https://connector.dingtalk.com/webhook/flow/other");

  assert.equal(calls.length, 2);
  assert.equal(calls[0][0], "/api/local");
  assert.equal(calls[1][0], "https://connector.dingtalk.com/webhook/flow/other");
});

test("an exact URL uses the proxy once and rebuilds the upstream response", async () => {
  const calls = [];
  const nativeFetch = async (...args) => {
    calls.push(args);
    return new Response('{"accepted":true}', {
      status: 202,
      headers: {
        "Content-Type": "application/json",
        "X-Multica-Fetch-Proxy-Result": "upstream",
        "X-Upstream": "kept",
      },
    });
  };
  const target = "https://connector.dingtalk.com/webhook/flow/exact";
  const window = loadRuntime(nativeFetch, [target]);

  const response = await window.fetch(target, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: '{"score":5}',
  });

  assert.equal(calls.length, 1);
  assert.equal(calls[0][0], "/api/sitehosting/sites/public-id/fetch-proxy");
  const envelope = JSON.parse(calls[0][1].body);
  assert.equal(envelope.url, target);
  assert.equal(envelope.method, "POST");
  assert.equal(Buffer.from(envelope.body_base64, "base64").toString(), '{"score":5}');
  assert.equal(response.status, 202);
  assert.equal(response.headers.get("X-Upstream"), "kept");
  assert.equal(response.headers.has("X-Multica-Fetch-Proxy-Result"), false);
  assert.equal(await response.text(), '{"accepted":true}');
});

test("proxy failure rejects without retrying the original URL", async () => {
  const calls = [];
  const nativeFetch = async (...args) => {
    calls.push(args);
    return new Response('{"error":{"code":"upstream_failed"}}', { status: 502 });
  };
  const target = "https://connector.dingtalk.com/webhook/flow/exact";
  const window = loadRuntime(nativeFetch, [target]);

  await assert.rejects(() => window.fetch(target, { method: "POST", body: "once" }), {
    name: "TypeError",
    message: "Multica fetch proxy failed",
  });
  assert.equal(calls.length, 1);
  assert.equal(calls[0][0], "/api/sitehosting/sites/public-id/fetch-proxy");
});
