#!/usr/bin/env python3
"""Back up an RSS Workshop container or restore into new isolated storage.

Uses Docker and Python's standard library; no helper image or in-place restore.
Run with the privileges needed to use Docker and read/write the backup path.
"""
import argparse
import contextlib
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path
import posixpath
import re
import shutil
import sqlite3
import stat
import subprocess
import sys
import tarfile
import tempfile
import uuid

FORMAT = "rss-workshop.sqlite-backup"
TABLES = ("feeds", "items", "runs")
SCHEMA_VERSIONS = (1, 2, 3)


def docker(*args, **kwargs):
    result = subprocess.run(["docker", *args], check=True, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, **kwargs)
    return result.stdout


def inspect_container(container):
    identity = docker("inspect", "--type", "container", "--format", "{{.Id}}", container).decode().strip()
    environment = json.loads(docker("inspect", "--format", "{{json .Config.Env}}", identity)) or []
    if any(value.startswith("DATABASE_URL=") and value.partition("=")[2].strip() for value in environment):
        raise ValueError("this container uses PostgreSQL; use pg_dump instead of the SQLite backup tool")
    if any(value.startswith("DATA_DIR=") and value.partition("=")[2] != "/data" for value in environment):
        raise ValueError("this tool requires the container's DATA_DIR to be /data")
    state = json.loads(docker("inspect", "--format", "{{json .State}}", identity))
    mounts = json.loads(docker("inspect", "--format", "{{json .Mounts}}", identity))
    mount = next((entry for entry in mounts if entry["Destination"] == "/data"), None)
    if not mount or mount["Type"] not in ("volume", "bind"):
        raise ValueError("the container must have a named volume or host directory mounted at /data")
    if mount["Type"] == "bind" and not posixpath.isabs(mount.get("Source", "")):
        raise ValueError("the /data bind mount must identify an absolute host directory")
    if any(entry["Destination"].startswith("/data/") for entry in mounts):
        raise ValueError("nested mounts inside /data are unsupported; use one data mount")
    if state.get("Paused") or state.get("Restarting"):
        raise ValueError("resume the paused/restarting container before backing it up")
    others = [entry for entry in docker("ps", "--no-trunc", "--format", "{{.ID}}").decode().split()
              if entry != identity]
    if others:
        # Inspect mounts only: full container inspection would include secrets.
        for line in docker("inspect", "--format", "{{json .Mounts}}", *others).decode().splitlines():
            if any(mounts_overlap(mount, entry) for entry in json.loads(line)):
                raise ValueError("another running container uses overlapping data storage; stop it before backup")
    return identity, state["Running"], mount


def mounts_overlap(first, second):
    if first["Type"] == second["Type"] == "volume" and first.get("Name") == second.get("Name"):
        return True
    paths = [entry.get("Source", "") for entry in (first, second)]
    if not all(posixpath.isabs(path) for path in paths):
        return False
    # Docker reports host paths even for named volumes. Compare ancestors too:
    # binding a parent directory can expose the same database to another app.
    normalized = [posixpath.normpath(path) for path in paths]
    if posixpath.commonpath(normalized) in normalized:
        return True
    # On the Docker host, also catch two bind paths reaching the same directory
    # through a symlink. Remote Docker contexts cannot resolve host aliases here.
    try:
        resolved = [str(Path(path).resolve(strict=True)) for path in normalized]
    except (OSError, RuntimeError):
        return False
    return posixpath.commonpath(resolved) in resolved


def database_info(path, immutable=False, *, include_schema=False):
    if path.is_symlink() or not path.is_file():
        raise ValueError("rss.db must be a regular file")
    suffix = "?mode=ro" + ("&immutable=1" if immutable else "")
    with contextlib.closing(sqlite3.connect(path.resolve().as_uri() + suffix, uri=True)) as db:
        db.execute("PRAGMA trusted_schema=OFF")
        if db.execute("PRAGMA integrity_check").fetchall() != [("ok",)]:
            raise ValueError("SQLite integrity check failed")
        if db.execute("PRAGMA foreign_key_check").fetchone() is not None:
            raise ValueError("SQLite foreign-key check failed")
        versions = db.execute("SELECT version FROM schema_version").fetchmany(2)
        if len(versions) != 1 or type(versions[0][0]) is not int or versions[0][0] not in SCHEMA_VERSIONS:
            raise ValueError("unsupported database schema; this tool supports exactly one version row of 1, 2 or 3")
        counts = {table: db.execute("SELECT count(*) FROM " + table).fetchone()[0] for table in TABLES}
        return {"schema_version": versions[0][0], "counts": counts} if include_schema else counts


def checksum(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def snapshot_database(source, destination, *, include_schema=False):
    """Consolidate the stopped copy's database and any WAL into one file."""
    database_info(source)
    with contextlib.closing(sqlite3.connect(source.resolve().as_uri() + "?mode=ro", uri=True)) as original:
        with contextlib.closing(sqlite3.connect(destination)) as backup:
            original.backup(backup)
            backup.execute("PRAGMA journal_mode=DELETE")
    destination.chmod(0o600)
    return database_info(destination, immutable=True, include_schema=include_schema)


def verify_backup(directory):
    manifest_path, database = directory / "manifest.json", directory / "rss.db"
    if manifest_path.is_symlink() or not manifest_path.is_file() or manifest_path.stat().st_size > 65536:
        raise ValueError("missing or invalid backup manifest")
    manifest = json.loads(manifest_path.read_text())
    if not isinstance(manifest, dict) or manifest.get("format") != FORMAT or manifest.get("version") != 1:
        raise ValueError("unsupported backup format")
    schema_version = manifest.get("schema_version")
    if type(schema_version) is not int or schema_version not in SCHEMA_VERSIONS:
        raise ValueError("unsupported backup database schema; this tool supports versions 1 and 2")
    if database.is_symlink() or not database.is_file():
        raise ValueError("rss.db must be a regular file")
    if any(os.path.lexists(str(database) + suffix) for suffix in ("-wal", "-shm", "-journal")):
        raise ValueError("backup must be a standalone database without SQLite sidecar files")
    if manifest.get("sha256") != checksum(database):
        raise ValueError("backup checksum does not match; the database may be incomplete or changed")
    info = database_info(database, immutable=True, include_schema=True)
    if schema_version != info["schema_version"] or manifest.get("counts") != info["counts"]:
        raise ValueError("backup metadata does not match the database")
    return manifest


@contextlib.contextmanager
def container_lock(identity):
    # Keep the lock file after closing: unlinking could let a third process take
    # a different inode while a second process still holds the original lock.
    path = "/tmp/rss-workshop-backup-" + identity + ".lock"
    fd = os.open(path, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode):
            raise ValueError("backup lock must be a regular file")
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise ValueError("another backup is already running for this container") from None
        yield
    finally:
        os.close(fd)


def backup_container(container, output):
    identity = docker("inspect", "--type", "container", "--format", "{{.Id}}", container).decode().strip()
    with container_lock(identity):
        return backup_locked(identity, output)


def backup_locked(container, output):
    if os.path.lexists(output):
        raise ValueError("backup destination already exists; choose a new directory")
    if not output.parent.is_dir():
        raise ValueError("create the backup parent directory first")
    identity, running, mount = inspect_container(container)
    with tempfile.TemporaryDirectory(prefix="rss-backup-") as work:
        copied = Path(work) / "data"
        copied.mkdir(mode=0o700)
        try:
            if running:
                print("Stopping the selected container briefly to copy its data…", flush=True)
                docker("stop", "--time", "20", identity)
            docker("cp", identity + ":/data/.", str(copied))
        finally:
            if running:
                docker("start", identity)
                print("Container restarted; validating the copied database…", flush=True)
        snapshot = Path(work) / "rss.db"
        info = snapshot_database(copied / "rss.db", snapshot, include_schema=True)
        manifest = {"format": FORMAT, "version": 1, "schema_version": info["schema_version"],
                    "created_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                    "sha256": checksum(snapshot), "counts": info["counts"]}
        if mount["Type"] == "volume":
            manifest["source_volume"] = mount["Name"]
        else:
            manifest["source_directory"] = mount["Source"]
        output.mkdir(mode=0o700)
        shutil.copyfile(snapshot, output / "rss.db")
        (output / "rss.db").chmod(0o600)
        (output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
        (output / "manifest.json").chmod(0o600)
        verify_backup(output)
        for path in (output / "rss.db", output / "manifest.json"):
            with path.open("rb") as stream:
                os.fsync(stream.fileno())
        for directory in (output, output.parent):
            directory_fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(directory_fd)
            finally:
                os.close(directory_fd)
    print("Verified backup: " + str(output))
    if not running:
        print("The source container was already stopped and remains stopped.")
    return manifest


def archive_database(database, archive):
    """Preserve runtime ownership, including the newly populated /data root."""
    with tarfile.open(archive, "w") as stream:
        directory = tarfile.TarInfo(".")
        directory.type = tarfile.DIRTYPE
        directory.mode = 0o700
        directory.uid = directory.gid = 65532
        stream.addfile(directory)
        entry = stream.gettarinfo(str(database), arcname="rss.db")
        entry.uid = entry.gid = 65532
        entry.uname = entry.gname = ""
        entry.mode = 0o600
        with database.open("rb") as source:
            stream.addfile(entry, source)


def restore_volume(backup, volume, image):
    manifest = verify_backup(backup)
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]{1,199}", volume):
        raise ValueError("use a new Docker volume name of 2–200 letters, digits, dots, dashes, or underscores")
    existing = docker("volume", "ls", "--format", "{{.Name}}").decode().splitlines()
    if volume in existing:
        raise ValueError("restore refuses an existing volume; choose a new name")
    # Inspect before creating anything. Restoration must never pull an image.
    docker("image", "inspect", image, "--format", "{{.Id}}")
    helper = "rss-restore-" + uuid.uuid4().hex[:12]
    docker("volume", "create", "--label", "rss-workshop.restore=" + helper, volume)
    labels = json.loads(docker("volume", "inspect", volume, "--format", "{{json .Labels}}")) or {}
    if labels.get("rss-workshop.restore") != helper:
        raise ValueError("volume was created by another operation; refusing to write to it")
    created = False
    try:
        docker("create", "--pull", "never", "--name", helper, "--network", "none",
               "--read-only", "--entrypoint", "/rss-workshop", "--mount",
               "type=volume,source=" + volume + ",target=/data", image)
        created = True
        with tempfile.TemporaryDirectory(prefix="rss-restore-") as work:
            archive = Path(work) / "data.tar"
            archive_database(backup / "rss.db", archive)
            with archive.open("rb") as stream:
                docker("cp", "--archive", "-", helper + ":/data", stdin=stream)
            copied = Path(work) / "restored.db"
            docker("cp", helper + ":/data/rss.db", str(copied))
            if checksum(copied) != manifest["sha256"]:
                raise ValueError("restored database checksum differs from the verified backup")
            database_info(copied, immutable=True)
    except BaseException:
        print("Restore did not finish. The original volume is untouched. Inspect the new volume: " + volume,
              file=sys.stderr)
        raise
    finally:
        if created:
            docker("rm", helper)
    print("Verified restored volume: " + volume)
    print("No app container was started or changed. Select this volume in your deployment when ready.")
    return manifest


def restore_directory(backup, directory):
    """Restore locally without Docker; never adopt an existing path or symlink."""
    manifest = verify_backup(backup)
    if os.geteuid() != 0:
        raise ValueError("host-directory restore requires root to set UID/GID 65532; run this command with sudo")
    # Do not resolve the destination: resolving would follow a supplied symlink.
    if ".." in directory.parts:
        raise ValueError("restore destination must not contain '..' path components")
    directory = Path(os.path.abspath(directory))
    if os.path.lexists(directory):
        raise ValueError("restore refuses an existing directory or symlink; choose a new path")
    # Walk each parent using directory descriptors and O_NOFOLLOW. Keeping the
    # final parent open also avoids following a swapped parent symlink at mkdir.
    parent_fd = os.open("/", os.O_RDONLY | os.O_DIRECTORY)
    directory_fd = None
    created = False
    try:
        for part in directory.parent.parts[1:]:
            child_fd = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent_fd)
            os.close(parent_fd)
            parent_fd = child_fd
        os.mkdir(directory.name, mode=0o700, dir_fd=parent_fd)
        created = True
        directory_fd = os.open(directory.name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent_fd)
        target_fd = os.open("rss.db", os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW,
                            0o600, dir_fd=directory_fd)
        with os.fdopen(target_fd, "wb") as target, (backup / "rss.db").open("rb") as source:
            shutil.copyfileobj(source, target)
            target.flush()
            os.fchmod(target.fileno(), 0o600)
            os.fchown(target.fileno(), 65532, 65532)
            os.fsync(target.fileno())
        # Descriptor-relative verification never follows a renamed parent.
        restored = Path("/proc/self/fd") / str(directory_fd) / "rss.db"
        if checksum(restored) != manifest["sha256"]:
            raise ValueError("restored database checksum differs from the verified backup")
        database_info(restored, immutable=True)
        os.fchmod(directory_fd, 0o700)
        os.fchown(directory_fd, 65532, 65532)
        os.fsync(directory_fd)
        os.fsync(parent_fd)
        entry = os.stat(directory.name, dir_fd=parent_fd, follow_symlinks=False)
        if (entry.st_dev, entry.st_ino) != (os.fstat(directory_fd).st_dev, os.fstat(directory_fd).st_ino):
            raise ValueError("restore destination changed during the operation")
        published = os.stat(directory, follow_symlinks=False)
        if (published.st_dev, published.st_ino) != (entry.st_dev, entry.st_ino):
            raise ValueError("restore destination path changed during the operation")
    except BaseException:
        if created:
            print("Restore did not finish. The backup is untouched. Inspect the new directory: " + str(directory),
                  file=sys.stderr)
        raise
    finally:
        if directory_fd is not None:
            os.close(directory_fd)
        os.close(parent_fd)
    print("Verified restored directory: " + str(directory))
    print("No app container was started or changed. Set RSS_DATA_DIR to this host directory when ready.")
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    backup = commands.add_parser("backup", help="briefly stop a container and create a new verified backup directory")
    backup.add_argument("--container", required=True, help="exact existing app container name or ID")
    backup.add_argument("--output", required=True, type=Path, help="new directory under an existing private parent")
    verify = commands.add_parser("verify", help="verify a backup without Docker or changes to the backup")
    verify.add_argument("backup", type=Path)
    restore = commands.add_parser("restore", help="restore into a NEW host directory or named volume without changing app containers")
    restore.add_argument("backup", type=Path)
    target = restore.add_mutually_exclusive_group(required=True)
    target.add_argument("--volume", help="new named volume; existing volumes are refused")
    target.add_argument("--directory", type=Path, help="new local host directory; existing paths and symlinks are refused (requires root)")
    restore.add_argument("--image", help="required with --volume: already-local RSS Workshop runtime image, preferably a digest")
    args = parser.parse_args()
    os.umask(0o077)
    try:
        if args.command == "backup":
            backup_container(args.container, Path(os.path.abspath(args.output)))
        elif args.command == "verify":
            result = verify_backup(args.backup.resolve())
            print(f"Verified SQLite backup (schema {result['schema_version']}): " + json.dumps(result["counts"]))
        elif args.directory is not None:
            if args.image is not None:
                raise ValueError("--image is only used with --volume")
            restore_directory(args.backup.resolve(), args.directory)
        else:
            if args.image is None:
                raise ValueError("--image is required when restoring a named volume")
            restore_volume(args.backup.resolve(), args.volume, args.image)
    except (ValueError, OSError, sqlite3.Error, subprocess.CalledProcessError) as error:
        if isinstance(error, subprocess.CalledProcessError):
            # Docker inspect can contain environment secrets; never print captured output.
            message = "Docker command failed (exit " + str(error.returncode) + "); check daemon access and the selected container/image"
        else:
            message = str(error)
        print("Backup/restore failed: " + message, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
