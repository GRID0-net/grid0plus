package main

import (
	"context"
	"fmt"
	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/toyohr"
	commonpb "github.com/n-popescu/grid0plus/proto/common"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"time"
)

func val(x any) *commonpb.Value {
	switch x := x.(type) {
	case string:
		return &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: x}}
	case int:
		return &commonpb.Value{ValueType: &commonpb.Value_IntegerValue{IntegerValue: int64(x)}}
	case bool:
		return &commonpb.Value{ValueType: &commonpb.Value_BooleanValue{BooleanValue: x}}
	case map[string]any:
		return &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: mv(x)}}
	case []any:
		a := []*commonpb.Value{}
		for _, v := range x {
			a = append(a, val(v))
		}
		return &commonpb.Value{ValueType: &commonpb.Value_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: a}}}
	case time.Time:
		return &commonpb.Value{ValueType: &commonpb.Value_TimestampValue{TimestampValue: timestamppb.New(x)}}
	}
	panic("unsupported demo value")
}
func mv(x map[string]any) *commonpb.MapValue {
	f := map[string]*commonpb.Value{}
	for k, v := range x {
		f[k] = val(v)
	}
	return &commonpb.MapValue{Fields: f}
}
func demoStore(ctx context.Context) (db.Store, error) {
	s := db.NewMemory()
	now := time.Now().UTC()
	uid := "inklink-demo"
	hash, err := bcrypt.GenerateFromPassword([]byte("inklink-demo"), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	if err = s.CreateUserWithLogin(ctx, db.User{ID: uid, Nickname: "Nico", LoginUsername: "demo", LoginPasswordHash: string(hash), CreatedAt: now}, true); err != nil {
		return nil, err
	}
	if err = s.SetFriendCode(ctx, db.FriendCode{UserID: uid, Code: "123456789012", CreatedAt: now, UpdatedAt: now}); err != nil {
		return nil, err
	}
	root := "tenants/t-dce9377b-lp1/"
	put := func(name string, fields map[string]any, at time.Time) error {
		b, err := proto.Marshal(mv(fields))
		if err != nil {
			return err
		}
		return s.PutUGCDocument(ctx, db.UGCDocument{Name: root + name, Owner: uid, Fields: b, CreateTime: at, UpdateTime: at})
	}
	save := map[string]any{"PlayerRank": 34, "CatalogLevel": 32, "CatalogPoint": 4100, "CatalogBookCompleteNum": 1, "Money": 42000, "BronzeUrokoNum": 145, "SilverUrokoNum": 92, "GoldUrokoNum": 11, "HaveWeaponMap": map[string]any{"1010": map[string]any{"WinNum": 12, "MaxWeaponPower": 1500}, "0": map[string]any{"WinNum": 9}}, "HaveGearHeadMap": map[string]any{"21015": map[string]any{}}, "HaveGearClothesMap": map[string]any{"25000": map[string]any{}}, "HaveGearShoesMap": map[string]any{"25000": map[string]any{}}}
	if err = put("saveRecords/"+uid, save, now); err != nil {
		return nil, err
	}
	prefix := "documents/services/GameRecord/users/" + uid + "/"
	if err = put(prefix+"PointCardTotal/Data", map[string]any{"job_num": 339, "golden_ikura_total": 18487, "ikura_total": 660402, "boss_total": 2, "rescue_total": 962, "kuma_point": 51410}, now); err != nil {
		return nil, err
	}
	if err = put(prefix+"CoopUserAttribute/Data", map[string]any{"total_grade_point": 830}, now); err != nil {
		return nil, err
	}
	preview := toyohr.NewService(toyohr.Options{Store: s, Tenant: "t-dce9377b-lp1"})
	shifts, err := preview.PreviewCoopSchedule(ctx, now)
	if err != nil {
		return nil, err
	}
	shift := shifts[0].Start.Format("20060102150405")
	if err = put(prefix+"PointCardRegular/"+shift, map[string]any{"job_num": 2, "kuma_point": 172, "best_golden_ikura_num": 49, "total_clear_wave": 1, "job_type_string": "Normal Job"}, now); err != nil {
		return nil, err
	}
	for i := 0; i < 12; i++ {
		at := now.Add(-time.Duration(i+1) * 20 * time.Minute)
		won := 0
		if i%3 == 2 {
			won = 1
		}
		id := fmt.Sprintf("demo-battle-%02d", i)
		f := map[string]any{"started_at": at, "stage": 21, "mode": "regular_match_config", "elapsed_time": 180, "winner": 1, "personal_data_team1": []any{map[string]any{"npln_user_id": uid, "team": 1, "profile": map[string]any{"player_info": map[string]any{"weapon_id": 1010}}, "result": map[string]any{"paint_point": 1252 - i*23, "kill_num": 6, "kill_assist_num": 1, "death_num": 5, "special_num": 2, "deemed_result": won}}}}
		if err = put(prefix+"VsResults/"+id, f, at); err != nil {
			return nil, err
		}
	}
	for i := 0; i < 6; i++ {
		at := now.Add(-time.Duration(i+1) * 45 * time.Minute)
		result := 1
		if i%3 == 0 {
			result = 0
		}
		after := 830 + i*10
		before := after + 10
		if result == 0 {
			before = after - 20
		}
		f := map[string]any{"started_at": at, "shift_id": shift, "scenario": map[string]any{"stage": 6}, "result": result, "final_wave": 2, "personal_data": []any{map[string]any{"npln_user_id": uid, "grade_before": 8, "grade_after": 8, "grade_point_before": before, "grade_point_after": after, "kuma_point": 172, "personal_wave_result": []any{map[string]any{"wave": 0, "weapon_id": 1010, "golden_ikura_num": 49, "ikura_num": 1895}}}}}
		if err = put(prefix+fmt.Sprintf("CoopResults/demo-job-%02d", i), f, at); err != nil {
			return nil, err
		}
	}
	return s, nil
}
