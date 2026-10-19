package toyohr

import (
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	toyohrpb "github.com/n-popescu/grid0plus/proto/toyohr/v1"
)

// registerFestService attaches FestService with SelectFestBreakingNews added.
//
// The game calls SelectFestBreakingNews while a fest is open, and the method
// is not in this project's proto. A method missing from a registered service
// never reaches an unknown-service handler: grpc-go answers Unimplemented
// itself, and the game treats that as a hard communication error.
//
// Nintendo's answer is one empty message, five bytes of DATA -- a gRPC frame
// header with length zero. That is a message, not the no-message answer
// GetSaveRecord gives, and the two are not interchangeable.
//
// UNCONFIRMED: the shape of a response that actually carries news.
func registerFestService(srv *grpc.Server, impl *Service) {
	desc := toyohrpb.FestService_ServiceDesc // copy

	streams := make([]grpc.StreamDesc, 0, len(desc.Streams)+1)
	streams = append(streams, desc.Streams...)
	streams = append(streams, grpc.StreamDesc{
		StreamName: "SelectFestBreakingNews",
		Handler:    selectFestBreakingNewsHandler,
	})
	desc.Streams = streams

	srv.RegisterService(&desc, impl)
}

func selectFestBreakingNewsHandler(srv any, stream grpc.ServerStream) error {
	// Read into Empty so the request's fields survive as unknown fields; the
	// size is the only record of what the game asks for.
	req := &emptypb.Empty{}
	if err := stream.RecvMsg(req); err != nil {
		return err
	}
	if s, ok := srv.(*Service); ok {
		s.log.Info("toyohr: SelectFestBreakingNews answered empty",
			"request_bytes", len(req.ProtoReflect().GetUnknown()))
	}
	return stream.SendMsg(&emptypb.Empty{})
}
