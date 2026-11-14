// Copyright (c) 2026 Gembit Soultan Shirazi
// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0 OR PolyForm-Small-Business-1.0.0 OR LicenseRef-Commercial
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.
// It is free to use for personal and other noncommercial purposes, for
// nonprofits, and for companies with fewer than 100 people and under
// 1,000,000 USD in yearly revenue, under the PolyForm Noncommercial and
// PolyForm Small Business licenses. Any other use needs a commercial
// license from Gembit Soultan Shirazi. See the LICENSE and LICENSING.md files
// at the root of this repository.

package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/ask"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/store"
)

// maxShadows is how many shadow answers run at once. A question that
// comes while that many run gets no shadow answer: a trial counts what it
// can, and never slows the live answers.
const maxShadows = 8

// shadowTimeout bounds one shadow answer.
const shadowTimeout = 30 * time.Second

// trialKey is what the caches load a model's version on trial under, so
// it stays loaded beside the live one instead of replacing it.
func trialKey(modelID string) string { return modelID + "/trial" }

// forgetTrial lets the caches drop a model's version on trial.
func (s *Service) forgetTrial(modelID string) {
	s.cache.Forget(trialKey(modelID))
	if s.Pool != nil {
		s.Pool.Forget(trialKey(modelID), "")
	}
}

// question is what a Predict request asked, kept apart from the request
// so a shadow answer can ask it again after the reply has gone.
type question struct {
	raw    []float32 // the numbers sent, when the question was sent so
	fields map[string]string
	photo  []byte
	sound  []byte
}

func questionOf(msg *mimirv1.PredictRequest) question {
	return question{raw: msg.GetInput().GetValues(), fields: msg.GetFields(), photo: msg.GetPhoto(), sound: msg.GetSound()}
}

// trialAsk returns model m as its version on trial answers, and q as
// that version reads it: asked by column name, a photo or a sound, it is
// encoded by the version's own columns, which a keep-learning pass may
// have changed. ok is false when the version can't answer q, such as when
// it is gone or reads another number of values; the live version answers
// then.
func (s *Service) trialAsk(ctx context.Context, m store.Model, a arch.Architecture, q question) (tm store.Model, input []float32, ok bool) {
	v, err := s.store.GetVersion(ctx, m.ID, m.Trial.VersionID)
	if err != nil {
		s.log.WarnContext(ctx, "trial: version unavailable", "model_id", m.ID, "version_id", m.Trial.VersionID, "error", err)
		return tm, nil, false
	}
	tm = m
	tm.VersionID, tm.SHA256, tm.SizeBytes = v.ID, v.SHA256, v.SizeBytes
	tm.InputShape, tm.Labels, tm.Features, tm.Columns = v.InputShape, v.Labels, v.Features, v.Columns
	tm.Accuracy, tm.JobID = v.Accuracy, v.JobID
	input = q.raw
	if len(input) == 0 {
		if input, err = ask.Encode(a, tm.Columns, tm.Features, q.fields, q.photo, q.sound); err != nil {
			return tm, nil, false
		}
	}
	spec := arch.Resolve(a, arch.Spec{InputShape: tm.InputShape, Labels: tm.Labels})
	return tm, input, size(spec.InputShape) == len(input)
}

// shadowAsk has model m's version on trial, in a shadow trial, answer a
// copy of question q in the background, and keeps its answer beside the
// live one, kept as liveID, for the trial's results.
func (s *Service) shadowAsk(m store.Model, a arch.Architecture, q question, live store.Answer, liveID string) {
	if !m.Trial.On() || !m.Trial.Shadow || liveID == "" {
		return
	}
	if s.shadows.Add(1) > maxShadows {
		s.shadows.Add(-1)
		return
	}
	go func() {
		defer s.shadows.Add(-1)
		ctx, cancel := context.WithTimeout(context.Background(), shadowTimeout)
		defer cancel()
		tm, input, ok := s.trialAsk(ctx, m, a, q)
		if !ok {
			return
		}
		spec := arch.Resolve(a, arch.Spec{InputShape: tm.InputShape, Labels: tm.Labels})
		r, err := s.run(ctx, ctx, tm, trialKey(m.ID), a, spec, input)
		if err != nil {
			s.log.DebugContext(ctx, "trial: shadow answer failed", "model_id", m.ID, "error", err)
			return
		}
		sh := store.Answer{ModelID: m.ID, VersionID: tm.VersionID, Time: time.Now(), Number: live.Number, ShadowOf: liveID}
		if live.Number {
			if len(r.Output) == 0 {
				return
			}
			sh.Value = float64(r.Output[0])
		} else {
			top := r.Top(spec.Labels, 1)
			if len(top) == 0 {
				return
			}
			sh.Label, sh.Confidence = top[0].Label, float64(top[0].Probability)
		}
		sh.Agrees = live.Judge(sh.Label, sh.Value)
		s.keepAnswer(sh)
	}()
}

// trialable says why model m's versions can't be tried on its questions,
// or "" when they can: a trial compares kept answers, which only models
// that pick an answer or give a number have.
func trialable(m store.Model, a arch.Architecture) string {
	switch {
	case m.FineTune() || a.Answers() == arch.Written:
		return "Language models can't be tried on part of their questions."
	case a.Answers() == arch.Boxes:
		return "Models that find things in photos can't be tried on part of their questions yet."
	}
	return ""
}

func (s *Service) StartModelTrial(ctx context.Context, req *connect.Request[mimirv1.StartModelTrialRequest]) (*connect.Response[mimirv1.StartModelTrialResponse], error) {
	msg := req.Msg
	m, err := s.get(ctx, msg.GetModelId())
	if err != nil {
		return nil, err
	}
	a, ok := arch.Lookup(m.Architecture)
	if !ok {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("This model's type (%s) isn't available on this server anymore.", m.Architecture))
	}
	if why := trialable(m, a); why != "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(why))
	}
	vid := msg.GetVersionId()
	if !store.ValidID(vid) {
		return nil, versionNotFound(vid)
	}
	v, err := s.store.GetVersion(ctx, m.ID, vid)
	if errors.Is(err, store.ErrVersionNotFound) {
		return nil, versionNotFound(vid)
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	if v.ID == m.VersionID {
		return nil, invalid("Version %d already answers every question. Pick another version to try.", v.Number)
	}
	share := int(msg.GetShare())
	if !msg.GetShadow() && (share < 1 || share > 99) {
		return nil, invalid("Send the share of questions to try it on, from 1 to 99 percent.")
	}
	m, err = s.store.StartTrial(ctx, m.ID, v.ID, share, msg.GetShadow())
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound(msg.GetModelId())
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	how := fmt.Sprintf("trying version %d on %d%% of questions", v.Number, share)
	if msg.GetShadow() {
		how = fmt.Sprintf("trying version %d on a copy of every question", v.Number)
	}
	s.trialChanged(ctx, m, how)
	return connect.NewResponse(&mimirv1.StartModelTrialResponse{Model: toProto(m)}), nil
}

func (s *Service) StopModelTrial(ctx context.Context, req *connect.Request[mimirv1.StopModelTrialRequest]) (*connect.Response[mimirv1.StopModelTrialResponse], error) {
	m, err := s.get(ctx, req.Msg.GetModelId())
	if err != nil {
		return nil, err
	}
	if !m.Trial.On() {
		return connect.NewResponse(&mimirv1.StopModelTrialResponse{Model: toProto(m)}), nil
	}
	m, err = s.store.StopTrial(ctx, m.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound(req.Msg.GetModelId())
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.forgetTrial(m.ID)
	s.trialChanged(ctx, m, "stopped the trial")
	return connect.NewResponse(&mimirv1.StopModelTrialResponse{Model: toProto(m)}), nil
}

// trialChanged records a change to model m's trial, described by how.
func (s *Service) trialChanged(ctx context.Context, m store.Model, how string) {
	p, _ := auth.From(ctx)
	s.audit(ctx, p.Actor(), "model.trial", m.Name, how)
	ev := modelEvent(m, p)
	if m.Trial.On() {
		ev["trial"] = map[string]any{"version_id": m.Trial.VersionID, "share": m.Trial.Share, "shadow": m.Trial.Shadow}
	}
	s.emit(ctx, "model.updated", ev)
	s.log.InfoContext(ctx, "model trial changed", "model_id", m.ID, "trial", how)
}

func (s *Service) GetModelTrial(ctx context.Context, req *connect.Request[mimirv1.GetModelTrialRequest]) (*connect.Response[mimirv1.GetModelTrialResponse], error) {
	m, err := s.get(ctx, req.Msg.GetModelId())
	if err != nil {
		return nil, err
	}
	t := m.Trial
	if !t.On() {
		return connect.NewResponse(&mimirv1.GetModelTrialResponse{}), nil
	}
	tallies, err := s.store.TrialTallies(ctx, m.ID, t.Start)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	side := func(versionID string) (*mimirv1.TrialSide, store.VersionTally) {
		out := &mimirv1.TrialSide{VersionId: versionID}
		if v, err := s.store.GetVersion(ctx, m.ID, versionID); err == nil {
			out.VersionNumber, out.HeldOutAccuracy = int32(v.Number), v.Accuracy
		}
		var n store.VersionTally
		for _, x := range tallies {
			if x.VersionID == versionID {
				n = x
			}
		}
		out.Answers, out.Feedback, out.Correct = n.Answers, n.Feedback, n.Correct
		if n.Feedback > 0 {
			out.Accuracy = float64(n.Correct) / float64(n.Feedback)
		}
		return out, n
	}
	live, ln := side(m.VersionID)
	tried, tn := side(t.VersionID)
	res := &mimirv1.GetModelTrialResponse{
		Trial: toProto(m).GetTrial(), Live: live, Tried: tried,
		EnoughFeedback: ln.Feedback >= qualityMinFeedback && tn.Feedback >= qualityMinFeedback,
	}
	if t.Shadow {
		res.Agree = tn.Agree
		if tn.Shadow > 0 {
			res.AgreeShare = float64(tn.Agree) / float64(tn.Shadow)
		}
	}
	return connect.NewResponse(res), nil
}

func (s *Service) SetNewVersionShare(ctx context.Context, req *connect.Request[mimirv1.SetNewVersionShareRequest]) (*connect.Response[mimirv1.SetNewVersionShareResponse], error) {
	m, err := s.get(ctx, req.Msg.GetModelId())
	if err != nil {
		return nil, err
	}
	share := int(req.Msg.GetShare())
	if share < 0 || share > 99 {
		return nil, invalid("Send 0 to put better versions live at once, or the share of questions to try them on first, from 1 to 99 percent.")
	}
	if share > 0 {
		if a, ok := arch.Lookup(m.Architecture); ok {
			if why := trialable(m, a); why != "" {
				return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(why))
			}
		}
	}
	m, err = s.store.SetNewVersionShare(ctx, m.ID, share)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound(req.Msg.GetModelId())
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	p, _ := auth.From(ctx)
	how := "new versions go live at once"
	if share > 0 {
		how = fmt.Sprintf("new versions are tried on %d%% of questions first", share)
	}
	s.audit(ctx, p.Actor(), "model.update", m.Name, how)
	return connect.NewResponse(&mimirv1.SetNewVersionShareResponse{Model: toProto(m)}), nil
}
