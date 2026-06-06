-- Sample SQL statements for the SQL Data API (role.cli.function=sql / POST /sql).
-- One statement per invocation — pipe a single statement to
--   tango --config cli.sql.min.yaml
-- or POST it as JSON to the gateway's /sql endpoint:
--   curl -X POST localhost:8080/sql -H 'Content-Type: application/json' -d '{"sql":"SELECT * FROM event LIMIT 5"}'
--
-- Table name = MongoDB collection name. The target database is the one in the
-- connection URI. Responses are relaxed Extended JSON (SELECT rows carry BSON types).
-- Note: DocumentDB rejects pipeline-form UPDATE (expression assignments); prefer
-- constant assignments like SET vip = true.

SELECT * FROM event WHERE `#event_name` = 'login' ORDER BY `#time` DESC LIMIT 5;

SELECT `#event_name`, COUNT(*) AS n FROM event GROUP BY `#event_name`;

INSERT INTO event (`#event_name`, `#time`) VALUES ('login', '2026-01-01T00:00:00Z');

UPDATE user SET vip = true WHERE `#user_id` = 1;

DELETE FROM event WHERE `#uuid` = 'abc-123';
