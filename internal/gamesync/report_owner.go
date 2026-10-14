package gamesync

import (
	"strings"

	commonpb "github.com/n-popescu/grid0plus/proto/common"
	syncpb "github.com/n-popescu/grid0plus/proto/gamesync/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// A whole-roster client report is not authority for other players' results.
// The signed user-session identity selects the reporter's entry; the account
// id is stamped from that identity and a conflicting claim is refused.
func reporterResult(caller *session, value *commonpb.Value) (*commonpb.Value, error) {
	own := sessionID(caller.userSession)
	fields := value.GetMapValue().GetFields()
	result := map[string]*commonpb.Value{}
	if entry := fields[own]; entry.GetMapValue() != nil {
		claimed := entry.GetMapValue().GetFields()["npln_user_id"]
		if claimed != nil && claimed.GetStringValue() != caller.userID {
			return nil, status.Error(codes.PermissionDenied, "report identity disagrees with session")
		}
		verified := proto.Clone(entry).(*commonpb.Value)
		if verified.GetMapValue().Fields == nil {
			verified.GetMapValue().Fields = map[string]*commonpb.Value{}
		}
		verified.GetMapValue().Fields["npln_user_id"] = stringValueForReport(caller.userID)
		result[own] = verified
	}
	return &commonpb.Value{ValueType: &commonpb.Value_MapValue{MapValue: &commonpb.MapValue{Fields: result}}}, nil
}

func stringValueForReport(value string) *commonpb.Value {
	return &commonpb.Value{ValueType: &commonpb.Value_StringValue{StringValue: value}}
}

func ownedReportWrites(caller *session, ops []*syncpb.WriteOperation) ([]*syncpb.WriteOperation, error) {
	out := append([]*syncpb.WriteOperation(nil), ops...)
	for i, op := range ops {
		var name string
		var document *syncpb.Document
		switch v := op.GetOperationType().(type) {
		case *syncpb.WriteOperation_UpdateDocument:
			document = v.UpdateDocument.GetDocument()
			name = document.GetName()
		case *syncpb.WriteOperation_MergeDocument:
			document = v.MergeDocument.GetDocument()
			name = document.GetName()
		case *syncpb.WriteOperation_DeleteDocument:
			name = v.DeleteDocument.GetName()
		case *syncpb.WriteOperation_TransformDocument:
			name = v.TransformDocument.GetName()
		}
		prefix := refereeReportPrefix
		if strings.HasPrefix(name, refereeProfilePrefix) {
			prefix = refereeProfilePrefix
		} else if !strings.HasPrefix(name, refereeReportPrefix) {
			continue
		}
		pieces := strings.Split(strings.TrimPrefix(name, prefix), "/")
		if len(pieces) != 3 || pieces[1] != "User" || pieces[2] != sessionID(caller.userSession) || scopeOf(caller) == "" || !strings.HasSuffix(pieces[0], "_"+scopeOf(caller)) {
			return nil, status.Error(codes.PermissionDenied, "report is outside the authenticated user session")
		}
		if prefix == refereeProfilePrefix {
			continue
		}
		if personal := document.GetFields().GetFields()["personal_result"]; personal != nil {
			verified, err := reporterResult(caller, personal)
			if err != nil {
				return nil, err
			}
			out[i] = proto.Clone(op).(*syncpb.WriteOperation)
			if update := out[i].GetUpdateDocument(); update != nil {
				update.Document.Fields.Fields["personal_result"] = verified
			} else {
				out[i].GetMergeDocument().Document.Fields.Fields["personal_result"] = verified
			}
		}
	}
	return out, nil
}
