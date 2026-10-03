package employeememory

import (
	"math"
	"sort"
)

// TextHit is one ranked plain text (by its index in the ranked slice) with
// its overlap evidence.
type TextHit struct {
	Index   int      `json:"index"`
	Score   float64  `json:"score"`
	Overlap int      `json:"overlap"`
	Matched []string `json:"matched"`
}

// RankTexts ranks plain texts (transcript lines, duty text) against the query
// with the same Chinese-aware units and IDF weighting as RankLearnings. Texts
// sharing fewer than RetrievalMinOverlap distinct units are dropped; ties keep
// the earlier text first. Pure and deterministic, no model.
func RankTexts(query string, texts []string, limit int) []TextHit {
	return RankTextsMinOverlap(query, texts, limit, RetrievalMinOverlap)
}

// RankTextsMinOverlap is RankTexts with an explicit minimum of distinct
// overlapping units (at least one).
func RankTextsMinOverlap(query string, texts []string, limit, minOverlap int) []TextHit {
	if limit <= 0 || len(texts) == 0 {
		return nil
	}
	if minOverlap < 1 {
		minOverlap = 1
	}
	qUnits, qTri := retrievalFeatures(query)
	// Question forms ("哪天", "是谁") are not topics: they never count as
	// overlap between a question and a plain text.
	for unit := range qUnits {
		if !topicalUnit(unit) {
			delete(qUnits, unit)
		}
	}
	if len(qUnits) < minOverlap {
		return nil
	}
	docUnits := make([]map[string]bool, len(texts))
	docTri := make([]map[string]bool, len(texts))
	df := map[string]int{}
	for i, text := range texts {
		docUnits[i], docTri[i] = retrievalFeatures(text)
		for f := range docUnits[i] {
			df[f]++
		}
		for f := range docTri[i] {
			df[f]++
		}
	}
	n := float64(len(texts))
	idf := func(f string) float64 { return math.Log(1 + n/float64(df[f])) }
	var hits []TextHit
	for i := range texts {
		hit := TextHit{Index: i}
		for f := range qUnits {
			if docUnits[i][f] {
				hit.Overlap++
				hit.Score += idf(f)
				hit.Matched = append(hit.Matched, f[2:])
			}
		}
		if hit.Overlap < minOverlap {
			continue
		}
		for f := range qTri {
			if docTri[i][f] {
				hit.Score += retrievalTrigramW * idf(f)
			}
		}
		sort.Strings(hit.Matched)
		hits = append(hits, hit)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Index < hits[j].Index
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// TextOverlap returns the distinct retrieval units query and text share,
// sorted, question forms included.
func TextOverlap(query, text string) []string {
	qUnits, _ := retrievalFeatures(query)
	tUnits, _ := retrievalFeatures(text)
	var out []string
	for f := range qUnits {
		if tUnits[f] {
			out = append(out, f[2:])
		}
	}
	sort.Strings(out)
	return out
}

// retrievalQuestionChars mark a unit as part of a question form ("哪天",
// "是谁", "版哪"), not a topic.
var retrievalQuestionChars = map[rune]bool{'哪': true, '谁': true, '吗': true, '么': true, '几': true, '啥': true, '怎': true, '呢': true, '吧': true}

// RetrievalTopicalUnits counts the query's overlap units that are not part
// of a question form. A short question ("发版哪天？") has a single topical
// unit, so requiring two shared units could never recall anything for it.
func RetrievalTopicalUnits(query string) int {
	units, _ := retrievalFeatures(query)
	n := 0
	for unit := range units {
		if topicalUnit(unit) {
			n++
		}
	}
	return n
}

func topicalUnit(unit string) bool {
	for _, r := range unit[2:] {
		if retrievalQuestionChars[r] {
			return false
		}
	}
	return true
}
