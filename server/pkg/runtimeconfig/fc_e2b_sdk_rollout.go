package runtimeconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/google/uuid"
)

// FCE2BSDKRolloutDiamondDataID is the optional, live document that moves
// FC/E2B operations from the e2b CLI to the Go SDK. It is separate from the
// strict runtime document, which older binaries would refuse to start with
// once it carried a new field. Reads and listener failures never block
// startup; without a valid document callers use their environment fallback.
const FCE2BSDKRolloutDiamondDataID = "dt-fde-multica-fc-e2b-sdk-rollout.json"

// FCE2BSDKRollout selects the FC/E2B operations that use the Go SDK. The zero
// value selects none.
type FCE2BSDKRollout struct {
	// Enabled is the master switch; false keeps every operation on the CLI
	// whatever the lists say.
	Enabled      bool     `json:"enabled"`
	WorkspaceIDs []string `json:"workspace_ids,omitempty"`
	AgentIDs     []string `json:"agent_ids,omitempty"`
	RuntimeIDs   []string `json:"runtime_ids,omitempty"`
	// Percent buckets scoped operations by agent, then runtime, then
	// workspace. 100 also selects operations that carry no scope.
	Percent int `json:"percent,omitempty"`
}

// FCE2BSDKRolloutSnapshot is the rollout currently applied from Diamond.
type FCE2BSDKRolloutSnapshot struct {
	Rollout FCE2BSDKRollout
	// Present is false until a valid document has been applied and again
	// after the document is removed. Callers then use their fallback.
	Present    bool
	Generation uint64
	SHA256     string
}

// ParseFCE2BSDKRollout strictly validates a rollout document. Blank input is
// the empty rollout.
func ParseFCE2BSDKRollout(data []byte) (FCE2BSDKRollout, error) {
	var rollout FCE2BSDKRollout
	if len(bytes.TrimSpace(data)) == 0 {
		return rollout, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rollout); err != nil {
		return FCE2BSDKRollout{}, err
	}
	// More() misses a stray closing bracket; only the end of input is valid.
	if _, err := decoder.Token(); err != io.EOF {
		return FCE2BSDKRollout{}, errors.New("trailing data")
	}
	if rollout.Percent < 0 || rollout.Percent > 100 {
		return FCE2BSDKRollout{}, errors.New("percent must be between 0 and 100")
	}
	for _, list := range [][]string{rollout.WorkspaceIDs, rollout.AgentIDs, rollout.RuntimeIDs} {
		for i, id := range list {
			parsed, err := uuid.Parse(strings.TrimSpace(id))
			if err != nil || parsed == uuid.Nil {
				return FCE2BSDKRollout{}, fmt.Errorf("%q is not a UUID", id)
			}
			list[i] = parsed.String()
		}
	}
	return rollout, nil
}

// FCE2BSDKRollout returns the rollout snapshot applied from Diamond.
func (s *Service) FCE2BSDKRollout() FCE2BSDKRolloutSnapshot {
	if s == nil {
		return FCE2BSDKRolloutSnapshot{}
	}
	current := s.fcE2BSDKRollout.Load()
	if current == nil {
		return FCE2BSDKRolloutSnapshot{}
	}
	return cloneFCE2BSDKRolloutSnapshot(*current)
}

// ApplyFCE2BSDKRolloutJSON installs a new rollout document. Blank content
// means the document was removed. An invalid document leaves the current
// snapshot in place and returns it with the error.
func (s *Service) ApplyFCE2BSDKRolloutJSON(data []byte) (FCE2BSDKRolloutSnapshot, error) {
	if s == nil {
		return FCE2BSDKRolloutSnapshot{}, errors.New("runtime config service is nil")
	}
	s.fcE2BSDKRolloutApplyMu.Lock()
	defer s.fcE2BSDKRolloutApplyMu.Unlock()

	rollout, err := ParseFCE2BSDKRollout(data)
	if err != nil {
		return s.FCE2BSDKRollout(), err
	}
	generation := uint64(1)
	if previous := s.fcE2BSDKRollout.Load(); previous != nil {
		generation = previous.Generation + 1
	}
	sum := sha256.Sum256(data)
	next := &FCE2BSDKRolloutSnapshot{
		Rollout:    rollout,
		Present:    len(bytes.TrimSpace(data)) > 0,
		Generation: generation,
		SHA256:     hex.EncodeToString(sum[:]),
	}
	s.fcE2BSDKRollout.Store(next)
	return cloneFCE2BSDKRolloutSnapshot(*next), nil
}

func cloneFCE2BSDKRolloutSnapshot(snapshot FCE2BSDKRolloutSnapshot) FCE2BSDKRolloutSnapshot {
	snapshot.Rollout.WorkspaceIDs = append([]string(nil), snapshot.Rollout.WorkspaceIDs...)
	snapshot.Rollout.AgentIDs = append([]string(nil), snapshot.Rollout.AgentIDs...)
	snapshot.Rollout.RuntimeIDs = append([]string(nil), snapshot.Rollout.RuntimeIDs...)
	return snapshot
}

func logFCE2BSDKRolloutUpdate(logger *slog.Logger, message string, snapshot FCE2BSDKRolloutSnapshot) {
	if logger == nil {
		return
	}
	logger.Info(message,
		slog.String("data_id", FCE2BSDKRolloutDiamondDataID),
		slog.Uint64("generation", snapshot.Generation),
		slog.String("sha256", snapshot.SHA256),
		slog.Bool("present", snapshot.Present),
		slog.Bool("enabled", snapshot.Rollout.Enabled),
		slog.Int("workspaces", len(snapshot.Rollout.WorkspaceIDs)),
		slog.Int("agents", len(snapshot.Rollout.AgentIDs)),
		slog.Int("runtimes", len(snapshot.Rollout.RuntimeIDs)),
		slog.Int("percent", snapshot.Rollout.Percent),
	)
}

func applyFCE2BSDKRolloutUpdate(logger *slog.Logger, service *Service, content string, message string) {
	next, err := service.ApplyFCE2BSDKRolloutJSON([]byte(content))
	if err != nil {
		if logger != nil {
			current := service.FCE2BSDKRollout()
			logger.Error("FC/E2B SDK rollout rejected; retaining previous snapshot",
				slog.String("data_id", FCE2BSDKRolloutDiamondDataID),
				slog.Uint64("generation", current.Generation),
				slog.Bool("present", current.Present),
				slog.String("error", err.Error()),
			)
		}
		return
	}
	logFCE2BSDKRolloutUpdate(logger, message, next)
}
