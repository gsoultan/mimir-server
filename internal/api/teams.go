package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/store"
)

var _ mimirv1connect.TeamServiceHandler = (*Service)(nil)

func (s *Service) ListTeams(ctx context.Context, _ *connect.Request[mimirv1.ListTeamsRequest]) (*connect.Response[mimirv1.ListTeamsResponse], error) {
	ts, err := s.store.ListTeams(ctx)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.ListTeamsResponse{}
	for _, t := range ts {
		res.Teams = append(res.Teams, teamProto(t))
	}
	return connect.NewResponse(res), nil
}

func (s *Service) CreateTeam(ctx context.Context, req *connect.Request[mimirv1.CreateTeamRequest]) (*connect.Response[mimirv1.CreateTeamResponse], error) {
	p, _ := auth.From(ctx)
	name, err := teamName(req.Msg.GetName())
	if err != nil {
		return nil, err
	}
	t, err := s.store.CreateTeam(ctx, name)
	if errors.Is(err, store.ErrTeamNameTaken) {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("There's already a team with that name."))
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "team.create", t.Name, "")
	return connect.NewResponse(&mimirv1.CreateTeamResponse{Team: teamProto(t)}), nil
}

func (s *Service) UpdateTeam(ctx context.Context, req *connect.Request[mimirv1.UpdateTeamRequest]) (*connect.Response[mimirv1.UpdateTeamResponse], error) {
	p, _ := auth.From(ctx)
	name, err := teamName(req.Msg.GetName())
	if err != nil {
		return nil, err
	}
	old, err := s.getTeam(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	t, err := s.store.RenameTeam(ctx, old.ID, name)
	if errors.Is(err, store.ErrTeamNameTaken) {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("There's already a team with that name."))
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "team.rename", t.Name, "was "+old.Name)
	return connect.NewResponse(&mimirv1.UpdateTeamResponse{Team: teamProto(t)}), nil
}

func (s *Service) DeleteTeam(ctx context.Context, req *connect.Request[mimirv1.DeleteTeamRequest]) (*connect.Response[mimirv1.DeleteTeamResponse], error) {
	p, _ := auth.From(ctx)
	t, err := s.getTeam(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	err = s.store.DeleteTeam(ctx, t.ID)
	if errors.Is(err, store.ErrTeamInUse) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("This team still has people, models, examples or training runs. Move or delete them first."))
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "team.delete", t.Name, "")
	return connect.NewResponse(&mimirv1.DeleteTeamResponse{}), nil
}

func (s *Service) getTeam(ctx context.Context, id string) (store.Team, error) {
	missing := connect.NewError(connect.CodeNotFound, errors.New("No such team."))
	if !store.ValidID(id) {
		return store.Team{}, missing
	}
	t, err := s.store.GetTeam(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Team{}, missing
	}
	if err != nil {
		return store.Team{}, s.internal(ctx, err)
	}
	return t, nil
}

func teamName(name string) (string, error) {
	return cleanName("team", name)
}

func teamProto(t store.Team) *mimirv1.Team {
	return &mimirv1.Team{Id: t.ID, Name: t.Name, CreateTime: timestamppb.New(t.Created), Members: int32(t.Members), Models: int32(t.Models), Datasets: int32(t.Datasets)}
}
