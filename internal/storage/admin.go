package storage

import (
	"context"
	"fmt"
	"time"
)

// AdminStats is the operator's view of the whole service: counts and queue
// names only, never a customer's name.
type AdminStats struct {
	Businesses      int          `json:"businesses"`
	Queues          int          `json:"queues"`
	QueuesLast7d    int          `json:"queuesLast7d"`
	QueuesLast30d   int          `json:"queuesLast30d"`
	ActiveQueues7d  int          `json:"activeQueues7d"`
	JoinsLast7d     int          `json:"joinsLast7d"`
	JoinsLast30d    int          `json:"joinsLast30d"`
	ServedLast30d   int          `json:"servedLast30d"`
	WaitingNow      int          `json:"waitingNow"`
	PushSubscribers int          `json:"pushSubscribers"`
	JoinsByDay      []DayCount   `json:"joinsByDay"`
	TopQueues       []QueueUsage `json:"topQueues"`
}

type DayCount struct {
	Day   string `json:"day"`
	Joins int    `json:"joins"`
}

type QueueUsage struct {
	Name       string     `json:"name"`
	Slug       string     `json:"slug"`
	CreatedAt  time.Time  `json:"createdAt"`
	ArchivedAt *time.Time `json:"archivedAt"`
	Joins30d   int        `json:"joins30d"`
	LastJoinAt *time.Time `json:"lastJoinAt"`
}

func (s *Store) AdminStats(ctx context.Context) (AdminStats, error) {
	var st AdminStats
	err := s.pool.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM owners),
		        (SELECT count(*) FROM queues),
		        (SELECT count(*) FROM queues WHERE created_at > now() - interval '7 days'),
		        (SELECT count(*) FROM queues WHERE created_at > now() - interval '30 days'),
		        (SELECT count(DISTINCT queue_id) FROM queue_entries WHERE joined_at > now() - interval '7 days'),
		        (SELECT count(*) FROM queue_entries WHERE joined_at > now() - interval '7 days'),
		        (SELECT count(*) FROM queue_entries WHERE joined_at > now() - interval '30 days'),
		        (SELECT count(*) FROM queue_entries WHERE status = 'ATTENDED' AND completed_at > now() - interval '30 days'),
		        (SELECT count(*) FROM queue_entries WHERE status IN ('WAITING', 'SERVING')),
		        (SELECT count(*) FROM push_subscriptions)`,
	).Scan(&st.Businesses, &st.Queues, &st.QueuesLast7d, &st.QueuesLast30d, &st.ActiveQueues7d,
		&st.JoinsLast7d, &st.JoinsLast30d, &st.ServedLast30d, &st.WaitingNow, &st.PushSubscribers)
	if err != nil {
		return AdminStats{}, fmt.Errorf("admin totals: %w", err)
	}

	// Every one of the last 14 days, zeros included, so a quiet day shows as one.
	rows, err := s.pool.Query(ctx,
		`SELECT to_char(d, 'YYYY-MM-DD'), count(e.id)
		   FROM generate_series(current_date - 13, current_date, interval '1 day') d
		   LEFT JOIN queue_entries e ON e.joined_at >= d AND e.joined_at < d + interval '1 day'
		  GROUP BY d ORDER BY d`)
	if err != nil {
		return AdminStats{}, fmt.Errorf("admin joins by day: %w", err)
	}
	for rows.Next() {
		var d DayCount
		if err := rows.Scan(&d.Day, &d.Joins); err != nil {
			rows.Close()
			return AdminStats{}, fmt.Errorf("scan day: %w", err)
		}
		st.JoinsByDay = append(st.JoinsByDay, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return AdminStats{}, fmt.Errorf("iterate days: %w", err)
	}

	rows, err = s.pool.Query(ctx,
		`SELECT q.name, q.slug, q.created_at, q.archived_at,
		        count(e.id) FILTER (WHERE e.joined_at > now() - interval '30 days'),
		        max(e.joined_at)
		   FROM queues q
		   LEFT JOIN queue_entries e ON e.queue_id = q.id
		  GROUP BY q.id
		  ORDER BY 5 DESC, q.created_at DESC
		  LIMIT 25`)
	if err != nil {
		return AdminStats{}, fmt.Errorf("admin top queues: %w", err)
	}
	defer rows.Close()
	st.TopQueues = []QueueUsage{}
	for rows.Next() {
		var u QueueUsage
		if err := rows.Scan(&u.Name, &u.Slug, &u.CreatedAt, &u.ArchivedAt, &u.Joins30d, &u.LastJoinAt); err != nil {
			return AdminStats{}, fmt.Errorf("scan queue usage: %w", err)
		}
		st.TopQueues = append(st.TopQueues, u)
	}
	if err := rows.Err(); err != nil {
		return AdminStats{}, fmt.Errorf("iterate queue usage: %w", err)
	}
	return st, nil
}

// Ping is one round trip to the database, for the admin health line.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}
