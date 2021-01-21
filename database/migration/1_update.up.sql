ALTER TABLE config ADD componentsUpdateInfo BLOB;
UPDATE config SET componentsUpdateInfo = "";

ALTER TABLE services ADD allowedConnections TEXT;
UPDATE services SET allowedConnections = "";

ALTER TABLE services ADD exposedPorts TEXT;
UPDATE services SET exposedPorts = "";
