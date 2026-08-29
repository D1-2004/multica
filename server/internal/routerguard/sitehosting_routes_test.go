package routerguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticSiteRoutesReachBackendInAoneContainer(t *testing.T) {
	routerPath := filepath.Join("..", "..", "cmd", "server", "router.go")
	routerBody, err := os.ReadFile(routerPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{
		`r.Put("/api/sitehosting/uploads/{uploadId}", h.UploadStaticSite)`,
		`r.Get("/sites/{publicSiteId}/", h.ServeStaticSite)`,
		`r.Get("/sites/{publicSiteId}/*", h.ServeStaticSite)`,
		`r.Head("/sites/{publicSiteId}", h.ServeStaticSite)`,
		`r.Head("/sites/{publicSiteId}/", h.ServeStaticSite)`,
		`r.Head("/sites/{publicSiteId}/*", h.ServeStaticSite)`,
	} {
		if !strings.Contains(string(routerBody), route) {
			t.Errorf("missing Go route %s", route)
		}
	}

	nginxPath := filepath.Join("..", "..", "..", "APP-META", "docker-config", "environment", "cai", "conf", "nginx-proxy.conf")
	nginxBody, err := os.ReadFile(nginxPath)
	if err != nil {
		t.Fatal(err)
	}
	nginx := string(nginxBody)
	if !strings.Contains(nginx, "location /api/sitehosting/uploads/ {") ||
		!strings.Contains(nginx, "client_max_body_size 50m;") ||
		!strings.Contains(nginx, "proxy_request_buffering off;") {
		t.Fatal("Aone nginx does not preserve the 50 MiB streaming upload contract")
	}
	if !strings.Contains(nginx, "location /sites/ {") || !strings.Contains(nginx, "proxy_pass http://127.0.0.1:8080;") {
		t.Fatal("Aone nginx does not proxy /sites/ to the Go backend")
	}
}
