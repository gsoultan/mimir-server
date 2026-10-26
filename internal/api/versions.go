package api

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/store"
)

func (s *Service) ListModelVersions(ctx context.Context, req *connect.Request[mimirv1.ListModelVersionsRequest]) (*connect.Response[mimirv1.ListModelVersionsResponse], error) {
	m, err := s.get(ctx, req.Msg.GetModelId())
	if err != nil {
		return nil, err
	}
	vs, err := s.store.ListVersions(ctx, m.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound(m.ID)
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	out := make([]*mimirv1.ModelVersion, len(vs))
	for i, v := range vs {
		out[i] = versionProto(v)
	}
	return connect.NewResponse(&mimirv1.ListModelVersionsResponse{Versions: out}), nil
}

func (s *Service) UseModelVersion(ctx context.Context, req *connect.Request[mimirv1.UseModelVersionRequest]) (*connect.Response[mimirv1.UseModelVersionResponse], error) {
	old, err := s.get(ctx, req.Msg.GetModelId())
	if err != nil {
		return nil, err
	}
	vid := req.Msg.GetVersionId()
	if !store.ValidID(vid) {
		return nil, versionNotFound(vid)
	}
	v, err := s.store.GetVersion(ctx, old.ID, vid)
	if errors.Is(err, store.ErrVersionNotFound) {
		return nil, versionNotFound(vid)
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	if v.ID == old.VersionID {
		return connect.NewResponse(&mimirv1.UseModelVersionResponse{Model: toProto(old)}), nil
	}
	m, err := s.store.UseVersion(ctx, old.ID, v.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound(old.ID)
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	// Workers and the cache load weights by version, so the next question
	// gets these; warming them now spares it the wait.
	s.WarmModel(m.ID)
	p, _ := auth.From(ctx)
	s.audit(ctx, p.Actor(), "model.version", m.Name, fmt.Sprintf("now answers with version %d", v.Number))
	ev := modelEvent(m, p)
	ev["version"] = map[string]any{"id": v.ID, "number": v.Number}
	s.emit(ctx, "model.updated", ev)
	s.log.InfoContext(ctx, "model version put live", "model_id", m.ID, "version", v.Number)
	return connect.NewResponse(&mimirv1.UseModelVersionResponse{Model: toProto(m)}), nil
}

func versionNotFound(id string) error {
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("No version with id %q.", id))
}

func versionProto(v store.Version) *mimirv1.ModelVersion {
	return &mimirv1.ModelVersion{
		Id:         v.ID,
		Number:     int32(v.Number),
		Source:     mimirv1.ModelVersionSource(v.Source),
		CreateTime: timestamppb.New(v.Created),
		SizeBytes:  v.SizeBytes,
		Sha256:     v.SHA256,
		Accuracy:   v.Accuracy,
		JobId:      v.JobID,
		Note:       v.Note,
		Live:       v.Live,
	}
}
