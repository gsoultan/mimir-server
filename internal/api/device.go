package api

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/store"
)

// MaxDeviceBytes is the largest model people may download to answer with
// in their browser.
const MaxDeviceBytes = 50 << 20

// deviceRefusal says why model m can't run in a browser, or "" if it can.
func deviceRefusal(m store.Model) string {
	a, ok := arch.Lookup(m.Architecture)
	switch {
	case !ok:
		return fmt.Sprintf("This model's type (%s) isn't available on this server anymore.", m.Architecture)
	case a.Answers() == arch.Written:
		return "Language models run on the server only for now."
	case a.Custom:
		return "Models in your own code run on the workers that have it, not in a browser."
	case m.SizeBytes > MaxDeviceBytes:
		return fmt.Sprintf("This model is %d MB; a browser takes models of up to %d MB.", m.SizeBytes>>20, MaxDeviceBytes>>20)
	}
	return ""
}

// GetDeviceModel downloads a model someone allowed to run on devices.
func (s *Service) GetDeviceModel(ctx context.Context, req *connect.Request[mimirv1.GetDeviceModelRequest]) (*connect.Response[mimirv1.GetDeviceModelResponse], error) {
	m, err := s.get(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if !m.OnDevice {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("This model runs on the server only. Someone who can change it can let it run on devices on its page."))
	}
	if why := deviceRefusal(m); why != "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(why))
	}
	w, err := s.store.Weights(m.ID)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	return connect.NewResponse(&mimirv1.GetDeviceModelResponse{Model: toProto(m), Weights: w}), nil
}
