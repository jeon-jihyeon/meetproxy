import gzip
import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest import mock

import verify_release as release


class ReleaseTests(unittest.TestCase):
    def setUp(self) -> None:
        temporary = tempfile.TemporaryDirectory(prefix="release-test-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.stamp = 1700000000
        for name in ("LICENSE", "NOTICE"):
            (self.root / name).write_bytes(b"Neutral notice\n")
        patch = mock.patch.object(release, "ROOT", self.root)
        patch.start()
        self.addCleanup(patch.stop)

    def archive(self, owner: str = "root", uid: int = 0, pax: dict[str, str] | None = None) -> bytes:
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w", format=tarfile.PAX_FORMAT) as archive:
            for name, mode in release.MEMBERS.items():
                payload = b"fixture" if name == "meetproxy" else (self.root / name).read_bytes()
                header = tarfile.TarInfo(name)
                header.size, header.mode, header.mtime = len(payload), mode, self.stamp
                header.uname, header.gname, header.uid = owner, "root", uid
                header.pax_headers = pax or {}
                archive.addfile(header, io.BytesIO(payload))
        compressed = io.BytesIO()
        with gzip.GzipFile(fileobj=compressed, mode="wb", filename="", mtime=0) as output:
            output.write(buffer.getvalue())
        return compressed.getvalue()

    def test_archive_accepts_normalized_members_and_rejects_metadata(self) -> None:
        cases = [
            ("normalized", "root", 0, None, None),
            ("numeric mtime", "root", 0, {"mtime": str(self.stamp)}, None),
            ("owner", "ann", 0, None, "tar_owner_name"),
            ("uid", "root", 7, None, "tar_owner_id"),
            ("extra PAX", "root", 0, {"comment": "fixture"}, "unexpected_pax_field"),
            ("masked owner", "ann", 0, {"uname": "root"}, "unexpected_pax_field"),
        ]
        for name, owner, uid, pax, error in cases:
            with self.subTest(name=name):
                path = self.root / "archive.tar.gz"
                path.write_bytes(self.archive(owner, uid, pax))
                if error is None:
                    self.assertEqual(b"fixture", release.archive_payload(path, self.stamp))
                else:
                    with self.assertRaisesRegex(release.VerificationError, "^" + error + "$"):
                        release.archive_payload(path, self.stamp)

    def test_archive_rejects_gzip_fields_and_trailing_members(self) -> None:
        original = self.archive()
        for name, payload, error in [
            ("filename", original[:3] + b"\x08" + original[4:10] + b"fixture\0" + original[10:], "gzip_header_fields"),
            ("second stream", original + original, "gzip_trailing_data"),
        ]:
            with self.subTest(name=name):
                path = self.root / "archive.tar.gz"
                path.write_bytes(payload)
                with self.assertRaisesRegex(release.VerificationError, "^" + error + "$"):
                    release.archive_payload(path, self.stamp)

    def test_artifacts_reject_changed_and_extra_checksums(self) -> None:
        payload = self.archive()
        for name in release.ARCHIVES:
            (self.root / name).write_bytes(payload)
        correct = "".join(hashlib.sha256(payload).hexdigest() + "  " + name + "\n" for name in release.ARCHIVES)
        for name, checksum, error in [
            ("accepted", correct, None),
            ("changed", "0" * 64 + correct[64:], "checksum_mismatch"),
            ("extra", correct + correct.splitlines()[0] + "\n", "invalid_checksum_entry"),
        ]:
            with self.subTest(name=name), mock.patch.object(release, "commit_time", return_value=self.stamp), \
                    mock.patch.object(release, "check_binary", return_value=False):
                (self.root / "checksums.txt").write_text(checksum)
                if error is None:
                    self.assertEqual(4, release.check_artifacts(self.root, "v0.1.0", "a" * 40)["binaries"])
                else:
                    with self.assertRaisesRegex(release.VerificationError, "^" + error + "$"):
                        release.check_artifacts(self.root, "v0.1.0", "a" * 40)

    def test_binary_checks_actual_supported_metadata_and_native_version(self) -> None:
        metadata = b"fixture: go1.26.10\n\tbuild\tGOOS=linux\n\tbuild\tGOARCH=amd64\n\tbuild\tCGO_ENABLED=0\n\tbuild\t-trimpath=true\n"
        vcs = b"\tbuild\tvcs=git\n\tbuild\tvcs.revision=" + b"a" * 40 + b"\n\tbuild\tvcs.modified=false\n"
        cases = [
            ("trimpath omits ldflags", metadata, b"0.1.0\n", False, None),
            ("compiler", metadata.replace(b"go1.26.10", b"go1.24.10"), b"0.1.0\n", False, "binary_compiler"),
            ("CGO", metadata.replace(b"CGO_ENABLED=0", b"CGO_ENABLED=1"), b"0.1.0\n", False, "binary_build_settings"),
            ("target", metadata.replace(b"GOARCH=amd64", b"GOARCH=arm64"), b"0.1.0\n", False, "binary_build_settings"),
            ("native version", metadata, b"dev\n", False, "native_version_mismatch"),
            ("source", metadata + vcs, b"0.1.0\n", True, None),
            ("other source", metadata + vcs.replace(b"a" * 40, b"b" * 40), b"0.1.0\n", False, "binary_source_commit"),
            ("dirty source", metadata + vcs.replace(b"modified=false", b"modified=true"), b"0.1.0\n", False, "binary_source_dirty"),
        ]
        for name, observed, version, has_vcs, error in cases:
            with self.subTest(name=name), mock.patch.object(release, "run", side_effect=[observed, version]), \
                    mock.patch.object(release, "native_target", return_value=("linux", "amd64")):
                if error is None:
                    self.assertEqual(has_vcs, release.check_binary(b"fixture", "linux", "amd64", "0.1.0", "a" * 40))
                else:
                    with self.assertRaisesRegex(release.VerificationError, "^" + error + "$"):
                        release.check_binary(b"fixture", "linux", "amd64", "0.1.0", "a" * 40)

    def test_tag_matches_the_manifest(self) -> None:
        manifest = self.root / "plugin/.claude-plugin/plugin.json"
        manifest.parent.mkdir(parents=True)
        manifest.write_text(json.dumps({"version": "0.1.0"}))
        release.check_tag("v0.1.0")
        with self.assertRaisesRegex(release.VerificationError, "^tag_version_mismatch$"):
            release.check_tag("v0.1.1")

    def test_attestations_fix_subjects_and_policy_and_stop_on_failure(self) -> None:
        for name in release.SUBJECTS:
            (self.root / name).write_bytes(b"fixture")
        for scenario, error, count in [
            ("accepted", None, 5),
            ("changed bytes", "uploaded_digest_mismatch", 0),
            ("verify error", "attestation_failed", 2),
            ("empty result", "attestation_empty", 1),
        ]:
            with self.subTest(scenario=scenario):
                verified = []

                def cli(argv: list[str], code: str) -> bytes:
                    if argv[:3] == ["gh", "release", "download"]:
                        destination = Path(argv[argv.index("--dir") + 1])
                        for name in release.SUBJECTS:
                            (destination / name).write_bytes(b"changed" if scenario == "changed bytes" else b"fixture")
                        return b""
                    self.assertEqual(["gh", "attestation", "verify"], argv[:3])
                    self.assertEqual(release.SUBJECTS[len(verified)], Path(argv[3]).name)
                    self.assertEqual([
                        "--hostname", "github.com", "--repo", "o/svc",
                        "--signer-workflow", "o/svc/.github/workflows/release.yml",
                        "--source-digest", "a" * 40, "--source-ref", "refs/tags/v0.1.0",
                        "--deny-self-hosted-runners", "--format", "json",
                    ], argv[4:])
                    verified.append(argv[3])
                    if scenario == "verify error" and len(verified) == 2:
                        raise release.VerificationError(code)
                    return b"[]" if scenario == "empty result" else b"[{}]"

                with mock.patch.object(release, "run", side_effect=cli), \
                        mock.patch.object(release, "check_tag") as tag, \
                        mock.patch.object(release, "check_artifacts") as artifacts:
                    if error is None:
                        release.check_attestations(self.root, "v0.1.0", "a" * 40, "o/svc")
                    else:
                        with self.assertRaisesRegex(release.VerificationError, "^" + error + "$"):
                            release.check_attestations(self.root, "v0.1.0", "a" * 40, "o/svc")
                    tag.assert_called_once_with("v0.1.0")
                    artifacts.assert_called_once_with(self.root, "v0.1.0", "a" * 40)
                self.assertEqual(count, len(verified))


if __name__ == "__main__":
    unittest.main()
