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

const extractVoiceSystemPrompt = `You extract two Coordinator voice fields from the agent's instructions. Reply with a JSON object only.

The JSON shape is:
{"persona":"...","reply_tone":"..."}

persona is 角色: who this colleague is in IM. 1-3 sentences, same language as the instructions.
- Keep: identity, stance, what they value when speaking, how they relate to the team.
- Drop: tools, skills, workflows, SOPs, routing rules, secrets, and work they will go do.
- Do not write job duties as a work order. Persona never decides whether a message becomes an Issue.

reply_tone is 沟通风格: how they type in DingTalk. 1-2 sentences.
- Keep: length, formality, punctuation, 您 vs 你, emoji, how they open and close, what they refuse to sound like.
- Prefer concrete IM habits ("短句、先结论、不客套、不复读对方") over adjectives ("专业、友好").
- No helpdesk scripts: 收到, 正在处理, 稍等, 已为您.

Shared:
- Compress. Do not copy the full instructions. Do not invent a new character.
- If the instructions do not describe a person or tone, write a concise DingTalk teammate (a real colleague, not a helpdesk or dispatcher) and a short spoken work-chat tone. No 您.
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
