package handler

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/agentsource"
)

func TestAgentSchemaDownloadMatchesValidator(t *testing.T) {
	w := httptest.NewRecorder()
	testHandler.DownloadAgentSchema(w, newRequest(http.MethodGet, "/api/agent-schema", nil))
	if w.Code != http.StatusOK || w.Body.String() != string(agentsource.PortableSchema) { t.Fatal("schema download must serve the exact embedded validator schema") }
	if w.Header().Get("Content-Disposition") != `attachment; filename="agent.schema.json"` || w.Header().Get("Content-Type") != "application/schema+json" { t.Fatal("schema must be a downloadable JSON Schema file") }
}

func agentPackageFixture(t *testing.T, manifest string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	for name, body := range map[string]string{"agent.json":manifest, "AGENTS.md":"Review changes"} {
		f, err := w.Create(name); if err != nil { t.Fatal(err) }
		if _, err := f.Write([]byte(body)); err != nil { t.Fatal(err) }
	}
	if err := w.Close(); err != nil { t.Fatal(err) }
	return buffer.Bytes()
}

func agentPackageRequest(t *testing.T, content []byte, multipartBody bool) *http.Request {
	t.Helper()
	req := withURLParam(newRequest(http.MethodPost, "/", nil), "id", testWorkspaceID)
	if multipartBody {
		var buffer bytes.Buffer
		writer := multipart.NewWriter(&buffer)
		part, err := writer.CreateFormFile("file", "agent.zip"); if err != nil { t.Fatal(err) }
		if _, err := part.Write(content); err != nil { t.Fatal(err) }
		if err := writer.Close(); err != nil { t.Fatal(err) }
		req.Body = io.NopCloser(bytes.NewReader(buffer.Bytes()))
		req.Header.Set("Content-Type", writer.FormDataContentType())
	} else {
		req.Body = io.NopCloser(bytes.NewReader(content))
		req.Header.Set("Content-Type", "application/zip")
	}
	return req
}

func TestAgentPackagePreviewValidatesAndParsesUpload(t *testing.T) {
	archive := agentPackageFixture(t, `{"$schema":"agent.schema.json","version":"multica.agent/v2","name":"Package reviewer","instructions":"AGENTS.md","skills":[],"configuration":{"persona":"Reviewer","custom_env":{"TOKEN":"fixture-private-value"}}}`)
	for _, multipartBody := range []bool{false, true} {
		w := httptest.NewRecorder()
		testHandler.PreviewAgentPackage(w, agentPackageRequest(t, archive, multipartBody))
		if w.Code != http.StatusOK { t.Fatalf("status %d: %s", w.Code, w.Body.String()) }
		var response map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil { t.Fatal(err) }
		if string(response["name"]) != `"Package reviewer"` || string(response["instructions"]) != `"Review changes"` || len(response["package_hash"]) != 66 { t.Fatalf("unexpected preview: %s", w.Body.String()) }
		if strings.Contains(w.Body.String(), "fixture-private-value") { t.Fatal("preview exposed configuration secret value") }
	}
}

func TestAgentPackagePreviewReturnsSchemaLocations(t *testing.T) {
	archive := agentPackageFixture(t, `{"$schema":"agent.schema.json","version":"multica.agent/v2","name":"Reviewer","instructions":"AGENTS.md","skills":[],"configuration":{"persona":123}}`)
	w := httptest.NewRecorder()
	testHandler.PreviewAgentPackage(w, agentPackageRequest(t, archive, false))
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "/configuration/persona") { t.Fatalf("expected schema issue, got %d %s", w.Code, w.Body.String()) }
}

func TestAgentPackagePreviewRequiresWorkspaceAdmin(t *testing.T) {
	memberID := createPlainMember(t, "package-import-member@example.test")
	archive := agentPackageFixture(t, `{}`)
	req := agentPackageRequest(t, archive, false)
	req.Header.Set("X-User-ID", memberID)
	w := httptest.NewRecorder()
	testHandler.PreviewAgentPackage(w, req)
	if w.Code != http.StatusForbidden { t.Fatalf("expected permission rejection, got %d: %s", w.Code, w.Body.String()) }
}

func TestAgentPackageUploadRejectsAmbiguousMultipartAndMediaType(t *testing.T) {
	for _, mediaType := range []string{"application/json", "text/plain"} {
		req := agentPackageRequest(t, []byte("{}"), false)
		req.Header.Set("Content-Type", mediaType)
		w := httptest.NewRecorder()
		testHandler.PreviewAgentPackage(w, req)
		if w.Code != http.StatusUnsupportedMediaType { t.Fatalf("media type %q: %d", mediaType, w.Code) }
	}
	var buffer bytes.Buffer
	w := multipart.NewWriter(&buffer)
	for range 2 { file, _ := w.CreateFormFile("file", "agent.zip"); _, _ = file.Write([]byte("zip")) }
	_ = w.Close()
	req := agentPackageRequest(t, nil, false)
	req.Body = io.NopCloser(bytes.NewReader(buffer.Bytes()))
	req.Header.Set("Content-Type", w.FormDataContentType())
	response := httptest.NewRecorder()
	testHandler.PreviewAgentPackage(response, req)
	if response.Code != http.StatusBadRequest { t.Fatalf("multiple files: %d %s", response.Code, response.Body.String()) }
}
