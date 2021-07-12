ALTER TABLE users ADD overrideEnvVars TEXT;
UPDATE users SET overrideEnvVars = "";