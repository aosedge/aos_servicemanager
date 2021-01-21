CREATE TABLE config_new (operationVersion INTEGER, cursor TEXT);

INSERT INTO config_new (operationVersion, cursor)
SELECT operationVersion, cursor
FROM config;

DROP TABLE config;

ALTER TABLE config_new RENAME TO config;

CREATE TABLE services_new (id TEXT NOT NULL PRIMARY KEY,
                           aosVersion INTEGER,
                           serviceProvider TEXT,
                           path TEXT,
                           unit TEXT,
                           uid INTEGER,
                           gid INTEGER,
                           hostName TEXT,
                           permissions TEXT,
                           state INTEGER,
                           status INTEGER,
                           startat TIMESTAMP,
                           ttl INTEGER,
                           alertRules TEXT,
                           ulLimit INTEGER,
                           dlLimit INTEGER,
                           ulSpeed INTEGER,
                           dlSpeed INTEGER,
                           storageLimit INTEGER,
                           stateLimit INTEGER,
                           layerList TEXT,
                           deviceResources TEXT,
                           boardResources TEXT,
                           vendorVersion TEXT,
                           description TEXT);

INSERT INTO  services_new (id, aosVersion, serviceProvider, path, unit, uid, gid, hostName, permissions,
        state, status, startat, ttl, alertRules, ulLimit, dlLimit, ulSpeed, dlSpeed,
        storageLimit, stateLimit, layerList, deviceResources, boardResources, vendorVersion,
        description)
SELECT id, aosVersion, serviceProvider, path, unit, uid, gid, hostName, permissions,
        state, status, startat, ttl, alertRules, ulLimit, dlLimit, ulSpeed, dlSpeed,
        storageLimit, stateLimit, layerList, deviceResources, boardResources, vendorVersion,
        description
FROM services;

DROP TABLE services;

ALTER TABLE services_new RENAME TO services;