package inklink

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/toyohr"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
	"google.golang.org/protobuf/proto"
)

type record struct {
	ID     string         `json:"id"`
	Time   time.Time      `json:"time"`
	Fields map[string]any `json:"fields"`
}

func unpack(v *commonpb.Value) any {
	if v == nil {
		return nil
	}
	switch x := v.ValueType.(type) {
	case *commonpb.Value_MapValue:
		return unpackMap(x.MapValue)
	case *commonpb.Value_ArrayValue:
		a := make([]any, 0, len(x.ArrayValue.GetValues()))
		for _, v := range x.ArrayValue.GetValues() {
			a = append(a, unpack(v))
		}
		return a
	case *commonpb.Value_StringValue:
		return x.StringValue
	case *commonpb.Value_IntegerValue:
		return x.IntegerValue
	case *commonpb.Value_DoubleValue:
		return x.DoubleValue
	case *commonpb.Value_BooleanValue:
		return x.BooleanValue
	case *commonpb.Value_TimestampValue:
		if x.TimestampValue != nil {
			return x.TimestampValue.AsTime().UTC().Format(time.RFC3339)
		}
	}
	return nil
}
func unpackMap(m *commonpb.MapValue) map[string]any {
	out := map[string]any{}
	for k, v := range m.GetFields() {
		out[k] = unpack(v)
	}
	return out
}
func decode(raw []byte) (map[string]any, error) {
	var m commonpb.MapValue
	if err := proto.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return unpackMap(&m), nil
}
func (s *Server) documents(ctx context.Context, uid, collection string) ([]record, error) {
	prefix := "tenants/" + s.opts.Tenant + "/documents/services/GameRecord/users/" + uid + "/" + collection + "/"
	docs, err := s.opts.Store.ListUGCDocuments(ctx, prefix)
	if err != nil {
		return nil, err
	}
	out := make([]record, 0, len(docs))
	for _, d := range docs {
		// Store prefixes are not authority: retain both owner and exact collection checks.
		if d.Owner != uid || !strings.HasPrefix(d.Name, prefix) || strings.Contains(strings.TrimPrefix(d.Name, prefix), "/") {
			continue
		}
		fields, err := decode(d.Fields)
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", collection, err)
		}
		out = append(out, record{ID: strings.TrimPrefix(d.Name, prefix), Time: d.CreateTime, Fields: fields})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Time.Equal(out[j].Time) {
			return out[i].ID > out[j].ID
		}
		return out[i].Time.After(out[j].Time)
	})
	if len(out) > 200 {
		out = out[:200]
	}
	return out, nil
}
func (s *Server) oneDocument(ctx context.Context, uid, collection string) (map[string]any, error) {
	d, err := s.opts.Store.GetUGCDocument(ctx, "tenants/"+s.opts.Tenant+"/documents/services/GameRecord/users/"+uid+"/"+collection+"/Data")
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if d.Owner != uid {
		return nil, nil
	}
	return decode(d.Fields)
}
func (s *Server) data(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	now := s.opts.Now()
	p, err := s.presence.Effective(ctx, user.ID, now)
	if err != nil {
		s.internal(w, err)
		return
	}
	profile := map[string]any{"id": user.ID, "nickname": user.Nickname, "username": user.LoginUsername, "presence": p.State, "avatar": "/api/avatar/" + user.ID}
	if fc, err := s.opts.Store.GetFriendCode(ctx, user.ID); err == nil {
		profile["friendCode"] = fc.Code
	} else if !errors.Is(err, db.ErrNotFound) {
		s.internal(w, err)
		return
	}
	friends, err := s.opts.Store.ListFriends(ctx, user.ID, 500)
	if err != nil {
		s.internal(w, err)
		return
	}
	friendRows := []map[string]any{}
	for _, f := range friends {
		u, err := s.opts.Store.GetUser(ctx, f.FriendID)
		if errors.Is(err, db.ErrNotFound) || u.Deleted {
			continue
		}
		if err != nil {
			s.internal(w, err)
			return
		}
		p, err := s.presence.Effective(ctx, u.ID, now)
		if err != nil {
			s.internal(w, err)
			return
		}
		friendRows = append(friendRows, map[string]any{"id": u.ID, "nickname": u.Nickname, "presence": p.State, "favorite": f.Favorite, "avatar": "/api/avatar/" + u.ID})
	}
	sort.SliceStable(friendRows, func(i, j int) bool {
		a, b := friendRows[i], friendRows[j]
		if a["favorite"] != b["favorite"] {
			return a["favorite"].(bool)
		}
		return a["nickname"].(string) < b["nickname"].(string)
	})
	collections := map[string][]record{}
	for _, name := range []string{"VsResults", "CoopResults", "PointCardRegular", "PointCardBigRun", "PointCardLimited"} {
		rows, err := s.documents(ctx, user.ID, name)
		if err != nil {
			s.internal(w, err)
			return
		}
		collections[name] = rows
	}
	docs := map[string]map[string]any{}
	for _, name := range []string{"PointCardTotal", "CoopUserAttribute", "VsUserAttribute", "WeaponPowerMeasurement", "BankaraChallenges"} {
		row, err := s.oneDocument(ctx, user.ID, name)
		if err != nil {
			s.internal(w, err)
			return
		}
		docs[name] = row
	}
	progress := map[string]any{}
	var savedAt *time.Time
	save, err := s.opts.Store.GetUGCDocument(ctx, "tenants/"+s.opts.Tenant+"/saveRecords/"+user.ID)
	if err == nil && save.Owner == user.ID {
		all, err := decode(save.Fields)
		if err != nil {
			s.internal(w, err)
			return
		}
		for _, k := range []string{"PlayerRank", "PlayerRankExp", "Money", "CatalogLevel", "CatalogPoint", "CatalogBookCompleteNum", "HaveWeaponMap", "HaveGearHeadMap", "HaveGearClothesMap", "HaveGearShoesMap", "Coop", "Fest", "Hero", "Sdodr", "MySetMap", "BadgeMap", "BronzeUrokoNum", "SilverUrokoNum", "GoldUrokoNum", "BronzeUrokoSpendNum", "SilverUrokoSpendNum", "GoldUrokoSpendNum"} {
			if v, ok := all[k]; ok {
				progress[k] = v
			}
		}
		savedAt = &save.UpdateTime
	} else if err != nil && !errors.Is(err, db.ErrNotFound) {
		s.internal(w, err)
		return
	}
	schedules, err := s.schedules(ctx, now)
	if err != nil {
		s.internal(w, err)
		return
	}
	fests, err := s.opts.Store.ListFests(ctx)
	if err != nil {
		s.internal(w, err)
		return
	}
	festRows := []map[string]any{}
	for _, f := range fests {
		if f.Enabled {
			festRows = append(festRows, map[string]any{"id": f.ID, "start": f.StartAt, "end": f.EndAt, "regions": f.Regions})
		}
	}
	write(w, 200, map[string]any{"profile": profile, "friends": friendRows, "records": collections, "documents": docs, "progress": progress, "savedAt": savedAt, "schedules": schedules, "fests": festRows, "updatedAt": now, "historyLimit": 200, "demo": s.opts.Demo})
}
func (s *Server) schedules(ctx context.Context, now time.Time) (map[string]any, error) {
	vs := []map[string]any{}
	for _, v := range s.schedule.PreviewVsSchedule(now, 12) {
		banks := []map[string]any{}
		for _, b := range v.GetBankaraSettings() {
			banks = append(banks, map[string]any{"rule": toyohr.VsRuleName(b.GetRule()), "stages": b.GetStages()})
		}
		row := map[string]any{"start": v.GetStartTime().AsTime(), "end": v.GetEndTime().AsTime(), "regular": v.GetRegularSettings().GetStages(), "anarchy": banks}
		if x := v.GetXSettings(); x != nil {
			row["x"] = map[string]any{"rule": toyohr.VsRuleName(x.GetRule()), "stages": x.GetStages()}
		}
		if x := v.GetLeagueSettings(); x != nil {
			row["challenge"] = map[string]any{"rule": toyohr.VsRuleName(x.GetRule()), "stages": x.GetStages()}
		}
		vs = append(vs, row)
	}
	shifts, err := s.schedule.PreviewCoopSchedule(ctx, now)
	if err != nil {
		return nil, err
	}
	coop := []map[string]any{}
	for _, c := range shifts {
		stage, _ := toyohr.CoopStageByID(c.StageID)
		coop = append(coop, map[string]any{"id": c.ID(), "gameId": c.Start.UTC().Format("20060102150405"), "start": c.Start, "end": c.End, "kind": c.Kind.String(), "stage": c.StageID, "stageName": stage.Name, "boss": c.Boss, "weapons": c.Weapons, "rewardGear": toyohr.CoopRewardForShift(c.RewardGearID, c.Start).ID, "rewardGearKind": strings.ToLower(toyohr.CoopRewardForShift(c.RewardGearID, c.Start).Kind)})
	}
	return map[string]any{"vs": vs, "coop": coop}, nil
}
