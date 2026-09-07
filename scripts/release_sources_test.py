#!/usr/bin/env python3
"""Offline checks for durable release source verification."""

import hashlib
import importlib.util
import io
import json
import pathlib
import tarfile
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("verifier", pathlib.Path(__file__).with_name("check-release-sources.py"))
verifier = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verifier)


class ReleaseSourceTests(unittest.TestCase):
    def fixture(self, path, mutate=None, extra=None):
        files = {}
        body = b"matching source bytes"
        digest = hashlib.sha256(body).hexdigest()
        dsc = f"Source: fixture\nVersion: 1.0\nChecksums-Sha256:\n {digest} {len(body)} fixture_1.0.tar.xz\n".encode()
        for variant in ("static", "browser"):
            records = []
            for name, data in (("fixture_1.0.tar.xz", body), ("fixture_1.0.dsc", dsc)):
                relative = "sources/fixture/1.0/" + name
                records.append(dict(path=relative, size=len(data), sha256=hashlib.sha256(data).hexdigest()))
                files[variant + "/" + relative] = data
            index = dict(schema=1, complete=True, image_id="old-build", packages=[dict(binary="fixture", version="1.0", source="fixture", source_version="1.0")], sources=[dict(package="fixture", version="1.0", files=records)])
            files[variant + "/index.json"] = json.dumps(index).encode()
            files[variant + "/SHA256SUMS"] = "".join(item["sha256"] + "  " + item["path"] + "\n" for item in records).encode()
        if mutate:
            mutate(files)
        with tarfile.open(path, "w:gz") as archive:
            for name, data in files.items():
                entry = tarfile.TarInfo(name)
                entry.size = len(data)
                archive.addfile(entry, io.BytesIO(data))
            if extra:
                archive.addfile(extra)

    def test_complete_archive_and_split_parts(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "sources.tar.gz"
            self.fixture(path)
            expected = verifier.check_archive(path)
            data = path.read_bytes()
            parts = []
            for number, start in enumerate(range(0, len(data), 71)):
                part = pathlib.Path(directory) / f"part-{number:02d}"
                part.write_bytes(data[start:start + 71])
                parts.append(part)
            self.assertEqual(expected, verifier.check_archive(parts))

    def test_corrupt_archive_bytes_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "sources.tar.gz"
            self.fixture(path, lambda files: files.update({"static/sources/fixture/1.0/fixture_1.0.tar.xz": b"wrong source bytes"}))
            with self.assertRaisesRegex(ValueError, "SHA-256 mismatch"):
                verifier.check_archive(path)

    def test_incomplete_index_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "sources.tar.gz"
            def change(files):
                index = json.loads(files["static/index.json"])
                index["complete"] = False
                files["static/index.json"] = json.dumps(index).encode()
            self.fixture(path, change)
            with self.assertRaisesRegex(ValueError, "not complete"):
                verifier.check_archive(path)

    def test_links_and_parent_paths_are_rejected_without_extraction(self):
        for name, kind in (("static/link", tarfile.SYMTYPE), ("static/hardlink", tarfile.LNKTYPE), ("../escaped", tarfile.REGTYPE)):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                path = pathlib.Path(directory) / "sources.tar.gz"
                entry = tarfile.TarInfo(name)
                entry.type, entry.linkname = kind, "../../escaped"
                self.fixture(path, extra=entry)
                with self.assertRaises(ValueError):
                    verifier.check_archive(path)
                self.assertEqual(list(pathlib.Path(directory).iterdir()), [path])

    def test_current_image_packages_must_match(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "sources.tar.gz"
            self.fixture(path)
            indices = verifier.check_archive(path)
            packages = indices["static"]["packages"]
            with mock.patch.object(verifier.collector, "inventory", return_value=("new-build-id", packages)), mock.patch.object(verifier.collector, "run", return_value=b"amd64\n"):
                verifier.check_images(indices, "amd64", dict(static="static", browser="browser"), ["docker"])
            changed = [dict(packages[0], version="1.1", source_version="1.1")]
            with mock.patch.object(verifier.collector, "inventory", return_value=("new-build-id", changed)), mock.patch.object(verifier.collector, "run", return_value=b"amd64\n"):
                with self.assertRaisesRegex(ValueError, "inventory differs"):
                    verifier.check_images(indices, "amd64", dict(static="static", browser="browser"), ["docker"])
            with mock.patch.object(verifier.collector, "inventory", return_value=("new-build-id", packages)), mock.patch.object(verifier.collector, "run", return_value=b"arm64\n"):
                with self.assertRaisesRegex(ValueError, "architecture"):
                    verifier.check_images(indices, "amd64", dict(static="static", browser="browser"), ["docker"])


if __name__ == "__main__":
    unittest.main()
