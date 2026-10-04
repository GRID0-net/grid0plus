package npln

import (
	"context"

	"github.com/n-popescu/grid0plus/internal/ugcstore"
	globalcounterpb "github.com/n-popescu/grid0plus/proto/globalcounter/v1"
)

// globalCounterService answers the one GlobalCounterService call Splatoon 3
// is known to make: GetSeason, for a special Big Run's community golden-egg
// total (see internal/ugcstore/bigrun_eggs.go). The rest stays Unimplemented.
type globalCounterService struct {
	globalcounterpb.UnimplementedGlobalCounterServiceServer
	s *Server
}

func (g *globalCounterService) GetSeason(ctx context.Context, req *globalcounterpb.GetSeasonRequest) (*globalcounterpb.Season, error) {
	if _, ok := CallerFrom(ctx); !ok {
		return nil, ErrUnauthenticated
	}
	count, jobs, err := ugcstore.BigRunEggs(ctx, g.s.store, g.s.tenant)
	if err != nil {
		g.s.log.Error("npln: read the Big Run egg total", "error", err)
		count, jobs = 0, 0
	}
	g.s.log.Info("npln: global counter season read", "name", req.GetName(), "count", count, "jobs", jobs)
	return &globalcounterpb.Season{Name: req.GetName(), Count: count}, nil
}
