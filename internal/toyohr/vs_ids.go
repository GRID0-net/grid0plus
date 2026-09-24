package toyohr

// The real stage ids Turf War and Anarchy's rotation content uses.
//
// Unlike the *shape* of nn.npln.toyohr.v1.VsSchedule (an operator-authored
// mechanism -- see schedule.go's own package comment), these numeric ids are
// the game's *content*: which integer means "Scorch Gorge," which means
// "Hagglefish Market." Getting one of these wrong means the console asks its
// own local asset tables for something that is not there -- the same,
// materially smaller failure class CoopStages' own doc comment describes,
// not an invented FRES container.
//
// Every id and name below was read directly out of the game's own shipped
// parameter tables, not written down from memory or a wiki's prose: cloned
// github.com/Leanny/splat3 (a public, actively-maintained community dump of
// Splatoon 3's own data files, not a packet capture) and cross-referenced
// two of its tables against each other, the same method CoopStages uses --
//
//	data/mush/930/VersusSceneInfo.json   Id, __RowId, IsDisplayInStageList
//	data/language/USen.json              CommonMsg/VS/VSStageName, keyed by
//	                                      __RowId with "Vss_" stripped and
//	                                      trailing digits removed
//
// All 24 stages IsDisplayInStageList marks true are listed, Turf War and
// Anarchy alike -- nothing here distinguishes which stages a given mode
// rotates through on a real server, only that each id and name is real.
// Two entries are worth naming explicitly because their codename reads
// nothing like the stage: "AutoWalk" is Lemuria Hub (the newest stage at
// the time this table was built, Season 8), and "Manbou" -- present in the
// language file but IsDisplayInStageList false in this data pull, so not
// listed below -- is Grand Splatlands Bowl, already known from CoopStages
// as a Big Run stage; Versus and Big Run share that one map.
//
// What this table is NOT: a claim that every one of these ids is
// *currently* live in Nintendo's own rotation, or a source for BankaraRule
// wire values (Splat Zones/Tower Control/Rainmaker/Clam Blitz as the int32
// Schedule.proto's own `rule` field carries) -- that remains genuinely
// undocumented. Neither this data dump (game *content*, not NPLN wire
// protocol) nor kinnay/NPLN-Protocols' own schedule.proto (`int32 rule = 1`,
// no enum) gives real numbers for it. TODO(capture-needed).
type VsStage struct {
	// ID is VersusSceneInfo's own Id -- what VsSchedule's Stages field
	// carries.
	ID int32
	// Name is here for the operator's benefit only; nothing sends it to the
	// console, which resolves ID against its own local strings.
	Name string
}

// VsStages is every stage VersusSceneInfo lists with IsDisplayInStageList
// true, in ascending id order.
var VsStages = []VsStage{
	{ID: 1, Name: "Scorch Gorge"},
	{ID: 2, Name: "Eeltail Alley"},
	{ID: 3, Name: "Hagglefish Market"},
	{ID: 4, Name: "Undertow Spillway"},
	{ID: 5, Name: "Um'ami Ruins"},
	{ID: 6, Name: "Mincemeat Metalworks"},
	{ID: 7, Name: "Brinewater Springs"},
	{ID: 8, Name: "Barnacle & Dime"},
	{ID: 9, Name: "Flounder Heights"},
	{ID: 10, Name: "Hammerhead Bridge"},
	{ID: 11, Name: "Museum d'Alfonsino"},
	{ID: 12, Name: "Mahi-Mahi Resort"},
	{ID: 13, Name: "Inkblot Art Academy"},
	{ID: 14, Name: "Sturgeon Shipyard"},
	{ID: 15, Name: "MakoMart"},
	{ID: 16, Name: "Wahoo World"},
	{ID: 17, Name: "Humpback Pump Track"},
	{ID: 18, Name: "Manta Maria"},
	{ID: 19, Name: "Crableg Capital"},
	{ID: 20, Name: "Shipshape Cargo Co."},
	{ID: 21, Name: "Robo ROM-en"},
	{ID: 22, Name: "Bluefin Depot"},
	{ID: 23, Name: "Marlin Airport"},
	{ID: 24, Name: "Lemuria Hub"},
}

// VsStageByID looks up a real stage, or reports ok=false for anything not
// in VsStages.
func VsStageByID(id int32) (stage VsStage, ok bool) {
	for _, s := range VsStages {
		if s.ID == id {
			return s, true
		}
	}
	return VsStage{}, false
}
