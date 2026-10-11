package ugcstore

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	ugcpb "github.com/n-popescu/grid0plus/proto/ugcstore/v1"
)

// screeningService is nn.npln.ugcstore.v1.Screening: reporting a published
// document (a drawing, a locker, a replay), with an optional uploaded
// screenshot. Splatoon 3 links it (Screening.IssueUploadUri is in its
// descriptor pool); unregistered, a report failed on an unknown service.
//
// As with toyohr's UserScreening.CreateReport, there is no moderation queue
// on a server for a group of friends: the useful outcome of a report is that
// the operator hears about it, so it is logged as a warning and echoed back.
type screeningService struct {
	*Service
}

func (s screeningService) CreateReport(ctx context.Context, req *ugcpb.CreateReportRequest) (*ugcpb.Report, error) {
	uid, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}
	r := req.GetReport()
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "no report")
	}
	s.log.Warn("ugcstore: a player reported a post",
		"reporter", uid, "target", r.GetScreeningTarget(), "target_type", r.GetScreeningTargetType(),
		"category", r.GetCategory(), "reason", r.GetReason(), "attachments", len(r.GetAttachmentContext()))
	out := &ugcpb.Report{
		Name:                "tenants/" + s.tenant + "/reports/" + r.GetCategory(),
		Category:            r.GetCategory(),
		Reason:              r.GetReason(),
		LanguageCode:        r.GetLanguageCode(),
		CreateTime:          timestamppb.New(s.now()),
		ScreeningTarget:     r.GetScreeningTarget(),
		ScreeningTargetType: r.GetScreeningTargetType(),
		Context:             r.GetContext(),
		UseAttachment:       r.GetUseAttachment(),
		AttachmentContext:   r.GetAttachmentContext(),
	}
	return out, nil
}

// IssueUploadUri opens an upload for a report's screenshot, the same
// session Ugcstore's own IssueUploadUri opens.
func (s screeningService) IssueUploadUri(ctx context.Context, req *ugcpb.IssueUploadUriRequest) (*ugcpb.IssueUploadUriResponse, error) {
	return s.Service.IssueUploadUri(ctx, req)
}
