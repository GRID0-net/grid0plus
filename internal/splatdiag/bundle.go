// Package splatdiag holds an opt-in, one-account comparison against private
// protobuf records. The source files are never part of the repository.
package splatdiag

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/n-popescu/switchnet/proto/common"
	toyohrpb "github.com/n-popescu/switchnet/proto/toyohr/v1"
	ugcpb "github.com/n-popescu/switchnet/proto/ugcstore/v1"
)

// Bundle is disabled unless all four SWITCHNET_NPLN_DIAGNOSTIC_* variables
// are set. Only UserID can receive its records; other users take the normal
// server path. The source protobufs stay read-only and immutable in memory.
type Bundle struct {
	UserID string
	save   *toyohrpb.SaveRecord
	vs     *ugcpb.Document
	coop   *ugcpb.Document
}

func FromEnvironment() (*Bundle, error) {
	user := os.Getenv("SWITCHNET_NPLN_DIAGNOSTIC_USER")
	paths := []string{
		os.Getenv("SWITCHNET_NPLN_DIAGNOSTIC_SAVE_PATH"),
		os.Getenv("SWITCHNET_NPLN_DIAGNOSTIC_VS_PATH"),
		os.Getenv("SWITCHNET_NPLN_DIAGNOSTIC_COOP_PATH"),
	}
	active := user != ""
	for _, path := range paths {
		active = active || path != ""
	}
	if !active {
		return nil, nil
	}
	if user == "" {
		return nil, errors.New("diagnostic bundle needs a user id")
	}
	for _, path := range paths {
		if path == "" {
			return nil, errors.New("diagnostic bundle needs all three protobuf paths")
		}
	}
	b := &Bundle{UserID: user, save: &toyohrpb.SaveRecord{}, vs: &ugcpb.Document{}, coop: &ugcpb.Document{}}
	for i, msg := range []proto.Message{b.save, b.vs, b.coop} {
		data, err := os.ReadFile(paths[i])
		if err != nil {
			return nil, fmt.Errorf("diagnostic source %d: %w", i, err)
		}
		if err := proto.Unmarshal(data, msg); err != nil {
			return nil, fmt.Errorf("diagnostic source %d: %w", i, err)
		}
	}
	if b.save.GetName() == "" || len(b.save.GetSaveData().GetFields()) == 0 {
		return nil, errors.New("diagnostic save is empty or unnamed")
	}
	for _, doc := range []*ugcpb.Document{b.vs, b.coop} {
		if doc.GetName() == "" || len(doc.GetFields().GetFields()) == 0 {
			return nil, errors.New("diagnostic attribute is empty or unnamed")
		}
	}
	if !strings.HasSuffix(b.vs.GetName(), "/VsUserAttribute/Data") || !strings.HasSuffix(b.coop.GetName(), "/CoopUserAttribute/Data") {
		return nil, errors.New("diagnostic attribute has the wrong document kind")
	}
	// The preserved working sessions themselves pair a replayed save with
	// attribute documents from another source account. Retarget each record
	// independently; the two attribute documents must agree with each other.
	attributeOwner := sourceDocumentUserID(b.vs.GetName())
	if sourceSaveUserID(b.save.GetName()) == "" || attributeOwner == "" || sourceDocumentUserID(b.coop.GetName()) != attributeOwner {
		return nil, errors.New("diagnostic source owner is missing or attributes disagree")
	}
	return b, nil
}

// SaveFor retargets the resource name and displayed nickname to the caller.
// It intentionally does not replace game progression or invent initial values.
func (b *Bundle) SaveFor(uid, tenant, nickname string, now time.Time) (*toyohrpb.SaveRecord, bool) {
	if b == nil || uid != b.UserID {
		return nil, false
	}
	rec := proto.Clone(b.save).(*toyohrpb.SaveRecord)
	rec.Name = "tenants/" + tenant + "/saveRecords/" + uid
	replaceUID(rec.SaveData, sourceSaveUserID(b.save.GetName()), uid)
	if nickname != "" {
		rec.SaveData.Fields["UserName"] = &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: nickname}}
	}
	rec.UpdateTime = timestamppb.New(now)
	return rec, true
}

// AttributeFor serves only the two GameRecord documents associated with the
// same account. Other missing documents, including Hammer, keep their normal
// NotFound response.
func (b *Bundle) AttributeFor(uid, key string, now time.Time) (*ugcpb.Document, bool) {
	if b == nil || uid != b.UserID {
		return nil, false
	}
	var source *ugcpb.Document
	switch {
	case strings.HasSuffix(key, "/services/GameRecord/users/"+uid+"/VsUserAttribute/Data"):
		source = b.vs
	case strings.HasSuffix(key, "/services/GameRecord/users/"+uid+"/CoopUserAttribute/Data"):
		source = b.coop
	default:
		return nil, false
	}
	oldUID := sourceDocumentUserID(source.GetName())
	if oldUID == "" {
		return nil, false
	}
	doc := proto.Clone(source).(*ugcpb.Document)
	doc.Name = key
	replaceUID(doc.Fields, oldUID, uid)
	doc.UpdateTime = timestamppb.New(now)
	return doc, true
}

func replaceUID(fields *commonpb.MapValue, oldUID, uid string) {
	if fields == nil || oldUID == uid {
		return
	}
	for _, v := range fields.GetFields() {
		replaceValue(v, oldUID, uid)
	}
}
func replaceValue(value *commonpb.Value, oldUID, uid string) {
	if value == nil {
		return
	}
	switch v := value.ValueType.(type) {
	case *commonpb.Value_StringValue:
		v.StringValue = strings.ReplaceAll(v.StringValue, oldUID, uid)
	case *commonpb.Value_ReferenceValue:
		v.ReferenceValue = strings.ReplaceAll(v.ReferenceValue, oldUID, uid)
	case *commonpb.Value_MapValue:
		replaceUID(v.MapValue, oldUID, uid)
	case *commonpb.Value_ArrayValue:
		for _, element := range v.ArrayValue.GetValues() {
			replaceValue(element, oldUID, uid)
		}
	}
}

func sourceSaveUserID(name string) string {
	_, uid, ok := strings.Cut(name, "/saveRecords/")
	if !ok || uid == "" || strings.Contains(uid, "/") {
		return ""
	}
	return uid
}

func sourceDocumentUserID(name string) string {
	_, rest, ok := strings.Cut(name, "/users/")
	if !ok {
		return ""
	}
	uid, _, ok := strings.Cut(rest, "/")
	if !ok {
		return ""
	}
	return uid
}
