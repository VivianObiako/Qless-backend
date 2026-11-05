-- +goose Up

-- Whether a customer can give up their place from their phone. Off, only staff
-- take someone off the list. Every existing queue keeps the cancel button.
ALTER TABLE queues ADD COLUMN customers_can_leave boolean NOT NULL DEFAULT true;

-- +goose Down

ALTER TABLE queues DROP COLUMN customers_can_leave;
