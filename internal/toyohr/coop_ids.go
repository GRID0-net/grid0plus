package toyohr

// The real stage, weapon and boss ids Salmon Run's CoopSchedule content uses.
//
// Unlike the *shape* of nn.npln.toyohr.v1.CoopSchedule (known from its own
// decompiled proto -- see coop_schedule.go), these numeric ids and codenames
// are the game's *content*: which integer means "Wahoo World", which means
// "Grizzco Blaster". Getting the shape wrong breaks the wire message; getting
// one of these wrong means the console asks its own local asset tables for
// something that is not there, which is a materially smaller failure -- CLAUDE.md
// rule 6 is why this file exists rather than leaving these fields to be typed
// in freehand, but a wrong id here is not the same risk class as an invented
// FRES container (internal/bcat/README.md).
//
// Every id and codename below was read directly out of the game's own shipped
// parameter tables, not written down from memory or a wiki's prose: cloned
// github.com/Leanny/splat3 (a public, actively-maintained community dump of
// Splatoon 3's own data files, not a packet capture) and cross-referenced
// three of its tables against each other --
//
//	data/mush/930/WeaponInfoMain.json    Id, __RowId (e.g. "Blaster_Bear_Coop")
//	data/mush/930/CoopSceneInfo.json     Id, __RowId, IsBigRun
//	data/mush/930/CoopEnemyInfo.json     Type, Category
//	data/language/USen.json              __RowId -> the display string
//
// The stage list corroborates itself: every entry CoopSceneInfo marks
// IsBigRun matches Inkipedia's independently-maintained "List of Big Runs in
// Salmon Run Next Wave" (Wahoo World, Inkblot Art Academy, Undertow Spillway,
// Um'ami Ruins, Barnacle & Dime, Eeltail Alley, Grand Splatlands Bowl) --
// two independent sources agreeing is as much confidence as this project
// gets without a console. The weapon list corroborates itself a different
// way: every "_Coop" row's Id is its Versus counterpart's Id plus 20000,
// exactly matching the "WmnG_Blaster_Bear_Coop..." naming this project has
// already seen in the game's own asset paths (internal/bcat/README.md).
//
// What this table is NOT: a claim that every one of these ids is *currently*
// live in Nintendo's own rotation, or that this is every id that exists --
// only that each one is real. reward_gear_id has no table here at all (no
// gear-id dump was pulled into this pass); CoopShift.Validate does not check
// it, and RewardGearId is the operator's to set correctly or leave at 0.

// CoopStage is one Salmon Run map, real dimensions the console's own asset
// tables key on.
type CoopStage struct {
	// ID is CoopSceneInfo's own Id -- what CoopSchedule_Normal.Stage and
	// CoopSchedule_BigRun.Stage carry.
	ID int32
	// Name is here for the operator's benefit only; nothing sends it to the
	// console, which resolves ID against its own local strings.
	Name string
	// BigRun reports whether this stage is one Big Run runs on. A "normal"
	// shift must not use a BigRun stage and a "big_run" shift must
	// (CoopShift.Validate) -- Big Run and the ordinary rotation have never
	// shared a stage in the real data this was checked against.
	BigRun bool
}

// CoopStages is every Salmon Run stage CoopSceneInfo lists: seven ordinary
// rotation stages, seven Big Run stages.
var CoopStages = []CoopStage{
	{ID: 1, Name: "Spawning Grounds"},
	{ID: 2, Name: "Sockeye Station"},
	{ID: 4, Name: "Salmonid Smokeyard"},
	{ID: 6, Name: "Marooner's Bay"},
	{ID: 7, Name: "Gone Fission Hydroplant"},
	{ID: 8, Name: "Jammin' Salmon Junction"},
	{ID: 9, Name: "Bonerattle Arena"},

	{ID: 100, Name: "Wahoo World", BigRun: true},
	{ID: 102, Name: "Inkblot Art Academy", BigRun: true},
	{ID: 103, Name: "Undertow Spillway", BigRun: true},
	{ID: 104, Name: "Um'ami Ruins", BigRun: true},
	{ID: 105, Name: "Barnacle & Dime", BigRun: true},
	{ID: 106, Name: "Eeltail Alley", BigRun: true},
	{ID: 107, Name: "Grand Splatlands Bowl", BigRun: true},
}

// CoopStageByID looks up a real stage, or reports ok=false for anything not
// in CoopStages.
func CoopStageByID(id int32) (stage CoopStage, ok bool) {
	for _, s := range CoopStages {
		if s.ID == id {
			return s, true
		}
	}
	return CoopStage{}, false
}

// CoopWeapon is one weapon Salmon Run's shift rotation can offer.
type CoopWeapon struct {
	// ID is WeaponInfoMain's own Id for that weapon's "_Coop" row -- what
	// CoopSchedule_Normal/BigRun.MainWeapons and .KumaWeapon carry. Always
	// the Versus weapon's own id plus 20000.
	ID   int64
	Name string
}

// CoopWeapons is every ordinary weapon Salmon Run can put in a shift's four
// main_weapons -- every weapon in the game that has a "_Coop" row, which per
// WeaponInfoMain is all of them. CoopKumaWeapons, not this list, is where the
// Grizzco/"_Bear_Coop" weapons live -- kuma_weapon is a separate slot the
// console does not draw from main_weapons.
var CoopWeapons = []CoopWeapon{
	{ID: 20000, Name: "Sploosh-o-matic"},
	{ID: 20010, Name: "Splattershot Jr."},
	{ID: 20020, Name: "Splash-o-matic"},
	{ID: 20030, Name: "Aerospray MG"},
	{ID: 20040, Name: "Splattershot"},
	{ID: 20050, Name: ".52 Gal"},
	{ID: 20060, Name: "N-ZAP '85"},
	{ID: 20070, Name: "Splattershot Pro"},
	{ID: 20080, Name: ".96 Gal"},
	{ID: 20090, Name: "Jet Squelcher"},
	{ID: 20100, Name: "Splattershot Nova"},
	{ID: 20200, Name: "Luna Blaster"},
	{ID: 20210, Name: "Blaster"},
	{ID: 20220, Name: "Range Blaster"},
	{ID: 20230, Name: "Clash Blaster"},
	{ID: 20240, Name: "Rapid Blaster"},
	{ID: 20250, Name: "Rapid Blaster Pro"},
	{ID: 20260, Name: "S-BLAST '92"},
	{ID: 20300, Name: "L-3 Nozzlenose"},
	{ID: 20310, Name: "H-3 Nozzlenose"},
	{ID: 20400, Name: "Squeezer"},
	{ID: 21000, Name: "Carbon Roller"},
	{ID: 21010, Name: "Splat Roller"},
	{ID: 21020, Name: "Dynamo Roller"},
	{ID: 21030, Name: "Flingza Roller"},
	{ID: 21040, Name: "Big Swig Roller"},
	{ID: 21100, Name: "Inkbrush"},
	{ID: 21110, Name: "Octobrush"},
	{ID: 21120, Name: "Painbrush"},
	{ID: 22000, Name: "Classic Squiffer"},
	{ID: 22010, Name: "Splat Charger"},
	{ID: 22030, Name: "E-liter 4K"},
	{ID: 22050, Name: "Bamboozler 14 Mk I"},
	{ID: 22060, Name: "Goo Tuber"},
	{ID: 22070, Name: "Snipewriter 5H"},
	{ID: 23000, Name: "Slosher"},
	{ID: 23010, Name: "Tri-Slosher"},
	{ID: 23020, Name: "Sloshing Machine"},
	{ID: 23030, Name: "Bloblobber"},
	{ID: 23040, Name: "Explosher"},
	{ID: 23050, Name: "Dread Wringer"},
	{ID: 24000, Name: "Mini Splatling"},
	{ID: 24010, Name: "Heavy Splatling"},
	{ID: 24020, Name: "Hydra Splatling"},
	{ID: 24030, Name: "Ballpoint Splatling"},
	{ID: 24040, Name: "Nautilus 47"},
	{ID: 24050, Name: "Heavy Edit Splatling"},
	{ID: 25000, Name: "Dapple Dualies"},
	{ID: 25010, Name: "Splat Dualies"},
	{ID: 25020, Name: "Glooga Dualies"},
	{ID: 25030, Name: "Dualie Squelchers"},
	{ID: 25040, Name: "Dark Tetra Dualies"},
	{ID: 25050, Name: "Douser Dualies FF"},
	{ID: 26000, Name: "Splat Brella"},
	{ID: 26010, Name: "Tenta Brella"},
	{ID: 26020, Name: "Undercover Brella"},
	{ID: 26030, Name: "Recycled Brella 24 Mk I"},
	{ID: 27010, Name: "Tri-Stringer"},
	{ID: 27020, Name: "REEF-LUX 450"},
	{ID: 27030, Name: "Wellstring V"},
	{ID: 28000, Name: "Splatana Stamper"},
	{ID: 28010, Name: "Splatana Wiper"},
	{ID: 28020, Name: "Mint Decavitator"},
}

// CoopKumaWeapons is the Grizzco-branded weapon a shift's boss wave hands
// every player -- kuma_weapon, never one of main_weapons's four.
var CoopKumaWeapons = []CoopWeapon{
	{ID: 20900, Name: "Grizzco Blaster"},
	{ID: 21900, Name: "Grizzco Roller"},
	{ID: 22900, Name: "Grizzco Charger"},
	{ID: 23900, Name: "Grizzco Slosher"},
	{ID: 25900, Name: "Grizzco Dualies"},
	{ID: 26900, Name: "Grizzco Brella"},
	{ID: 27900, Name: "Grizzco Stringer"},
	{ID: 28900, Name: "Grizzco Splatana"},
}

// Random main-weapon slots. A shift's main_weapons can hold these instead of
// a weapon id, and the game deals each player a weapon when the job starts.
// -1 is the green "?" (a capture of Nextendo's working server carries it as
// ["20400","25000","24020","-1"], with that shift's KumaWeapons attribute
// listing all eight Grizzco weapons); -2 is the gold "?", Grizzco weapons only.
const (
	CoopRandomWeapon        int64 = -1
	CoopRandomGrizzcoWeapon int64 = -2
)

// coopRandomSlot reports whether id is one of the random slot values.
func coopRandomSlot(id int64) bool {
	return id == CoopRandomWeapon || id == CoopRandomGrizzcoWeapon
}

// CoopWeaponByID looks up a real main-weapon, or reports ok=false.
func CoopWeaponByID(id int64) (weapon CoopWeapon, ok bool) {
	for _, w := range CoopWeapons {
		if w.ID == id {
			return w, true
		}
	}
	return CoopWeapon{}, false
}

// CoopKumaWeaponByID looks up a real Grizzco weapon, or reports ok=false.
func CoopKumaWeaponByID(id int64) (weapon CoopWeapon, ok bool) {
	for _, w := range CoopKumaWeapons {
		if w.ID == id {
			return w, true
		}
	}
	return CoopWeapon{}, false
}

// CoopBosses is every king salmonid CoopEnemyInfo lists under Category
// "Boss" -- the codenames CoopSchedule_Normal/BigRun.Boss carries for a
// shift's Xtrawave. Real names, in case they help: SakeJaw is Cohozuna,
// SakeRope is Horrorboros, SakelienGiant is Megalodontia -- but the console
// reads the codename, not a display name this project would have to invent.
var CoopBosses = []string{"SakeJaw", "SakeRope", "SakelienGiant"}

// CoopBossValid reports whether boss is empty (no Xtrawave boss scheduled) or
// one of CoopBosses.
func CoopBossValid(boss string) bool {
	if boss == "" {
		return true
	}
	for _, b := range CoopBosses {
		if b == boss {
			return true
		}
	}
	return false
}
