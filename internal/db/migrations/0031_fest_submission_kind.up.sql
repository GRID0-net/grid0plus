-- What kind of LAN game a submission is: open, pro or tricolor. Each counts in
-- its own fest category. Existing submissions were all open games.
ALTER TABLE bot_fest_submissions ADD COLUMN kind TEXT NOT NULL DEFAULT 'open';
