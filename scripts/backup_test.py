#!/usr/bin/env python3
"""Standard-library backup tests; opt into isolated Docker fixtures via RSS_BACKUP_DOCKER_TEST=1."""
import contextlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import sys
import tempfile
import time
import unittest
from unittest import mock
import uuid

# Docker ownership tests run as root; do not leave root-owned import caches in
# the checkout used for ordinary development checks.
sys.dont_write_bytecode = True
import backup

ROOT = Path(__file__).resolve().parents[1]


def fixture_database(path, *, schema_version=3):
    db = sqlite3.connect(path)
    schema = (ROOT / "internal/store/schema.sql" if schema_version == 3 else
              ROOT / f"internal/store/testdata/schema_v{schema_version}.sql")
    db.executescript(schema.read_text())
    recipe = json.dumps({"mode": "static", "type": "css", "items": "article", "title": {"selector": "h2"}})
    db.execute("INSERT INTO feeds(id,rss_token,title,url,recipe,interval,enabled,next_run) VALUES(?,?,?,?,?,60,0,0)",
               ("fixture-feed", "fixture-token", "Backup fixture", "https://example.org/", recipe))
    db.execute("INSERT INTO items VALUES(?,?,?,?,?,?,?,?,?,?)",
               ("fixture-feed", "fixture-key", "fixture-guid", "Saved story", "https://example.org/story",
                "<p>Saved content</p>", "", 123, 124, 125))
    db.execute("INSERT INTO runs(feed_id,ended,status,count,error) VALUES(?,126,200,1,'')", ("fixture-feed",))
    db.commit()
    return db


def make_backup(source, destination):
    destination.mkdir()
    info = backup.snapshot_database(source, destination / "rss.db", include_schema=True)
    (destination / "manifest.json").write_text(json.dumps({"format": backup.FORMAT, "version": 1,
        "schema_version": info["schema_version"], "counts": info["counts"],
        "sha256": backup.checksum(destination / "rss.db")}))


class BackupTests(unittest.TestCase):
    def assert_backup_rejected_before_restore(self, root, message):
        with self.assertRaisesRegex(ValueError, message):
            backup.verify_backup(root / "backup")
        with mock.patch.object(backup, "docker") as docker, mock.patch.object(backup.os, "geteuid", return_value=0):
            with self.assertRaisesRegex(ValueError, message):
                backup.restore_directory(root / "backup", root / "restored")
            with self.assertRaisesRegex(ValueError, message):
                backup.restore_volume(root / "backup", "fixture-new-volume", "fixture-image")
            docker.assert_not_called()
        self.assertFalse((root / "restored").exists())

    def test_current_backup_retains_wal_diagnostics_through_restore(self):
        diagnostics = json.dumps({"mode": "static", "selected_items": 1, "saved_items": 1,
                                  "warnings": ["fixture diagnostic"]})
        with tempfile.TemporaryDirectory() as work:
            root = Path(work)
            source = root / "source"
            source.mkdir()
            with contextlib.closing(fixture_database(source / "rss.db")) as db:
                self.assertEqual(db.execute("SELECT version FROM schema_version").fetchall(), [(3,)])
                db.execute("PRAGMA journal_mode=WAL")
                db.execute("PRAGMA wal_autocheckpoint=0")
                db.execute("UPDATE runs SET diagnostics=?", (diagnostics,))
                db.commit()
                self.assertGreater((source / "rss.db-wal").stat().st_size, 0)

                def copy_stopped_fixture(*args):
                    self.assertEqual(args[:2], ("cp", "fixture-container:/data/."))
                    for path in source.iterdir():
                        shutil.copyfile(path, Path(args[2]) / path.name)

                mount = {"Type": "bind", "Source": str(source)}
                with mock.patch.object(backup, "inspect_container", return_value=("fixture-container", False, mount)), \
                        mock.patch.object(backup, "docker", side_effect=copy_stopped_fixture) as docker:
                    manifest = backup.backup_locked("fixture-container", root / "backup")
                    self.assertEqual(docker.call_count, 1)
            self.assertEqual(manifest["version"], 1)
            self.assertEqual(manifest["schema_version"], 3)
            self.assertEqual(manifest["counts"], {"feeds": 1, "items": 1, "runs": 1})
            self.assertEqual(backup.verify_backup(root / "backup"), manifest)
            self.assertFalse((root / "backup/rss.db-wal").exists())
            with mock.patch.object(backup.os, "geteuid", return_value=0), mock.patch.object(backup.os, "fchown"):
                backup.restore_directory(root / "backup", root / "restored")
            self.assertEqual(backup.checksum(root / "restored/rss.db"), manifest["sha256"])
            with contextlib.closing(sqlite3.connect(root / "restored/rss.db")) as restored:
                self.assertEqual(restored.execute("SELECT version FROM schema_version").fetchall(), [(3,)])
                self.assertEqual(restored.execute("SELECT diagnostics FROM runs").fetchall(), [(diagnostics,)])
                self.assertEqual(restored.execute("SELECT name FROM sqlite_master WHERE type='index' "
                                                  "AND name='runs_feed_history'").fetchall(), [("runs_feed_history",)])

    def test_schema_one_backup_verifies_and_restores_without_migration(self):
        with tempfile.TemporaryDirectory() as work:
            root = Path(work)
            fixture_database(root / "source.db", schema_version=1).close()
            make_backup(root / "source.db", root / "backup")
            manifest = backup.verify_backup(root / "backup")
            self.assertEqual((manifest["version"], manifest["schema_version"]), (1, 1))
            self.assertEqual(manifest["counts"], {"feeds": 1, "items": 1, "runs": 1})
            with mock.patch.object(backup.os, "geteuid", return_value=0), mock.patch.object(backup.os, "fchown"):
                backup.restore_directory(root / "backup", root / "restored")
            with contextlib.closing(sqlite3.connect(root / "restored/rss.db")) as restored:
                self.assertEqual(restored.execute("SELECT version FROM schema_version").fetchall(), [(1,)])
                self.assertNotIn("diagnostics", [row[1] for row in restored.execute("PRAGMA table_info(runs)")])
                self.assertEqual(restored.execute("SELECT feed_id,ended,status,count,error FROM runs").fetchall(),
                                 [("fixture-feed", 126, 200, 1, "")])

    def test_schema_two_backup_restores_diagnostics_without_migration(self):
        with tempfile.TemporaryDirectory() as work:
            root = Path(work)
            diagnostic = json.dumps({"mode": "static", "outcome": "success"})
            with contextlib.closing(fixture_database(root / "source.db", schema_version=2)) as db:
                db.execute("UPDATE runs SET diagnostics=?", (diagnostic,))
                db.commit()
            make_backup(root / "source.db", root / "backup")
            manifest = backup.verify_backup(root / "backup")
            self.assertEqual((manifest["version"], manifest["schema_version"]), (1, 2))
            with mock.patch.object(backup.os, "geteuid", return_value=0), mock.patch.object(backup.os, "fchown"):
                backup.restore_directory(root / "backup", root / "restored")
            with contextlib.closing(sqlite3.connect(root / "restored/rss.db")) as restored:
                self.assertEqual(restored.execute("SELECT version FROM schema_version").fetchall(), [(2,)])
                self.assertEqual(restored.execute("SELECT diagnostics FROM runs").fetchall(), [(diagnostic,)])
                self.assertEqual(backup.database_info(root / "restored/rss.db"), manifest["counts"])

    def test_manifest_schema_must_match_database_before_restore(self):
        for version in (1, 2, 3):
            with self.subTest(schema_version=version), tempfile.TemporaryDirectory() as work:
                root = Path(work)
                fixture_database(root / "source.db", schema_version=version).close()
                make_backup(root / "source.db", root / "backup")
                path = root / "backup/manifest.json"
                manifest = json.loads(path.read_text())
                manifest["schema_version"] = 2 if version == 1 else 1
                path.write_text(json.dumps(manifest))
                self.assert_backup_rejected_before_restore(root, "metadata does not match")

    def test_unsupported_manifest_schema_rejected_before_restore(self):
        for version in (4, 0, None, True, "3", 3.0):
            with self.subTest(schema_version=version), tempfile.TemporaryDirectory() as work:
                root = Path(work)
                fixture_database(root / "source.db").close()
                make_backup(root / "source.db", root / "backup")
                path = root / "backup/manifest.json"
                manifest = json.loads(path.read_text())
                manifest["schema_version"] = version
                path.write_text(json.dumps(manifest))
                self.assert_backup_rejected_before_restore(root, "unsupported backup database schema")

    def test_future_database_schema_rejected_before_snapshot_or_restore(self):
        with tempfile.TemporaryDirectory() as work:
            root = Path(work)
            fixture_database(root / "source.db").close()
            make_backup(root / "source.db", root / "backup")
            with contextlib.closing(sqlite3.connect(root / "backup/rss.db")) as db:
                db.execute("UPDATE schema_version SET version=4")
                db.commit()
            path = root / "backup/manifest.json"
            manifest = json.loads(path.read_text())
            manifest["sha256"] = backup.checksum(root / "backup/rss.db")
            path.write_text(json.dumps(manifest))
            self.assert_backup_rejected_before_restore(root, "unsupported database schema")
            with self.assertRaisesRegex(ValueError, "unsupported database schema"):
                backup.snapshot_database(root / "backup/rss.db", root / "future-snapshot.db")
            self.assertFalse((root / "future-snapshot.db").exists())
            manifest["schema_version"] = 4
            path.write_text(json.dumps(manifest))
            self.assert_backup_rejected_before_restore(root, "unsupported backup database schema")

    def test_database_requires_exactly_one_supported_schema_row(self):
        for versions in ([], [1, 1], [1, 2], [2, 2], [0], ["future"], [2.5]):
            with self.subTest(versions=versions), tempfile.TemporaryDirectory() as work:
                root = Path(work)
                with contextlib.closing(fixture_database(root / "source.db")) as db:
                    db.execute("DELETE FROM schema_version")
                    db.executemany("INSERT INTO schema_version(version) VALUES(?)", [(v,) for v in versions])
                    db.commit()
                with self.assertRaisesRegex(ValueError, "unsupported database schema"):
                    backup.snapshot_database(root / "source.db", root / "snapshot.db")
                self.assertFalse((root / "snapshot.db").exists())

    def test_future_backup_format_rejected_before_restore(self):
        with tempfile.TemporaryDirectory() as work:
            root = Path(work)
            fixture_database(root / "source.db").close()
            make_backup(root / "source.db", root / "backup")
            path = root / "backup/manifest.json"
            manifest = json.loads(path.read_text())
            manifest["version"] = 2
            path.write_text(json.dumps(manifest))
            self.assert_backup_rejected_before_restore(root, "unsupported backup format")

    def test_whitespace_database_url_keeps_sqlite_backup_available(self):
        def inspect_sqlite(*args):
            if args[0] == "inspect":
                if "{{.Id}}" in args:
                    return b"fixture-container\n"
                if "{{json .Config.Env}}" in args:
                    return json.dumps(["DATA_DIR=/data", "DATABASE_URL= \t\r\n "]).encode()
                if "{{json .State}}" in args:
                    return b'{"Running":true,"Paused":false,"Restarting":false}'
                if "{{json .Mounts}}" in args:
                    return b'[{"Destination":"/data","Type":"volume","Name":"fixture-data"}]'
            if args[0] == "ps":
                return b"fixture-container\n"
            self.fail("unexpected Docker command")

        with mock.patch.object(backup, "docker", side_effect=inspect_sqlite):
            self.assertEqual(backup.inspect_container("fixture-container"),
                             ("fixture-container", True, {"Destination": "/data", "Type": "volume", "Name": "fixture-data"}))

    def test_bind_mount_inspection_and_other_writer_refusal(self):
        selected = {"Destination": "/data", "Type": "bind", "Source": "/srv/rss-workshop/data"}
        other = {"Destination": "/work", "Type": "bind", "Source": "/srv/rss-workshop/data-old"}

        def inspect(*args):
            if args[0] == "ps":
                return b"fixture-container\nother-container\n"
            self.assertEqual(args[0], "inspect")
            if "{{.Id}}" in args:
                return b"fixture-container\n"
            if "{{json .Config.Env}}" in args:
                return b'["DATA_DIR=/data"]'
            if "{{json .State}}" in args:
                return b'{"Running":true}'
            if "{{json .Mounts}}" in args:
                return json.dumps([other if "other-container" in args else selected]).encode()
            self.fail("unexpected Docker command")

        with mock.patch.object(backup, "docker", side_effect=inspect):
            self.assertEqual(backup.inspect_container("fixture-container"), ("fixture-container", True, selected))
            for source in ("/srv/rss-workshop/data", "/srv/rss-workshop", "/srv/rss-workshop/data/child"):
                other["Source"] = source
                with self.assertRaisesRegex(ValueError, "another running container"):
                    backup.inspect_container("fixture-container")

    def test_storage_overlap_alias_and_named_volume_bind(self):
        with tempfile.TemporaryDirectory() as work:
            root = Path(work)
            (root / "data").mkdir()
            (root / "alias").symlink_to(root / "data", target_is_directory=True)
            first = {"Type": "bind", "Source": str(root / "data")}
            self.assertTrue(backup.mounts_overlap(first, {"Type": "bind", "Source": str(root / "alias")}))
            first = {"Type": "volume", "Name": "fixture-volume", "Source": str(root / "data")}
            self.assertTrue(backup.mounts_overlap(first, {"Type": "bind", "Source": str(root)}))
            self.assertTrue(backup.mounts_overlap(first, {"Type": "volume", "Name": "fixture-volume"}))
            self.assertFalse(backup.mounts_overlap(first, {"Type": "volume", "Name": "unrelated"}))

    def test_directory_restore_verified_and_no_overwrite(self):
        with tempfile.TemporaryDirectory() as work:
            root = Path(work)
            fixture_database(root / "source.db").close()
            make_backup(root / "source.db", root / "backup")
            with mock.patch.object(backup.os, "geteuid", return_value=0), mock.patch.object(backup.os, "fchown") as chown:
                manifest = backup.restore_directory(root / "backup", root / "restored")
                self.assertEqual(manifest["sha256"], backup.checksum(root / "restored/rss.db"))
                self.assertEqual(backup.database_info(root / "restored/rss.db", immutable=True), manifest["counts"])
                self.assertEqual((root / "restored").stat().st_mode & 0o777, 0o700)
                self.assertEqual((root / "restored/rss.db").stat().st_mode & 0o777, 0o600)
                self.assertEqual([call.args[1:] for call in chown.call_args_list], [(65532, 65532), (65532, 65532)])
                with self.assertRaisesRegex(ValueError, "existing directory"):
                    backup.restore_directory(root / "backup", root / "restored")
                (root / "empty").mkdir()
                with self.assertRaisesRegex(ValueError, "existing directory"):
                    backup.restore_directory(root / "backup", root / "empty")
                (root / "link").symlink_to(root / "unused")
                with self.assertRaisesRegex(ValueError, "symlink"):
                    backup.restore_directory(root / "backup", root / "link")
                (root / "parent-link").symlink_to(root / "empty", target_is_directory=True)
                with self.assertRaises(OSError):
                    backup.restore_directory(root / "backup", root / "parent-link/new")
                self.assertFalse((root / "empty/new").exists())
                with self.assertRaisesRegex(ValueError, "path components"):
                    backup.restore_directory(root / "backup", root / "empty/../elsewhere")

    def test_directory_restore_refuses_corruption_and_missing_permissions_before_creation(self):
        with tempfile.TemporaryDirectory() as work:
            root = Path(work)
            fixture_database(root / "source.db").close()
            make_backup(root / "source.db", root / "backup")
            with mock.patch.object(backup.os, "geteuid", return_value=1000):
                with self.assertRaisesRegex(ValueError, "requires root"):
                    backup.restore_directory(root / "backup", root / "restored")
            self.assertFalse((root / "restored").exists())
            with (root / "backup/rss.db").open("ab") as stream:
                stream.write(b"corrupted")
            with mock.patch.object(backup.os, "geteuid", return_value=0):
                with self.assertRaisesRegex(ValueError, "checksum"):
                    backup.restore_directory(root / "backup", root / "restored")
            self.assertFalse((root / "restored").exists())

    def test_postgres_container_refused_before_any_stop_or_copy(self):
        secret_url = "postgres://fixture:do-not-print-this@database/rss_workshop"

        def inspect_only(*args):
            self.assertEqual(args[0], "inspect", "PostgreSQL rejection must not mutate the container")
            if "{{.Id}}" in args:
                return b"fixture-container\n"
            if "{{json .Config.Env}}" in args:
                return json.dumps(["DATA_DIR=/data", "DATABASE_URL=" + secret_url]).encode()
            self.fail("PostgreSQL configuration must be checked before examining SQLite storage")

        with tempfile.TemporaryDirectory() as work, mock.patch.object(backup, "docker", side_effect=inspect_only):
            output = Path(work) / "backup"
            with self.assertRaisesRegex(ValueError, "PostgreSQL.*pg_dump") as raised:
                backup.backup_locked("fixture-container", output)
            self.assertNotIn(secret_url, str(raised.exception))
            self.assertFalse(output.exists())

    def test_concurrent_backup_refused(self):
        identity = "test-" + uuid.uuid4().hex
        try:
            with backup.container_lock(identity):
                with self.assertRaisesRegex(ValueError, "already running"):
                    with backup.container_lock(identity):
                        self.fail("concurrent backup acquired the lock")
        finally:
            Path("/tmp/rss-workshop-backup-" + identity + ".lock").unlink(missing_ok=True)

    def test_wal_snapshot_and_independent_verification(self):
        with tempfile.TemporaryDirectory() as work:
            root = Path(work)
            with contextlib.closing(fixture_database(root / "source.db")) as db:
                db.execute("PRAGMA journal_mode=WAL")
                db.execute("PRAGMA wal_autocheckpoint=0")
                db.execute("UPDATE items SET title='Updated in WAL'")
                db.commit()
                self.assertGreater((root / "source.db-wal").stat().st_size, 0)
                make_backup(root / "source.db", root / "backup")
                manifest = backup.verify_backup(root / "backup")
                self.assertEqual(manifest["counts"], {"feeds": 1, "items": 1, "runs": 1})
                with contextlib.closing(sqlite3.connect(root / "backup/rss.db")) as restored:
                    self.assertEqual(restored.execute("SELECT title,published,guid FROM items").fetchone(),
                                     ("Updated in WAL", 123, "fixture-guid"))
                self.assertFalse((root / "backup/rss.db-wal").exists())

    def test_corruption_and_sidecars_refused(self):
        with tempfile.TemporaryDirectory() as work:
            root = Path(work)
            fixture_database(root / "source.db").close()
            make_backup(root / "source.db", root / "backup")
            database = root / "backup/rss.db"
            (root / "backup/rss.db-wal").write_bytes(b"unexpected")
            with self.assertRaisesRegex(ValueError, "sidecar"):
                backup.verify_backup(root / "backup")
            (root / "backup/rss.db-wal").unlink()
            with database.open("ab") as stream:
                stream.write(b"changed")
            with self.assertRaisesRegex(ValueError, "checksum"):
                backup.verify_backup(root / "backup")

    @unittest.skipUnless(os.environ.get("RSS_BACKUP_DOCKER_TEST") == "1", "isolated Docker test not requested")
    def test_named_volume_backup_restore(self):
        image = os.environ.get("RSS_BACKUP_TEST_IMAGE", "rss-workshop:local")
        prefix = "rss-backup-test-" + uuid.uuid4().hex[:10]
        containers, volumes = [], []

        def start(volume, name):
            backup.docker("create", "--pull", "never", "--name", name, "--network", "none",
                          "--read-only", "--tmpfs", "/tmp:size=16m,mode=1777",
                          "--mount", "type=volume,source=" + volume + ",target=/data",
                          "-e", "ADMIN_PASSWORD=temporary-backup-fixture-password", "-e", "CHROMIUM_PATH=",
                          "-e", "DATA_DIR=/data", image)
            containers.append(name)
            backup.docker("start", name)
            for _ in range(50):
                try:
                    backup.docker("exec", name, "/rss-workshop", "-healthcheck")
                    return
                except Exception:
                    time.sleep(0.1)
            self.fail("fixture app did not become ready")

        try:
            with tempfile.TemporaryDirectory() as work:
                root = Path(work)
                fixture_database(root / "source.db").close()
                make_backup(root / "source.db", root / "seed")
                original = prefix + "-original"
                volumes.append(original)
                backup.restore_volume(root / "seed", original, image)
                with self.assertRaisesRegex(ValueError, "existing volume"):
                    backup.restore_volume(root / "seed", original, image)
                start(original, prefix + "-app")
                saved = backup.backup_container(prefix + "-app", root / "saved")
                self.assertTrue(backup.inspect_container(prefix + "-app")[1], "backup failed to restart the app")
                self.assertEqual(saved["counts"], {"feeds": 1, "items": 1, "runs": 1})
                restored = prefix + "-restored"
                volumes.append(restored)
                backup.restore_volume(root / "saved", restored, image)
                start(restored, prefix + "-restored-app")
                backup.backup_container(prefix + "-restored-app", root / "roundtrip")
                with contextlib.closing(sqlite3.connect(root / "saved/rss.db")) as a:
                    with contextlib.closing(sqlite3.connect(root / "roundtrip/rss.db")) as b:
                        for table in ("schema_version", *backup.TABLES):
                            self.assertEqual(a.execute("SELECT * FROM " + table).fetchall(),
                                             b.execute("SELECT * FROM " + table).fetchall(), table)
                # A stopped app remains stopped; the original still exists independently.
                backup.docker("stop", prefix + "-app")
                backup.backup_container(prefix + "-app", root / "stopped")
                self.assertFalse(backup.inspect_container(prefix + "-app")[1])
                if os.geteuid() == 0:
                    # The operational migration path keeps the old volume
                    # intact while materializing a separately owned directory.
                    backup.restore_directory(root / "saved", root / "migrated")
                    self.assertEqual(backup.checksum(root / "migrated/rss.db"), saved["sha256"])
                    self.assertEqual((root / "migrated").stat().st_uid, 65532)
                    self.assertEqual((root / "migrated/rss.db").stat().st_uid, 65532)
        finally:
            for container in containers:
                backup.docker("rm", "-f", container)
            for volume in volumes:
                try:
                    backup.docker("volume", "rm", volume)
                except Exception:
                    pass

    @unittest.skipUnless(os.environ.get("RSS_BACKUP_DOCKER_TEST") == "1" and os.geteuid() == 0,
                         "isolated bind-directory Docker test requires root and RSS_BACKUP_DOCKER_TEST=1")
    def test_bind_directory_backup_restore(self):
        image = os.environ.get("RSS_BACKUP_TEST_IMAGE", "rss-workshop:local")
        prefix = "rss-bind-backup-test-" + uuid.uuid4().hex[:10]
        containers, volumes = [], []

        def start(path, name):
            backup.docker("create", "--pull", "never", "--name", name, "--network", "none",
                          "--read-only", "--tmpfs", "/tmp:size=16m,mode=1777",
                          "--mount", "type=bind,source=" + str(path) + ",target=/data",
                          "-e", "ADMIN_PASSWORD=temporary-backup-fixture-password", "-e", "CHROMIUM_PATH=",
                          "-e", "DATABASE_URL=", "-e", "DATA_DIR=/data", image)
            containers.append(name)
            backup.docker("start", name)
            for _ in range(50):
                try:
                    backup.docker("exec", name, "/rss-workshop", "-healthcheck")
                    return
                except Exception:
                    time.sleep(0.1)
            self.fail("bind fixture app did not become ready")

        temporary = tempfile.TemporaryDirectory()
        try:
            with contextlib.nullcontext(temporary.name) as work:
                root = Path(work)
                fixture_database(root / "source.db").close()
                make_backup(root / "source.db", root / "seed")
                backup.restore_directory(root / "seed", root / "original")
                start(root / "original", prefix + "-app")
                saved = backup.backup_container(prefix + "-app", root / "saved")
                self.assertTrue(backup.inspect_container(prefix + "-app")[1])
                self.assertEqual(saved["source_directory"], str(root / "original"))
                self.assertNotIn("source_volume", saved)
                backup.restore_directory(root / "saved", root / "restored")
                for path, mode in ((root / "restored", 0o700), (root / "restored/rss.db", 0o600)):
                    info = path.stat()
                    self.assertEqual((info.st_uid, info.st_gid, info.st_mode & 0o777), (65532, 65532, mode))
                start(root / "restored", prefix + "-restored-app")
                backup.backup_container(prefix + "-restored-app", root / "roundtrip")
                with contextlib.closing(sqlite3.connect(root / "saved/rss.db")) as a:
                    with contextlib.closing(sqlite3.connect(root / "roundtrip/rss.db")) as b:
                        for table in ("schema_version", *backup.TABLES):
                            self.assertEqual(a.execute("SELECT * FROM " + table).fetchall(),
                                             b.execute("SELECT * FROM " + table).fetchall(), table)
                # Backups from bind storage retain named-volume restore
                # compatibility as well; only informational metadata differs.
                volume = prefix + "-named"
                volumes.append(volume)
                backup.restore_volume(root / "saved", volume, image)
                backup.docker("stop", prefix + "-app")
                backup.backup_container(prefix + "-app", root / "stopped")
                self.assertFalse(backup.inspect_container(prefix + "-app")[1])
        finally:
            for container in containers:
                backup.docker("rm", "-f", container)
            for volume in volumes:
                try:
                    backup.docker("volume", "rm", volume)
                except Exception:
                    pass
            temporary.cleanup()


if __name__ == "__main__":
    unittest.main(verbosity=2)
