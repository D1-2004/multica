(() => {
  "use strict";

  const nativeFetch = window.fetch.bind(window);
  const script = document.currentScript;
  const publicSiteId = script && script.dataset
    ? script.dataset.publicSiteId
    : "";
  if (!publicSiteId) return;

  const proxyEndpoint = `/api/sitehosting/sites/${encodeURIComponent(publicSiteId)}/fetch-proxy`;

  function exactAllowedURL(target) {
    const declared = window.__MULTICA_FETCH_PROXY_ALLOWLIST__;
    if (!Array.isArray(declared)) return false;
    return declared.some((raw) => {
      if (typeof raw !== "string") return false;
      try {
        const allowed = new URL(raw);
        return allowed.protocol === "https:" &&
          !allowed.username &&
          !allowed.password &&
          !allowed.hash &&
          allowed.href === target.href;
      } catch {
        return false;
      }
    });
  }

  function bytesToBase64(bytes) {
    let binary = "";
    const chunkSize = 0x8000;
    for (let offset = 0; offset < bytes.length; offset += chunkSize) {
      binary += String.fromCharCode(...bytes.subarray(offset, offset + chunkSize));
    }
    return btoa(binary);
  }

  window.fetch = async function multicaFetch(input, init) {
    const rawURL = typeof input === "string" || input instanceof URL
      ? String(input)
      : input.url;
    const target = new URL(rawURL, window.location.href);
    if (target.origin === window.location.origin || !exactAllowedURL(target)) {
      return nativeFetch(input, init);
    }

    const request = new Request(input, init);
    const headers = [];
    request.headers.forEach((value, name) => headers.push([name, value]));
    let bodyBase64 = "";
    if (request.method !== "GET" && request.method !== "HEAD") {
      bodyBase64 = bytesToBase64(new Uint8Array(await request.clone().arrayBuffer()));
    }

    let proxyResponse;
    try {
      proxyResponse = await nativeFetch(proxyEndpoint, {
        method: "POST",
        credentials: "omit",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          version: 1,
          url: target.href,
          method: request.method,
          headers,
          body_base64: bodyBase64,
        }),
        signal: request.signal,
      });
    } catch (error) {
      throw new TypeError("Multica fetch proxy failed", { cause: error });
    }

    if (proxyResponse.headers.get("X-Multica-Fetch-Proxy-Result") !== "upstream") {
      throw new TypeError("Multica fetch proxy failed");
    }

    const responseHeaders = new Headers(proxyResponse.headers);
    responseHeaders.delete("X-Multica-Fetch-Proxy-Result");
    const noBody = request.method === "HEAD" ||
      proxyResponse.status === 204 ||
      proxyResponse.status === 205 ||
      proxyResponse.status === 304;
    const body = noBody ? null : await proxyResponse.arrayBuffer();
    return new Response(body, {
      status: proxyResponse.status,
      statusText: proxyResponse.statusText,
      headers: responseHeaders,
    });
  };
})();
