-- +goose Up

-- What a customer has told the counter about where they are.
--
-- Three answers, and the customer gives them from the pass: on the way when
-- they are heading back, here when they have arrived, and hold when they have
-- been called and need a moment. It lives on the entry rather than on the
-- customer because it is an answer about this visit; a new number starts with
-- nothing said. Nullable, because most customers never say anything and the
-- counter reads silence as "no news", not as absent.
CREATE TYPE entry_presence AS ENUM ('ON_THE_WAY', 'HERE', 'HOLD');

ALTER TABLE queue_entries ADD COLUMN presence entry_presence;
ALTER TABLE queue_entries ADD COLUMN presence_at timestamptz;

-- +goose Down

ALTER TABLE queue_entries DROP COLUMN presence_at;
ALTER TABLE queue_entries DROP COLUMN presence;
DROP TYPE entry_presence;
