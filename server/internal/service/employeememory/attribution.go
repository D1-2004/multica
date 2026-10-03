package employeememory

import (
	"sort"
	"strings"
	"time"
)

var attributionZone = time.FixedZone("Asia/Shanghai", 8*3600)

// SceneAttribution renders who said a captured statement and when, e.g.
// "张三 10-03 说" or "张三 10-03 说（候选）" for flush output. It returns ""
// for records without speaker attribution so callers keep their own phrase.
func SceneAttribution(rec LearningRecord) string {
	if rec.SpeakerRef == "" && rec.SpeakerName == "" {
		return ""
	}
	name := strings.Join(strings.Fields(rec.SpeakerName), " ")
	if name == "" {
		name = "成员"
	}
	out := name
	if !rec.SaidAt.IsZero() {
		out += " " + rec.SaidAt.In(attributionZone).Format("01-02")
	}
	out += " 说"
	if rec.CaptureOrigin == CaptureOriginFlush || rec.Source == LearningSourceSynthesis {
		out += "（候选）"
	}
	return out
}

// ConflictPeers maps each scene record to the other records in the input that
// share its type and key but were written by another author. Such records are
// conflicting candidates and must be shown together ("说法不一").
func ConflictPeers(records []LearningRecord) map[string][]string {
	groups := map[string][]LearningRecord{}
	for _, rec := range records {
		if rec.Scope != string(ScopeScene) || rec.ID == "" {
			continue
		}
		key := string(rec.Type) + "|" + rec.Key
		groups[key] = append(groups[key], rec)
	}
	out := map[string][]string{}
	for _, group := range groups {
		for _, rec := range group {
			for _, peer := range group {
				if peer.ID != rec.ID && peer.CreatedBy != rec.CreatedBy {
					out[rec.ID] = append(out[rec.ID], peer.ID)
				}
			}
		}
	}
	for id := range out {
		sort.Strings(out[id])
	}
	return out
}
