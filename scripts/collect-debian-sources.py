#!/usr/bin/env python3
"""Inventory a release image and optionally collect matching Debian source files.

No network is used unless --download is supplied. Source archives are fetched
from Debian Snapshot by exact source package version, never by the current suite.
This script prepares local artifacts; it does not publish them.
"""

import argparse
import hashlib
import io
import json
import pathlib
import re
import shlex
import subprocess
import sys
import tarfile
import time
import urllib.error
import urllib.parse
import urllib.request

SNAPSHOT = "https://snapshot.debian.org"
FORMAT = "${db:Status-Status}\t${binary:Package}\t${Version}\t${source:Package}\t${source:Version}\n"


def run(docker, *args):
    return subprocess.run(docker + list(args), check=True, capture_output=True).stdout


def inventory(docker, image):
    image_id = run(docker, "image", "inspect", "--format", "{{.Id}}", image).decode().strip()
    try:
        text = run(docker, "run", "--rm", "--network", "none", "--read-only",
                   "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
                   "--entrypoint", "dpkg-query", image_id, "-W", "-f=" + FORMAT).decode()
        rows = [line.split("\t")[1:] for line in text.splitlines()
                if line.startswith("installed\t")]
    except subprocess.CalledProcessError:
        # Scratch cannot execute dpkg-query. Its builder records only the Debian
        # package whose data is copied into the final image: ca-certificates.
        container = run(docker, "create", "--entrypoint", "/rss-workshop", image_id).decode().strip()
        try:
            archive = run(docker, "cp", container + ":/licenses/debian-packages.tsv", "-")
            with tarfile.open(fileobj=io.BytesIO(archive)) as bundle:
                files = [entry for entry in bundle.getmembers() if entry.isfile()]
                if len(files) != 1 or files[0].size > 1 << 20:
                    raise ValueError("invalid scratch package inventory")
                text = bundle.extractfile(files[0]).read().decode()
            rows = [line.split("\t") for line in text.splitlines() if line.strip()]
        finally:
            run(docker, "rm", container)
    packages = []
    for row in rows:
        if len(row) != 4 or not all(row):
            raise ValueError("incomplete Debian package inventory row")
        binary, version, source, source_version = row
        if not re.fullmatch(r"[a-z0-9][a-z0-9+.-]+", source):
            raise ValueError("invalid source package name")
        packages.append(dict(binary=binary, version=version, source=source, source_version=source_version))
    if not packages:
        raise ValueError("the image contains no recorded Debian packages")
    return image_id, sorted(packages, key=lambda p: p["binary"])


def get(url):
    for attempt in range(3):
        try:
            return urllib.request.urlopen(urllib.request.Request(
                url, headers={"User-Agent": "RSS-Workshop-source-collector/1"}), timeout=60)
        except urllib.error.HTTPError as error:
            if error.code not in (429, 500, 502, 503, 504) or attempt == 2:
                raise
        except urllib.error.URLError:
            if attempt == 2:
                raise
        time.sleep(2 ** attempt)


def metadata(path):
    with get(SNAPSHOT + path) as response:
        body = response.read((1 << 20) + 1)
    if len(body) > 1 << 20:
        raise ValueError("oversized Snapshot metadata")
    return json.loads(body)


def hashes(path):
    sha1, sha256 = hashlib.sha1(), hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1 << 20), b""):
            sha1.update(block)
            sha256.update(block)
    return sha1.hexdigest(), sha256.hexdigest()


def download_file(filename, info, directory, output):
    digest, size = info["snapshot_sha1"], info["size"]
    destination = directory / filename
    existing = hashes(destination)[0] if destination.is_file() else ""
    if existing != digest or destination.stat().st_size != size:
        partial = destination.with_name(destination.name + ".part")
        try:
            with get(SNAPSHOT + "/file/" + digest) as response, partial.open("wb") as target:
                received = 0
                for block in iter(lambda: response.read(1 << 20), b""):
                    received += len(block)
                    if received > size:
                        raise ValueError("source file exceeds recorded size")
                    target.write(block)
            if received != size or hashes(partial)[0] != digest:
                raise ValueError("source file size or Snapshot identity mismatch")
            partial.replace(destination)
        finally:
            partial.unlink(missing_ok=True)
    return dict(path=str(destination.relative_to(output)), size=size,
                sha256=hashes(destination)[1], snapshot_sha1=digest,
                origin=SNAPSHOT + "/file/" + digest)


def download_source(name, version, output):
    path = "/mr/package/" + urllib.parse.quote(name, safe="") + "/" + urllib.parse.quote(version, safe="") + "/srcfiles"
    response = metadata(path)
    if response.get("package") != name or response.get("version") != version or not response.get("result"):
        raise ValueError("Snapshot did not return the exact requested source version")
    directory = output / "sources" / name / urllib.parse.quote(version, safe="")
    directory.mkdir(parents=True, exist_ok=True)
    available = {}
    for entry in response["result"]:
        digest = entry["hash"]
        if not re.fullmatch(r"[0-9a-f]{40}", digest):
            raise ValueError("invalid Snapshot file identity")
        variants = metadata("/mr/file/" + digest + "/info")["result"]
        if not variants:
            raise ValueError("missing source file metadata")
        info = variants[0]
        filename, size = info["name"], info["size"]
        if not isinstance(filename, str) or pathlib.PurePosixPath(filename).name != filename or filename in ("", ".", "..") or "\\" in filename or type(size) is not int or size < 1:
            raise ValueError("invalid source file metadata")
        if filename in available:
            raise ValueError("duplicate source filename in Snapshot metadata")
        available[filename] = dict(size=size, snapshot_sha1=digest)
    descriptors = [filename for filename in available if filename.endswith(".dsc")]
    if len(descriptors) != 1:
        raise ValueError("source package must contain exactly one Debian .dsc descriptor")
    descriptor_name = descriptors[0]
    if available[descriptor_name]["size"] > 4 << 20:
        raise ValueError("source descriptor exceeds the metadata size limit")
    files = [download_file(descriptor_name, available[descriptor_name], directory, output)]
    # Check the package's own SHA-256 list as well as Snapshot's content identity.
    # This is not a verification of the uploader's OpenPGP signature.
    descriptor = (directory / descriptor_name).read_text()
    for field, expected in (("Source", name), ("Version", version)):
        if re.findall(r"(?m)^" + field + r": ([^\n]+)$", descriptor) != [expected]:
            raise ValueError("source descriptor does not match the requested package/version")
    matches = re.findall(r"(?m)^Checksums-Sha256:\n((?:[ \t].*\n)+)", descriptor)
    if len(matches) != 1:
        raise ValueError("source descriptor has no unique SHA-256 checksum list")
    required = {}
    for line in matches[0].splitlines():
        digest, size, filename = line.split()
        if not re.fullmatch(r"[0-9a-f]{64}", digest) or not re.fullmatch(r"[0-9]+", size) or filename in required or filename == descriptor_name:
            raise ValueError("invalid source descriptor checksum entry")
        if filename not in available:
            raise ValueError("source descriptor names an unavailable Snapshot archive")
        if available[filename]["size"] != int(size):
            raise ValueError("source archive differs from its .dsc descriptor")
        required[filename] = digest
    # Snapshot also indexes auxiliary artifacts, such as debianutils' .git.tar.xz.
    # Debian Policy 5.6.24 defines the .dsc checksum list as the complete source
    # package. Download every listed file; do not add unreferenced VCS archives.
    # https://www.debian.org/doc/debian-policy/ch-controlfields.html#checksums-sha1-and-checksums-sha256
    if {path.name for path in directory.iterdir()} - set(required) - {descriptor_name}:
        raise ValueError("source output contains files not listed by its .dsc; choose a new empty output directory")
    for filename, digest in required.items():
        item = download_file(filename, available[filename], directory, output)
        if item["sha256"] != digest:
            raise ValueError("source archive differs from its .dsc descriptor")
        files.append(item)
    return dict(package=name, version=version, files=files)


def write_index(output, index):
    temporary = output / "index.json.part"
    temporary.write_text(json.dumps(index, indent=2) + "\n")
    temporary.replace(output / "index.json")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True, help="locally built final release image, preferably an immutable digest")
    parser.add_argument("--output", required=True, type=pathlib.Path)
    parser.add_argument("--docker", default="docker", help="Docker command, e.g. 'sudo docker'")
    parser.add_argument("--download", action="store_true", help="download exact sources; browser images may require several GiB")
    args = parser.parse_args()
    image_id, packages = inventory(shlex.split(args.docker), args.image)
    args.output.mkdir(parents=True, exist_ok=True)
    # Resume interrupted downloads only for the same installed packages. Mixing
    # versions leaves stale files in the tarball that strict release checks
    # correctly reject; preserve that earlier collection instead of replacing it.
    if any(args.output.iterdir()):
        previous_index = args.output / "index.json"
        if previous_index.is_symlink() or not previous_index.is_file():
            raise ValueError("source output must be empty or contain an existing collection index")
        previous = json.loads(previous_index.read_text())
        if not isinstance(previous, dict) or previous.get("schema") != 1 or previous.get("packages") != packages:
            raise ValueError("source output belongs to a different package inventory; choose a new empty output directory")
    index = dict(schema=1, image=args.image, image_id=image_id, complete=False, packages=packages, sources=[])
    write_index(args.output, index)
    pairs = sorted({(p["source"], p["source_version"]) for p in packages})
    print(f"Recorded {len(packages)} binary packages, {len(pairs)} exact source versions.", flush=True)
    if not args.download:
        print("Inventory only; matching source archives have NOT been collected.")
        return
    for number, (name, version) in enumerate(pairs, 1):
        print(f"[{number}/{len(pairs)}] {name} {version}", flush=True)
        index["sources"].append(download_source(name, version, args.output))
        write_index(args.output, index)
    sums = [item["sha256"] + "  " + item["path"] for source in index["sources"] for item in source["files"]]
    temporary = args.output / "SHA256SUMS.part"
    temporary.write_text("\n".join(sorted(sums)) + "\n")
    temporary.replace(args.output / "SHA256SUMS")
    index["complete"] = True
    write_index(args.output, index)
    print("Verified complete local source collection. Publish the index and archives together with the release.")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, subprocess.CalledProcessError) as error:
        print("Source collection failed: " + str(error), file=sys.stderr)
        sys.exit(1)
