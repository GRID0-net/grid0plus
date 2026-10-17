package baas

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/presencebus"
	"github.com/n-popescu/grid0plus/internal/toyohr"
)

// The Splatfest side of the bot API: the vote post a mod creates on Discord,
// the LAN games players submit for a mod to review, and the stream that tells
// the bot the moment somebody picks a team in game.
//
// Every bot fest is an ordinary fest row, so votes, battles and results are
// the console's own tables and toyohr's own scoring rules. A Discord-only fest
// is a row that is never enabled, which every console-facing path skips.
//
// Who may create a fest or review a game is the bot's check (configured mod
// roles); this API trusts the bot token for that, as it does for everything.
// Who a player is stays fail-closed here: always their main account, never a
// fallback.

const (
	// lanBattlePrefix marks a battle that came from an accepted LAN
	// submission, so averages and personal records can tell them apart.
	lanBattlePrefix = "lan-"
)

// lanBattleModes is each LAN game kind's battle mode. Each contains the word
// toyohr's category for it matches -- "regular" (Open), "challenge" (Pro),
// "tricolor" -- so an accepted LAN game counts the same as an online one.
var lanBattleModes = map[string]string{
	db.SubmissionOpen:     "lan_regular",
	db.SubmissionPro:      "lan_challenge",
	db.SubmissionTricolor: "lan_tricolor",
}

const (
	// Used for a LAN player's paint when nobody gave it, no online battle has
	// been played to average, and the mod set no default of their own.
	fallbackWinPaint  = 2000
	fallbackLossPaint = 900

	maxPaint    = 9999
	maxTeamSize = 4
	// In a Tricolor battle the halftime leader defends with four players
	// against two from each other team.
	tricolorAttackers = 2
	duplicateWindow   = 30 * time.Minute
)

func (b *botAPI) festRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /bot/v1/fest-events", b.festEvents)
	mux.HandleFunc("GET /bot/v1/fests", b.listBotFests)
	mux.HandleFunc("POST /bot/v1/fests", b.withDiscordID(b.createBotFest))
	mux.HandleFunc("GET /bot/v1/fests/{id}", b.getBotFest)
	mux.HandleFunc("PATCH /bot/v1/fests/{id}", b.editBotFest)
	mux.HandleFunc("DELETE /bot/v1/fests/{id}", b.deleteBotFest)
	mux.HandleFunc("POST /bot/v1/fests/{id}/posts/{kind}", b.claimBotFestPost)
	mux.HandleFunc("GET /bot/v1/fests/{id}/standings", b.festStandings)
	mux.HandleFunc("GET /bot/v1/fests/{id}/entries", b.festEntries)
	mux.HandleFunc("POST /bot/v1/fests/{id}/entry", b.withUser(b.enterFest))
	mux.HandleFunc("GET /bot/v1/fests/{id}/me", b.withUser(b.festRecord))
	mux.HandleFunc("POST /bot/v1/fests/{id}/submissions", b.withDiscordID(b.submitLANGame))
	mux.HandleFunc("GET /bot/v1/submissions/{id}", b.getSubmission)
	mux.HandleFunc("PUT /bot/v1/submissions/{id}/message", b.setSubmissionMessage)
	mux.HandleFunc("POST /bot/v1/submissions/{id}/flag", b.withDiscordID(b.flagSubmission))
	mux.HandleFunc("POST /bot/v1/submissions/{id}/accept", b.withDiscordID(b.acceptSubmission))
	mux.HandleFunc("POST /bot/v1/submissions/{id}/refuse", b.withDiscordID(b.refuseSubmission))
}

// ---- shapes -----------------------------------------------------------------

type botFestTimetable struct {
	Open  time.Time `json:"open"`
	Start time.Time `json:"start"`
	Mid   time.Time `json:"mid"`
	End   time.Time `json:"end"`
	Close time.Time `json:"close"`
}

type botFestView struct {
	FestID           string               `json:"festId"`
	DiscordOnly      bool                 `json:"discordOnly"`
	Enabled          bool                 `json:"enabled"`
	Title            string               `json:"title"`
	Description      string               `json:"description"`
	Teams            []db.BotFestTeam     `json:"teams"`
	ChannelID        string               `json:"channelId"`
	MessageID        string               `json:"messageId"`
	DefaultWinPaint  int64                `json:"defaultWinPaint"`
	DefaultLossPaint int64                `json:"defaultLossPaint"`
	Posted           map[string]time.Time `json:"posted"`
	CreatedBy        string               `json:"createdBy"`
	Phase            string               `json:"phase"`
	Timetable        botFestTimetable     `json:"timetable"`
}

func (b *botAPI) festView(f db.BotFest, row db.Fest) botFestView {
	tf := toyohr.Fest{Start: row.StartAt, End: row.EndAt}
	tt := tf.Timetable()
	return botFestView{
		FestID: f.FestID, DiscordOnly: f.DiscordOnly, Enabled: row.Enabled,
		Title: f.Title, Description: f.Description, Teams: f.Teams,
		ChannelID: f.ChannelID, MessageID: f.MessageID,
		DefaultWinPaint: f.DefaultWinPaint, DefaultLossPaint: f.DefaultLossPaint,
		Posted: f.Posted, CreatedBy: f.CreatedBy,
		Phase:     tf.PhaseAt(b.s.now()).String(),
		Timetable: botFestTimetable{Open: tt.Open, Start: tt.Start, Mid: tt.Mid, End: tt.End, Close: tt.Close},
	}
}

type submissionView struct {
	ID              string                    `json:"id"`
	FestID          string                    `json:"festId"`
	SubmittedBy     string                    `json:"submittedBy"`
	Kind            string                    `json:"kind"`
	WinningTeam     string                    `json:"winningTeam"`
	LosingTeam      string                    `json:"losingTeam,omitempty"`
	Players         []db.FestSubmissionPlayer `json:"players"`
	ScreenshotURL   string                    `json:"screenshotUrl,omitempty"`
	Status          string                    `json:"status"`
	Reason          string                    `json:"reason,omitempty"`
	ReviewedBy      string                    `json:"reviewedBy,omitempty"`
	ReviewedAt      *time.Time                `json:"reviewedAt,omitempty"`
	Flags           []db.FestSubmissionFlag   `json:"flags"`
	ReviewChannelID string                    `json:"reviewChannelId,omitempty"`
	ReviewMessageID string                    `json:"reviewMessageId,omitempty"`
	CreatedAt       time.Time                 `json:"createdAt"`
	// PossibleDuplicateOf names earlier submissions with the same players
	// shortly before, for the mod to compare. Only on a new submission.
	PossibleDuplicateOf []string `json:"possibleDuplicateOf,omitempty"`
}

func viewSubmission(s db.FestSubmission) submissionView {
	v := submissionView{
		ID: s.ID, FestID: s.FestID, SubmittedBy: s.SubmittedBy,
		Kind: s.Kind, WinningTeam: s.WinningTeam, LosingTeam: s.LosingTeam, Players: s.Players,
		ScreenshotURL: s.ScreenshotURL, Status: s.Status, Reason: s.Reason, ReviewedBy: s.ReviewedBy,
		Flags: s.Flags, ReviewChannelID: s.ReviewChannelID, ReviewMessageID: s.ReviewMessageID,
		CreatedAt: s.CreatedAt,
	}
	if v.Flags == nil {
		v.Flags = []db.FestSubmissionFlag{}
	}
	if !s.ReviewedAt.IsZero() {
		at := s.ReviewedAt
		v.ReviewedAt = &at
	}
	return v
}

// playersError names the Discord accounts a submission failed on, so the bot
// can mention them rather than say "someone".
type playersError struct {
	Code           string   `json:"code"`
	Message        string   `json:"message"`
	DiscordUserIDs []string `json:"discordUserIds"`
}

// ---- loading ----------------------------------------------------------------

// loadBotFest reads a bot fest and its fest row, answering the error itself.
func (b *botAPI) loadBotFest(w http.ResponseWriter, ctx context.Context, id string) (db.BotFest, db.Fest, bool) {
	f, err := b.s.store.GetBotFest(ctx, id)
	if errors.Is(err, db.ErrNotFound) {
		writeBotError(w, http.StatusNotFound, "no_such_fest", "no fest by that id on Discord")
		return db.BotFest{}, db.Fest{}, false
	}
	if err != nil {
		b.internal(w, "load bot fest", err)
		return db.BotFest{}, db.Fest{}, false
	}
	row, err := b.s.store.GetFest(ctx, id)
	if err != nil {
		b.internal(w, "load fest", err)
		return db.BotFest{}, db.Fest{}, false
	}
	return f, row, true
}

func festPhase(row db.Fest, now time.Time) toyohr.Phase {
	return toyohr.Fest{Start: row.StartAt, End: row.EndAt}.PhaseAt(now)
}

// ---- fests --------------------------------------------------------------------

func (b *botAPI) listBotFests(w http.ResponseWriter, r *http.Request) {
	fests, err := b.s.store.ListBotFests(r.Context())
	if err != nil {
		b.internal(w, "list bot fests", err)
		return
	}
	out := make([]botFestView, 0, len(fests))
	for _, f := range fests {
		row, err := b.s.store.GetFest(r.Context(), f.FestID)
		if err != nil {
			b.internal(w, "load fest", err)
			return
		}
		out = append(out, b.festView(f, row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"fests": out})
}

func (b *botAPI) getBotFest(w http.ResponseWriter, r *http.Request) {
	f, row, ok := b.loadBotFest(w, r.Context(), r.PathValue("id"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, b.festView(f, row))
}

type createFestRequest struct {
	// FestID links the vote post to an in-game fest. Empty makes a
	// Discord-only fest.
	FestID      string `json:"festId"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// StartAt and EndAt are a Discord-only fest's play window. A linked
	// fest's come from the fest itself.
	StartAt          time.Time        `json:"startAt"`
	EndAt            time.Time        `json:"endAt"`
	Teams            []db.BotFestTeam `json:"teams"`
	ChannelID        string           `json:"channelId"`
	DefaultWinPaint  int64            `json:"defaultWinPaint"`
	DefaultLossPaint int64            `json:"defaultLossPaint"`
}

// protocolTeams are the names votes and battles are stored under, in the
// order the game lists the teams.
var protocolTeams = []string{"Alpha", "Bravo", "Charlie"}

func (b *botAPI) createBotFest(w http.ResponseWriter, r *http.Request, creator string) {
	var body createFestRequest
	if err := decodeJSONBody(r, &body); err != nil {
		writeBotError(w, http.StatusBadRequest, "bad_request", "unreadable body")
		return
	}
	// One fest at a time on Discord: a fest stays until a mod deletes it,
	// results included, and two at once would split the votes, roles and
	// announcements between two posts.
	existing, err := b.s.store.ListBotFests(r.Context())
	if err != nil {
		b.internal(w, "list fests", err)
		return
	}
	if len(existing) > 0 {
		writeBotError(w, http.StatusConflict, "fest_exists",
			"fest "+existing[0].FestID+" ("+existing[0].Title+") is still up; delete it with /fest cancel first")
		return
	}
	body.Title, body.Description = strings.TrimSpace(body.Title), strings.TrimSpace(body.Description)
	if body.Title == "" || len(body.Title) > 200 || body.Description == "" || len(body.Description) > 3500 {
		writeBotError(w, http.StatusBadRequest, "bad_title",
			"a fest needs a title (200 characters at most) and its announcement text (3500 at most)")
		return
	}
	linked := strings.TrimSpace(body.FestID) != ""
	// Three, always: Alpha, Bravo and Charlie, as every Splatoon 3 fest has,
	// whether or not it is played in game.
	if len(body.Teams) != len(protocolTeams) {
		writeBotError(w, http.StatusBadRequest, "bad_teams", "a fest has three teams: Alpha, Bravo and Charlie")
		return
	}
	seen := map[string]bool{}
	for i := range body.Teams {
		t := &body.Teams[i]
		t.Team = protocolTeams[i]
		t.Name, t.Emoji = strings.TrimSpace(t.Name), strings.TrimSpace(t.Emoji)
		if t.Name == "" || t.Emoji == "" || !discordIDPattern.MatchString(t.RoleID) ||
			seen["e"+t.Emoji] || seen["r"+t.RoleID] {
			writeBotError(w, http.StatusBadRequest, "bad_teams",
				"every team needs a name, its own emoji and its own role")
			return
		}
		seen["e"+t.Emoji], seen["r"+t.RoleID] = true, true
	}
	if body.DefaultWinPaint < 0 || body.DefaultWinPaint > maxPaint ||
		body.DefaultLossPaint < 0 || body.DefaultLossPaint > maxPaint {
		writeBotError(w, http.StatusBadRequest, "bad_paint", fmt.Sprintf("paint is 0 to %d", maxPaint))
		return
	}
	if body.DefaultWinPaint == 0 {
		body.DefaultWinPaint = fallbackWinPaint
	}
	if body.DefaultLossPaint == 0 {
		body.DefaultLossPaint = fallbackLossPaint
	}

	ctx := r.Context()
	now := b.s.now()
	var row db.Fest
	if linked {
		id := strings.ToUpper(strings.TrimSpace(body.FestID))
		var err error
		row, err = b.s.store.GetFest(ctx, id)
		if errors.Is(err, db.ErrNotFound) {
			writeBotError(w, http.StatusNotFound, "no_such_fest", "GRID0+ has no fest "+id)
			return
		}
		if err != nil {
			b.internal(w, "load fest", err)
			return
		}
		if row.Resource == "" {
			writeBotError(w, http.StatusConflict, "not_in_game",
				id+" is a Discord-only fest, not one the game can show")
			return
		}
	} else {
		start := toyohr.AlignToSlot(body.StartAt)
		standard := toyohr.Fest{Start: start}.Timetable()
		end := standard.End
		if !body.EndAt.IsZero() {
			end = toyohr.AlignToSlot(body.EndAt)
		}
		if body.StartAt.IsZero() || !end.After(standard.Mid) {
			writeBotError(w, http.StatusBadRequest, "bad_times",
				"a fest needs a start, and its end must be more than 24 hours after it: the halftime report is at 24 hours, as in game")
			return
		}
		if !end.After(now) {
			writeBotError(w, http.StatusBadRequest, "bad_times", "that fest would already be over")
			return
		}
		id, err := b.newDiscordFestID(ctx)
		if err != nil {
			b.internal(w, "choose a fest id", err)
			return
		}
		// Never enabled, and no pack: every console-facing path skips a
		// disabled fest, so the game never sees it.
		row = db.Fest{ID: id, StartAt: start, EndAt: end, Enabled: false}
		if err := b.s.store.UpsertFest(ctx, row); err != nil {
			b.internal(w, "create fest", err)
			return
		}
		if row, err = b.s.store.GetFest(ctx, id); err != nil {
			b.internal(w, "load fest", err)
			return
		}
	}
	if _, err := b.s.store.GetBotFest(ctx, row.ID); err == nil {
		writeBotError(w, http.StatusConflict, "already_posted", "that fest already has a vote post")
		return
	} else if !errors.Is(err, db.ErrNotFound) {
		b.internal(w, "load bot fest", err)
		return
	}
	f := db.BotFest{
		FestID: row.ID, DiscordOnly: !linked, Title: body.Title, Description: body.Description,
		Teams: body.Teams, ChannelID: body.ChannelID,
		DefaultWinPaint: body.DefaultWinPaint, DefaultLossPaint: body.DefaultLossPaint,
		CreatedBy: creator,
	}
	if err := b.s.store.UpsertBotFest(ctx, f); err != nil {
		b.internal(w, "save bot fest", err)
		return
	}
	stored, err := b.s.store.GetBotFest(ctx, row.ID)
	if err != nil {
		b.internal(w, "load bot fest", err)
		return
	}
	b.s.log.Info("botapi: fest created", "fest", row.ID, "discord_only", !linked, "by", creator)
	writeJSON(w, http.StatusCreated, b.festView(stored, row))
}

// newDiscordFestID picks an unused id for a Discord-only fest. Its own
// prefix, so it can never collide with an in-game fest built later.
func (b *botAPI) newDiscordFestID(ctx context.Context) (string, error) {
	for range 20 {
		var buf [4]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return "", err
		}
		n := (uint32(buf[0])<<24 | uint32(buf[1])<<16 | uint32(buf[2])<<8 | uint32(buf[3])) % 100000
		id := fmt.Sprintf("LANF-%05d", n)
		if _, err := b.s.store.GetFest(ctx, id); errors.Is(err, db.ErrNotFound) {
			return id, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("no free fest id")
}

type editFestRequest struct {
	Title            *string `json:"title"`
	Description      *string `json:"description"`
	ChannelID        *string `json:"channelId"`
	MessageID        *string `json:"messageId"`
	DefaultWinPaint  *int64  `json:"defaultWinPaint"`
	DefaultLossPaint *int64  `json:"defaultLossPaint"`
}

// editBotFest changes what the vote post says or where it is. Not the teams:
// votes are already stored under them.
func (b *botAPI) editBotFest(w http.ResponseWriter, r *http.Request) {
	var body editFestRequest
	if err := decodeJSONBody(r, &body); err != nil {
		writeBotError(w, http.StatusBadRequest, "bad_request", "unreadable body")
		return
	}
	f, row, ok := b.loadBotFest(w, r.Context(), r.PathValue("id"))
	if !ok {
		return
	}
	if body.Title != nil {
		if t := strings.TrimSpace(*body.Title); t != "" && len(t) <= 200 {
			f.Title = t
		} else {
			writeBotError(w, http.StatusBadRequest, "bad_title", "a title is 1 to 200 characters")
			return
		}
	}
	if body.Description != nil {
		if len(*body.Description) > 3500 {
			writeBotError(w, http.StatusBadRequest, "bad_title", "the text is 3500 characters at most")
			return
		}
		f.Description = *body.Description
	}
	if body.ChannelID != nil {
		f.ChannelID = *body.ChannelID
	}
	if body.MessageID != nil {
		f.MessageID = *body.MessageID
	}
	for _, p := range []struct {
		in  *int64
		out *int64
	}{{body.DefaultWinPaint, &f.DefaultWinPaint}, {body.DefaultLossPaint, &f.DefaultLossPaint}} {
		if p.in == nil {
			continue
		}
		if *p.in < 0 || *p.in > maxPaint {
			writeBotError(w, http.StatusBadRequest, "bad_paint", fmt.Sprintf("paint is 0 to %d", maxPaint))
			return
		}
		*p.out = *p.in
	}
	if err := b.s.store.UpsertBotFest(r.Context(), f); err != nil {
		b.internal(w, "save bot fest", err)
		return
	}
	writeJSON(w, http.StatusOK, b.festView(f, row))
}

// deleteBotFest removes a vote post. A Discord-only fest goes with every vote
// and game in it; an in-game fest only loses its post.
func (b *botAPI) deleteBotFest(w http.ResponseWriter, r *http.Request) {
	f, _, ok := b.loadBotFest(w, r.Context(), r.PathValue("id"))
	if !ok {
		return
	}
	var err error
	if f.DiscordOnly {
		err = b.s.store.DeleteFest(r.Context(), f.FestID)
	} else {
		err = b.s.store.DeleteBotFest(r.Context(), f.FestID)
	}
	if err != nil {
		b.internal(w, "delete fest", err)
		return
	}
	b.s.log.Info("botapi: fest vote post removed", "fest", f.FestID, "discord_only", f.DiscordOnly)
	w.WriteHeader(http.StatusNoContent)
}

var postKinds = []string{"announced", "started", "halftime", "ended", "results"}

func (b *botAPI) claimBotFestPost(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if !slices.Contains(postKinds, kind) {
		writeBotError(w, http.StatusNotFound, "bad_kind", "kind is one of "+strings.Join(postKinds, ", "))
		return
	}
	claimed, err := b.s.store.ClaimBotFestPost(r.Context(), r.PathValue("id"), kind, b.s.now())
	if errors.Is(err, db.ErrNotFound) {
		writeBotError(w, http.StatusNotFound, "no_such_fest", "no fest by that id on Discord")
		return
	}
	if err != nil {
		b.internal(w, "claim fest post", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"claimed": claimed})
}

// festStandings is what the game would show now, never more: see
// toyohr.Standings.
func (b *botAPI) festStandings(w http.ResponseWriter, r *http.Request) {
	_, row, ok := b.loadBotFest(w, r.Context(), r.PathValue("id"))
	if !ok {
		return
	}
	st, err := toyohr.FestStandings(r.Context(), b.s.store, row, b.s.now())
	if err != nil {
		b.internal(w, "fest standings", err)
		return
	}
	out := map[string]any{"phase": st.Phase.String()}
	if st.Midterm != nil {
		out["midterm"] = st.Midterm
	}
	if st.Final != nil {
		out["final"] = st.Final
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- votes --------------------------------------------------------------------

type festEntryView struct {
	UserID        string `json:"userId"`
	DiscordUserID string `json:"discordUserId"`
	Team          string `json:"team"`
}

// discordOf returns the Discord account a user is the MAIN account of, or ""
// when it has none or is one of that account's extras. Roles follow the main
// account only: one person with two accounts on two teams must not hold both
// roles.
func (b *botAPI) discordOf(ctx context.Context, userID string) (string, error) {
	u, err := b.s.store.GetUser(ctx, userID)
	if errors.Is(err, db.ErrNotFound) {
		return "", nil
	}
	if err != nil || u.DiscordUserID == "" || u.Deleted {
		return "", err
	}
	main, err := b.s.store.GetUserByDiscordID(ctx, u.DiscordUserID)
	if errors.Is(err, db.ErrNotFound) {
		return "", nil
	}
	if err != nil || main.ID != userID {
		return "", err
	}
	return u.DiscordUserID, nil
}

// festEntries lists the entries the bot can turn into roles: the ones made by
// a Discord account's main account. The bot reads it once per reconnect to
// catch up on events it missed, never on a timer.
func (b *botAPI) festEntries(w http.ResponseWriter, r *http.Request) {
	_, _, ok := b.loadBotFest(w, r.Context(), r.PathValue("id"))
	if !ok {
		return
	}
	entries, err := b.s.store.ListFestEntries(r.Context(), r.PathValue("id"))
	if err != nil {
		b.internal(w, "list fest entries", err)
		return
	}
	out := []festEntryView{}
	for _, e := range entries {
		discordID, err := b.discordOf(r.Context(), e.UserID)
		if err != nil {
			b.internal(w, "resolve discord account", err)
			return
		}
		if discordID != "" {
			out = append(out, festEntryView{UserID: e.UserID, DiscordUserID: discordID, Team: e.Team})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": out})
}

// enterFest is a vote from the post. The first pick is final, the same rule
// a console's vote follows (toyohr.CreateFestEntry): a second vote gets the
// first one back with created false.
func (b *botAPI) enterFest(w http.ResponseWriter, r *http.Request, user db.User) {
	var body struct {
		Team string `json:"team"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeBotError(w, http.StatusBadRequest, "bad_request", "unreadable body")
		return
	}
	f, row, ok := b.loadBotFest(w, r.Context(), r.PathValue("id"))
	if !ok {
		return
	}
	if !slices.ContainsFunc(f.Teams, func(t db.BotFestTeam) bool { return t.Team == body.Team }) {
		writeBotError(w, http.StatusBadRequest, "bad_team", "not one of this fest's teams")
		return
	}
	switch festPhase(row, b.s.now()) {
	case toyohr.PhaseCounting, toyohr.PhaseClosed:
		writeBotError(w, http.StatusConflict, "fest_over", "play has ended")
		return
	}
	entry, created, err := b.s.store.CreateFestEntry(r.Context(), db.FestEntry{
		FestID: f.FestID, UserID: user.ID, Team: body.Team,
	})
	if err != nil {
		b.internal(w, "save fest entry", err)
		return
	}
	if created {
		b.s.log.Info("botapi: fest entry", "fest", f.FestID, "user", user.ID, "team", entry.Team)
	}
	writeJSON(w, http.StatusOK, map[string]any{"team": entry.Team, "created": created})
}

// festRecord is a player's own record: their team, their games and their
// submissions. Never a team total.
func (b *botAPI) festRecord(w http.ResponseWriter, r *http.Request, user db.User) {
	f, _, ok := b.loadBotFest(w, r.Context(), r.PathValue("id"))
	if !ok {
		return
	}
	ctx := r.Context()
	out := map[string]any{"festId": f.FestID}
	switch e, err := b.s.store.GetFestEntry(ctx, f.FestID, user.ID); {
	case err == nil:
		out["team"] = e.Team
	case !errors.Is(err, db.ErrNotFound):
		b.internal(w, "load fest entry", err)
		return
	}
	battles, err := b.s.store.ListFestBattles(ctx, f.FestID)
	if err != nil {
		b.internal(w, "list fest battles", err)
		return
	}
	winner := make(map[string]string, len(battles))
	for _, bt := range battles {
		winner[bt.BattleID] = bt.WinningTeam
	}
	players, err := b.s.store.ListFestBattlePlayers(ctx, f.FestID)
	if err != nil {
		b.internal(w, "list fest battle players", err)
		return
	}
	type tally struct {
		Games        int   `json:"games"`
		Wins         int   `json:"wins"`
		Contribution int64 `json:"contribution"`
	}
	var online, lan tally
	for _, p := range players {
		if p.UserID != user.ID {
			continue
		}
		t := &online
		if strings.HasPrefix(p.BattleID, lanBattlePrefix) {
			t = &lan
		}
		t.Games++
		t.Contribution += p.Paint
		if winner[p.BattleID] == p.Team {
			t.Wins++
		}
	}
	out["online"], out["lan"] = online, lan
	subs, err := b.s.store.ListFestSubmissions(ctx, f.FestID)
	if err != nil {
		b.internal(w, "list submissions", err)
		return
	}
	mine := []submissionView{}
	for _, s := range subs {
		if s.SubmittedBy == user.DiscordUserID || slices.ContainsFunc(s.Players,
			func(p db.FestSubmissionPlayer) bool { return p.UserID == user.ID }) {
			mine = append(mine, viewSubmission(s))
		}
	}
	out["submissions"] = mine
	writeJSON(w, http.StatusOK, out)
}

// ---- LAN games ----------------------------------------------------------------

type submitRequest struct {
	// Kind is "open" (the default), "pro" or "tricolor".
	Kind          string           `json:"kind"`
	Winners       []string         `json:"winners"`
	Losers        []string         `json:"losers"`
	Paint         map[string]int64 `json:"paint"`
	ScreenshotURL string           `json:"screenshotUrl"`
}

// submitLANGame records a LAN game for review. Every player must be
// registered and on the team they are listed under, and the person submitting
// must have played in it. Nothing counts until a mod accepts it.
func (b *botAPI) submitLANGame(w http.ResponseWriter, r *http.Request, submitter string) {
	var body submitRequest
	if err := decodeJSONBody(r, &body); err != nil {
		writeBotError(w, http.StatusBadRequest, "bad_request", "unreadable body")
		return
	}
	ctx := r.Context()
	f, row, ok := b.loadBotFest(w, ctx, r.PathValue("id"))
	if !ok {
		return
	}
	if p := festPhase(row, b.s.now()); p != toyohr.PhaseFirstHalf && p != toyohr.PhaseSecondHalf {
		writeBotError(w, http.StatusConflict, "not_in_play", "games can only be submitted while play is open")
		return
	}
	if body.Kind == "" {
		body.Kind = db.SubmissionOpen
	}
	if _, ok := lanBattleModes[body.Kind]; !ok {
		writeBotError(w, http.StatusBadRequest, "bad_kind", "a game is open, pro or tricolor")
		return
	}
	tricolor := body.Kind == db.SubmissionTricolor
	// Tricolor battles only exist in the second half, as in game: the
	// halftime ranking picks who defends.
	if tricolor && festPhase(row, b.s.now()) != toyohr.PhaseSecondHalf {
		writeBotError(w, http.StatusConflict, "no_tricolor_yet", "Tricolor battles start in the second half")
		return
	}
	maxLosers := maxTeamSize
	if tricolor {
		maxLosers = maxTeamSize + tricolorAttackers
	}
	if len(body.Winners) == 0 || len(body.Losers) == 0 ||
		len(body.Winners) > maxTeamSize || len(body.Losers) > maxLosers {
		writeBotError(w, http.StatusBadRequest, "bad_players", "each side has one to four players")
		return
	}
	all := append(append([]string{}, body.Winners...), body.Losers...)
	seen := map[string]bool{}
	for _, id := range all {
		if !discordIDPattern.MatchString(id) || seen[id] {
			writeBotError(w, http.StatusBadRequest, "bad_players", "a player can only be listed once")
			return
		}
		seen[id] = true
	}
	if !seen[submitter] {
		writeBotError(w, http.StatusForbidden, "not_a_player", "only a player in the game can submit it")
		return
	}
	for id, p := range body.Paint {
		if !seen[id] || p < 0 || p > maxPaint {
			writeBotError(w, http.StatusBadRequest, "bad_paint",
				fmt.Sprintf("paint is 0 to %d, for players in the game", maxPaint))
			return
		}
	}

	var unregistered, unentered []string
	teamOf := map[string]string{}
	players := make([]db.FestSubmissionPlayer, 0, len(all))
	for _, id := range all {
		u, err := b.s.store.GetUserByDiscordID(ctx, id)
		if errors.Is(err, db.ErrNotFound) || (err == nil && u.Deleted) {
			unregistered = append(unregistered, id)
			continue
		}
		if err != nil {
			b.internal(w, "load player", err)
			return
		}
		e, err := b.s.store.GetFestEntry(ctx, f.FestID, u.ID)
		if errors.Is(err, db.ErrNotFound) {
			unentered = append(unentered, id)
			continue
		}
		if err != nil {
			b.internal(w, "load fest entry", err)
			return
		}
		teamOf[id] = e.Team
		p := db.FestSubmissionPlayer{UserID: u.ID, DiscordUserID: id, Team: e.Team}
		if paint, ok := body.Paint[id]; ok {
			p.Paint = &paint
		}
		players = append(players, p)
	}
	if len(unregistered) > 0 {
		writeJSON(w, http.StatusConflict, playersError{Code: "players_not_registered",
			Message: "every player needs a GRID0+ account", DiscordUserIDs: unregistered})
		return
	}
	if len(unentered) > 0 {
		writeJSON(w, http.StatusConflict, playersError{Code: "players_not_entered",
			Message: "every player needs to have picked a team", DiscordUserIDs: unentered})
		return
	}
	sideTeam := func(side []string) (string, []string) {
		team, wrong := teamOf[side[0]], []string{}
		for _, id := range side {
			if teamOf[id] != team {
				wrong = append(wrong, id)
			}
		}
		return team, wrong
	}
	winning, wrongW := sideTeam(body.Winners)
	var losing string
	if tricolor {
		bad, err := b.tricolorLineup(ctx, f, row, winning, wrongW, body.Losers, teamOf)
		if err != nil {
			b.internal(w, "tricolor lineup", err)
			return
		}
		if bad != nil {
			writeJSON(w, http.StatusConflict, *bad)
			return
		}
	} else {
		var wrongL []string
		losing, wrongL = sideTeam(body.Losers)
		if len(wrongW) > 0 || len(wrongL) > 0 || winning == losing {
			ids := append(wrongW, wrongL...)
			if len(ids) == 0 {
				ids = all
			}
			writeJSON(w, http.StatusConflict, playersError{Code: "team_mismatch",
				Message: "each side must be one team, and the two sides different teams", DiscordUserIDs: ids})
			return
		}
	}

	id, err := newSubmissionID()
	if err != nil {
		b.internal(w, "choose submission id", err)
		return
	}
	sub := db.FestSubmission{
		ID: id, FestID: f.FestID, SubmittedBy: submitter, Kind: body.Kind,
		WinningTeam: winning, LosingTeam: losing, Players: players,
		ScreenshotURL: body.ScreenshotURL, Status: db.SubmissionPending, CreatedAt: b.s.now().UTC(),
	}
	earlier, err := b.s.store.ListFestSubmissions(ctx, f.FestID)
	if err != nil {
		b.internal(w, "list submissions", err)
		return
	}
	if err := b.s.store.CreateFestSubmission(ctx, sub); err != nil {
		b.internal(w, "save submission", err)
		return
	}
	view := viewSubmission(sub)
	for _, e := range earlier {
		if e.Status != db.SubmissionRefused && sub.CreatedAt.Sub(e.CreatedAt) < duplicateWindow && samePlayers(e, sub) {
			view.PossibleDuplicateOf = append(view.PossibleDuplicateOf, e.ID)
		}
	}
	b.s.log.Info("botapi: LAN game submitted", "fest", f.FestID, "submission", id, "kind", body.Kind,
		"winning", winning, "losing", losing, "players", len(players))
	writeJSON(w, http.StatusCreated, view)
}

// tricolorLineup checks a Tricolor game's sides the way the game sets them:
// the halftime leader defends with up to four players, each other team
// attacks with up to two, and every team is there. Any one team can win. A
// non-nil error response says what is wrong.
func (b *botAPI) tricolorLineup(ctx context.Context, f db.BotFest, row db.Fest, winning string, wrongW, losers []string, teamOf map[string]string) (*playersError, error) {
	if len(wrongW) > 0 {
		return &playersError{Code: "team_mismatch", Message: "the winners must all be on one team", DiscordUserIDs: wrongW}, nil
	}
	st, err := toyohr.FestStandings(ctx, b.s.store, row, b.s.now())
	if err != nil {
		return nil, err
	}
	if len(st.Midterm) == 0 {
		return &playersError{Code: "no_tricolor_yet", Message: "there is no halftime ranking yet to pick the defenders"}, nil
	}
	defender := st.Midterm[0].Team
	count := map[string]int{}
	var wrong []string
	for _, id := range losers {
		if teamOf[id] == winning {
			wrong = append(wrong, id)
		}
		count[teamOf[id]]++
	}
	if len(wrong) > 0 {
		return &playersError{Code: "team_mismatch", Message: "a loser is on the winning team", DiscordUserIDs: wrong}, nil
	}
	count[winning] += len(teamOfSide(winning, teamOf))
	for _, t := range f.Teams {
		n := count[t.Team]
		limit := tricolorAttackers
		if t.Team == defender {
			limit = maxTeamSize
		}
		if n == 0 || n > limit {
			return &playersError{Code: "bad_tricolor",
				Message: fmt.Sprintf("a Tricolor battle has all three teams: %s (the halftime leader) defends with up to %d players, the others attack with up to %d each",
					defender, maxTeamSize, tricolorAttackers)}, nil
		}
	}
	return nil, nil
}

// teamOfSide lists the players of one team.
func teamOfSide(team string, teamOf map[string]string) []string {
	var out []string
	for id, t := range teamOf {
		if t == team {
			out = append(out, id)
		}
	}
	return out
}

func samePlayers(a, b db.FestSubmission) bool {
	if len(a.Players) != len(b.Players) {
		return false
	}
	ids := map[string]bool{}
	for _, p := range a.Players {
		ids[p.UserID] = true
	}
	for _, p := range b.Players {
		if !ids[p.UserID] {
			return false
		}
	}
	return true
}

func newSubmissionID() (string, error) {
	var buf [5]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf[:])), nil
}

func (b *botAPI) loadSubmission(w http.ResponseWriter, ctx context.Context, id string) (db.FestSubmission, bool) {
	s, err := b.s.store.GetFestSubmission(ctx, id)
	if errors.Is(err, db.ErrNotFound) {
		writeBotError(w, http.StatusNotFound, "no_such_submission", "no submission by that id")
		return db.FestSubmission{}, false
	}
	if err != nil {
		b.internal(w, "load submission", err)
		return db.FestSubmission{}, false
	}
	return s, true
}

func (b *botAPI) getSubmission(w http.ResponseWriter, r *http.Request) {
	if s, ok := b.loadSubmission(w, r.Context(), r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, viewSubmission(s))
	}
}

func (b *botAPI) setSubmissionMessage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChannelID string `json:"channelId"`
		MessageID string `json:"messageId"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeBotError(w, http.StatusBadRequest, "bad_request", "unreadable body")
		return
	}
	err := b.s.store.SetFestSubmissionMessage(r.Context(), r.PathValue("id"), body.ChannelID, body.MessageID)
	if errors.Is(err, db.ErrNotFound) {
		writeBotError(w, http.StatusNotFound, "no_such_submission", "no submission by that id")
		return
	}
	if err != nil {
		b.internal(w, "save review message", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// settleConflict answers a review of a submission that is no longer pending
// with what it is now, so the bot can tell the mod who got there first.
func (b *botAPI) settleConflict(w http.ResponseWriter, ctx context.Context, id string) {
	s, ok := b.loadSubmission(w, ctx, id)
	if !ok {
		return
	}
	writeJSON(w, http.StatusConflict, map[string]any{
		"code": "already_reviewed", "message": "that submission was already " + s.Status,
		"submission": viewSubmission(s),
	})
}

// flagSubmission lets a player in the game dispute it before a mod rules.
func (b *botAPI) flagSubmission(w http.ResponseWriter, r *http.Request, discordID string) {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeBotError(w, http.StatusBadRequest, "bad_request", "unreadable body")
		return
	}
	s, ok := b.loadSubmission(w, r.Context(), r.PathValue("id"))
	if !ok {
		return
	}
	if !slices.ContainsFunc(s.Players, func(p db.FestSubmissionPlayer) bool { return p.DiscordUserID == discordID }) {
		writeBotError(w, http.StatusForbidden, "not_a_player", "only a player in the game can dispute it")
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if len(reason) > 500 {
		reason = reason[:500]
	}
	flagged, err := b.s.store.FlagFestSubmission(r.Context(), s.ID,
		db.FestSubmissionFlag{DiscordUserID: discordID, Reason: reason, At: b.s.now().UTC()})
	if errors.Is(err, db.ErrConflict) {
		b.settleConflict(w, r.Context(), s.ID)
		return
	}
	if err != nil {
		b.internal(w, "flag submission", err)
		return
	}
	writeJSON(w, http.StatusOK, viewSubmission(flagged))
}

func (b *botAPI) refuseSubmission(w http.ResponseWriter, r *http.Request, reviewer string) {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeBotError(w, http.StatusBadRequest, "bad_request", "unreadable body")
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if reason == "" || len(reason) > 500 {
		writeBotError(w, http.StatusBadRequest, "bad_reason", "a refusal needs a reason (500 characters at most)")
		return
	}
	s, err := b.s.store.RefuseFestSubmission(r.Context(), r.PathValue("id"), reviewer, reason, b.s.now().UTC())
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeBotError(w, http.StatusNotFound, "no_such_submission", "no submission by that id")
		return
	case errors.Is(err, db.ErrConflict):
		b.settleConflict(w, r.Context(), r.PathValue("id"))
		return
	case err != nil:
		b.internal(w, "refuse submission", err)
		return
	}
	b.s.log.Info("botapi: LAN game refused", "fest", s.FestID, "submission", s.ID, "by", reviewer)
	writeJSON(w, http.StatusOK, viewSubmission(s))
}

// acceptSubmission counts a LAN game exactly like an online one: a battle in
// the Regular (Open) category, with every player's paint. A player's missing
// paint is this fest's online average for their side, or the fest's own
// default before anyone has played online.
func (b *botAPI) acceptSubmission(w http.ResponseWriter, r *http.Request, reviewer string) {
	ctx := r.Context()
	s, ok := b.loadSubmission(w, ctx, r.PathValue("id"))
	if !ok {
		return
	}
	if s.Status != db.SubmissionPending {
		b.settleConflict(w, ctx, s.ID)
		return
	}
	f, row, ok := b.loadBotFest(w, ctx, s.FestID)
	if !ok {
		return
	}
	if festPhase(row, b.s.now()) == toyohr.PhaseClosed {
		writeBotError(w, http.StatusConflict, "results_out",
			"the results are already out; accepting this now would change a published result")
		return
	}
	winAvg, lossAvg, err := b.onlinePaintAverages(ctx, f)
	if err != nil {
		b.internal(w, "average online paint", err)
		return
	}
	battle := db.FestBattle{
		FestID: s.FestID, BattleID: lanBattlePrefix + s.ID, Mode: lanBattleModes[s.Kind],
		WinningTeam: s.WinningTeam, Ink: map[string]int64{}, FinishedAt: s.CreatedAt,
	}
	players := make([]db.FestBattlePlayer, 0, len(s.Players))
	for _, p := range s.Players {
		paint := lossAvg
		if p.Team == s.WinningTeam {
			paint = winAvg
		}
		if p.Paint != nil {
			paint = *p.Paint
		}
		battle.Ink[p.Team] += paint
		players = append(players, db.FestBattlePlayer{
			FestID: s.FestID, BattleID: battle.BattleID, UserID: p.UserID, Team: p.Team, Paint: paint,
		})
	}
	accepted, err := b.s.store.AcceptFestSubmission(ctx, s.ID, reviewer, b.s.now().UTC(), battle, players)
	switch {
	case errors.Is(err, db.ErrConflict):
		b.settleConflict(w, ctx, s.ID)
		return
	case err != nil:
		b.internal(w, "accept submission", err)
		return
	}
	b.s.log.Info("botapi: LAN game accepted", "fest", s.FestID, "submission", s.ID, "by", reviewer,
		"winning", s.WinningTeam)
	writeJSON(w, http.StatusOK, viewSubmission(accepted))
}

// onlinePaintAverages is the mean paint of a winner and of a loser across the
// fest's online battles, falling back to the fest's own defaults for a side
// with no online games yet.
func (b *botAPI) onlinePaintAverages(ctx context.Context, f db.BotFest) (win, loss int64, err error) {
	battles, err := b.s.store.ListFestBattles(ctx, f.FestID)
	if err != nil {
		return 0, 0, err
	}
	winner := map[string]string{}
	for _, bt := range battles {
		if !strings.HasPrefix(bt.BattleID, lanBattlePrefix) {
			winner[bt.BattleID] = bt.WinningTeam
		}
	}
	players, err := b.s.store.ListFestBattlePlayers(ctx, f.FestID)
	if err != nil {
		return 0, 0, err
	}
	var winSum, lossSum, winN, lossN int64
	for _, p := range players {
		w, online := winner[p.BattleID]
		if !online {
			continue
		}
		if p.Team == w {
			winSum, winN = winSum+p.Paint, winN+1
		} else {
			lossSum, lossN = lossSum+p.Paint, lossN+1
		}
	}
	win, loss = f.DefaultWinPaint, f.DefaultLossPaint
	if winN > 0 {
		win = winSum / winN
	}
	if lossN > 0 {
		loss = lossSum / lossN
	}
	return win, loss, nil
}

// ---- the event stream ---------------------------------------------------------

// festEventsHeartbeat keeps an idle stream from looking dead to the bot, and
// to anything between them.
var festEventsHeartbeat = 30 * time.Second

// festEvents streams new fest entries as they happen, one JSON object per
// line, so the bot gives a player the team's role the moment they pick in
// game without polling anything. Only entries the bot can act on are sent:
// in a fest with a vote post, by a Discord account's main account.
//
// Redis pub/sub is fire-and-forget, so an entry made while the bot is not
// connected is never sent; the bot reads GET /fests/{id}/entries once when it
// connects to catch up.
func (b *botAPI) festEvents(w http.ResponseWriter, r *http.Request) {
	if b.festBus == nil {
		writeBotError(w, http.StatusServiceUnavailable, "no_event_bus",
			"redis.url is not set, so in-game votes cannot reach the bot")
		return
	}
	ctx := r.Context()
	events, err := b.festBus.Subscribe(ctx)
	if err != nil {
		b.internal(w, "subscribe to fest entries", err)
		return
	}
	controller := http.NewResponseController(w)
	// The listener's ReadTimeout would cancel this request's context thirty
	// seconds in, whatever the writes do: net/http's background read of the
	// connection times out and ends the request. A stream that dies every
	// thirty seconds is a reconnect, and a catch-up read, every thirty
	// seconds -- the polling this stream exists to avoid.
	if err := controller.SetReadDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		b.internal(w, "keep the fest stream open", err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	send := func(v any) bool {
		// The listener's WriteTimeout would otherwise end the stream after
		// thirty seconds.
		_ = controller.SetWriteDeadline(time.Now().Add(festEventsHeartbeat + 10*time.Second))
		return enc.Encode(v) == nil && controller.Flush() == nil
	}
	if !send(map[string]string{"type": "hello"}) {
		return
	}
	ticker := time.NewTicker(festEventsHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !send(map[string]string{"type": "ping"}) {
				return
			}
		case e, ok := <-events:
			if !ok {
				return
			}
			if e.Type != presencebus.FestEntryType {
				continue
			}
			var p presencebus.FestEntry
			if json.Unmarshal(e.Payload, &p) != nil {
				continue
			}
			if _, err := b.s.store.GetBotFest(ctx, p.FestID); err != nil {
				continue
			}
			discordID, err := b.discordOf(ctx, e.UserID)
			if err != nil {
				b.s.log.Warn("botapi: resolve discord account for a fest entry", "error", err)
				continue
			}
			if discordID == "" {
				continue
			}
			if !send(map[string]string{"type": "fest_entry", "festId": p.FestID, "userId": e.UserID,
				"discordUserId": discordID, "team": p.Team}) {
				return
			}
		}
	}
}
