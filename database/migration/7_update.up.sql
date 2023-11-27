ALTER TABLE network ADD vlanIfName TEXT;
UPDATE network SET vlanIfName = "";
