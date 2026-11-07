import argparse
import decimal
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shlex
import subprocess
import sys
import tarfile
import tempfile
import zlib


ROOT = Path(__file__).resolve().parents[2]
TARGETS = [(system, arch) for system in ("darwin", "linux") for arch in ("amd64", "arm64")]
ARCHIVES = [f"meetproxy_{system}_{arch}.tar.gz" for system, arch in TARGETS]
SUBJECTS = [*ARCHIVES, "checksums.txt"]
MEMBERS = {"meetproxy": 0o755, "LICENSE": 0o644, "NOTICE": 0o644}
MAX_ARCHIVE = 128 * 1024 * 1024
GO_LICENSE_SHA256 = "911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad"


class VerificationError(Exception):
    pass


def require(condition: bool, code: str) -> None:
    if not condition:
        raise VerificationError(code)


def run(argv: list[str], code: str, environment: dict[str, str] | None = None) -> bytes:
    try:
        result = subprocess.run(argv, cwd=ROOT, capture_output=True, timeout=120,
                                env=dict(os.environ if environment is None else environment, GIT_OPTIONAL_LOCKS="0"))
    except (OSError, subprocess.TimeoutExpired):
        raise VerificationError(code) from None
    require(result.returncode == 0, code)
    return result.stdout


def version_of(tag: str) -> str:
    require(re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?", tag) is not None, "invalid_tag")
    return tag[1:]


def check_tag(tag: str) -> None:
    version = version_of(tag)
    check_go_notice((ROOT / "NOTICE").read_bytes())
    manifest = json.loads((ROOT / "plugin/.claude-plugin/plugin.json").read_text())
    require(version == manifest["version"], "tag_version_mismatch")
    marketplace = json.loads((ROOT / ".claude-plugin/marketplace.json").read_text())
    require(version == marketplace["version"], "marketplace_version_mismatch")
    readme = (ROOT / "README.md").read_text()
    modules = re.findall(r"github\.com/jeon-jihyeon/meetproxy(/cmd/meetproxy)?@v([0-9A-Za-z.-]+)", readme)
    builds = re.findall(r"main\.version=([0-9A-Za-z.-]+)", readme)
    plugins = re.findall(r"jeon-jihyeon/meetproxy#v([0-9A-Za-z.-]+)", readme)
    require({path for path, _ in modules} == {"", "/cmd/meetproxy"} and bool(builds) and bool(plugins)
            and all(observed == version for _, observed in modules)
            and all(observed == version for observed in [*builds, *plugins]), "readme_version_mismatch")


def release_notes(tag: str) -> str:
    version_of(tag)
    changelog = (ROOT / "CHANGELOG.md").read_text()
    headings = list(re.finditer(r"^##[ \t]+([^\n]+?)[ \t]*$", changelog, re.MULTILINE))
    matches = [index for index, heading in enumerate(headings) if heading[1] == tag]
    require(len(matches) == 1, "release_notes_section")
    index = matches[0]
    end = headings[index + 1].start() if index + 1 < len(headings) else len(changelog)
    body = changelog[headings[index].end():end].strip()
    require(bool(body), "release_notes_section")
    return body + "\n"


def commit_time(commit: str) -> int:
    require(re.fullmatch(r"[0-9a-f]{40}", commit) is not None, "invalid_source_digest")
    head = run(["git", "rev-parse", "HEAD"], "source_lookup_failed").decode().strip()
    require(head == commit, "source_digest_mismatch")
    stamp = run(["git", "show", "-s", "--format=%ct", commit], "commit_time_failed").decode().strip()
    require(stamp.isdigit(), "invalid_commit_time")
    return int(stamp)


def check_source(tag: str, commit: str) -> int:
    check_tag(tag)
    stamp = commit_time(commit)
    status = run(["git", "status", "--porcelain=v1", "--untracked-files=all"], "source_status_failed")
    require(not status.strip(), "source_dirty")
    kind = run(["git", "cat-file", "-t", "refs/tags/" + tag], "source_tag_failed").decode().strip()
    require(kind == "tag", "source_tag_type")
    tagged = run(["git", "rev-parse", "refs/tags/" + tag + "^{commit}"], "source_tag_failed").decode().strip()
    require(tagged == commit, "source_tag_mismatch")
    return stamp


def is_private_path_free(data: bytes) -> bool:
    paths = [str(ROOT).encode(), os.environ.get("HOME", "").encode()]
    return not any(path and len(path) > 1 and path in data for path in paths) and re.search(
        rb"/Users/|/home/[^/\x00\s]+/|[A-Za-z]:\\Users\\", data) is None


def check_pax(payload: bytes, stamp: int) -> None:
    while payload:
        length, separator, _ = payload.partition(b" ")
        require(bool(separator) and length.isdigit(), "invalid_pax_record")
        size = int(length)
        require(len(length) + 1 < size <= len(payload), "invalid_pax_length")
        record, payload = payload[len(length) + 1:size], payload[size:]
        require(record.endswith(b"\n"), "invalid_pax_record")
        key, separator, value = record[:-1].partition(b"=")
        require(bool(separator) and key == b"mtime", "unexpected_pax_field")
        require(re.fullmatch(rb"\d+(?:\.\d+)?", value) is not None, "invalid_pax_mtime")
        require(decimal.Decimal(value.decode()) == stamp, "pax_mtime_mismatch")


def check_go_notice(data: bytes) -> None:
    sections = data.split(b"\nGo runtime and standard library\n\n")
    require(len(sections) == 2 and hashlib.sha256(sections[1]).hexdigest() == GO_LICENSE_SHA256,
            "go_notice_mismatch")


def archive_payload(path: Path, stamp: int) -> bytes:
    require(path.is_file() and not path.is_symlink(), "invalid_archive_file")
    require(path.stat().st_size <= MAX_ARCHIVE, "archive_too_large")
    compressed = path.read_bytes()
    require(len(compressed) >= 18 and compressed[:4] == b"\x1f\x8b\x08\x00", "gzip_header_fields")
    require(compressed[4:8] == b"\0" * 4 and compressed[9] == 255, "gzip_header_metadata")
    decoder = zlib.decompressobj(31)
    data = decoder.decompress(compressed, MAX_ARCHIVE + 1)
    require(len(data) <= MAX_ARCHIVE, "archive_too_large")
    require(decoder.eof and not decoder.unused_data and not decoder.unconsumed_tail, "gzip_trailing_data")
    require(len(data) % 512 == 0, "invalid_tar_size")
    require(is_private_path_free(data), "private_path_in_archive")
    payloads: dict[str, bytes] = {}
    offset, has_end = 0, False
    while offset < len(data):
        block = data[offset:offset + 512]
        if block == b"\0" * 512:
            require(len(data) - offset >= 1024 and not any(data[offset:]), "invalid_tar_end")
            has_end = True
            break
        require(len(block) == 512, "invalid_tar_header")
        header = tarfile.TarInfo.frombuf(block, "utf-8", "strict")
        offset += 512
        require(0 <= header.size <= len(data) - offset, "invalid_tar_size")
        payload = data[offset:offset + header.size]
        offset += ((header.size + 511) // 512) * 512
        require(header.uid == header.gid == 0, "tar_owner_id")
        if header.type in (tarfile.XHDTYPE, tarfile.XGLTYPE):
            require(header.uname in ("", "root") and header.gname in ("", "root"), "pax_owner_name")
            require(header.name in ("GlobalHead.0.0", "././@PaxHeader") or
                    header.name in {"PaxHeaders.0/" + name for name in MEMBERS}, "pax_header_name")
            check_pax(payload, stamp)
            continue
        require(header.isfile() and header.name in MEMBERS, "unexpected_tar_member")
        require(header.name not in payloads, "duplicate_tar_member")
        require(header.uname == header.gname == "root", "tar_owner_name")
        require(header.mode == MEMBERS[header.name], "tar_mode")
        require(header.mtime == stamp, "tar_mtime")
        payloads[header.name] = payload
    require(has_end and set(payloads) == set(MEMBERS), "missing_tar_member")
    for name in ("LICENSE", "NOTICE"):
        require(payloads[name] == (ROOT / name).read_bytes(), "notice_content_mismatch")
    check_go_notice(payloads["NOTICE"])
    return payloads["meetproxy"]


def native_target() -> tuple[str, str]:
    system, machine = platform.system().lower(), platform.machine().lower()
    arch = {"x86_64": "amd64", "amd64": "amd64", "arm64": "arm64", "aarch64": "arm64"}.get(machine, "")
    require((system, arch) in TARGETS, "unsupported_native_target")
    return system, arch


def check_binary(data: bytes, system: str, arch: str, version: str, commit: str) -> None:
    with tempfile.TemporaryDirectory(prefix="release-binary-") as directory:
        binary = Path(directory) / "meetproxy"
        binary.write_bytes(data)
        output = run(["go", "version", "-m", str(binary)], "binary_metadata_failed").decode()
        lines = output.splitlines()
        require(bool(lines) and lines[0].endswith(": go1.26.10"), "binary_compiler")
        settings: dict[str, str] = {}
        for line in lines[1:]:
            fields = shlex.split(line.strip())
            if len(fields) == 2 and fields[0] == "build":
                key, _, value = fields[1].partition("=")
                settings[key] = value
        expected = {"GOOS": system, "GOARCH": arch, "CGO_ENABLED": "0", "-trimpath": "true"}
        require(all(settings.get(key) == value for key, value in expected.items()), "binary_build_settings")
        require(settings.get("vcs") == "git" and settings.get("vcs.revision") == commit, "binary_source_commit")
        require(settings.get("vcs.modified") == "false", "binary_source_dirty")
        if (system, arch) == native_target():
            binary.chmod(0o700)
            actual = run([str(binary), "version"], "native_version_failed", {"PATH": os.defpath})
            require(actual.decode().strip() == version, "native_version_mismatch")


def check_artifacts(directory: Path, tag: str, commit: str) -> dict[str, int]:
    version, stamp = version_of(tag), check_source(tag, commit)
    checksum = directory / "checksums.txt"
    require(checksum.is_file() and not checksum.is_symlink(), "invalid_checksum_file")
    require(checksum.stat().st_size <= 4096, "checksum_too_large")
    hashes: dict[str, str] = {}
    for line in checksum.read_text().splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  (meetproxy_(?:darwin|linux)_(?:amd64|arm64)\.tar\.gz)", line)
        if match is None or match[2] in hashes:
            raise VerificationError("invalid_checksum_entry")
        hashes[match[2]] = match[1]
    require(set(hashes) == set(ARCHIVES), "checksum_subjects")
    vcs_commits = 0
    for name, (system, arch) in zip(ARCHIVES, TARGETS):
        path = directory / name
        require(path.is_file() and not path.is_symlink() and path.stat().st_size <= MAX_ARCHIVE,
                "invalid_archive_file")
        require(hashlib.sha256(path.read_bytes()).hexdigest() == hashes[name], "checksum_mismatch")
        check_binary(archive_payload(path, stamp), system, arch, version, commit)
        vcs_commits += 1
    return {"binaries": len(ARCHIVES), "native_versions": 1, "vcs_commits": vcs_commits}


def check_remote_release(tag: str, repo: str) -> None:
    output = run(["gh", "release", "view", tag, "--repo", "github.com/" + repo,
                  "--json", "tagName,isDraft,assets"], "release_metadata_failed")
    try:
        metadata = json.loads(output)
    except (ValueError, UnicodeError):
        raise VerificationError("invalid_release_metadata") from None
    require(isinstance(metadata, dict) and isinstance(metadata.get("tagName"), str)
            and isinstance(metadata.get("isDraft"), bool) and isinstance(metadata.get("assets"), list),
            "invalid_release_metadata")
    require(metadata["tagName"] == tag, "uploaded_tag_mismatch")
    require(metadata["isDraft"], "release_not_draft")
    names = []
    for asset in metadata["assets"]:
        require(isinstance(asset, dict) and isinstance(asset.get("name"), str), "invalid_release_metadata")
        names.append(asset["name"])
    require(len(names) == len(SUBJECTS) and set(names) == set(SUBJECTS), "uploaded_subjects")


def download_and_compare(directory: Path, tag: str, repo: str, uploaded: Path) -> None:
    version_of(tag)
    require(re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repo) is not None
            and all(part not in (".", "..") for part in repo.split("/")), "invalid_repository")
    for name in SUBJECTS:
        local = directory / name
        require(local.is_file() and not local.is_symlink() and local.stat().st_size <= MAX_ARCHIVE,
                "invalid_local_subject")
    check_remote_release(tag, repo)
    command = ["gh", "release", "download", tag, "--repo", "github.com/" + repo, "--dir", str(uploaded)]
    for name in SUBJECTS:
        command.extend(["--pattern", name])
    run(command, "release_download_failed")
    for name in SUBJECTS:
        artifact = uploaded / name
        require(artifact.is_file() and not artifact.is_symlink(), "uploaded_subject_missing")
        require(artifact.stat().st_size == (directory / name).stat().st_size, "uploaded_size_mismatch")
        require(hashlib.sha256(artifact.read_bytes()).digest() ==
                hashlib.sha256((directory / name).read_bytes()).digest(), "uploaded_digest_mismatch")
    require({path.name for path in uploaded.iterdir()} == set(SUBJECTS), "uploaded_subjects")
    check_remote_release(tag, repo)


def check_uploaded_bytes(directory: Path, tag: str, repo: str) -> dict[str, int]:
    with tempfile.TemporaryDirectory(prefix="release-assets-") as destination:
        download_and_compare(directory, tag, repo, Path(destination))
    return {"uploaded_subjects": len(SUBJECTS)}


def check_uploaded(directory: Path, tag: str, commit: str, repo: str) -> dict[str, int]:
    check_tag(tag)
    check_artifacts(directory, tag, commit)
    with tempfile.TemporaryDirectory(prefix="release-assets-") as destination:
        uploaded = Path(destination)
        download_and_compare(directory, tag, repo, uploaded)
        observed = check_artifacts(uploaded, tag, commit)
        return {**observed, "uploaded_subjects": len(SUBJECTS)}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=("tag", "notes", "source", "artifacts", "uploaded", "uploaded-bytes"))
    parser.add_argument("--tag", required=True)
    parser.add_argument("--source-digest")
    parser.add_argument("--repo")
    parser.add_argument("--output", type=Path)
    parser.add_argument("--dist", type=Path, default=ROOT / "dist")
    args = parser.parse_args()
    observed: dict[str, int] = {}
    try:
        if args.mode == "tag":
            check_tag(args.tag)
        elif args.mode == "notes":
            check_tag(args.tag)
            require(args.output is not None, "missing_notes_output")
            args.output.write_text(release_notes(args.tag))
        elif args.mode == "source":
            check_source(args.tag, args.source_digest or "")
        elif args.mode == "artifacts":
            observed = check_artifacts(args.dist, args.tag, args.source_digest or "")
        elif args.mode == "uploaded-bytes":
            observed = check_uploaded_bytes(args.dist, args.tag, args.repo or "")
        else:
            observed = check_uploaded(args.dist, args.tag, args.source_digest or "", args.repo or "")
    except VerificationError as error:
        print(json.dumps({"ok": False, "code": str(error)}))
        return 1
    except (OSError, ValueError, KeyError, tarfile.TarError, EOFError, zlib.error):
        print(json.dumps({"ok": False, "code": "invalid_release_data"}))
        return 1
    print(json.dumps({"ok": True, "check": args.mode, **observed}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
