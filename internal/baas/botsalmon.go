package baas

import (
	"context"
	"errors"
	"math"
	"net/http"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/toyohr"
	"github.com/n-popescu/grid0plus/internal/ugcstore"
)

// Salmon Run over LAN. The bot asks for a job; GRID0+ answers with the
// current rotation's stage and the hazard level the players' rank calls for.
// A player reports the result, a mod accepts it, and only then does each
// player's rank move -- the same CoopUserAttribute.total_grade_point online
// jobs move, so LAN progress is what the game shows online.

const (
	minSalmonPlayers = 2
	maxSalmonPlayers = 4
	// unmoddedHazardMax is the highest hazard a stock game's LAN lobby can
	// set; above it the host needs the IPS patches.
	unmoddedHazardMax = 200
	// hazardCap is the game's own top hazard, reached at an average total
	// of 1665 (Eggsecutive VP 865).
	hazardCap     = 333
	gradeCapForHz = 1665
)

func (b *botAPI) salmonRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /bot/v1/salmon/jobs", b.withDiscordID(b.requestSalmonJob))
	mux.HandleFunc("GET /bot/v1/salmon/jobs/{id}", b.getSalmonJob)
	mux.HandleFunc("POST /bot/v1/salmon/jobs/{id}/report", b.withDiscordID(b.reportSalmonJob))
	mux.HandleFunc("PUT /bot/v1/salmon/jobs/{id}/message", b.setSalmonJobMessage)
	mux.HandleFunc("POST /bot/v1/salmon/jobs/{id}/accept", b.withDiscordID(b.acceptSalmonJob))
	mux.HandleFunc("POST /bot/v1/salmon/jobs/{id}/refuse", b.withDiscordID(b.refuseSalmonJob))
	mux.HandleFunc("GET /bot/v1/salmon/rank/{discordId}", b.salmonRank)
}

// salmonHazard is the hazard level, in percent, for players at these grade
// totals: the average total times 0.2%, rounded to LAN's 5% steps, at most
// 333%. Inkipedia gives the formula for four players; with two or three the
// average of the players present is the reading used -- a job short of
// players keeps its hazard and only lowers its quotas.
//
// TODO(capture-needed): the game's own hazard for two or three players.
// RE of the game's hazard computation would settle it; change salmonHazard.
func salmonHazard(totals []int64) int64 {
	var sum int64
	for _, t := range totals {
		sum += min(max(t, 0), gradeCapForHz)
	}
	pct := float64(sum) / float64(len(totals)) * 0.2
	return min(int64(math.Round(pct/5))*5, hazardCap)
}

// salmonDelta is the points one accepted job is worth: +20 for a clear (+10
// when played below the hazard the rank asked for), -20/-10/0 for a loss in
// wave 1/2/3, halved when a teammate disconnected.
func salmonDelta(j db.SalmonJob) int64 {
	r := j.Result
	if r.Cleared {
		if j.HalfWin {
			return 10
		}
		return 20
	}
	d := -20 + (r.Wave-1)*10
	if r.Disconnect {
		d /= 2
	}
	return d
}

type salmonPlayerView struct {
	DiscordUserID string `json:"discordUserId"`
	Title         string `json:"title"`
	Points        int64  `json:"points"`
	TitleAfter    string `json:"titleAfter,omitempty"`
	PointsAfter   *int64 `json:"pointsAfter,omitempty"`
}

type salmonJobView struct {
	ID              string             `json:"id"`
	RequestedBy     string             `json:"requestedBy"`
	Stage           string             `json:"stage"`
	ShiftID         string             `json:"shiftId"`
	Hazard          int64              `json:"hazard"`
	Played          int64              `json:"played"`
	Modded          bool               `json:"modded"`
	HalfWin         bool               `json:"halfWin"`
	Players         []salmonPlayerView `json:"players"`
	Status          string             `json:"status"`
	Result          *db.SalmonResult   `json:"result,omitempty"`
	Review          *db.SalmonDecision `json:"review,omitempty"`
	Delta           *int64             `json:"delta,omitempty"`
	ReviewChannelID string             `json:"reviewChannelId,omitempty"`
	ReviewMessageID string             `json:"reviewMessageId,omitempty"`
}

var coopTitles = []string{"Apprentice", "Part-Timer", "Go-Getter", "Overachiever", "Profreshional",
	"Profreshional +1", "Profreshional +2", "Profreshional +3", "Eggsecutive VP"}

// coopTitle splits a grade total into its title and that title's points.
func coopTitle(total int64) (string, int64) {
	t := min(max(total, 0)/100, 8)
	return coopTitles[t], total - t*100
}

func stageName(id int32) string {
	if s, ok := toyohr.CoopStageByID(id); ok {
		return s.Name
	}
	return "an unknown stage"
}

func viewSalmonJob(j db.SalmonJob) salmonJobView {
	v := salmonJobView{ID: j.ID, RequestedBy: j.RequestedBy, Stage: stageName(j.StageID), ShiftID: j.ShiftID,
		Hazard: j.Hazard, Played: j.Played, Modded: j.Modded, HalfWin: j.HalfWin, Status: j.Status,
		Result: j.Result, Review: j.Review, ReviewChannelID: j.ReviewChannelID, ReviewMessageID: j.ReviewMessageID}
	if j.Result != nil {
		d := salmonDelta(j)
		v.Delta = &d
	}
	for _, p := range j.Players {
		pv := salmonPlayerView{DiscordUserID: p.DiscordUserID}
		pv.Title, pv.Points = coopTitle(p.GradeBefore)
		if p.GradeAfter != nil {
			title, points := coopTitle(*p.GradeAfter)
			pv.TitleAfter, pv.PointsAfter = title, &points
		}
		v.Players = append(v.Players, pv)
	}
	return v
}

type salmonRequest struct {
	Players []string `json:"players"`
	Modded  bool     `json:"modded"`
}

func (b *botAPI) requestSalmonJob(w http.ResponseWriter, r *http.Request, requester string) {
	var body salmonRequest
	if err := decodeJSONBody(r, &body); err != nil {
		writeBotError(w, http.StatusBadRequest, "bad_request", "unreadable body")
		return
	}
	ctx := r.Context()
	if len(body.Players) < minSalmonPlayers || len(body.Players) > maxSalmonPlayers {
		writeBotError(w, http.StatusBadRequest, "bad_players", "a job has two to four players")
		return
	}
	seen := map[string]bool{}
	for _, id := range body.Players {
		if !discordIDPattern.MatchString(id) || seen[id] {
			writeBotError(w, http.StatusBadRequest, "bad_players", "a player can only be listed once")
			return
		}
		seen[id] = true
	}
	if !seen[requester] {
		writeBotError(w, http.StatusForbidden, "not_a_player", "only a player in the job can request it")
		return
	}
	now := b.s.now().UTC()
	shift, ok, err := toyohr.CurrentCoopShift(ctx, b.s.store, now)
	if err != nil {
		b.internal(w, "current coop shift", err)
		return
	}
	if !ok || shift.Kind != toyohr.CoopShiftNormal {
		writeBotError(w, http.StatusConflict, "no_rotation", "no regular Salmon Run rotation is open right now")
		return
	}
	// Every player must resolve to their own account, or nobody's rank moves.
	users, ok := b.salmonUsers(w, ctx, body.Players)
	if !ok {
		return
	}
	job := db.SalmonJob{RequestedBy: requester, ShiftID: toyohr.CoopShiftID(shift.Start), StageID: shift.StageID,
		Modded: body.Modded, Status: db.SalmonRequested, CreatedAt: now}
	totals := make([]int64, 0, len(users))
	for i, u := range users {
		// The rank the hazard is read from is this rotation's, reset first
		// if the player has not played in it yet.
		if _, err := toyohr.RollCoopRotation(ctx, b.s.store, b.tenant, u.ID, shift, now); err != nil {
			b.internal(w, "roll coop rotation", err)
			return
		}
		total, _, err := ugcstore.CoopGradePoint(ctx, b.s.store, b.tenant, u.ID)
		if err != nil {
			b.internal(w, "load coop grade", err)
			return
		}
		totals = append(totals, total)
		job.Players = append(job.Players, db.SalmonPlayer{UserID: u.ID, DiscordUserID: body.Players[i], GradeBefore: total})
	}
	job.Hazard = salmonHazard(totals)
	job.Played = job.Hazard
	if job.Hazard > unmoddedHazardMax && !body.Modded {
		job.Played, job.HalfWin = unmoddedHazardMax, true
	}
	if job.ID, err = newSubmissionID(); err != nil {
		b.internal(w, "job id", err)
		return
	}
	if err := b.s.store.CreateSalmonJob(ctx, job); err != nil {
		b.internal(w, "create salmon job", err)
		return
	}
	b.s.log.Info("botapi: Salmon Run LAN job requested", "job", job.ID, "by", requester,
		"players", len(job.Players), "hazard", job.Hazard, "played", job.Played)
	writeJSON(w, http.StatusCreated, viewSalmonJob(job))
}

// salmonUsers resolves Discord users to their main accounts, refusing the
// whole job if any is missing.
func (b *botAPI) salmonUsers(w http.ResponseWriter, ctx context.Context, ids []string) ([]db.User, bool) {
	var users []db.User
	var unregistered []string
	for _, id := range ids {
		u, err := b.s.store.GetUserByDiscordID(ctx, id)
		if errors.Is(err, db.ErrNotFound) || (err == nil && u.Deleted) {
			unregistered = append(unregistered, id)
			continue
		}
		if err != nil {
			b.internal(w, "load player", err)
			return nil, false
		}
		users = append(users, u)
	}
	if len(unregistered) > 0 {
		writeJSON(w, http.StatusConflict, playersError{Code: "players_not_registered",
			Message: "every player needs a GRID0+ account", DiscordUserIDs: unregistered})
		return nil, false
	}
	return users, true
}

func (b *botAPI) loadSalmonJob(w http.ResponseWriter, ctx context.Context, id string) (db.SalmonJob, bool) {
	j, err := b.s.store.GetSalmonJob(ctx, id)
	if errors.Is(err, db.ErrNotFound) {
		writeBotError(w, http.StatusNotFound, "no_job", "no such Salmon Run job")
		return j, false
	}
	if err != nil {
		b.internal(w, "load salmon job", err)
		return j, false
	}
	return j, true
}

func (b *botAPI) getSalmonJob(w http.ResponseWriter, r *http.Request) {
	if j, ok := b.loadSalmonJob(w, r.Context(), r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, viewSalmonJob(j))
	}
}

// salmonSettled answers a job whose status moved on under the caller.
func (b *botAPI) salmonSettled(w http.ResponseWriter, ctx context.Context, id string) {
	j, ok := b.loadSalmonJob(w, ctx, id)
	if !ok {
		return
	}
	writeJSON(w, http.StatusConflict, struct {
		botError
		Job salmonJobView `json:"job"`
	}{botError{Code: "already_" + j.Status, Message: "the job is " + j.Status}, viewSalmonJob(j)})
}

// stillInRotation refuses a job whose rotation is over: the reset has moved
// everyone's points since the hazard was set, so the result no longer fits.
func (b *botAPI) stillInRotation(w http.ResponseWriter, ctx context.Context, j db.SalmonJob) bool {
	shift, ok, err := toyohr.CurrentCoopShift(ctx, b.s.store, b.s.now().UTC())
	if err != nil {
		b.internal(w, "current coop shift", err)
		return false
	}
	if !ok || toyohr.CoopShiftID(shift.Start) != j.ShiftID {
		writeBotError(w, http.StatusConflict, "rotation_over", "the rotation this job was played in is over")
		return false
	}
	return true
}

type salmonReport struct {
	Cleared    bool  `json:"cleared"`
	Wave       int64 `json:"wave"`
	Disconnect bool  `json:"disconnect"`
}

func (b *botAPI) reportSalmonJob(w http.ResponseWriter, r *http.Request, reporter string) {
	var body salmonReport
	if err := decodeJSONBody(r, &body); err != nil {
		writeBotError(w, http.StatusBadRequest, "bad_request", "unreadable body")
		return
	}
	if body.Cleared {
		body.Wave, body.Disconnect = 0, false
	} else if body.Wave < 1 || body.Wave > 3 {
		writeBotError(w, http.StatusBadRequest, "bad_wave", "a lost job was lost in wave 1, 2 or 3")
		return
	}
	ctx := r.Context()
	j, ok := b.loadSalmonJob(w, ctx, r.PathValue("id"))
	if !ok {
		return
	}
	player := false
	for _, p := range j.Players {
		player = player || p.DiscordUserID == reporter
	}
	if !player {
		writeBotError(w, http.StatusForbidden, "not_a_player", "only a player in the job can report it")
		return
	}
	if j.Status != db.SalmonRequested {
		b.salmonSettled(w, ctx, j.ID)
		return
	}
	if !b.stillInRotation(w, ctx, j) {
		return
	}
	j.Status = db.SalmonReported
	j.Result = &db.SalmonResult{ReportedBy: reporter, Cleared: body.Cleared, Wave: body.Wave,
		Disconnect: body.Disconnect, At: b.s.now().UTC()}
	if err := b.s.store.UpdateSalmonJob(ctx, j, db.SalmonRequested); errors.Is(err, db.ErrConflict) {
		b.salmonSettled(w, ctx, j.ID)
		return
	} else if err != nil {
		b.internal(w, "report salmon job", err)
		return
	}
	writeJSON(w, http.StatusOK, viewSalmonJob(j))
}

func (b *botAPI) setSalmonJobMessage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChannelID string `json:"channelId"`
		MessageID string `json:"messageId"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeBotError(w, http.StatusBadRequest, "bad_request", "unreadable body")
		return
	}
	ctx := r.Context()
	j, ok := b.loadSalmonJob(w, ctx, r.PathValue("id"))
	if !ok {
		return
	}
	from := j.Status
	j.ReviewChannelID, j.ReviewMessageID = body.ChannelID, body.MessageID
	if err := b.s.store.UpdateSalmonJob(ctx, j, from); errors.Is(err, db.ErrConflict) {
		b.salmonSettled(w, ctx, j.ID)
		return
	} else if err != nil {
		b.internal(w, "set salmon job message", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (b *botAPI) acceptSalmonJob(w http.ResponseWriter, r *http.Request, reviewer string) {
	ctx := r.Context()
	j, ok := b.loadSalmonJob(w, ctx, r.PathValue("id"))
	if !ok {
		return
	}
	if j.Status != db.SalmonReported {
		b.salmonSettled(w, ctx, j.ID)
		return
	}
	if !b.stillInRotation(w, ctx, j) {
		return
	}
	// Credit before settling: the credit is once per job and player
	// (CreditLANCoopGrade), so two Accepts at once still move each rank
	// once, and a credit that fails leaves the job reported, to accept again.
	now := b.s.now().UTC()
	delta := salmonDelta(j)
	for i, p := range j.Players {
		_, after, err := ugcstore.CreditLANCoopGrade(ctx, b.s.store, b.tenant, j.ID, p.UserID, delta, now)
		if err != nil {
			b.internal(w, "credit LAN coop grade", err)
			return
		}
		j.Players[i].GradeAfter = &after
	}
	j.Status = db.SalmonAccepted
	j.Review = &db.SalmonDecision{By: reviewer, At: now}
	if err := b.s.store.UpdateSalmonJob(ctx, j, db.SalmonReported); errors.Is(err, db.ErrConflict) {
		b.salmonSettled(w, ctx, j.ID)
		return
	} else if err != nil {
		b.internal(w, "accept salmon job", err)
		return
	}
	b.s.log.Info("botapi: Salmon Run LAN job accepted", "job", j.ID, "by", reviewer, "delta", delta)
	writeJSON(w, http.StatusOK, viewSalmonJob(j))
}

func (b *botAPI) refuseSalmonJob(w http.ResponseWriter, r *http.Request, reviewer string) {
	var body struct {
		Reason string `json:"reason"`
	}
	_ = decodeJSONBody(r, &body)
	ctx := r.Context()
	j, ok := b.loadSalmonJob(w, ctx, r.PathValue("id"))
	if !ok {
		return
	}
	if j.Status != db.SalmonReported && j.Status != db.SalmonRequested {
		b.salmonSettled(w, ctx, j.ID)
		return
	}
	from := j.Status
	j.Status = db.SalmonRefused
	j.Review = &db.SalmonDecision{By: reviewer, Reason: body.Reason, At: b.s.now().UTC()}
	if err := b.s.store.UpdateSalmonJob(ctx, j, from); errors.Is(err, db.ErrConflict) {
		b.salmonSettled(w, ctx, j.ID)
		return
	} else if err != nil {
		b.internal(w, "refuse salmon job", err)
		return
	}
	writeJSON(w, http.StatusOK, viewSalmonJob(j))
}

type salmonBadge struct {
	Stage string `json:"stage"`
	Best  int64  `json:"best"`
	Badge string `json:"badge,omitempty"`
}

type salmonRankView struct {
	Title  string        `json:"title"`
	Points int64         `json:"points"`
	Stage  string        `json:"stage,omitempty"`
	Badges []salmonBadge `json:"badges"`
}

func badgeName(best int64) string {
	switch {
	case best >= 999:
		return "gold"
	case best >= 600:
		return "silver"
	case best >= 400:
		return "bronze"
	}
	return ""
}

func (b *botAPI) salmonRank(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	users, ok := b.salmonUsers(w, ctx, []string{r.PathValue("discordId")})
	if !ok {
		return
	}
	uid := users[0].ID
	now := b.s.now().UTC()
	v := salmonRankView{}
	// Shown as the game would show it in the open rotation: reset first.
	if shift, ok, err := toyohr.CurrentCoopShift(ctx, b.s.store, now); err != nil {
		b.internal(w, "current coop shift", err)
		return
	} else if ok && shift.Kind == toyohr.CoopShiftNormal {
		v.Stage = stageName(shift.StageID)
		if _, err := toyohr.RollCoopRotation(ctx, b.s.store, b.tenant, uid, shift, now); err != nil {
			b.internal(w, "roll coop rotation", err)
			return
		}
	}
	total, _, err := ugcstore.CoopGradePoint(ctx, b.s.store, b.tenant, uid)
	if err != nil {
		b.internal(w, "load coop grade", err)
		return
	}
	v.Title, v.Points = coopTitle(total)
	for _, s := range toyohr.CoopStages {
		if s.BigRun {
			continue
		}
		best, err := toyohr.CoopStageBest(ctx, b.s.store, b.tenant, uid, s.ID)
		if err != nil {
			b.internal(w, "load coop badges", err)
			return
		}
		v.Badges = append(v.Badges, salmonBadge{Stage: s.Name, Best: best, Badge: badgeName(best)})
	}
	writeJSON(w, http.StatusOK, v)
}
