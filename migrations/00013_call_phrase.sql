-- +goose Up

-- What being called is named on the screens the room sees: "Now serving",
-- "Now presenting", "Now seeing" or "Now up". A short list rather than a free
-- word, because the phrase appears in several forms that a typed word cannot
-- be bent into; the web app holds the wording for each. Every existing queue
-- keeps saying what it said.
ALTER TABLE queues ADD COLUMN call_phrase text NOT NULL DEFAULT 'SERVING'
    CHECK (call_phrase IN ('SERVING', 'PRESENTING', 'SEEING', 'UP'));

-- +goose Down

ALTER TABLE queues DROP COLUMN call_phrase;
