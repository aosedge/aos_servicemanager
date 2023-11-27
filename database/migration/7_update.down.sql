CREATE TABLE IF NOT EXISTS network_temp (
    networkID TEXT NOT NULL PRIMARY KEY,
    ip TEXT,
    subnet TEXT,
    vlanID INTEGER
);

INSERT INTO network_temp (networkID, ip, subnet, vlanID)
SELECT networkID, ip, subnet, vlanID
FROM network;

DROP TABLE network;

ALTER TABLE network_temp RENAME TO network;
