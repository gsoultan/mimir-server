package api

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/store"
)

// archName is what a custom architecture may be called: what workers run
// it under, so it stays plain.
var archName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

const (
	maxCustomLabels = 10000
	maxCustomInput  = 1 << 20
)

// LoadArchitectures reads the described architectures into package arch.
// The server calls it once at start; saving or deleting one reloads them.
func (s *Service) LoadArchitectures(ctx context.Context) error {
	as, err := s.store.ListArchitectures(ctx)
	if err != nil {
		return err
	}
	out := make([]arch.Architecture, len(as))
	for i, a := range as {
		out[i] = customArch(a)
	}
	arch.SetCustom(out)
	return nil
}

func customArch(a store.Architecture) arch.Architecture {
	return arch.Architecture{
		Name: a.Name, Title: a.Title, Description: a.Description,
		Input: arch.Input(a.Input), Output: arch.Output(a.Output), Items: a.Items, Custom: true,
	}
}

// SaveArchitecture describes a model type written in someone's own code,
// or changes the description of one they described.
func (s *Service) SaveArchitecture(ctx context.Context, req *connect.Request[mimirv1.SaveArchitectureRequest]) (*connect.Response[mimirv1.SaveArchitectureResponse], error) {
	p, _ := auth.From(ctx)
	msg := req.Msg
	a := store.Architecture{
		Name:        msg.GetName(),
		Title:       strings.TrimSpace(msg.GetTitle()),
		Description: strings.TrimSpace(msg.GetDescription()),
		Input:       msg.GetInput(),
		Output:      msg.GetOutput(),
		Items:       int(msg.GetItems()),
		CreatedBy:   p.User.ID,
	}
	if a.Title == "" {
		a.Title = a.Name
	}
	switch {
	case !archName.MatchString(a.Name):
		return nil, invalid("Name it in lower case letters, digits and dashes, starting with a letter or digit, at most 40.")
	case len(a.Title) > 100:
		return nil, invalid("Keep the title to 100 characters.")
	case len(a.Description) > 500:
		return nil, invalid("Keep the description to 500 characters.")
	case a.Input != string(arch.Table) && a.Input != string(arch.Image) && a.Input != string(arch.Tensor):
		return nil, invalid("Say what people send: a row of columns, a photo or a list of numbers.")
	case a.Output != string(arch.Class) && a.Output != string(arch.Number) && a.Output != string(arch.Ranked):
		return nil, invalid("Say what it answers: one of a few groups, a number or a ranked list.")
	case a.Output == string(arch.Ranked) && (a.Items < 1 || a.Items > maxTopK):
		return nil, invalid("A ranked list has 1 to %d items.", maxTopK)
	}
	if a.Output != string(arch.Ranked) {
		a.Items = 0
	}
	if b, ok := arch.Lookup(a.Name); ok && !b.Custom {
		return nil, invalid("%s is a built-in model type. Pick another name.", a.Name)
	}

	old, err := s.store.GetArchitecture(ctx, a.Name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		if a, err = s.store.CreateArchitecture(ctx, a); errors.Is(err, store.ErrArchitectureTaken) {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("Someone just described %s. Reload to see it.", a.Name))
		}
	case err == nil:
		if err := s.mayChange(p, old); err != nil {
			return nil, err
		}
		a.ID, a.CreatedBy, a.Created = old.ID, old.CreatedBy, old.Created
		err = s.store.UpdateArchitecture(ctx, a)
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	if err := s.LoadArchitectures(ctx); err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "architecture.save", a.Name, fmt.Sprintf("reads %s, answers %s", a.Input, a.Output))
	return connect.NewResponse(&mimirv1.SaveArchitectureResponse{Architecture: archProto(customArch(a))}), nil
}

// DeleteArchitecture removes a described model type no model uses.
func (s *Service) DeleteArchitecture(ctx context.Context, req *connect.Request[mimirv1.DeleteArchitectureRequest]) (*connect.Response[mimirv1.DeleteArchitectureResponse], error) {
	p, _ := auth.From(ctx)
	a, err := s.store.GetArchitecture(ctx, req.Msg.GetName())
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("No model type called %q was described.", req.Msg.GetName()))
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	if err := s.mayChange(p, a); err != nil {
		return nil, err
	}
	switch err := s.store.DeleteArchitecture(ctx, a); {
	case errors.Is(err, store.ErrArchitectureInUse):
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("Models of type %s still exist. Delete them first.", a.Name))
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return nil, s.internal(ctx, err)
	}
	if err := s.LoadArchitectures(ctx); err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "architecture.delete", a.Name, "")
	return connect.NewResponse(&mimirv1.DeleteArchitectureResponse{}), nil
}

// mayChange says whether p may change or delete a described type: the
// person who described it, or an owner.
func (s *Service) mayChange(p auth.Principal, a store.Architecture) error {
	if p.User.ID == a.CreatedBy || p.Can(auth.SettingsManage) {
		return nil
	}
	return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s was described by someone else. Ask them or an owner to change it, or pick another name.", a.Name))
}

// customSpec fills in what an uploaded model of custom architecture a
// reads and answers, as the uploader says.
func customSpec(m *store.Model, a arch.Architecture, shape []int64, labels []string) error {
	n := 1
	for _, d := range shape {
		if d < 1 || d > maxCustomInput {
			return invalid("Say how many numbers the model reads, like 28 or 3 × 64 × 64.")
		}
		n *= int(d)
		if n > maxCustomInput {
			return invalid("The model reads at most %d numbers.", maxCustomInput)
		}
	}
	if len(shape) == 0 {
		return invalid("Say how many numbers the model reads.")
	}
	switch {
	case a.Answers() == arch.Number:
		if len(labels) > 1 {
			return invalid("A number model has one answer; give it one name or none.")
		}
		if len(labels) == 0 {
			labels = []string{"value"}
		}
	case len(labels) < 2:
		return invalid("List the answers it picks from, at least two.")
	case len(labels) > maxCustomLabels:
		return invalid("At most %d answers.", maxCustomLabels)
	}
	seen := make(map[string]bool, len(labels))
	for i, l := range labels {
		l = strings.TrimSpace(l)
		if l == "" || len(l) > 200 || seen[l] {
			return invalid("Each answer needs a name of up to 200 characters, and no two the same.")
		}
		seen[l], labels[i] = true, l
	}
	m.InputShape, m.Labels = make([]int, len(shape)), labels
	for i, d := range shape {
		m.InputShape[i] = int(d)
	}
	return nil
}
