package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
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

const teamQuery = `SELECT t.id, t.name, t.create_time,
	(SELECT count(*) FROM users u WHERE u.team_id = t.id),
	(SELECT count(*) FROM models m WHERE m.team_id = t.id),
	(SELECT count(*) FROM datasets d WHERE d.team_id = t.id)
	FROM teams t`

func scanTeam(row scanner) (Team, error) {
	var t Team
	var created int64
	err := row.Scan(&t.ID, &t.Name, &created, &t.Members, &t.Models, &t.Datasets)
	t.Created = time.UnixMilli(created).UTC()
	return t, err
}

// CreateTeam adds an empty team.
func (s *Store) CreateTeam(ctx context.Context, name string) (Team, error) {
	id, err := newID()
	if err != nil {
		return Team{}, err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO teams (id, name, create_time) VALUES (?, ?, ?)", id, name, millis(time.Now()))
	if isUnique(err) {
		return Team{}, ErrTeamNameTaken
	}
	if err != nil {
		return Team{}, fmt.Errorf("store: create team: %w", err)
	}
	return s.GetTeam(ctx, id)
}

// GetTeam returns a team with its counts.
func (s *Store) GetTeam(ctx context.Context, id string) (Team, error) {
	t, err := scanTeam(s.db.QueryRowContext(ctx, teamQuery+" WHERE t.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Team{}, ErrNotFound
	}
	if err != nil {
		return Team{}, fmt.Errorf("store: get team: %w", err)
	}
	return t, nil
}

// ListTeams returns every team by name.
func (s *Store) ListTeams(ctx context.Context) ([]Team, error) {
	rows, err := s.db.QueryContext(ctx, teamQuery+" ORDER BY t.name COLLATE NOCASE, t.id LIMIT 1000")
	if err != nil {
		return nil, fmt.Errorf("store: list teams: %w", err)
	}
	defer rows.Close()
	var out []Team
	for rows.Next() {
		t, err := scanTeam(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list teams: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RenameTeam changes a team's name.
func (s *Store) RenameTeam(ctx context.Context, id, name string) (Team, error) {
	res, err := s.db.ExecContext(ctx, "UPDATE teams SET name = ? WHERE id = ?", name, id)
	if isUnique(err) {
		return Team{}, ErrTeamNameTaken
	}
	if err != nil {
		return Team{}, fmt.Errorf("store: rename team: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Team{}, ErrNotFound
	}
	return s.GetTeam(ctx, id)
}

// DeleteTeam removes a team that has no people, models, examples or
// training runs left. Its webhooks go with it; its audit events stay.
func (s *Store) DeleteTeam(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var used int
	err = tx.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM users WHERE team_id = ?) +
		(SELECT count(*) FROM models WHERE team_id = ?) +
		(SELECT count(*) FROM datasets WHERE team_id = ?) +
		(SELECT count(*) FROM training_jobs WHERE team_id = ?)`, id, id, id, id).Scan(&used)
	if err != nil {
		return fmt.Errorf("store: delete team: %w", err)
	}
	if used > 0 {
		return ErrTeamInUse
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM teams WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("store: delete team: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}
