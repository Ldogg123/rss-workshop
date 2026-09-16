CREATE TABLE schema_version(version INTEGER NOT NULL);
INSERT INTO schema_version VALUES(5);
CREATE TABLE feeds (
 id TEXT PRIMARY KEY, rss_token TEXT NOT NULL UNIQUE, title TEXT NOT NULL, url TEXT NOT NULL, recipe TEXT NOT NULL,
 interval INTEGER NOT NULL, enabled INTEGER NOT NULL, next_run INTEGER NOT NULL,
 last_attempt INTEGER NOT NULL DEFAULT 0, last_success INTEGER NOT NULL DEFAULT 0,
 error TEXT NOT NULL DEFAULT '', failures INTEGER NOT NULL DEFAULT 0,
 etag TEXT NOT NULL DEFAULT '', modified TEXT NOT NULL DEFAULT '', version INTEGER NOT NULL DEFAULT 1,
 last_changed INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX feeds_due ON feeds(enabled,next_run);
CREATE TABLE items (
 feed_id TEXT NOT NULL REFERENCES feeds(id) ON DELETE CASCADE, key TEXT NOT NULL,
 guid TEXT NOT NULL, title TEXT NOT NULL, url TEXT NOT NULL, html TEXT NOT NULL, image TEXT NOT NULL,
 content_full TEXT NOT NULL DEFAULT '',
 published INTEGER NOT NULL, first_seen INTEGER NOT NULL, last_seen INTEGER NOT NULL,
 last_changed INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(feed_id,key)
);
CREATE TABLE runs (
 id INTEGER PRIMARY KEY, feed_id TEXT NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
 ended INTEGER NOT NULL, status INTEGER NOT NULL, count INTEGER NOT NULL, error TEXT NOT NULL,
 diagnostics TEXT NOT NULL DEFAULT ''
);
CREATE INDEX runs_feed_history ON runs(feed_id,id DESC);
CREATE TABLE filters (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, rules TEXT NOT NULL
);
CREATE TABLE feed_filters (
 feed_id TEXT NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
 filter_id TEXT NOT NULL REFERENCES filters(id),
 position INTEGER NOT NULL,
 PRIMARY KEY(feed_id,filter_id)
);
CREATE INDEX feed_filters_filter ON feed_filters(filter_id);
