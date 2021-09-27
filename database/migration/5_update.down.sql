ALTER TABLE services ADD status TEXT;
UPDATE services SET status = 0;

ALTER TABLE config ADD componentsUpdateInfo BLOB;