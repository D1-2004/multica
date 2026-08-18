package routerguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestForkRoutesRemainRegistered(t *testing.T) {
	path := filepath.Join("..", "..", "cmd", "server", "router.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	router := string(source)
	registrations := []string{
		`r.Post("/tasks/{taskId}/llm-traces", h.RelayTaskLLMTrace)`,
		`r.Get("/api/runtimes/fc-e2b/stable-channel", h.GetFCE2BStableChannel)`,
		`r.Post("/api/runtimes/fc-e2b/stable-releases", h.CreateFCE2BStableRelease)`,
		`r.Get("/api/runtimes/fc-e2b/stable-releases", h.ListFCE2BStableReleases)`,
		`r.Get("/api/runtimes/fc-e2b/stable-releases/{releaseId}", h.GetFCE2BStableRelease)`,
		`r.Get("/api/runtimes/fc-e2b/stable-runtimes", h.ListFCE2BStableRuntimes)`,
		`r.Post("/api/runtimes/fc-e2b/stable-releases/{releaseId}/pause", h.PauseFCE2BStableRelease)`,
		`r.Post("/api/runtimes/fc-e2b/stable-releases/{releaseId}/resume", h.ResumeFCE2BStableRelease)`,
		`r.Post("/api/runtimes/fc-e2b/stable-releases/{releaseId}/start-rollout", h.StartFCE2BStableRollout)`,
		`r.Post("/api/runtimes/fc-e2b/stable-releases/{releaseId}/advance-rollout", h.AdvanceFCE2BStableRollout)`,
		`r.Post("/api/runtimes/fc-e2b/stable-releases/{releaseId}/complete-observation", h.CompleteFCE2BStableObservation)`,
		`r.Post("/api/runtimes/fc-e2b/stable-releases/{releaseId}/terminate", h.TerminateFCE2BStableRelease)`,
		`r.Post("/api/runtimes/fc-e2b/stable-releases/{releaseId}/rollback", h.RollbackFCE2BStableRelease)`,
		`r.Get("/api/runtimes/cloud-sandbox/stable-channel", h.GetCloudSandboxStableChannel)`,
		`r.Post("/api/runtimes/cloud-sandbox/stable-releases", h.CreateCloudSandboxStableRelease)`,
		`r.Get("/api/runtimes/cloud-sandbox/stable-releases", h.ListCloudSandboxStableReleases)`,
		`r.Get("/api/runtimes/cloud-sandbox/stable-releases/{releaseId}", h.GetFCE2BStableRelease)`,
		`r.Get("/api/runtimes/cloud-sandbox/stable-runtimes", h.ListCloudSandboxStableRuntimes)`,
		`r.Post("/api/runtimes/cloud-sandbox/stable-releases/{releaseId}/pause", h.PauseFCE2BStableRelease)`,
		`r.Post("/api/runtimes/cloud-sandbox/stable-releases/{releaseId}/resume", h.ResumeFCE2BStableRelease)`,
		`r.Post("/api/runtimes/cloud-sandbox/stable-releases/{releaseId}/start-rollout", h.StartFCE2BStableRollout)`,
		`r.Post("/api/runtimes/cloud-sandbox/stable-releases/{releaseId}/advance-rollout", h.AdvanceFCE2BStableRollout)`,
		`r.Post("/api/runtimes/cloud-sandbox/stable-releases/{releaseId}/complete-observation", h.CompleteFCE2BStableObservation)`,
		`r.Post("/api/runtimes/cloud-sandbox/stable-releases/{releaseId}/terminate", h.TerminateFCE2BStableRelease)`,
		`r.Post("/api/runtimes/cloud-sandbox/stable-releases/{releaseId}/rollback", h.RollbackFCE2BStableRelease)`,
		`r.With(handler.RequireDingTalkHumanActor).Get("/api/fde/onboarding", h.GetFDEOnboarding)`,
		`r.With(handler.RequireDingTalkHumanActor).Post("/api/fde/onboarding", h.ProvisionFDEOnboarding)`,
		`r.Route("/api/dta/load-smokes", func(r chi.Router)`,
		`r.Post("/api/issue-delegations", h.DelegateIssue)`,
		`r.Post("/messages/{messageId}/received", h.AcknowledgeChatMessageReceived)`,
	}
	for _, registration := range registrations {
		if !strings.Contains(router, registration) {
			t.Errorf("fork route is not registered: %s", registration)
		}
	}
	if !strings.Contains(router, `r.Use(opts.DeploymentFence.Middleware)`) {
		t.Error("deployment fence middleware is not registered")
	}
}
