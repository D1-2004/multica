package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestWaitForASBCapacityReleaseObservesSandboxAndQuota(t *testing.T) {
	t.Parallel()

	var listCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes":
			if request.URL.Query().Get("state") != "Running" {
				t.Errorf("sandbox state filter = %q", request.URL.Query().Get("state"))
			}
			call := listCalls.Add(1)
			if call == 1 {
				_, _ = io.WriteString(response, `{
					"sandboxInfos":[{"id":"sandbox-reclaimed","status":{"state":"Running"},"createdAt":"2026-08-03T05:00:00Z"}],
					"pagination":{"page":1,"pageSize":100,"total":1,"hasNextPage":false,"hasPreviousPage":false}
				}`)
				return
			}
			_, _ = io.WriteString(response, `{
				"sandboxInfos":[],
				"pagination":{"page":1,"pageSize":100,"total":0,"hasNextPage":false,"hasPreviousPage":false}
			}`)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes/quotas":
			usage := 5
			if listCalls.Load() > 1 {
				usage = 4
			}
			_, _ = io.WriteString(response, `[{"networkZone":"ALITest","region":"cn-zhangjiakou","quota":5,"usage":`+strconv.Itoa(usage)+`}]`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client := newTestASBClient(t, server)

	if err := waitForASBCapacityRelease(
		context.Background(),
		client,
		"sandbox-reclaimed",
	); err != nil {
		t.Fatalf("waitForASBCapacityRelease: %v", err)
	}
	if listCalls.Load() < 2 {
		t.Fatalf("list calls = %d, want at least 2", listCalls.Load())
	}
}

func TestGetLiveASBSandboxTreatsNotFoundAsMissing(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes/missing-sandbox" {
			response.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(response, `{"code":"NOT_FOUND","message":"sandbox not found"}`)
			return
		}
		http.NotFound(response, request)
	}))
	defer server.Close()
	client := newTestASBClient(t, server)

	sandbox, exists, err := getLiveASBSandbox(
		context.Background(),
		client,
		"missing-sandbox",
	)
	if err != nil {
		t.Fatalf("getLiveASBSandbox: %v", err)
	}
	if exists || sandbox != nil {
		t.Fatalf("sandbox = %#v, exists = %t", sandbox, exists)
	}
}
