#!/usr/bin/env python3
"""Inventory a release image and optionally collect matching Debian source files.

No network is used unless --download is supplied. Source archives are fetched
by exact source package version: from the regular Debian archive while the
release's current suites still carry that exact version, and otherwise from
Debian Snapshot. Another version is never substituted for the installed one.
This script prepares local artifacts; it does not publish them.
"""

import argparse
import hashlib
import http.client
import io
import json
import lzma
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
# The Debian release the images are built from; a test keeps it in step with
# Dockerfile.browser. Its current suites, which the images install from, carry
# the exact versions a new build installs. They are served by Debian's regular
# mirrors, which are faster and far more available than Snapshot, so Snapshot
# is needed only for versions these suites no longer carry. The images enable
# only the main component.
DEBIAN_RELEASE = "trixie"
ARCHIVE_INDICES = (
    ("https://deb.debian.org/debian", DEBIAN_RELEASE),
    ("https://deb.debian.org/debian", DEBIAN_RELEASE + "-updates"),
    ("https://security.debian.org/debian-security", DEBIAN_RELEASE + "-security"),
)
INDEX_LIMIT = 64 << 20  # compressed bytes of one Sources.xz
INDEX_TEXT_LIMIT = 512 << 20  # its decompressed text
# Failures that mean an archive could not serve a file, as opposed to serving
# the wrong bytes. Only these move a package on to Snapshot.
NETWORK_ERRORS = (urllib.error.URLError, TimeoutError, ConnectionError, http.client.HTTPException)
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
            error.close()
        except NETWORK_ERRORS:
            # urlopen wraps a failed connection in URLError, but a response
            # that is dropped or stalls before its headers arrives unwrapped.
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


def descriptor_payload(text):
    """Return Debian control fields, excluding optional OpenPGP armor metadata.

    This extracts cleartext only; it does not verify the signature. Preserve the
    original downloaded .dsc bytes for their Snapshot identity and release hash.
    """
    text = text.replace("\r\n", "\n")
    signed = "-----BEGIN PGP SIGNED MESSAGE-----\n"
    signature = "\n-----BEGIN PGP SIGNATURE-----\n"
    if not text.startswith(signed):
        if "-----BEGIN PGP " in text:
            raise ValueError("malformed source descriptor OpenPGP armor")
        return text
    _, separator, remainder = text.partition("\n\n")
    if not separator:
        raise ValueError("malformed source descriptor OpenPGP headers")
    payload, separator, armor = remainder.partition(signature)
    if not separator or not armor.rstrip().endswith("\n-----END PGP SIGNATURE-----"):
        raise ValueError("malformed source descriptor OpenPGP signature armor")
    # OpenPGP cleartext signatures dash-escape lines that begin with a dash.
    return "\n".join(line[2:] if line.startswith("- ") else line
                     for line in payload.splitlines()) + "\n"


def safe_filename(filename):
    return (isinstance(filename, str) and filename not in ("", ".", "..") and "\\" not in filename
            and pathlib.PurePosixPath(filename).name == filename)


def download_file(filename, url, size, directory, output, *, sha1=None, sha256=None):
    """Download one file unless a verified copy is already present.

    Snapshot files are identified by their SHA-1 content address and archive
    files by the SHA-256 their index lists. Bytes that do not match are never kept.
    """
    destination = directory / filename

    def verified(path):
        if not path.is_file() or path.stat().st_size != size:
            return None
        digests = hashes(path)
        if (sha1 is None or digests[0] == sha1) and (sha256 is None or digests[1] == sha256):
            return digests
        return None

    digests = verified(destination)
    if digests is None:
        partial = destination.with_name(destination.name + ".part")
        try:
            with get(url) as response, partial.open("wb") as target:
                received = 0
                for block in iter(lambda: response.read(1 << 20), b""):
                    received += len(block)
                    if received > size:
                        raise ValueError("source file exceeds recorded size")
                    target.write(block)
            if received < size:
                # http.client ends a body early without an error when the
                # connection closes. That is a failed transfer, not wrong bytes.
                raise ConnectionError(f"the download ended after {received} of {size} bytes")
            digests = verified(partial)
            if digests is None:
                raise ValueError("source file size or " + ("Snapshot identity" if sha1 else "SHA-256") + " mismatch")
            partial.replace(destination)
        finally:
            partial.unlink(missing_ok=True)
    record = dict(path=str(destination.relative_to(output)), size=size, sha256=digests[1])
    if sha1:
        record["snapshot_sha1"] = sha1
    record["origin"] = url
    return record


def descriptor_checksums(text, name, version, descriptor_name):
    """Return a .dsc's {filename: (size, sha256)} list after checking its identity."""
    descriptor = descriptor_payload(text)
    for field, expected in (("Source", name), ("Version", version)):
        if re.findall(r"(?m)^" + field + r": ([^\n]+)$", descriptor) != [expected]:
            raise ValueError("source descriptor does not match the requested package/version")
    matches = re.findall(r"(?m)^Checksums-Sha256:\n((?:[ \t].*\n)+)", descriptor)
    if len(matches) != 1:
        raise ValueError("source descriptor has no unique SHA-256 checksum list")
    required = {}
    for line in matches[0].splitlines():
        digest, size, filename = line.split()
        if not re.fullmatch(r"[0-9a-f]{64}", digest) or not re.fullmatch(r"[0-9]+", size) or filename in required or filename == descriptor_name or not safe_filename(filename):
            raise ValueError("invalid source descriptor checksum entry")
        required[filename] = (int(size), digest)
    return required


def read_index(url):
    """Return the text of a Sources.xz index, bounded before and after decompression."""
    decompressor, text, received = lzma.LZMADecompressor(), bytearray(), 0
    try:
        with get(url) as response:
            for block in iter(lambda: response.read(1 << 20), b""):
                received += len(block)
                if received > INDEX_LIMIT:
                    raise ValueError("oversized Debian source index")
                if decompressor.eof:
                    continue  # stream padding after the end of the index
                text += decompressor.decompress(block, INDEX_TEXT_LIMIT + 1 - len(text))
                while not decompressor.needs_input and not decompressor.eof and len(text) <= INDEX_TEXT_LIMIT:
                    text += decompressor.decompress(b"", INDEX_TEXT_LIMIT + 1 - len(text))
                if len(text) > INDEX_TEXT_LIMIT:
                    raise ValueError("oversized Debian source index")
    except lzma.LZMAError:
        raise ValueError("corrupt Debian source index") from None
    if not decompressor.eof:
        # As with a source file, a body that stops early is a failed transfer.
        raise ConnectionError("the Debian source index ended early")
    return text.decode("utf-8")


def archive_sources(text, wanted, base):
    """Yield ((source, version), location) for each wanted version a Sources index lists."""
    for stanza in text.split("\n\n"):
        package = re.search(r"(?m)^Package: (\S+)$", stanza)
        version = re.search(r"(?m)^Version: (\S+)$", stanza)
        if not package or not version or (package[1], version[1]) not in wanted:
            continue
        directory = re.findall(r"(?m)^Directory: (\S+)$", stanza)
        checksums = re.findall(r"(?m)^Checksums-Sha256:\n((?:[ \t].*(?:\n|$))+)", stanza)
        # Every path component starts with a letter or digit, so none can be
        # empty, "." or "..", and the location stays inside the archive's pool.
        if len(directory) != 1 or not re.fullmatch(r"pool(?:/[A-Za-z0-9][A-Za-z0-9+._~-]*)+", directory[0]) or len(checksums) != 1:
            raise ValueError("invalid Debian source index entry")
        files = {}
        for line in checksums[0].splitlines():
            fields = line.split()
            if (len(fields) != 3 or not re.fullmatch(r"[0-9a-f]{64}", fields[0]) or not re.fullmatch(r"[1-9][0-9]*", fields[1])
                    or not safe_filename(fields[2]) or fields[2] in files):
                raise ValueError("invalid Debian source index entry")
            files[fields[2]] = (int(fields[1]), fields[0])
        yield (package[1], version[1]), dict(base=base, directory=directory[0], files=files)


def archive_locations(wanted):
    """Find each wanted exact source version in the current suites of DEBIAN_RELEASE.

    A suite whose index cannot be fetched is skipped, and the versions only it
    lists come from Snapshot. An index that arrives but is corrupt or invalid
    stops the collection: wrong content is a failure, not a reason to look elsewhere.
    """
    found, conflicting = {}, set()
    for base, suite in ARCHIVE_INDICES:
        try:
            text = read_index(base + "/dists/" + suite + "/main/source/Sources.xz")
        except NETWORK_ERRORS as error:
            print(f"The {suite} source index is unavailable ({error}); versions only it lists will come from Snapshot.", flush=True)
            if isinstance(error, urllib.error.HTTPError):
                error.close()
            continue
        for pair, location in archive_sources(text, wanted, base):
            if pair in found and found[pair]["files"] != location["files"]:
                conflicting.add(pair)
            found.setdefault(pair, location)
    for pair in sorted(conflicting):
        # Debian does not allow one version with two sets of files. Leave such
        # a version to Snapshot's content identities rather than pick one.
        print(f"Debian archive suites disagree about {pair[0]} {pair[1]}; it will come from Snapshot.", flush=True)
        del found[pair]
    return found


def download_from_archive(name, version, location, directory, output):
    """Download an exact source version that a current archive suite lists.

    The index's SHA-256 list must equal the .dsc's own, so the index and the
    package both vouch for every file, and each file is checked before it is kept.
    """
    listed = location["files"]
    descriptors = [filename for filename in listed if filename.endswith(".dsc")]
    if len(descriptors) != 1:
        raise ValueError("Debian archive index must list exactly one .dsc descriptor")
    descriptor_name = descriptors[0]
    if listed[descriptor_name][0] > 4 << 20:
        raise ValueError("source descriptor exceeds the metadata size limit")
    directory.mkdir(parents=True, exist_ok=True)
    if {path.name for path in directory.iterdir()} - set(listed):
        raise ValueError("source output contains files not listed by its .dsc; choose a new empty output directory")
    base = location["base"] + "/" + location["directory"] + "/"

    def fetch(filename):
        size, digest = listed[filename]
        return download_file(filename, base + urllib.parse.quote(filename, safe="+~"), size, directory, output, sha256=digest)

    files = [fetch(descriptor_name)]
    required = descriptor_checksums((directory / descriptor_name).read_text(), name, version, descriptor_name)
    if required != {filename: value for filename, value in listed.items() if filename != descriptor_name}:
        raise ValueError("source descriptor differs from the Debian archive index")
    files.extend(fetch(filename) for filename in required)
    return dict(package=name, version=version, files=files)


def download_from_snapshot(name, version, directory, output):
    path = "/mr/package/" + urllib.parse.quote(name, safe="") + "/" + urllib.parse.quote(version, safe="") + "/srcfiles"
    response = metadata(path)
    if response.get("package") != name or response.get("version") != version or not response.get("result"):
        raise ValueError("Snapshot did not return the exact requested source version")
    directory.mkdir(parents=True, exist_ok=True)
    available = {}
    for entry in response["result"]:
        digest = entry["hash"]
        if not re.fullmatch(r"[0-9a-f]{40}", digest):
            raise ValueError("invalid Snapshot file identity")
        variants = metadata("/mr/file/" + digest + "/info")["result"]
        if not variants:
            raise ValueError("missing source file metadata")
        size_for_digest = None
        # Snapshot is content-addressed: one blob can appear under several
        # package filenames. Fonts-liberation's original archive, for example,
        # first appeared under fonts-liberation2. Preserve every safe alias so
        # the .dsc can select its own exact filename, independently of order.
        for info in variants:
            filename, size = info["name"], info["size"]
            if not safe_filename(filename) or type(size) is not int or size < 1:
                raise ValueError("invalid source file metadata")
            if size_for_digest is not None and size_for_digest != size:
                raise ValueError("conflicting source file sizes in Snapshot metadata")
            size_for_digest = size
            record = dict(size=size, snapshot_sha1=digest)
            if filename in available and available[filename] != record:
                raise ValueError("ambiguous source filename in Snapshot metadata")
            available[filename] = record
    descriptors = sorted(filename for filename in available if filename.endswith(".dsc"))
    if len({available[filename]["snapshot_sha1"] for filename in descriptors}) != 1:
        raise ValueError("source package must contain exactly one Debian .dsc descriptor")
    canonical_name = name + "_" + version.split(":", 1)[-1] + ".dsc"
    descriptor_name = canonical_name if canonical_name in descriptors else descriptors[0]
    if available[descriptor_name]["size"] > 4 << 20:
        raise ValueError("source descriptor exceeds the metadata size limit")

    def fetch(filename):
        info = available[filename]
        return download_file(filename, SNAPSHOT + "/file/" + info["snapshot_sha1"], info["size"], directory, output, sha1=info["snapshot_sha1"])

    files = [fetch(descriptor_name)]
    # Check the package's own SHA-256 list as well as Snapshot's content identity.
    # This is not a verification of the uploader's OpenPGP signature.
    required = descriptor_checksums((directory / descriptor_name).read_text(), name, version, descriptor_name)
    for filename, (size, _) in required.items():
        if filename not in available:
            raise ValueError("source descriptor names an unavailable Snapshot archive")
        if available[filename]["size"] != size:
            raise ValueError("source archive differs from its .dsc descriptor")
    # Snapshot also indexes auxiliary artifacts, such as debianutils' .git.tar.xz.
    # Debian Policy 5.6.24 defines the .dsc checksum list as the complete source
    # package. Download every listed file; do not add unreferenced VCS archives.
    # https://www.debian.org/doc/debian-policy/ch-controlfields.html#checksums-sha1-and-checksums-sha256
    if {path.name for path in directory.iterdir()} - set(required) - {descriptor_name}:
        raise ValueError("source output contains files not listed by its .dsc; choose a new empty output directory")
    for filename, (_, digest) in required.items():
        item = fetch(filename)
        if item["sha256"] != digest:
            raise ValueError("source archive differs from its .dsc descriptor")
        files.append(item)
    return dict(package=name, version=version, files=files)


def download_source(name, version, output, location=None):
    """Collect one exact source version, from the archive location if one is given."""
    directory = output / "sources" / name / urllib.parse.quote(version, safe="")
    if location:
        try:
            return download_from_archive(name, version, location, directory, output)
        except NETWORK_ERRORS as error:
            # A version can leave its suite between reading the index and the
            # download. Wrong bytes are not retried elsewhere; they fail above.
            print(f"  {location['base']} could not serve it ({error}); trying Snapshot", flush=True)
            if isinstance(error, urllib.error.HTTPError):
                error.close()
    return download_from_snapshot(name, version, directory, output)


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
    located = archive_locations(set(pairs))
    print(f"{len(located)} of {len(pairs)} exact source versions are in the current {DEBIAN_RELEASE} suites; any others come from Snapshot.", flush=True)
    for number, (name, version) in enumerate(pairs, 1):
        print(f"[{number}/{len(pairs)}] {name} {version}" + ("" if (name, version) in located else " from Snapshot"), flush=True)
        index["sources"].append(download_source(name, version, args.output, located.get((name, version))))
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
    except (OSError, ValueError, KeyError, http.client.HTTPException, subprocess.CalledProcessError) as error:
        print("Source collection failed: " + str(error), file=sys.stderr)
        sys.exit(1)
