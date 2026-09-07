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


class SourceCollectionTests(unittest.TestCase):
    def fixture(self, output, *, wrong_descriptor=False, bad_filename=False):
        archive = b"a deterministic source archive"
        sha256 = "0" * 64 if wrong_descriptor else hashlib.sha256(archive).hexdigest()
        descriptor = f"Format: 3.0 (native)\nSource: fixture\nVersion: 1.0\nChecksums-Sha256:\n {sha256} {len(archive)} fixture_1.0.tar.xz\n".encode()
        blobs = {hashlib.sha1(archive).hexdigest(): archive, hashlib.sha1(descriptor).hexdigest(): descriptor}
        names = dict(zip(blobs, ("../escape" if bad_filename else "fixture_1.0.tar.xz", "fixture_1.0.dsc")))

        def metadata(path):
            if path == "/mr/package/fixture/1.0/srcfiles":
                return dict(package="fixture", version="1.0", result=[dict(hash=key) for key in blobs])
            for digest, data in blobs.items():
                if path == "/mr/file/" + digest + "/info":
                    return dict(result=[dict(name=names[digest], size=len(data))])
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
