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
        (self.root / "LICENSE").write_bytes(b"Neutral notice\n")
        (self.root / "NOTICE").write_bytes((release.ROOT / "NOTICE").read_bytes())
        for name in ("plugin/.claude-plugin/plugin.json", ".claude-plugin/marketplace.json"):
            path = self.root / name
            path.parent.mkdir(parents=True)
            path.write_text(json.dumps({"version": "0.1.0"}))
        (self.root / "README.md").write_text(
            'go install -ldflags="-X main.version=0.1.0" '
            'github.com/jeon-jihyeon/meetproxy/cmd/meetproxy@v0.1.0\n'
            'go get github.com/jeon-jihyeon/meetproxy@v0.1.0\n'
            'claude plugin marketplace add jeon-jihyeon/meetproxy#v0.1.0\n')
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

    def remote_metadata(self) -> bytes:
        return json.dumps({"tagName": "v0.1.0", "isDraft": True,
                           "assets": [{"name": name} for name in release.SUBJECTS]}).encode()

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

    def test_archive_rejects_missing_or_changed_go_license(self) -> None:
        original = (self.root / "NOTICE").read_bytes()
        for notice in [b"Neutral notice\n", original.replace(b"Copyright 2009", b"Copyright 2010")]:
            with self.subTest(notice=notice[:40]):
                (self.root / "NOTICE").write_bytes(notice)
                path = self.root / "archive.tar.gz"
                path.write_bytes(self.archive())
                with self.assertRaisesRegex(release.VerificationError, "^go_notice_mismatch$"):
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
            with self.subTest(name=name), mock.patch.object(release, "check_source", return_value=self.stamp), \
                    mock.patch.object(release, "check_binary"):
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
            ("trimpath omits ldflags", metadata + vcs, b"0.1.0\n", None),
            ("compiler", metadata.replace(b"go1.26.10", b"go1.24.10"), b"0.1.0\n", "binary_compiler"),
            ("CGO", metadata.replace(b"CGO_ENABLED=0", b"CGO_ENABLED=1"), b"0.1.0\n", "binary_build_settings"),
            ("target", metadata.replace(b"GOARCH=amd64", b"GOARCH=arm64"), b"0.1.0\n", "binary_build_settings"),
            ("native version", metadata + vcs, b"dev\n", "native_version_mismatch"),
            ("missing VCS", metadata, b"0.1.0\n", "binary_source_commit"),
            ("missing revision", metadata + vcs.replace(b"vcs.revision=", b"unused="), b"0.1.0\n", "binary_source_commit"),
            ("other source", metadata + vcs.replace(b"a" * 40, b"b" * 40), b"0.1.0\n", "binary_source_commit"),
            ("dirty source", metadata + vcs.replace(b"modified=false", b"modified=true"), b"0.1.0\n", "binary_source_dirty"),
        ]
        for name, observed, version, error in cases:
            with self.subTest(name=name), mock.patch.object(release, "run", side_effect=[observed, version]), \
                    mock.patch.object(release, "native_target", return_value=("linux", "amd64")):
                if error is None:
                    self.assertIsNone(release.check_binary(b"fixture", "linux", "amd64", "0.1.0", "a" * 40))
                else:
                    with self.assertRaisesRegex(release.VerificationError, "^" + error + "$"):
                        release.check_binary(b"fixture", "linux", "amd64", "0.1.0", "a" * 40)

    def test_tag_matches_the_manifest(self) -> None:
        manifest = self.root / "plugin/.claude-plugin/plugin.json"
        manifest.write_text(json.dumps({"version": "0.1.0"}))
        release.check_tag("v0.1.0")
        with self.assertRaisesRegex(release.VerificationError, "^tag_version_mismatch$"):
            release.check_tag("v0.1.1")

    def test_tag_rejects_missing_go_notice(self) -> None:
        (self.root / "NOTICE").write_bytes(b"Neutral notice\n")
        with self.assertRaisesRegex(release.VerificationError, "^go_notice_mismatch$"):
            release.check_tag("v0.1.0")

    def test_tag_rejects_marketplace_version_mismatch(self) -> None:
        (self.root / ".claude-plugin/marketplace.json").write_text(json.dumps({"version": "0.1.1"}))
        with self.assertRaisesRegex(release.VerificationError, "^marketplace_version_mismatch$"):
            release.check_tag("v0.1.0")

    def test_tag_rejects_missing_or_mismatched_readme_versions(self) -> None:
        path = self.root / "README.md"
        original = path.read_text()
        for content in [original.replace("@v0.1.0", "@v0.1.1"),
                        original.replace("#v0.1.0", "#v0.1.1"),
                        original.replace("#v0.1.0", ""),
                        original.replace("#v0.1.0", "#main"),
                        original.replace("main.version=0.1.0", "main.version=0.1.1"),
                        original.splitlines()[0], original.splitlines()[1], ""]:
            with self.subTest(content=content):
                path.write_text(content)
                with self.assertRaisesRegex(release.VerificationError, "^readme_version_mismatch$"):
                    release.check_tag("v0.1.0")

    def test_release_notes_select_only_the_tag_section(self) -> None:
        path = self.root / "CHANGELOG.md"
        path.write_text("# Changelog\n\n## Unreleased\n\n- Pending\n\n## v0.1.0\n\n- Acme\n\n### Details\n\n- Detail\n\n## v0.0.1\n\n- Previous\n")
        self.assertEqual("- Acme\n\n### Details\n\n- Detail\n", release.release_notes("v0.1.0"))

    def test_release_notes_reject_missing_duplicate_and_empty_sections(self) -> None:
        for content in ["# Changelog\n\n## Unreleased\n\n- Pending\n",
                        "## v0.1.0\n\n- Acme\n\n## v0.1.0\n\n- Duplicate\n",
                        "## v0.1.0\n\n## v0.0.1\n\n- Previous\n"]:
            with self.subTest(content=content):
                (self.root / "CHANGELOG.md").write_text(content)
                with self.assertRaisesRegex(release.VerificationError, "^release_notes_section$"):
                    release.release_notes("v0.1.0")

    def test_source_requires_a_clean_commit_and_matching_annotated_tag(self) -> None:
        manifest = self.root / "plugin/.claude-plugin/plugin.json"
        manifest.write_text(json.dumps({"version": "0.1.0"}))
        for scenario, error in [
            ("accepted", None),
            ("wrong manifest", "tag_version_mismatch"),
            ("wrong head", "source_digest_mismatch"),
            ("dirty", "source_dirty"),
            ("untracked", "source_dirty"),
            ("lightweight", "source_tag_type"),
            ("wrong tag", "source_tag_mismatch"),
        ]:
            def cli(argv: list[str], code: str) -> bytes:
                if argv == ["git", "rev-parse", "HEAD"]:
                    return (b"b" if scenario == "wrong head" else b"a") * 40 + b"\n"
                if argv[:3] == ["git", "show", "-s"]:
                    return str(self.stamp).encode() + b"\n"
                if argv == ["git", "status", "--porcelain=v1", "--untracked-files=all"]:
                    return {"dirty": b" M fixture\n", "untracked": b"?? fixture\n"}.get(scenario, b"")
                if argv == ["git", "cat-file", "-t", "refs/tags/v0.1.0"]:
                    return b"commit\n" if scenario == "lightweight" else b"tag\n"
                self.assertEqual(["git", "rev-parse", "refs/tags/v0.1.0^{commit}"], argv)
                return (b"b" if scenario == "wrong tag" else b"a") * 40 + b"\n"

            with self.subTest(scenario=scenario), mock.patch.object(release, "run", side_effect=cli):
                if error is None:
                    self.assertEqual(self.stamp, release.check_source("v0.1.0", "a" * 40))
                else:
                    with self.assertRaisesRegex(release.VerificationError, "^" + error + "$"):
                        release.check_source("v0.1.1" if scenario == "wrong manifest" else "v0.1.0", "a" * 40)

    def test_uploaded_bytes_need_no_checkout_or_toolchain(self) -> None:
        for name in release.SUBJECTS:
            (self.root / name).write_bytes(b"fixture")
        for changed in (False, True):
            def cli(argv: list[str], code: str) -> bytes:
                self.assertEqual("github.com/o/svc", argv[argv.index("--repo") + 1])
                if argv[:3] == ["gh", "release", "view"]:
                    self.assertEqual("tagName,isDraft,assets", argv[argv.index("--json") + 1])
                    return self.remote_metadata()
                self.assertEqual(["gh", "release", "download", "v0.1.0"], argv[:4])
                destination = Path(argv[argv.index("--dir") + 1])
                for name in release.SUBJECTS:
                    (destination / name).write_bytes(b"changed" if changed else b"fixture")
                return b""

            with self.subTest(changed=changed), mock.patch.object(release, "run", side_effect=cli), \
                    mock.patch.object(release, "check_tag", side_effect=AssertionError("source access")), \
                    mock.patch.object(release, "check_artifacts", side_effect=AssertionError("toolchain access")):
                if changed:
                    with self.assertRaisesRegex(release.VerificationError, "^uploaded_digest_mismatch$"):
                        release.check_uploaded_bytes(self.root, "v0.1.0", "o/svc")
                else:
                    self.assertEqual({"uploaded_subjects": 5}, release.check_uploaded_bytes(
                        self.root, "v0.1.0", "o/svc"))

    def test_uploaded_bytes_reject_extra_remote_asset(self) -> None:
        for name in release.SUBJECTS:
            (self.root / name).write_bytes(b"fixture")

        def cli(argv: list[str], code: str) -> bytes:
            if argv[:3] == ["gh", "release", "view"]:
                return json.dumps({"tagName": "v0.1.0", "isDraft": True,
                                   "assets": [{"name": name} for name in [*release.SUBJECTS, "extra"]]}).encode()
            destination = Path(argv[argv.index("--dir") + 1])
            for name in release.SUBJECTS:
                (destination / name).write_bytes(b"fixture")
            return b""

        with mock.patch.object(release, "run", side_effect=cli):
            with self.assertRaisesRegex(release.VerificationError, "^uploaded_subjects$"):
                release.check_uploaded_bytes(self.root, "v0.1.0", "o/svc")

    def test_uploaded_bytes_reject_invalid_metadata_before_download(self) -> None:
        for name in release.SUBJECTS:
            (self.root / name).write_bytes(b"fixture")
        metadata = json.loads(self.remote_metadata())
        cases = [
            ("wrong tag", {**metadata, "tagName": "v0.0.1"}, "uploaded_tag_mismatch"),
            ("published", {**metadata, "isDraft": False}, "release_not_draft"),
            ("missing asset", {**metadata, "assets": metadata["assets"][:-1]}, "uploaded_subjects"),
            ("duplicate asset", {**metadata, "assets": [*metadata["assets"][:-1], metadata["assets"][0]]}, "uploaded_subjects"),
            ("null", None, "invalid_release_metadata"),
            ("array", [], "invalid_release_metadata"),
            ("missing tag", {"isDraft": True, "assets": metadata["assets"]}, "invalid_release_metadata"),
            ("nonboolean draft", {**metadata, "isDraft": 1}, "invalid_release_metadata"),
            ("missing assets", {"tagName": "v0.1.0", "isDraft": True}, "invalid_release_metadata"),
            ("assets object", {**metadata, "assets": {}}, "invalid_release_metadata"),
            ("invalid asset", {**metadata, "assets": [None]}, "invalid_release_metadata"),
            ("missing asset name", {**metadata, "assets": [{}]}, "invalid_release_metadata"),
            ("invalid asset name", {**metadata, "assets": [{"name": 1}]}, "invalid_release_metadata"),
        ]
        outputs = [(name, json.dumps(value).encode(), error) for name, value, error in cases]
        outputs.extend([("invalid JSON", b"{", "invalid_release_metadata"),
                        ("invalid encoding", b"\xff", "invalid_release_metadata")])
        for name, output, error in outputs:
            with self.subTest(name=name), mock.patch.object(release, "run", return_value=output) as run:
                with self.assertRaisesRegex(release.VerificationError, "^" + error + "$"):
                    release.check_uploaded_bytes(self.root, "v0.1.0", "o/svc")
                run.assert_called_once_with(["gh", "release", "view", "v0.1.0", "--repo", "github.com/o/svc",
                                             "--json", "tagName,isDraft,assets"], "release_metadata_failed")

    def test_uploaded_bytes_stop_on_metadata_command_failure(self) -> None:
        for name in release.SUBJECTS:
            (self.root / name).write_bytes(b"fixture")
        failure = release.VerificationError("release_metadata_failed")
        with mock.patch.object(release, "run", side_effect=failure) as run:
            with self.assertRaisesRegex(release.VerificationError, "^release_metadata_failed$"):
                release.check_uploaded_bytes(self.root, "v0.1.0", "o/svc")
            self.assertEqual(1, run.call_count)

    def test_uploaded_bytes_recheck_remote_metadata_after_download(self) -> None:
        for name in release.SUBJECTS:
            (self.root / name).write_bytes(b"fixture")
        metadata = json.loads(self.remote_metadata())
        for scenario in ("accepted", "extra asset", "published", "metadata failure"):
            views = 0

            def cli(argv: list[str], code: str) -> bytes:
                nonlocal views
                if argv[:3] == ["gh", "release", "view"]:
                    views += 1
                    if views == 1 or scenario == "accepted":
                        return self.remote_metadata()
                    if scenario == "extra asset":
                        return json.dumps({**metadata, "assets": [*metadata["assets"], {"name": "extra"}]}).encode()
                    if scenario == "published":
                        return json.dumps({**metadata, "isDraft": False}).encode()
                    raise release.VerificationError(code)
                self.assertEqual(["gh", "release", "download", "v0.1.0"], argv[:4])
                destination = Path(argv[argv.index("--dir") + 1])
                for name in release.SUBJECTS:
                    (destination / name).write_bytes(b"fixture")
                return b""

            with self.subTest(scenario=scenario), mock.patch.object(release, "run", side_effect=cli):
                if scenario == "accepted":
                    self.assertEqual({"uploaded_subjects": 5}, release.check_uploaded_bytes(self.root, "v0.1.0", "o/svc"))
                else:
                    error = {"extra asset": "uploaded_subjects", "published": "release_not_draft",
                             "metadata failure": "release_metadata_failed"}[scenario]
                    with self.assertRaisesRegex(release.VerificationError, "^" + error + "$"):
                        release.check_uploaded_bytes(self.root, "v0.1.0", "o/svc")
                self.assertEqual(2, views)

    def test_uploaded_bytes_reject_invalid_inputs_before_download(self) -> None:
        for name in release.SUBJECTS:
            (self.root / name).write_bytes(b"fixture")
        first = self.root / release.SUBJECTS[0]
        for scenario, error in [("tag", "invalid_tag"), ("repo", "invalid_repository"),
                                ("missing", "invalid_local_subject"), ("symlink", "invalid_local_subject")]:
            if scenario in ("missing", "symlink"):
                first.unlink()
            if scenario == "symlink":
                first.symlink_to(self.root / "checksums.txt")
            with self.subTest(scenario=scenario), mock.patch.object(release, "run") as run:
                with self.assertRaisesRegex(release.VerificationError, "^" + error + "$"):
                    release.check_uploaded_bytes(self.root, "main" if scenario == "tag" else "v0.1.0",
                                                 "../svc" if scenario == "repo" else "o/svc")
                run.assert_not_called()
            if scenario in ("missing", "symlink"):
                if first.is_symlink():
                    first.unlink()
                first.write_bytes(b"fixture")

    def test_uploaded_bytes_fix_the_host_and_stop_on_download_or_content_failure(self) -> None:
        for name in release.SUBJECTS:
            (self.root / name).write_bytes(b"fixture")
        for scenario, error in [
            ("accepted", None),
            ("changed bytes", "uploaded_digest_mismatch"),
            ("changed size", "uploaded_size_mismatch"),
            ("missing subject", "uploaded_subject_missing"),
            ("extra subject", "uploaded_subjects"),
            ("download failure", "release_download_failed"),
        ]:
            with self.subTest(scenario=scenario):
                def cli(argv: list[str], code: str) -> bytes:
                    self.assertEqual("github.com/o/svc", argv[argv.index("--repo") + 1])
                    if argv[:3] == ["gh", "release", "view"]:
                        return self.remote_metadata()
                    self.assertEqual(["gh", "release", "download", "v0.1.0"], argv[:4])
                    self.assertEqual(release.SUBJECTS, argv[argv.index("--dir") + 2:][1::2])
                    if scenario == "download failure":
                        raise release.VerificationError(code)
                    destination = Path(argv[argv.index("--dir") + 1])
                    for name in release.SUBJECTS:
                        if scenario == "missing subject" and name == release.SUBJECTS[-1]:
                            continue
                        payload = {"changed bytes": b"changed", "changed size": b"large fixture"}.get(scenario, b"fixture")
                        (destination / name).write_bytes(payload)
                    if scenario == "extra subject":
                        (destination / "extra").write_bytes(b"fixture")
                    return b""

                with mock.patch.object(release, "run", side_effect=cli), \
                        mock.patch.dict(release.os.environ, {"GH_HOST": "enterprise.example.com"}), \
                        mock.patch.object(release, "check_tag") as tag, \
                        mock.patch.object(release, "check_artifacts", return_value={"binaries": 4}) as artifacts:
                    if error is None:
                        result = release.check_uploaded(self.root, "v0.1.0", "a" * 40, "o/svc")
                        self.assertEqual(5, result["uploaded_subjects"])
                    else:
                        with self.assertRaisesRegex(release.VerificationError, "^" + error + "$"):
                            release.check_uploaded(self.root, "v0.1.0", "a" * 40, "o/svc")
                    tag.assert_called_once_with("v0.1.0")
                    self.assertEqual(2 if error is None else 1, artifacts.call_count)


if __name__ == "__main__":
    unittest.main()
