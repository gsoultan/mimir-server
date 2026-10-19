package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/gsoultan/storm"
	"github.com/gsoultan/storm/runtime"

	"github.com/gsoultan/mimir-server/internal/store/db/dataset"
	"github.com/gsoultan/mimir-server/internal/store/db/model"
	"github.com/gsoultan/mimir-server/internal/store/db/team"
	"github.com/gsoultan/mimir-server/internal/store/db/trainingjob"
	"github.com/gsoultan/mimir-server/internal/store/db/user"
)

// DefaultTeam is the team the server starts with; everything made before
// teams existed belongs to it.
const DefaultTeam = "01900000-0000-7000-8000-000000000001"

// ErrTeamNameTaken reports a second team with the same name, and
// ErrTeamInUse a team that still has people or things in it.
var (
	ErrTeamNameTaken = errors.New("store: team name already in use")
	ErrTeamInUse     = errors.New("store: team still has members or models")
)

// Team is a group of people who share models.
type Team struct {
	ID       string
	Name     string
	Created  time.Time
	Members  int
	Models   int
	Datasets int
}

// teamCounts is how many people, models and datasets each team has.
type teamCounts struct{ members, models, datasets map[string]int }

// countTeams reads the per-team counts in three grouped queries, whatever
// the number of teams. one narrows them to a single team when set.
func (s *Store) countTeams(ctx context.Context, one string) (teamCounts, error) {
	c := teamCounts{members: map[string]int{}, models: map[string]int{}, datasets: map[string]int{}}
	users, err := user.New().WhereIf(one != "", user.TeamID.Eq(one)).AllPerTeam(ctx, s.ex)
	if err != nil {
		return c, err
	}
	for _, r := range users {
		c.members[r.TeamID.V] = int(r.N)
	}
	models, err := model.New().WhereIf(one != "", model.TeamID.Eq(one)).AllPerTeam(ctx, s.ex)
	if err != nil {
		return c, err
	}
	for _, r := range models {
		c.models[r.TeamID.V] = int(r.N)
	}
	datasets, err := dataset.New().WhereIf(one != "", dataset.TeamID.Eq(one)).AllPerTeam(ctx, s.ex)
	if err != nil {
		return c, err
	}
	for _, r := range datasets {
		c.datasets[r.TeamID.V] = int(r.N)
	}
	return c, nil
}

func (c teamCounts) team(r team.Row) Team {
	return Team{
		ID:       r.ID,
		Name:     r.Name,
		Created:  r.CreateTime.UTC(),
		Members:  c.members[r.ID],
		Models:   c.models[r.ID],
		Datasets: c.datasets[r.ID],
	}
}

// CreateTeam adds an empty team.
func (s *Store) CreateTeam(ctx context.Context, name string) (Team, error) {
	id, err := newID()
	if err != nil {
		return Team{}, err
	}
	n := team.Create()
	n.SetID(id)
	n.SetName(name)
	n.SetCreateTime(now())
	if _, err := n.Insert(ctx, s.ex); err != nil {
		if isUnique(err) {
			return Team{}, ErrTeamNameTaken
		}
		return Team{}, fmt.Errorf("store: create team: %w", err)
	}
	return s.GetTeam(ctx, id)
}

// GetTeam returns a team with its counts.
func (s *Store) GetTeam(ctx context.Context, id string) (Team, error) {
	r, ok, err := team.New().Where(team.ID.Eq(id)).One(ctx, s.ex)
	if err != nil {
		return Team{}, fmt.Errorf("store: get team: %w", err)
	}
	if !ok {
		return Team{}, ErrNotFound
	}
	c, err := s.countTeams(ctx, id)
	if err != nil {
		return Team{}, fmt.Errorf("store: get team: %w", err)
	}
	return c.team(r), nil
}

// ListTeams returns every team by name, ignoring case.
func (s *Store) ListTeams(ctx context.Context) ([]Team, error) {
	rows, err := team.New().Order(team.Name.Asc(), team.ID.Asc()).Limit(1000).All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: list teams: %w", err)
	}
	c, err := s.countTeams(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("store: list teams: %w", err)
	}
	out := make([]Team, 0, len(rows))
	for _, r := range rows {
		out = append(out, c.team(r))
	}
	// Case-insensitive, as the list always was; the database's collation
	// may order upper and lower case apart.
	slices.SortStableFunc(out, func(a, b Team) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

// RenameTeam changes a team's name.
func (s *Store) RenameTeam(ctx context.Context, id, name string) (Team, error) {
	m := team.MutateKey(id)
	m.SetName(name)
	if err := m.Update(ctx, s.ex); err != nil {
		switch {
		case isUnique(err):
			return Team{}, ErrTeamNameTaken
		case errors.Is(err, runtime.ErrNoRow):
			return Team{}, ErrNotFound
		}
		return Team{}, fmt.Errorf("store: rename team: %w", err)
	}
	return s.GetTeam(ctx, id)
}

// DeleteTeam removes a team that has no people, models, examples or
// training runs left. Its webhooks go with it; its audit events stay.
func (s *Store) DeleteTeam(ctx context.Context, id string) error {
	return storm.InTx(ctx, s.ex, func(ex storm.Executor) error {
		// Lock the team so nothing joins it between the count and the delete.
		if _, ok, err := team.New().Where(team.ID.Eq(id)).ForUpdate().One(ctx, ex); err != nil {
			return fmt.Errorf("store: delete team: %w", err)
		} else if !ok {
			return ErrNotFound
		}
		for _, count := range []func() (int64, error){
			func() (int64, error) { return user.New().Where(user.TeamID.Eq(id)).Count(ctx, ex) },
			func() (int64, error) { return model.New().Where(model.TeamID.Eq(id)).Count(ctx, ex) },
			func() (int64, error) { return dataset.New().Where(dataset.TeamID.Eq(id)).Count(ctx, ex) },
			func() (int64, error) { return trainingjob.New().Where(trainingjob.TeamID.Eq(id)).Count(ctx, ex) },
		} {
			n, err := count()
			if err != nil {
				return fmt.Errorf("store: delete team: %w", err)
			}
			if n > 0 {
				return ErrTeamInUse
			}
		}
		if err := team.Delete(ctx, ex, id); err != nil {
			if errors.Is(err, runtime.ErrNoRow) {
				return ErrNotFound
			}
			return fmt.Errorf("store: delete team: %w", err)
		}
		return nil
	})
}
