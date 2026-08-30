package sitehosting

import (
	_ "embed"
	"net/http"
	"strconv"
)

//go:embed fetch_proxy_runtime.js
var fetchProxyRuntime []byte

var fetchProxyRuntimeETag = func() string {
	return injectedHTMLETag("runtime", fetchProxyRuntime)
}()

func (s *Service) ServeFetchProxyRuntime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(fetchProxyRuntime)))
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", fetchProxyRuntimeETag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Header.Get("If-None-Match") == fetchProxyRuntimeETag {
		w.Header().Del("Content-Length")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(fetchProxyRuntime)
}
