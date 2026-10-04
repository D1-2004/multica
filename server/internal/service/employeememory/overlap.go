package employeememory

// SharedRetrievalUnits counts the distinct retrieval overlap units (ASCII
// words of three or more characters, CJK bigrams) that a shares with b, and
// how many units a has. It is the same deterministic unit set RankLearnings
// scores with, exported for Host grounding checks (scene digest writer).
func SharedRetrievalUnits(a, b string) (shared, total int) {
	unitsA, _ := retrievalFeatures(a)
	unitsB, _ := retrievalFeatures(b)
	for unit := range unitsA {
		if unitsB[unit] {
			shared++
		}
	}
	return shared, len(unitsA)
}
