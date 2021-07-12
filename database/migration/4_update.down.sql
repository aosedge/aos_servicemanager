CREATE TABLE config_new (operationVersion INTEGER, cursor TEXT);

CREATE TABLE users_new (users TEXT NOT NULL,
					    serviceid TEXT NOT NULL,
					    storageFolder TEXT,
					    stateCheckSum BLOB,
					    PRIMARY KEY(users, serviceid));

INSERT INTO  services_new (users, serviceid, storageFolder, stateCheckSum) 
SELECT users, serviceid, storageFolder, stateCheckSum
FROM  users;

DROP TABLE users_new;

ALTER TABLE users_new RENAME TO users;