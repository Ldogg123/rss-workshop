CREATE TABLE feeds (
 id TEXT COLLATE "C" PRIMARY KEY, rss_token TEXT NOT NULL UNIQUE,
 title TEXT COLLATE "C" NOT NULL, url TEXT NOT NULL, recipe TEXT NOT NULL,
 interval BIGINT NOT NULL, enabled BOOLEAN NOT NULL, next_run BIGINT NOT NULL,
 last_attempt BIGINT NOT NULL DEFAULT 0, last_success BIGINT NOT NULL DEFAULT 0,
 error TEXT NOT NULL DEFAULT '', failures BIGINT NOT NULL DEFAULT 0,
 etag BYTEA NOT NULL DEFAULT '', modified BYTEA NOT NULL DEFAULT '', version BIGINT NOT NULL DEFAULT 1,
 last_changed BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX feeds_due ON feeds(enabled,next_run);
CREATE TABLE items (
 feed_id TEXT NOT NULL REFERENCES feeds(id) ON DELETE CASCADE, key BYTEA NOT NULL,
 guid TEXT NOT NULL, title TEXT NOT NULL, url TEXT NOT NULL, html TEXT NOT NULL, image TEXT NOT NULL,
 content_full TEXT NOT NULL DEFAULT '',
 published BIGINT NOT NULL, first_seen BIGINT NOT NULL, last_seen BIGINT NOT NULL,
 last_changed BIGINT NOT NULL DEFAULT 0,
 -- The existing SHA-256 GUID fits a B-tree key even when the original URL
 -- exceeds PostgreSQL's index-entry size limit. BYTEA keeps opaque key bytes
 -- intact and orders them bytewise, matching SQLite's binary text collation.
 PRIMARY KEY(feed_id,guid)
);
CREATE TABLE runs (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 feed_id TEXT NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
 ended BIGINT NOT NULL, status INTEGER NOT NULL, count INTEGER NOT NULL, error TEXT NOT NULL,
 diagnostics TEXT NOT NULL DEFAULT ''
);
CREATE INDEX runs_feed_history ON runs(feed_id,id DESC);
CREATE TABLE filters (
 id TEXT COLLATE "C" PRIMARY KEY, name TEXT COLLATE "C" NOT NULL, rules TEXT NOT NULL
);
CREATE TABLE feed_filters (
 feed_id TEXT NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
 filter_id TEXT NOT NULL REFERENCES filters(id),
 position INTEGER NOT NULL,
 PRIMARY KEY(feed_id,filter_id)
);
CREATE INDEX feed_filters_filter ON feed_filters(filter_id);
