CREATE TABLE actor_state (
  did              text PRIMARY KEY,
  last_seen_notifs timestamptz NOT NULL DEFAULT 'epoch'
);

CREATE TABLE notification (
  id                bigserial PRIMARY KEY,
  did               text NOT NULL,
  reason            text NOT NULL,
  reason_subject    text,
  group_key         text,
  sort_at           timestamptz NOT NULL,
  count             int NOT NULL DEFAULT 0,
  latest_record_uri text NOT NULL,
  latest_record_cid text NOT NULL,
  latest_author     text NOT NULL,
  created_at        timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE notification_participant (
  id              bigserial PRIMARY KEY,
  notification_id bigint NOT NULL REFERENCES notification(id) ON DELETE CASCADE,
  record_uri      text NOT NULL,
  record_cid      text NOT NULL,
  recipient_did   text NOT NULL,
  author          text NOT NULL,
  sort_at         timestamptz NOT NULL
);

CREATE UNIQUE INDEX notification_participant_uri_recipient_idx
  ON notification_participant (record_uri, recipient_did);

CREATE INDEX notification_participant_notification_idx
  ON notification_participant (notification_id);

CREATE UNIQUE INDEX notification_group_idx
  ON notification (did, group_key)
  WHERE group_key IS NOT NULL;

CREATE INDEX notification_did_sort_idx
  ON notification (did, sort_at DESC, id DESC);
