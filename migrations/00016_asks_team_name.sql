-- +goose Up

-- Whether the join page asks for a team name rather than a person's. On, a
-- team already waiting or being seen cannot join a second time.
ALTER TABLE queues ADD COLUMN asks_team_name boolean NOT NULL DEFAULT false;

-- +goose Down

ALTER TABLE queues DROP COLUMN asks_team_name;
