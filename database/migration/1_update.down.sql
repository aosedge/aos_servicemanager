CREATE TABLE config_new (operationVersion INTEGER, cursor TEXT);

INSERT INTO config_new (operationVersion, cursor)
SELECT operationVersion, cursor
FROM config;

DROP TABLE config;

ALTER TABLE config_new RENAME TO config;
