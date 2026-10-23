-- +goose Up
-- +goose StatementBegin
GRANT USAGE, SELECT ON SEQUENCE webhook_events_id_seq TO tablekit_app;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
REVOKE USAGE, SELECT ON SEQUENCE webhook_events_id_seq FROM tablekit_app;
-- +goose StatementEnd
