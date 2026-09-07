#!/usr/bin/env python3
"""Verify durable Debian source archives against the final images to be pushed."""

import argparse
import hashlib
import importlib.util
import io
import json
import pathlib
import re
import shlex
import sys
import tarfile
import urllib.parse

spec = importlib.util.spec_from_file_location("collector", pathlib.Path(__file__).with_name("collect-debian-sources.py"))
collector = importlib.util.module_from_spec(spec)
spec.loader.exec_module(collector)
VARIANTS = ("static", "browser")


class JoinedFiles(io.RawIOBase):
    """Read ordered split archive parts with only one file open at a time."""

    def __init__(self, paths):
        self.paths = iter(paths)
        self.current = None

    def readable(self):
        return True

    def readinto(self, buffer):
        while True:
            if self.current is None:
                path = next(self.paths, None)
                if path is None:
                    return 0
                self.current = pathlib.Path(path).open("rb")
            count = self.current.readinto(buffer)
            if count:
                return count
            self.current.close()
            self.current = None

    def close(self):
        if self.current is not None:
            self.current.close()
        super().close()


def safe_path(raw):
    if not isinstance(raw, str) or raw.startswith("/") or "\\" in raw or ".." in raw.split("/"):
        raise ValueError("unsafe path in source archive")
    path = str(pathlib.PurePosixPath(raw))
    if path in ("", "."):
        raise ValueError("empty path in source archive")
    return path


def package_pairs(packages):
    if not isinstance(packages, list) or not packages:
        raise ValueError("missing package inventory")
    names, pairs = set(), set()
    for package in packages:
        if not isinstance(package, dict) or set(package) != {"binary", "version", "source", "source_version"} or not all(isinstance(v, str) and v for v in package.values()):
            raise ValueError("invalid package inventory")
        if package["binary"] in names:
            raise ValueError("duplicate binary package inventory")
        names.add(package["binary"])
        pairs.add((package["source"], package["source_version"]))
    return pairs


def verify_descriptor(text, source, indexed):
    for field, expected in (("Source", source["package"]), ("Version", source["version"])):
        matches = re.findall(r"(?m)^" + field + r": ([^\n]+)$", text)
        if matches != [expected]:
            raise ValueError("source descriptor does not match its recorded package/version")
    matches = re.findall(r"(?m)^Checksums-Sha256:\n((?:[ \t].*\n)+)", text)
    if len(matches) != 1:
        raise ValueError("source descriptor has no unique SHA-256 list")
    verified = set()
    for line in matches[0].splitlines():
        digest, size, name = line.split()
        item = indexed.get(name)
        if name in verified or not item or item["sha256"] != digest or item["size"] != int(size):
            raise ValueError("source descriptor archive checksum mismatch")
        verified.add(name)
    if verified != {name for name in indexed if not name.endswith(".dsc")}:
        raise ValueError("source descriptor does not cover every archive")


def check_archive(archive):
    files, metadata, seen = {}, {}, set()
    total = 0
    # Stream source bytes through hashers. No archive path is extracted to disk.
    paths = archive if isinstance(archive, list) else [archive]
    with io.BufferedReader(JoinedFiles(paths)) as stream, tarfile.open(fileobj=stream, mode="r|*") as bundle:
        for entry in bundle:
            if entry.isdir() and entry.name in (".", "./"):
                continue
            name = safe_path(entry.name)
            if name in seen or name.split("/", 1)[0] not in VARIANTS:
                raise ValueError("duplicate or unexpected source archive member")
            seen.add(name)
            if len(seen) > 20000:
                raise ValueError("too many source archive members")
            if entry.isdir():
                continue
            if not entry.isfile():
                raise ValueError("source archive links and special files are forbidden")
            total += entry.size
            if entry.size < 0 or total > 64 << 30:
                raise ValueError("source archive exceeds the verification size limit")
            save = name.endswith(("/index.json", "/SHA256SUMS", ".dsc"))
            if save and entry.size > 4 << 20:
                raise ValueError("source metadata exceeds the size limit")
            digest, content = hashlib.sha256(), bytearray()
            source = bundle.extractfile(entry)
            received = 0
            for block in iter(lambda: source.read(1 << 20), b""):
                received += len(block)
                digest.update(block)
                if save:
                    content.extend(block)
            if received != entry.size:
                raise ValueError("truncated source archive member")
            files[name] = dict(size=received, sha256=digest.hexdigest())
            if save:
                metadata[name] = bytes(content).decode("utf-8")
    indices = {}
    for variant in VARIANTS:
        index = json.loads(metadata[variant + "/index.json"])
        if index.get("schema") != 1 or index.get("complete") is not True:
            raise ValueError("source collection is not complete")
        pairs = package_pairs(index.get("packages"))
        actual_pairs, declared = set(), {}
        for source in index.get("sources", []):
            pair = (source["package"], source["version"])
            if pair in actual_pairs:
                raise ValueError("duplicate source package")
            actual_pairs.add(pair)
            prefix = "sources/" + source["package"] + "/" + urllib.parse.quote(source["version"], safe="") + "/"
            indexed, descriptors = {}, []
            for item in source["files"]:
                path = safe_path(item["path"])
                if not path.startswith(prefix) or "/" in path[len(prefix):] or path in declared:
                    raise ValueError("source file path does not match its package")
                full = variant + "/" + path
                if files.get(full) != {"size": item["size"], "sha256": item["sha256"]}:
                    raise ValueError("source file size or SHA-256 mismatch")
                declared[path] = item["sha256"]
                indexed[pathlib.PurePosixPath(path).name] = item
                if path.endswith(".dsc"):
                    descriptors.append(full)
            if len(descriptors) != 1:
                raise ValueError("source package lacks a unique .dsc descriptor")
            verify_descriptor(metadata[descriptors[0]], source, indexed)
        if actual_pairs != pairs:
            raise ValueError("source archives do not cover the complete package inventory")
        checksums = {}
        for line in metadata[variant + "/SHA256SUMS"].splitlines():
            match = re.fullmatch(r"([0-9a-f]{64})  (.+)", line)
            if not match:
                raise ValueError("invalid SHA256SUMS entry")
            path = safe_path(match[2])
            if path in checksums:
                raise ValueError("duplicate SHA256SUMS entry")
            checksums[path] = match[1]
        if checksums != declared:
            raise ValueError("SHA256SUMS differs from the source index")
        present = {name[len(variant) + 1:] for name in files if name.startswith(variant + "/")}
        if present != set(declared) | {"index.json", "SHA256SUMS"}:
            raise ValueError("source archive contains unindexed or missing files")
        indices[variant] = index
    return indices


def check_images(indices, architecture, images, docker):
    for variant in VARIANTS:
        image_id, packages = collector.inventory(docker, images[variant])
        actual_arch = collector.run(docker, "image", "inspect", "--format", "{{.Architecture}}", image_id).decode().strip()
        if actual_arch != architecture:
            raise ValueError(variant + " image architecture does not match the release")
        expected = sorted(indices[variant]["packages"], key=lambda p: p["binary"])
        if packages != expected:
            raise ValueError(variant + " image package inventory differs from its durable source archive")
        print(variant + ": verified source files and exact package versions for " + image_id)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", required=True, type=pathlib.Path, action="append", help="archive or ordered split part; repeat for each part")
    parser.add_argument("--architecture", required=True, choices=("amd64", "arm64"))
    parser.add_argument("--static-image", required=True)
    parser.add_argument("--browser-image", required=True)
    parser.add_argument("--docker", default="docker")
    args = parser.parse_args()
    indices = check_archive(args.archive)
    check_images(indices, args.architecture, dict(static=args.static_image, browser=args.browser_image), shlex.split(args.docker))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, TypeError, tarfile.TarError, collector.subprocess.CalledProcessError) as error:
        print("Release source verification failed: " + str(error), file=sys.stderr)
        sys.exit(1)
