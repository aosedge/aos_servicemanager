ALTER TABLE config ADD componentsUpdateInfo BLOB;
UPDATE config SET componentsUpdateInfo = "";
