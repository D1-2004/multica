package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/pkg/llm"
)

const (
	extractVoiceTimeout      = 20 * time.Second
	extractVoiceInstructions = 6000
	extractVoiceMaxTokens    = 256
	extractVoiceTemperature  = 0.2
)

const extractVoiceSystemPrompt = `You extract an agent's persona and reply tone from their instructions. Reply with a JSON object only.

The JSON shape is:
{"persona":"...","reply_tone":"..."}

Rules:
- persona: who this agent is, 1-3 sentences, in the same language as the instructions. No tools, no workflow, no secrets, no action policy.
- reply_tone: how they speak in chat, 1-2 sentences. Cover length, formality, and what to avoid.
- Do not copy the full instructions. Do not invent duties that change what the agent does.
- If the instructions do not describe a person or tone, write a concise professional colleague persona and a short, direct work-chat tone.
`

type extractAgentVoiceRequest struct {
	Instructions *string `json:"instructions"`
}

type extractAgentVoiceResponse struct {
	Persona   string `json:"persona"`
	ReplyTone string `json:"reply_tone"`
}

// ExtractAgentVoice uses the LLM to draft persona and reply_tone from
// instructions. It does not persist; the client fills the Instructions tab.
func (h *Handler) ExtractAgentVoice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, existing) {
		return
	}
	if h.LLM == nil || !h.LLM.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "voice extraction is not configured")
		return
	}

	var req extractAgentVoiceRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	instructions := existing.Instructions
	if req.Instructions != nil {
		instructions = *req.Instructions
	}
	instructions = strings.TrimSpace(instructions)
	if instructions == "" {
		writeError(w, http.StatusBadRequest, "instructions are required to extract persona and tone")
		return
	}
	instructions = clipVoiceRunes(instructions, extractVoiceInstructions)

	ctx, cancel := context.WithTimeout(r.Context(), extractVoiceTimeout)
	defer cancel()
	raw, err := h.LLM.GenerateJSONFast(ctx, "", extractVoiceSystemPrompt, "Instructions:\n"+instructions, extractVoiceTemperature, extractVoiceMaxTokens)
	if err != nil {
		if errors.Is(err, llm.ErrNotConfigured) {
			writeError(w, http.StatusServiceUnavailable, "voice extraction is not configured")
			return
		}
		slog.Warn("extract agent voice failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusBadGateway, "failed to extract persona and tone")
		return
	}
	voice, err := parseExtractedVoice(raw)
	if err != nil {
		slog.Warn("extract agent voice parse failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusBadGateway, "failed to extract persona and tone")
		return
	}
	writeJSON(w, http.StatusOK, voice)
}

func parseExtractedVoice(raw string) (extractAgentVoiceResponse, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	var out extractAgentVoiceResponse
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return extractAgentVoiceResponse{}, err
	}
	out.Persona = clipVoiceRunes(strings.TrimSpace(out.Persona), maxAgentPersonaLength)
	out.ReplyTone = clipVoiceRunes(strings.TrimSpace(out.ReplyTone), maxAgentReplyToneLength)
	if out.Persona == "" && out.ReplyTone == "" {
		return extractAgentVoiceResponse{}, errors.New("empty voice")
	}
	return out, nil
}

func clipVoiceRunes(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
