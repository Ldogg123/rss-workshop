#!/usr/bin/env python3
"""Offline checks for source matching, checksum enforcement, and release state."""

import hashlib
import importlib.util
import io
import http.client
import json
import lzma
import pathlib
import re
import tempfile
import unittest
import urllib.error
from unittest import mock

spec = importlib.util.spec_from_file_location("collector", pathlib.Path(__file__).with_name("collect-debian-sources.py"))
collector = importlib.util.module_from_spec(spec)
spec.loader.exec_module(collector)
offline = mock.patch("urllib.request.urlopen", side_effect=AssertionError("these tests must not use the network"))


def setUpModule():
    offline.start()


def tearDownModule():
    offline.stop()


def signed_descriptor(payload):
    return (b"-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA256\n\n" + payload +
            b"\n-----BEGIN PGP SIGNATURE-----\nVersion: GnuPG v2\n\n" +
            b"synthetic-signature-not-cryptographically-verified\n-----END PGP SIGNATURE-----\n")


class SourceCollectionTests(unittest.TestCase):
    def fixture(self, output, *, wrong_descriptor=False, bad_filename=False,
                extra_snapshot_file=False, missing_archive=False, change_descriptor=None,
                change_file_metadata=None):
        archive = b"a deterministic source archive"
        sha256 = "0" * 64 if wrong_descriptor else hashlib.sha256(archive).hexdigest()
        descriptor = f"Format: 3.0 (native)\nSource: fixture\nVersion: 1.0\nChecksums-Sha256:\n {sha256} {len(archive)} fixture_1.0.tar.xz\n".encode()
        if change_descriptor:
            descriptor = change_descriptor(descriptor)
        blobs = {hashlib.sha1(archive).hexdigest(): archive, hashlib.sha1(descriptor).hexdigest(): descriptor}
        names = dict(zip(blobs, ("../escape" if bad_filename else "fixture_1.0.tar.xz", "fixture_1.0.dsc")))
        if missing_archive:
            del blobs[hashlib.sha1(archive).hexdigest()]
        if extra_snapshot_file:
            auxiliary = b"auxiliary VCS history, not part of the Debian source package"
            digest = hashlib.sha1(auxiliary).hexdigest()
            blobs[digest] = auxiliary
            names[digest] = "fixture_1.0.git.tar.xz"

        def metadata(path):
            if path == "/mr/package/fixture/1.0/srcfiles":
                return dict(package="fixture", version="1.0", result=[dict(hash=key) for key in blobs])
            for digest, data in blobs.items():
                if path == "/mr/file/" + digest + "/info":
                    result = [dict(name=names[digest], size=len(data))]
                    if change_file_metadata:
                        result = change_file_metadata(result)
                    return dict(result=result)
            raise AssertionError("unexpected metadata path: " + path)

        def get(url):
            return io.BytesIO(blobs[url.rsplit("/", 1)[-1]])

        return mock.patch.object(collector, "metadata", side_effect=metadata), mock.patch.object(collector, "get", side_effect=get)

    def test_exact_archives_verified_and_reused(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory)
            metadata, get = self.fixture(output)
            with metadata, get as calls:
                result = collector.download_source("fixture", "1.0", output)
                self.assertEqual(len(result["files"]), 2)
                self.assertEqual(calls.call_count, 2)
                self.assertEqual(result, collector.download_source("fixture", "1.0", output))
                self.assertEqual(calls.call_count, 2, "valid cached archives should not download again")

    def test_descriptor_sha256_mismatch_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory)
            metadata, get = self.fixture(output, wrong_descriptor=True)
            with metadata, get, self.assertRaisesRegex(ValueError, "differs from its .dsc"):
                collector.download_source("fixture", "1.0", output)

    def test_snapshot_auxiliary_git_archive_is_not_a_source_package_input(self):
        # Debianutils 5.23.2's Snapshot srcfiles endpoint includes .git.tar.xz,
        # while its .dsc correctly lists only the native .tar.xz source archive.
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory)
            metadata, get = self.fixture(output, extra_snapshot_file=True)
            with metadata, get as calls:
                result = collector.download_source("fixture", "1.0", output)
            expected = {"fixture_1.0.dsc", "fixture_1.0.tar.xz"}
            self.assertEqual({pathlib.Path(item["path"]).name for item in result["files"]}, expected)
            self.assertEqual({p.name for p in (output / "sources/fixture/1.0").iterdir()}, expected)
            self.assertEqual(calls.call_count, 2, "unreferenced Git archives must not download")

    def test_every_descriptor_listed_archive_must_exist_in_snapshot(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory)
            metadata, get = self.fixture(output, missing_archive=True)
            with metadata, get, self.assertRaisesRegex(ValueError, "unavailable Snapshot archive"):
                collector.download_source("fixture", "1.0", output)

    def test_uses_descriptor_filename_from_later_snapshot_alias(self):
        def aliases(records):
            original = records[0]
            # Also exercise duplicate appearances in debian/debian-debug and
            # identical descriptor bytes under multiple safe filenames.
            alias = dict(original, name=original["name"].replace("fixture_", "fixture2_"))
            return [alias, original, dict(original)]

        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory)
            metadata, get = self.fixture(output, change_file_metadata=aliases)
            with metadata, get as calls:
                result = collector.download_source("fixture", "1.0", output)
            expected = {"fixture_1.0.dsc", "fixture_1.0.tar.xz"}
            self.assertEqual({pathlib.Path(item["path"]).name for item in result["files"]}, expected)
            self.assertEqual({p.name for p in (output / "sources/fixture/1.0").iterdir()}, expected)
            self.assertEqual(calls.call_count, 2)

    def test_ambiguous_aliases_and_inconsistent_sizes_are_rejected(self):
        def ambiguous(records):
            # Distinct descriptor/archive content cannot share one filename.
            return records + [dict(records[0], name="shared-alias.tar.xz")]

        def wrong_size(records):
            return records + [dict(records[0], name="other-alias.tar.xz", size=records[0]["size"] + 1)]

        def unsafe_alias(records):
            return records + [dict(records[0], name="../escape")]

        def multiple_descriptors(records):
            if records[0]["name"].endswith(".tar.xz"):
                return records + [dict(records[0], name="other.dsc")]
            return records

        for change in (ambiguous, wrong_size, unsafe_alias, multiple_descriptors):
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                output = pathlib.Path(directory)
                metadata, get = self.fixture(output, change_file_metadata=change)
                with metadata, get as calls, self.assertRaises(ValueError):
                    collector.download_source("fixture", "1.0", output)
                self.assertEqual(calls.call_count, 0, "invalid aliases must fail before downloads")

    def test_descriptor_identity_and_duplicate_checksums_are_rejected(self):
        changes = (
            lambda data: data.replace(b"Source: fixture", b"Source: other"),
            lambda data: data.replace(b"Version: 1.0", b"Version: 2.0"),
            lambda data: data + b"Version: 1.0\n",
            lambda data: data + data.splitlines(keepends=True)[-1],
        )
        for change in changes:
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                output = pathlib.Path(directory)
                metadata, get = self.fixture(output, change_descriptor=change)
                with metadata, get as calls, self.assertRaises(ValueError):
                    collector.download_source("fixture", "1.0", output)
                self.assertEqual(calls.call_count, 1, "invalid descriptors must stop before source archives download")

    def test_signature_version_metadata_is_outside_debian_control_fields(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory)
            metadata, get = self.fixture(output, change_descriptor=signed_descriptor)
            with metadata, get:
                result = collector.download_source("fixture", "1.0", output)
            self.assertEqual(len(result["files"]), 2)
            descriptor = (output / "sources/fixture/1.0/fixture_1.0.dsc").read_bytes()
            self.assertIn(b"Version: GnuPG v2", descriptor, "published descriptor bytes must remain unaltered")

    def test_wrong_or_duplicate_signed_payload_versions_are_rejected(self):
        for transform in (
            lambda data: data.replace(b"Version: 1.0", b"Version: 2.0"),
            lambda data: data.replace(b"Version: 1.0", b"Version: 1.0\nVersion: 1.0"),
        ):
            with self.subTest(transform=transform), tempfile.TemporaryDirectory() as directory:
                output = pathlib.Path(directory)
                metadata, get = self.fixture(output, change_descriptor=lambda data: signed_descriptor(transform(data)))
                with metadata, get, self.assertRaisesRegex(ValueError, "requested package/version"):
                    collector.download_source("fixture", "1.0", output)

    def test_incomplete_signature_armor_is_rejected(self):
        for text in (
            "-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA256\n",
            "-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA256\n\nSource: fixture\n",
            signed_descriptor(b"Source: fixture\n").decode().replace("-----END PGP SIGNATURE-----", ""),
        ):
            with self.subTest(text=text), self.assertRaisesRegex(ValueError, "OpenPGP"):
                collector.descriptor_payload(text)

    def test_old_auxiliary_download_is_preserved_and_requires_fresh_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory)
            old_file = output / "sources/fixture/1.0/fixture_1.0.git.tar.xz"
            old_file.parent.mkdir(parents=True)
            old_file.write_bytes(b"preserve previous collector output")
            metadata, get = self.fixture(output, extra_snapshot_file=True)
            with metadata, get, self.assertRaisesRegex(ValueError, "new empty output directory"):
                collector.download_source("fixture", "1.0", output)
            self.assertEqual(old_file.read_bytes(), b"preserve previous collector output")

    def test_snapshot_cannot_choose_parent_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory)
            metadata, get = self.fixture(output, bad_filename=True)
            with metadata, get as calls, self.assertRaisesRegex(ValueError, "invalid source file"):
                collector.download_source("fixture", "1.0", output)
            self.assertEqual(calls.call_count, 0)

    def test_incomplete_download_never_marks_release_complete(self):
        with tempfile.TemporaryDirectory() as directory:
            packages = [dict(binary="fixture", version="1.0", source="fixture", source_version="1.0")]
            args = ["collector", "--image", "fixture-image", "--output", directory, "--download"]
            with mock.patch("sys.argv", args), mock.patch.object(collector, "inventory", return_value=("sha256:fixture", packages)), mock.patch.object(collector, "archive_locations", return_value={}), mock.patch.object(collector, "download_source", side_effect=ValueError("missing exact version")), mock.patch("sys.stdout", io.StringIO()):
                with self.assertRaisesRegex(ValueError, "missing exact version"):
                    collector.main()
            self.assertFalse(json.loads((pathlib.Path(directory) / "index.json").read_text())["complete"])

    def test_changed_image_preserves_previous_source_collection(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory)
            old_packages = [dict(binary="fixture", version="1.0", source="fixture", source_version="1.0")]
            index = dict(schema=1, complete=True, packages=old_packages, sources=[])
            original = json.dumps(index).encode()
            (output / "index.json").write_bytes(original)
            (output / "existing-source.tar.xz").write_bytes(b"keep the old sources")
            new_packages = [dict(old_packages[0], version="2.0", source_version="2.0")]
            args = ["collector", "--image", "changed-image", "--output", directory, "--download"]
            with mock.patch("sys.argv", args), mock.patch.object(collector, "inventory", return_value=("sha256:new", new_packages)), mock.patch.object(collector, "download_source") as download:
                with self.assertRaisesRegex(ValueError, "new empty output directory"):
                    collector.main()
                download.assert_not_called()
            self.assertEqual((output / "index.json").read_bytes(), original)
            self.assertEqual((output / "existing-source.tar.xz").read_bytes(), b"keep the old sources")

    def test_unrelated_output_directory_is_not_adopted(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory)
            (output / "keep.txt").write_text("unrelated files")
            packages = [dict(binary="fixture", version="1.0", source="fixture", source_version="1.0")]
            args = ["collector", "--image", "fixture-image", "--output", directory]
            with mock.patch("sys.argv", args), mock.patch.object(collector, "inventory", return_value=("sha256:fixture", packages)):
                with self.assertRaisesRegex(ValueError, "source output must be empty"):
                    collector.main()
            self.assertEqual([path.name for path in output.iterdir()], ["keep.txt"])


ARCHIVE = "https://deb.debian.org/debian"
SOURCE = b"a deterministic source archive"
DESCRIPTOR = (f"Format: 3.0 (native)\nSource: fixture\nVersion: 1.0\nChecksums-Sha256:\n"
              f" {hashlib.sha256(SOURCE).hexdigest()} {len(SOURCE)} fixture_1.0.tar.xz\n").encode()


class Chunks(io.RawIOBase):
    """A response body delivered as the given separate reads."""

    def __init__(self, *chunks):
        self.chunks = list(chunks)

    def readable(self):
        return True

    def read(self, size=-1):
        return self.chunks.pop(0) if self.chunks else b""


def served(*chunks):
    return mock.patch.object(collector, "get", return_value=Chunks(*chunks))


def sources_stanza(package, version, files, directory="pool/main/f/fixture"):
    sha1 = "".join(f" {hashlib.sha1(data).hexdigest()} {len(data)} {name}\n" for name, data in files.items())
    sha256 = "".join(f" {hashlib.sha256(data).hexdigest()} {len(data)} {name}\n" for name, data in files.items())
    return (f"Package: {package}\nBinary: {package}-bin\nVersion: {version}\nMaintainer: Fixture <fixture@example.invalid>\n"
            f"Standards-Version: 4.7.0\nChecksums-Sha1:\n{sha1}Checksums-Sha256:\n{sha256}Directory: {directory}\n"
            f"Package-List:\n {package}-bin deb misc optional arch=any\n")


class ArchiveSourceTests(unittest.TestCase):
    """Exact versions the current suites still carry come from Debian's regular archive."""

    def archive(self, *, serve=None, listed=None, descriptor=DESCRIPTOR):
        blobs = {"fixture_1.0.dsc": descriptor, "fixture_1.0.tar.xz": SOURCE}
        files = {name: (len(data), hashlib.sha256(data).hexdigest()) for name, data in blobs.items()}
        if listed:
            listed(files)
        location = dict(base=ARCHIVE, directory="pool/main/f/fixture", files=files)
        served = dict(blobs)
        if serve:
            serve(served)
        by_sha1 = {hashlib.sha1(data).hexdigest(): data for data in blobs.values()}

        def get(url):
            prefix = ARCHIVE + "/pool/main/f/fixture/"
            if url.startswith(collector.SNAPSHOT + "/file/"):
                return io.BytesIO(by_sha1[url.rsplit("/", 1)[-1]])
            if url.startswith(prefix) and url[len(prefix):] in served:
                return io.BytesIO(served[url[len(prefix):]])
            raise urllib.error.HTTPError(url, 404, "Not Found", {}, None)

        return location, mock.patch.object(collector, "get", side_effect=get)

    def no_snapshot(self):
        return mock.patch.object(collector, "metadata", side_effect=AssertionError("Snapshot must not be consulted"))

    def test_listed_versions_come_from_the_archive_and_are_reused(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory)
            location, get = self.archive()
            with get as calls, self.no_snapshot():
                result = collector.download_source("fixture", "1.0", output, location)
                self.assertEqual(calls.call_count, 2)
                self.assertEqual(result, collector.download_source("fixture", "1.0", output, location))
                self.assertEqual(calls.call_count, 2, "verified files should not download again")
            self.assertEqual({pathlib.Path(item["path"]).name for item in result["files"]}, {"fixture_1.0.dsc", "fixture_1.0.tar.xz"})
            for item in result["files"]:
                self.assertEqual(item["origin"], ARCHIVE + "/pool/main/f/fixture/" + pathlib.Path(item["path"]).name)
                self.assertNotIn("snapshot_sha1", item)

    def test_archive_bytes_index_and_descriptor_must_agree(self):
        other = hashlib.sha256(b"other").hexdigest()
        cases = (
            ("same length, different bytes", dict(serve=lambda s: s.update({"fixture_1.0.tar.xz": SOURCE[:-1] + b"X"})), "SHA-256 mismatch"),
            ("index and descriptor disagree", dict(listed=lambda f: f.update({"fixture_1.0.tar.xz": (len(SOURCE), other)})), "differs from the Debian archive index"),
            ("index lists an extra file", dict(listed=lambda f: f.update({"fixture_1.0.debian.tar.xz": (5, other)})), "differs from the Debian archive index"),
            ("two descriptors", dict(listed=lambda f: f.update({"fixture_1.0-1.dsc": (5, other)})), "exactly one .dsc"),
            ("another version's descriptor", dict(descriptor=DESCRIPTOR.replace(b"Version: 1.0", b"Version: 2.0")), "requested package/version"),
        )
        for label, change, message in cases:
            with self.subTest(label), tempfile.TemporaryDirectory() as directory:
                output = pathlib.Path(directory)
                location, get = self.archive(**change)
                # Wrong bytes are a failure, not a reason to look elsewhere.
                with get, self.no_snapshot(), self.assertRaisesRegex(ValueError, message):
                    collector.download_source("fixture", "1.0", output, location)
                self.assertFalse((output / "sources/fixture/1.0/fixture_1.0.tar.xz").exists(), "unverified bytes were kept")

    def test_archive_path_keeps_unlisted_files_and_refuses_oversized_descriptors(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory)
            stray = output / "sources/fixture/1.0/fixture_1.0.git.tar.xz"
            stray.parent.mkdir(parents=True)
            stray.write_bytes(b"left by an earlier collection")
            location, get = self.archive()
            with get as calls, self.no_snapshot(), self.assertRaisesRegex(ValueError, "new empty output directory"):
                collector.download_source("fixture", "1.0", output, location)
            self.assertEqual(calls.call_count, 0)
            self.assertEqual(stray.read_bytes(), b"left by an earlier collection")
        with tempfile.TemporaryDirectory() as directory:
            location, get = self.archive(listed=lambda f: f.update({"fixture_1.0.dsc": ((4 << 20) + 1, f["fixture_1.0.dsc"][1])}))
            with get as calls, self.no_snapshot(), self.assertRaisesRegex(ValueError, "metadata size limit"):
                collector.download_source("fixture", "1.0", pathlib.Path(directory), location)
            self.assertEqual(calls.call_count, 0)

    def test_a_transfer_that_ends_early_is_a_network_failure(self):
        # A closed connection ends the body without an error; that must not read as wrong bytes.
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory)
            location, _ = self.archive()
            with mock.patch.object(collector, "get", side_effect=lambda url: Chunks(DESCRIPTOR[:10])), \
                    mock.patch.object(collector, "download_from_snapshot", return_value="from Snapshot") as snapshot, \
                    mock.patch("sys.stdout", io.StringIO()) as log:
                self.assertEqual(collector.download_source("fixture", "1.0", output, location), "from Snapshot")
            snapshot.assert_called_once()
            self.assertIn("ended after 10 of", log.getvalue())
            self.assertEqual(list((output / "sources/fixture/1.0").iterdir()), [], "a partial download was kept")

    def test_dropped_responses_are_retried(self):
        responses = [http.client.RemoteDisconnected("closed"), ConnectionResetError("reset"), io.BytesIO(b"ok")]
        with mock.patch.object(collector.urllib.request, "urlopen", side_effect=responses) as urlopen, \
                mock.patch.object(collector.time, "sleep"):
            self.assertEqual(collector.get("https://deb.debian.org/debian/x").read(), b"ok")
        self.assertEqual(urlopen.call_count, 3)

    def test_a_file_the_archive_cannot_serve_comes_from_snapshot(self):
        # The descriptor arrives from the archive; the archive has since dropped
        # the version. Snapshot's own path must accept what is already there.
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory)
            location, get = self.archive(serve=lambda s: s.pop("fixture_1.0.tar.xz"))
            snapshot_metadata, _ = SourceCollectionTests.fixture(None, output)
            with get, snapshot_metadata, mock.patch("sys.stdout", io.StringIO()) as log:
                result = collector.download_source("fixture", "1.0", output, location)
            self.assertIn("trying Snapshot", log.getvalue())
            self.assertEqual({pathlib.Path(item["path"]).name for item in result["files"]}, {"fixture_1.0.dsc", "fixture_1.0.tar.xz"})
            self.assertTrue(all(item["origin"].startswith(collector.SNAPSHOT) for item in result["files"]))

    def test_sources_index_yields_only_the_exact_wanted_versions(self):
        wanted = {("fixture", "1.0"), ("absent", "1.0")}
        blobs = {"fixture_1.0.dsc": DESCRIPTOR, "fixture_1.0.tar.xz": SOURCE}
        text = "\n".join((sources_stanza("fixture", "0.9", {"fixture_0.9.dsc": b"x"}),
                          sources_stanza("fixture", "1.0", blobs),
                          sources_stanza("unrelated", "1.0", {"unrelated_1.0.dsc": b"y"}))).rstrip("\n")
        found = dict(collector.archive_sources(text, wanted, ARCHIVE))
        self.assertEqual(list(found), [("fixture", "1.0")])
        self.assertEqual(found["fixture", "1.0"], dict(base=ARCHIVE, directory="pool/main/f/fixture", files={
            name: (len(data), hashlib.sha256(data).hexdigest()) for name, data in blobs.items()}))
        # The last stanza of an index has no trailing newline.
        last = dict(collector.archive_sources(sources_stanza("fixture", "1.0", blobs).rstrip("\n").replace(
            "Package-List:\n fixture-bin deb misc optional arch=any", "").rstrip("\n"), wanted, ARCHIVE))
        self.assertEqual(last, found)

    def test_unsafe_or_malformed_index_entries_are_rejected(self):
        good = {"fixture_1.0.dsc": DESCRIPTOR}
        stanzas = [sources_stanza("fixture", "1.0", good, directory) for directory in
                   ("pool/../etc", "/pool/main/f/fixture", "pool/main/f/../../x", "pool//main", "pool/main/.hidden", "dists/trixie")]
        stanzas.append(sources_stanza("fixture", "1.0", good).replace("Directory: pool/main/f/fixture\n", ""))
        stanzas.append(sources_stanza("fixture", "1.0", good).replace("fixture_1.0.dsc\n", "../fixture_1.0.dsc\n"))
        stanzas.append(sources_stanza("fixture", "1.0", {"fixture_1.0.dsc": DESCRIPTOR, "a": b"a"}).replace(" a\n", " fixture_1.0.dsc\n"))
        stanzas.append(re.sub(r"\n [0-9a-f]{64} ", "\n abc ", sources_stanza("fixture", "1.0", good)))
        stanzas.append(sources_stanza("fixture", "1.0", {"fixture_1.0.dsc": b""}))
        for stanza in stanzas:
            with self.subTest(stanza=stanza), self.assertRaisesRegex(ValueError, "invalid Debian source index entry"):
                list(collector.archive_sources(stanza, {("fixture", "1.0")}, ARCHIVE))

    def test_index_reading_is_bounded(self):
        text = sources_stanza("fixture", "1.0", {"fixture_1.0.dsc": DESCRIPTOR})
        compressed = lzma.compress(text.encode())

        with served(compressed):
            self.assertEqual(collector.read_index("index"), text)
        # Padding that arrives in a later read than the end of the stream.
        with served(compressed, b"\0" * 8):
            self.assertEqual(collector.read_index("index"), text, "stream padding is not part of the index")
        for data, message in ((compressed, "oversized"), (b"not an xz stream", "corrupt")):
            with self.subTest(message), served(data), mock.patch.object(collector, "INDEX_LIMIT", len(compressed) - 1), \
                    self.assertRaisesRegex(ValueError, message):
                collector.read_index("index")
        with served(compressed[:-12]), self.assertRaisesRegex(ConnectionError, "ended early"):
            collector.read_index("index")

    def test_decompression_stops_at_the_text_limit(self):
        # Checking the size only after decompressing would hold the whole bomb in memory first.
        largest = []
        real = lzma.LZMADecompressor

        class Measured:
            def __init__(self):
                self.inner = real()

            def __getattr__(self, name):
                return getattr(self.inner, name)

            def decompress(self, data, max_length=-1):
                result = self.inner.decompress(data, max_length)
                largest.append(len(result))
                return result

        with served(lzma.compress(b"\0" * 1000000)), mock.patch.object(collector.lzma, "LZMADecompressor", Measured), \
                mock.patch.object(collector, "INDEX_TEXT_LIMIT", 1000), self.assertRaisesRegex(ValueError, "oversized"):
            collector.read_index("index")
        self.assertLessEqual(max(largest), 1001)

    def test_suites_that_disagree_leave_the_version_to_snapshot(self):
        blobs = {"fixture_1.0.dsc": DESCRIPTOR}
        same = sources_stanza("same", "1.0", {"same_1.0.dsc": b"same"}, "pool/main/s/same")
        indices = {
            "https://deb.debian.org/debian/dists/trixie/main/source/Sources.xz": sources_stanza("fixture", "1.0", blobs) + "\n" + same,
            "https://deb.debian.org/debian/dists/trixie-updates/main/source/Sources.xz": "",
            "https://security.debian.org/debian-security/dists/trixie-security/main/source/Sources.xz":
                sources_stanza("fixture", "1.0", {"fixture_1.0.dsc": DESCRIPTOR + b"\n"}, "pool/updates/main/f/fixture") + "\n"
                + sources_stanza("same", "1.0", {"same_1.0.dsc": b"same"}, "pool/updates/main/s/same"),
        }
        with mock.patch.object(collector, "read_index", side_effect=indices.__getitem__), mock.patch("sys.stdout", io.StringIO()) as log:
            found = collector.archive_locations({("fixture", "1.0"), ("same", "1.0")})
        self.assertEqual(list(found), [("same", "1.0")])
        self.assertEqual(found["same", "1.0"]["base"], ARCHIVE, "the first suite listing a version supplies it")
        self.assertIn("disagree about fixture 1.0", log.getvalue())

    def run_main(self, output, located):
        packages = [dict(binary=name + "-bin", version=version, source=name, source_version=version)
                    for name, version in (("listed", "1.0"), ("old", "0.1"))]
        args = ["collector", "--image", "fixture-image", "--output", str(output), "--download"]
        with mock.patch("sys.argv", args), mock.patch.object(collector, "inventory", return_value=("sha256:fixture", packages)), \
                mock.patch.object(collector, "archive_locations", **located) as locate, \
                mock.patch.object(collector, "download_source", side_effect=lambda name, version, out, location: dict(package=name, version=version, files=[])) as download, \
                mock.patch("sys.stdout", io.StringIO()):
            collector.main()
        locate.assert_called_once_with({("listed", "1.0"), ("old", "0.1")})
        return [(c.args[0], c.args[3]) for c in download.call_args_list]

    def test_archive_first_and_snapshot_for_the_rest(self):
        location = dict(base=ARCHIVE, directory="pool/main/l/listed", files={})
        with tempfile.TemporaryDirectory() as directory:
            calls = self.run_main(pathlib.Path(directory), dict(return_value={("listed", "1.0"): location}))
        self.assertEqual(calls, [("listed", location), ("old", None)])

    def test_an_unavailable_suite_leaves_only_its_versions_to_snapshot(self):
        security = "https://security.debian.org/debian-security/dists/trixie-security/main/source/Sources.xz"
        listed = sources_stanza("listed", "1.0", {"listed_1.0.dsc": b"listed"}, "pool/main/l/listed")
        for error in (urllib.error.URLError("unreachable"), ConnectionError("the Debian source index ended early"),
                      http.client.RemoteDisconnected("closed")):
            def read(url):
                if url == security:
                    raise error
                return listed if "/trixie/" in url else ""

            with self.subTest(error=error), mock.patch.object(collector, "read_index", side_effect=read), \
                    mock.patch("sys.stdout", io.StringIO()) as log:
                found = collector.archive_locations({("listed", "1.0"), ("security-only", "2.0")})
            self.assertEqual(list(found), [("listed", "1.0")])
            self.assertIn("trixie-security source index is unavailable", log.getvalue())

    def test_wrong_index_content_stops_the_collection(self):
        for error in (ValueError("corrupt Debian source index"), UnicodeDecodeError("utf-8", b"\xff", 0, 1, "invalid start byte")):
            with self.subTest(error=error), mock.patch.object(collector, "read_index", side_effect=error), \
                    self.assertRaises(ValueError):
                collector.archive_locations({("listed", "1.0")})

    def test_wrong_index_content_leaves_the_collection_incomplete(self):
        packages = [dict(binary="fixture", version="1.0", source="fixture", source_version="1.0")]
        with tempfile.TemporaryDirectory() as directory:
            args = ["collector", "--image", "fixture-image", "--output", directory, "--download"]
            with mock.patch("sys.argv", args), mock.patch.object(collector, "inventory", return_value=("sha256:fixture", packages)), \
                    mock.patch.object(collector, "archive_locations", side_effect=ValueError("invalid Debian source index entry")), \
                    mock.patch.object(collector, "download_source") as download, mock.patch("sys.stdout", io.StringIO()), \
                    self.assertRaisesRegex(ValueError, "invalid Debian source index entry"):
                collector.main()
            download.assert_not_called()
            self.assertFalse(json.loads((pathlib.Path(directory) / "index.json").read_text())["complete"])

    def test_inventory_only_runs_use_no_network(self):
        packages = [dict(binary="fixture", version="1.0", source="fixture", source_version="1.0")]
        with tempfile.TemporaryDirectory() as directory:
            args = ["collector", "--image", "fixture-image", "--output", directory]
            with mock.patch("sys.argv", args), mock.patch.object(collector, "inventory", return_value=("sha256:fixture", packages)), \
                    mock.patch.object(collector, "archive_locations") as locate, mock.patch("sys.stdout", io.StringIO()):
                collector.main()
            locate.assert_not_called()

    def test_release_matches_the_browser_image_base(self):
        dockerfile = (pathlib.Path(__file__).resolve().parent.parent / "Dockerfile.browser").read_text()
        self.assertRegex(dockerfile, r"(?m)^FROM debian:" + re.escape(collector.DEBIAN_RELEASE) + r"-slim@",
                         "DEBIAN_RELEASE must name the Debian release the images install from")


if __name__ == "__main__":
    unittest.main()
