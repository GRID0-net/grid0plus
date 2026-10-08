package ugcstore

import (
	commonpb "github.com/n-popescu/grid0plus/proto/common"
	"sort"
)

// One player's report can arrive after other players have already reported.
// History is a match-wide document: retain the accumulated verdict's entries,
// with the current report as a fallback when no verdict exists yet. Array order
// must stay stable when the same match is rebuilt after a retry.
func resultPeople(report, verdict *commonpb.MapValue) (map[string]*commonpb.Value, []string) {
	people := map[string]*commonpb.Value{}
	for key, value := range report.GetFields()["personal_result"].GetMapValue().GetFields() {
		people[key] = value
	}
	for key, value := range verdict.GetFields()["personal_result"].GetMapValue().GetFields() {
		people[key] = value
	}
	keys := make([]string, 0, len(people))
	for key := range people {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return people, keys
}
