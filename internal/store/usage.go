package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/gsoultan/storm/runtime"

	"github.com/gsoultan/mimir-server/internal/store/db/trainingjob"
	"github.com/gsoultan/mimir-server/internal/store/db/usage"
	"github.com/gsoultan/mimir-server/internal/store/db/user"
	"github.com/gsoultan/mimir-server/internal/store/tables"
)

// UsageKey names one counter: a month, a person and the key they used.
type UsageKey struct {
	Month   string // "2026-10"
	UserID  string
	KeyID   string // "" for the web app
	KeyName string
}

// Usage is one counter with its owner's name.
type Usage struct {
	UsageKey
	UserName    string
	Email       string
	Predictions int64
}

// AddUsage adds counts in one transaction.
func (s *Store) AddUsage(ctx context.Context, counts map[UsageKey]int64) error {
	return s.inTx(ctx, func(ex runtime.Executor) error {
		for k, n := range counts {
			if _, err := tables.AddUsage.Exec(ctx, ex, k.Month, k.UserID, k.KeyID, k.KeyName, n); err != nil {
				return fmt.Errorf("store: add usage: %w", err)
			}
		}
		return nil
	})
}

// UserUsage returns a person's predictions in a month, keys included.
func (s *Store) UserUsage(ctx context.Context, month, userID string) (int64, error) {
	rows, err := usage.New().
		Where(usage.Month.Eq(month), usage.UserID.Eq(userID)).
		Limit(100000).
		All(ctx, s.ex, nil)
	if err != nil {
		return 0, err
	}
	var n int64
	for _, r := range rows {
		n += r.Predictions
	}
	return n, nil
}

// ListUsage returns a month's counters, busiest first; userID, when set,
// keeps only that person's, and teamID only that team's people.
func (s *Store) ListUsage(ctx context.Context, month, userID, teamID string) ([]Usage, error) {
	q := usage.New().Where(usage.Month.Eq(month)).WhereIf(userID != "", usage.UserID.Eq(userID))
	if teamID != "" {
		people, err := user.New().Where(user.TeamID.Eq(teamID)).Limit(100000).All(ctx, s.ex, nil)
		if err != nil {
			return nil, fmt.Errorf("store: list usage: %w", err)
		}
		if len(people) == 0 {
			return nil, nil
		}
		ids := make([]string, 0, len(people))
		for _, p := range people {
			ids = append(ids, p.ID)
		}
		q = q.Where(usage.UserID.In(ids...))
	}
	rows, err := q.Limit(1000).AllWithUser(ctx, s.ex)
	if err != nil {
		return nil, fmt.Errorf("store: list usage: %w", err)
	}
	out := make([]Usage, 0, len(rows))
	for _, r := range rows {
		out = append(out, Usage{
			UsageKey:    UsageKey{Month: r.Month, UserID: r.UserID, KeyID: r.KeyID, KeyName: r.KeyName},
			UserName:    r.UserName,
			Email:       r.Email,
			Predictions: r.Predictions,
		})
	}
	return out, nil
}

// ActiveJobs counts a person's training runs waiting or learning, whether
// they started them on the web or with one of their keys.
func (s *Store) ActiveJobs(ctx context.Context, email string) (int, error) {
	viaKey := " (" + email + ")" // how a key's runs name their owner
	n, err := trainingjob.New().
		Where(trainingjob.Status.In(int32(JobQueued), int32(JobRunning))).
		Any(
			trainingjob.CreatedBy.Eq(email),
			// Something, then the suffix: a key's name before the owner.
			trainingjob.CreatedBy.Like("_%"+likeEscape(viaKey)),
		).
		Count(ctx, s.ex)
	return int(n), err
}

// likeEscape makes s match itself in a LIKE pattern.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
