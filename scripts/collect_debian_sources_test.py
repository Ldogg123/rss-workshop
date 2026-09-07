#!/usr/bin/env python3
"""Offline checks for source matching, checksum enforcement, and release state."""

import hashlib
import importlib.util
import io
import json
import pathlib
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("collector", pathlib.Path(__file__).with_name("collect-debian-sources.py"))
collector = importlib.util.module_from_spec(spec)
spec.loader.exec_module(collector)


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
            with mock.patch("sys.argv", args), mock.patch.object(collector, "inventory", return_value=("sha256:fixture", packages)), mock.patch.object(collector, "download_source", side_effect=ValueError("missing exact version")):
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


if __name__ == "__main__":
    unittest.main()
