#!/usr/bin/env python3
"""Resumable static/deep security audit helper for Nakama EVR files."""

from __future__ import annotations

import argparse
import datetime as dt
import glob
import hashlib
import json
import os
import re
import shlex
import shutil
import subprocess
import sys
from pathlib import Path
from typing import Any


SCHEMA_VERSION = 1
DEFAULT_SCOPE = "server/socket_ws.go server/session_ws.go server/evr_*.go server/evr/*.go"

PASSES: list[dict[str, Any]] = [
    {
        "id": "map",
        "description": "Inventory security-sensitive constructs.",
        "rules": [
            ("exported-func", re.compile(r"^func\s+(?:\([^)]+\)\s*)?[A-Z]\w+\("), "Exported function or method"),
            ("goroutine", re.compile(r"\bgo\s+(?:func|\w|\([^)]+\)\.)"), "Goroutine launch"),
            ("channel", re.compile(r"\bmake\s*\(\s*chan\b|<-\s*|-\s*>"), "Channel operation"),
            ("mutex", re.compile(r"\b(?:sync\.)?(?:Mutex|RWMutex)\b|\.(?:Lock|Unlock|RLock|RUnlock)\("), "Mutex or lock operation"),
            ("network", re.compile(r"\b(?:ReadMessage|WriteMessage|NextReader|NextWriter|SetReadDeadline|SetWriteDeadline|SetReadLimit|PongHandler|Upgrade)\b"), "Network/websocket call"),
            ("unmarshal", re.compile(r"\b(?:Unmarshal|Decode|Stream)\b"), "Decode/unmarshal path"),
            ("db-call", re.compile(r"\b(?:Execute|Query|QueryRow|GetAccount|Storage(?:Read|Write|Delete))\b"), "Database/storage call"),
        ],
    },
    {
        "id": "wire-input",
        "description": "Attacker-controlled sizes, loops, parsing, and allocation.",
        "rules": [
            ("dynamic-make", re.compile(r"\bmake\s*\(\s*(?:\[\][^,]+|map\[[^]]+\][^,]+|chan\s+[^,]+)\s*,\s*[^)a-zA-Z0-9_]*(?:len\(|int\(|uint|size|length|count|num|n\b)", re.IGNORECASE), "Potential dynamic allocation from variable size"),
            ("read-all", re.compile(r"\b(?:io\.ReadAll|ioutil\.ReadAll)\b"), "Unbounded read helper"),
            ("read-full", re.compile(r"\b(?:io\.)?ReadFull\b"), "Length-driven read"),
            ("slice-bound", re.compile(r"\[[^:\]]*:\s*(?:int\(|uint|size|length|count|num|n\b)"), "Variable slice bound"),
            ("wire-loop", re.compile(r"\bfor\s+.*<\s*(?:len\(|int\(|uint|size|length|count|num|n\b)", re.IGNORECASE), "Loop bound may be input-derived"),
            ("binary-read", re.compile(r"\bbinary\.(?:Read|LittleEndian|BigEndian)\b"), "Binary protocol parsing"),
        ],
    },
    {
        "id": "lifecycle",
        "description": "Goroutine/channel/timer lifecycle and cleanup.",
        "rules": [
            ("goroutine", re.compile(r"\bgo\s+"), "Goroutine launch; verify lifetime, cancellation, and recover"),
            ("timer", re.compile(r"\btime\.(?:NewTimer|AfterFunc|After|Tick|NewTicker)\b"), "Timer/ticker; verify Stop/drain and disconnect cleanup"),
            ("context-background", re.compile(r"\bcontext\.(?:Background|TODO)\(\)"), "Context not propagated from request/session"),
            ("blocking-send", re.compile(r"^\s*\w+(?:\.\w+)?\s*<-\s*"), "Possibly blocking channel send"),
            ("close", re.compile(r"\bclose\s*\("), "Channel close; verify ownership"),
        ],
    },
    {
        "id": "deadlines-backpressure",
        "description": "Network deadlines, read limits, and slow-client backpressure.",
        "rules": [
            ("read-message", re.compile(r"\b(?:ReadMessage|NextReader|ReadJSON)\b"), "Read path; verify SetReadLimit and refreshed SetReadDeadline"),
            ("write-message", re.compile(r"\b(?:WriteMessage|NextWriter|WriteJSON|WritePreparedMessage)\b"), "Write path; verify per-write SetWriteDeadline"),
            ("set-read-limit", re.compile(r"\bSetReadLimit\s*\("), "Read limit; verify value is a hard protocol cap"),
            ("deadline", re.compile(r"\bSet(?:Read|Write|Deadline)\s*\("), "Deadline call; verify refresh semantics"),
            ("select-no-default", re.compile(r"^\s*select\s*\{"), "Select; verify ctx.Done and non-blocking backpressure branches"),
        ],
    },
    {
        "id": "auth-boundary",
        "description": "Pre-auth reachability and authorization boundaries.",
        "rules": [
            ("auth-check", re.compile(r"\b(?:Authenticated|Authenticate|Token|UserId|UserID|SessionID|sessionID)\b"), "Authentication/session boundary"),
            ("permission", re.compile(r"\b(?:Admin|Owner|Moderator|Role|Permission|Kick|Ban|Mute)\b"), "Privileged action or field"),
            ("preauth-db", re.compile(r"\b(?:Query|QueryRow|Execute|GetAccount|StorageRead|StorageWrite)\b"), "Potential DB/storage work; verify auth boundary"),
        ],
    },
    {
        "id": "trust-client",
        "description": "Client-sourced privileged fields.",
        "rules": [
            ("client-field", re.compile(r"\b(?:UserID|UserId|SessionID|SessionId|GroupID|GroupId|MatchID|MatchId|PartyID|PartyId|Role|IsModerator|Score|Level|Name)\b"), "Potential client-sourced privileged field"),
            ("message-field", re.compile(r"\bmsg\.\w+|\bmessage\.\w+|\bin\.\w+"), "Inbound message field use"),
            ("server-derived", re.compile(r"\b(?:session|pipeline|ctx)\.(?:UserID|UserId|ID|Username)\b"), "Server-derived identity; compare against client fields"),
        ],
    },
    {
        "id": "concurrency",
        "description": "Shared state, races, and locks across blocking work.",
        "rules": [
            ("map", re.compile(r"\bmap\["), "Map declaration/use; verify synchronized access if shared"),
            ("sync-map", re.compile(r"\bsync\.Map\b|\.(?:Load|Store|Range|Delete)\("), "sync.Map usage; verify iteration/write semantics"),
            ("lock", re.compile(r"\.(?:Lock|RLock)\(\)"), "Lock acquisition; verify unlock and no blocking I/O while held"),
            ("unlock", re.compile(r"\.(?:Unlock|RUnlock)\(\)"), "Lock release"),
            ("atomic", re.compile(r"\batomic\."), "Atomic access; verify all accesses use atomic/lock"),
        ],
    },
    {
        "id": "amplification",
        "description": "Inbound-to-outbound fanout and expensive work.",
        "rules": [
            ("broadcast", re.compile(r"\b(?:Broadcast|broadcast|SendTo|sendTo|Range|ForEach|Presence|Track|Untrack)\b"), "Potential fanout/amplification path"),
            ("loop-send", re.compile(r"\bfor\b.*(?:range|:=).*"), "Loop; inspect body for outbound work"),
            ("db-loop", re.compile(r"\bfor\b|\b(?:Query|Execute|StorageWrite|StorageRead)\b"), "Loop or DB work; verify bounded cost per inbound message"),
        ],
    },
    {
        "id": "error-path",
        "description": "Error handling, cleanup, and swallowed failures.",
        "rules": [
            ("error-branch", re.compile(r"\bif\s+err\s*!=\s*nil\b"), "Error branch; verify log/return/close/cleanup"),
            ("ignored-error", re.compile(r"\b_\s*=\s*[^=]"), "Ignored error/result"),
            ("continue-error", re.compile(r"\bcontinue\b"), "Continue path; verify errors are not silently swallowed"),
            ("return-nil", re.compile(r"\breturn\s+nil\b"), "Nil return; verify not masking an error"),
        ],
    },
    {
        "id": "panic-safety",
        "description": "Panic boundaries and per-connection recovery.",
        "rules": [
            ("panic", re.compile(r"\bpanic\s*\("), "Explicit panic"),
            ("recover", re.compile(r"\brecover\s*\("), "Recover boundary"),
            ("type-assert", re.compile(r"\.\([^,)]*\)"), "Type assertion; verify ok form where attacker-influenced"),
            ("goroutine", re.compile(r"\bgo\s+"), "Goroutine launch; verify recover if per-connection hot path"),
        ],
    },
]

STATIC_TOOLS: list[dict[str, Any]] = [
    {"id": "go-vet-server", "command": ["go", "vet", "./server/..."], "timeout_seconds": 1200},
    {"id": "staticcheck-server", "command": ["staticcheck", "./server/..."], "timeout_seconds": 1200},
    {"id": "golangci-lint-server", "command": ["golangci-lint", "run", "./server/..."], "timeout_seconds": 1800},
    {"id": "gosec-server", "command": ["gosec", "-fmt", "json", "-out", "audit-results/static/gosec.json", "./server/..."], "timeout_seconds": 1200},
    {"id": "govulncheck", "command": ["govulncheck", "-json", "./..."], "timeout_seconds": 1200},
    {"id": "go-test-race-server", "command": ["go", "test", "-race", "./server/...", "-timeout=10m"], "timeout_seconds": 660},
    {"id": "semgrep", "command": ["semgrep", "scan", "--config", "auto", "--json", "--output", "audit-results/static/semgrep.json", "."], "timeout_seconds": 1800},
    {"id": "trivy-fs", "command": ["trivy", "fs", "--scanners", "vuln,secret,misconfig", "--format", "json", "--output", "audit-results/static/trivy-fs.json", "--exit-code", "0", "."], "timeout_seconds": 1800},
    {"id": "osv-scanner", "command": ["osv-scanner", "--recursive", "--format", "json", "--output", "audit-results/static/osv-scanner.json", "."], "timeout_seconds": 1200},
    {"id": "gitleaks", "command": ["gitleaks", "detect", "--source", ".", "--report-format", "json", "--report-path", "audit-results/static/gitleaks.json", "--no-banner", "--redact"], "timeout_seconds": 1200},
    {"id": "syft-sbom", "command": ["syft", "dir:.", "-o", "spdx-json=audit-results/static/syft-spdx.json"], "timeout_seconds": 1200},
    {"id": "grype", "command": ["grype", "dir:.", "-o", "json", "--file", "audit-results/static/grype.json"], "timeout_seconds": 1200},
    {"id": "actionlint", "command": ["actionlint", "-format", "{{json .}}"], "timeout_seconds": 600},
    {"id": "zizmor", "command": ["zizmor", "--format", "json", ".github/workflows"], "timeout_seconds": 600},
    {"id": "shellcheck", "command": ["bash", "-lc", "find .github build -name '*.sh' -print0 2>/dev/null | xargs -0 -r shellcheck"], "timeout_seconds": 600},
    {"id": "yamllint", "command": ["bash", "-lc", "shopt -s globstar nullglob; files=(.github/**/*.yml .github/**/*.yaml docker-compose*.yml docker-compose*.yaml); ((${#files[@]} == 0)) || yamllint \"${files[@]}\""], "timeout_seconds": 600},
    {"id": "npm-audit-console-ui", "command": ["npm", "audit", "--prefix", "console/ui", "--audit-level=moderate", "--json"], "timeout_seconds": 1200},
]


def now() -> str:
    return dt.datetime.now(dt.UTC).replace(microsecond=0).isoformat()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as file_obj:
        for chunk in iter(lambda: file_obj.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def line_count(path: Path) -> int:
    with path.open("rb") as file_obj:
        return sum(1 for _ in file_obj)


def expand_scope(scope: str) -> list[Path]:
    files: list[Path] = []
    for pattern in shlex.split(scope):
        matches = [Path(match) for match in glob.glob(pattern)]
        if not matches and Path(pattern).is_file():
            matches = [Path(pattern)]
        files.extend(path for path in matches if path.is_file())
    return sorted(set(files), key=lambda item: item.as_posix())


def file_entry(path: Path) -> dict[str, Any]:
    return {
        "path": path.as_posix(),
        "sha256": sha256_file(path),
        "size_bytes": path.stat().st_size,
        "line_count": line_count(path),
        "status": "pending",
        "completed_passes": [],
        "finding_counts": {},
        "notes": [],
    }


def empty_manifest(scope: str, files: list[Path]) -> dict[str, Any]:
    return {
        "schema_version": SCHEMA_VERSION,
        "repository": os.environ.get("GITHUB_REPOSITORY", ""),
        "ref": os.environ.get("GITHUB_REF_NAME", ""),
        "sha": os.environ.get("GITHUB_SHA", ""),
        "run_id": os.environ.get("GITHUB_RUN_ID", ""),
        "run_attempt": os.environ.get("GITHUB_RUN_ATTEMPT", ""),
        "scope": scope,
        "created_at": now(),
        "last_updated_at": now(),
        "last_completed_file": "",
        "last_completed_pass": "",
        "files": [file_entry(path) for path in files],
        "passes": {item["id"]: {"status": "pending", "finding_count": 0} for item in PASSES},
        "static_tools": {item["id"]: {"status": "pending", "command": " ".join(item["command"])} for item in STATIC_TOOLS},
        "totals": {"files": len(files), "file_passes_completed": 0, "findings": 0},
    }


def load_manifest(path: Path, scope: str, files: list[Path]) -> dict[str, Any]:
    if path.is_file():
        with path.open("r", encoding="utf-8") as file_obj:
            manifest = json.load(file_obj)
    else:
        manifest = empty_manifest(scope, files)

    existing = {entry["path"]: entry for entry in manifest.get("files", [])}
    refreshed: list[dict[str, Any]] = []
    for path_item in files:
        path_text = path_item.as_posix()
        fresh = file_entry(path_item)
        old = existing.get(path_text)
        if old is None:
            fresh["notes"].append(f"Added to manifest at {now()}")
            refreshed.append(fresh)
            continue
        if old.get("sha256") != fresh["sha256"]:
            fresh["notes"] = old.get("notes", []) + [f"Hash changed at {now()}; previous sha256={old.get('sha256', '')}"]
            refreshed.append(fresh)
            continue
        old.update({
            "size_bytes": fresh["size_bytes"],
            "line_count": fresh["line_count"],
        })
        refreshed.append(old)

    current_paths = {path_item.as_posix() for path_item in files}
    for path_text, old in existing.items():
        if path_text not in current_paths:
            old["status"] = "missing"
            old.setdefault("notes", []).append(f"Missing from refreshed scope at {now()}")
            refreshed.append(old)

    manifest["schema_version"] = SCHEMA_VERSION
    manifest["scope"] = scope
    manifest["files"] = sorted(refreshed, key=lambda entry: entry["path"])
    manifest.setdefault("passes", {item["id"]: {"status": "pending", "finding_count": 0} for item in PASSES})
    for item in PASSES:
        manifest["passes"].setdefault(item["id"], {"status": "pending", "finding_count": 0})
    manifest.setdefault("static_tools", {})
    for item in STATIC_TOOLS:
        manifest["static_tools"].setdefault(item["id"], {"status": "pending", "command": " ".join(item["command"])})
    manifest.setdefault("totals", {})
    return manifest


def save_manifest(manifest: dict[str, Any], path: Path) -> None:
    manifest["last_updated_at"] = now()
    completed = 0
    findings = 0
    for entry in manifest.get("files", []):
        completed += len(entry.get("completed_passes", []))
        findings += sum(entry.get("finding_counts", {}).values())
    manifest["totals"] = {
        "files": len([entry for entry in manifest.get("files", []) if entry.get("status") != "missing"]),
        "file_passes_completed": completed,
        "findings": findings,
    }
    path.parent.mkdir(parents=True, exist_ok=True)
    temp_path = path.with_suffix(path.suffix + ".tmp")
    with temp_path.open("w", encoding="utf-8") as file_obj:
        json.dump(manifest, file_obj, indent=2, sort_keys=True)
        file_obj.write("\n")
    temp_path.replace(path)


def safe_name(path_text: str, pass_id: str) -> str:
    return re.sub(r"[^A-Za-z0-9_.-]+", "_", f"{path_text}.{pass_id}.md")


def scan_file(path: Path, pass_item: dict[str, Any], reports_dir: Path) -> int:
    findings: list[tuple[int, str, str]] = []
    with path.open("r", encoding="utf-8", errors="replace") as file_obj:
        for number, line in enumerate(file_obj, start=1):
            for rule_id, pattern, message in pass_item["rules"]:
                if pattern.search(line):
                    findings.append((number, rule_id, message))

    report_dir = reports_dir / "file-passes"
    report_dir.mkdir(parents=True, exist_ok=True)
    report_path = report_dir / safe_name(path.as_posix(), pass_item["id"])
    with report_path.open("w", encoding="utf-8") as report:
        report.write(f"# {path.as_posix()} - {pass_item['id']}\n\n")
        report.write(f"{pass_item['description']}\n\n")
        if not findings:
            report.write("No heuristic candidates found for this pass.\n")
            return 0
        report.write("| Line | Rule | Why it matters |\n")
        report.write("| --- | --- | --- |\n")
        for line_number, rule_id, message in findings:
            report.write(f"| {line_number} | `{rule_id}` | {message} |\n")
    return len(findings)


def update_pass_status(manifest: dict[str, Any]) -> None:
    files = [entry for entry in manifest.get("files", []) if entry.get("status") != "missing"]
    for pass_item in PASSES:
        pass_id = pass_item["id"]
        done = sum(1 for entry in files if pass_id in entry.get("completed_passes", []))
        finding_count = sum(entry.get("finding_counts", {}).get(pass_id, 0) for entry in files)
        status = "done" if files and done == len(files) else "in_progress" if done else "pending"
        manifest["passes"][pass_id] = {
            "status": status,
            "completed_files": done,
            "total_files": len(files),
            "finding_count": finding_count,
        }


def run_file_passes(manifest: dict[str, Any], manifest_path: Path, reports_dir: Path, max_file_passes: int) -> None:
    completed_this_run = 0
    pass_by_id = {item["id"]: item for item in PASSES}
    for entry in manifest.get("files", []):
        if entry.get("status") == "missing":
            continue
        path = Path(entry["path"])
        if not path.is_file():
            entry["status"] = "missing"
            save_manifest(manifest, manifest_path)
            continue
        for pass_item in PASSES:
            pass_id = pass_item["id"]
            if pass_id in entry.get("completed_passes", []):
                continue
            if max_file_passes > 0 and completed_this_run >= max_file_passes:
                update_pass_status(manifest)
                save_manifest(manifest, manifest_path)
                return
            finding_count = scan_file(path, pass_by_id[pass_id], reports_dir)
            entry.setdefault("completed_passes", []).append(pass_id)
            entry.setdefault("finding_counts", {})[pass_id] = finding_count
            entry["status"] = "done" if len(entry["completed_passes"]) == len(PASSES) else "in_progress"
            manifest["last_completed_file"] = entry["path"]
            manifest["last_completed_pass"] = pass_id
            completed_this_run += 1
            update_pass_status(manifest)
            save_manifest(manifest, manifest_path)


def run_command(command: list[str], output_path: Path, timeout_seconds: int) -> tuple[str, int | None]:
    if shutil.which(command[0]) is None:
        output_path.write_text(f"Tool unavailable: {command[0]}\n", encoding="utf-8")
        return "unavailable", None

    with output_path.open("w", encoding="utf-8") as output:
        output.write(f"$ {' '.join(shlex.quote(part) for part in command)}\n\n")
        output.flush()
        try:
            result = subprocess.run(
                command,
                stdout=output,
                stderr=subprocess.STDOUT,
                text=True,
                timeout=timeout_seconds,
                check=False,
            )
        except subprocess.TimeoutExpired:
            output.write(f"\nTimed out after {timeout_seconds} seconds.\n")
            return "timeout", None
    return "done", result.returncode


def run_static_tools(manifest: dict[str, Any], manifest_path: Path, reports_dir: Path) -> None:
    static_dir = reports_dir / "static"
    static_dir.mkdir(parents=True, exist_ok=True)
    for tool in STATIC_TOOLS:
        tool_state = manifest["static_tools"].setdefault(tool["id"], {"status": "pending"})
        if tool_state.get("status") == "done":
            continue
        output_path = static_dir / f"{tool['id']}.txt"
        tool_state.update({
            "status": "in_progress",
            "command": " ".join(tool["command"]),
            "output_path": output_path.as_posix(),
            "started_at": now(),
        })
        save_manifest(manifest, manifest_path)
        status, exit_code = run_command(tool["command"], output_path, int(tool["timeout_seconds"]))
        tool_state.update({
            "status": status,
            "exit_code": exit_code,
            "completed_at": now(),
        })
        save_manifest(manifest, manifest_path)


def write_summary(manifest: dict[str, Any], reports_dir: Path) -> None:
    reports_dir.mkdir(parents=True, exist_ok=True)
    summary_path = reports_dir / "summary.md"
    files = [entry for entry in manifest.get("files", []) if entry.get("status") != "missing"]
    total_file_passes = len(files) * len(PASSES)
    completed_file_passes = sum(len(entry.get("completed_passes", [])) for entry in files)
    findings = sum(sum(entry.get("finding_counts", {}).values()) for entry in files)

    with summary_path.open("w", encoding="utf-8") as summary:
        summary.write("# Nakama deep security audit\n\n")
        summary.write("This report is generated by `.github/scripts/deep-security-audit.py`. It is a resumable heuristic and static-analysis audit; GPT/Copilot review should use the manifest and per-pass reports for line-by-line follow-up.\n\n")
        summary.write("## Progress\n\n")
        summary.write(f"- Files in scope: {len(files)}\n")
        summary.write(f"- File/pass units complete: {completed_file_passes}/{total_file_passes}\n")
        summary.write(f"- Heuristic candidates found: {findings}\n")
        summary.write(f"- Last completed: `{manifest.get('last_completed_file', '')}` / `{manifest.get('last_completed_pass', '')}`\n")
        summary.write(f"- Last updated: {manifest.get('last_updated_at', '')}\n\n")

        summary.write("## Pass status\n\n")
        summary.write("| Pass | Status | Completed files | Candidates |\n")
        summary.write("| --- | --- | ---: | ---: |\n")
        for pass_item in PASSES:
            state = manifest.get("passes", {}).get(pass_item["id"], {})
            summary.write(
                f"| `{pass_item['id']}` | {state.get('status', 'pending')} | "
                f"{state.get('completed_files', 0)}/{state.get('total_files', len(files))} | "
                f"{state.get('finding_count', 0)} |\n"
            )

        summary.write("\n## Static tools\n\n")
        summary.write("| Tool | Status | Exit code | Output |\n")
        summary.write("| --- | --- | ---: | --- |\n")
        for tool in STATIC_TOOLS:
            state = manifest.get("static_tools", {}).get(tool["id"], {})
            summary.write(
                f"| `{tool['id']}` | {state.get('status', 'pending')} | "
                f"{state.get('exit_code', '')} | `{state.get('output_path', '')}` |\n"
            )

        summary.write("\n## Resume instructions\n\n")
        summary.write("Trigger the workflow again with `resume_run_id` set to the prior GitHub Actions run ID. The workflow downloads the `nakama-security-audit-manifest` artifact, skips completed file/pass units whose SHA-256 hash is unchanged, and continues from the first incomplete unit.\n")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", default="audit-state/manifest.json", help="Path to the resumable manifest JSON.")
    parser.add_argument("--reports-dir", default="audit-results", help="Directory for audit reports.")
    parser.add_argument("--scope", default=DEFAULT_SCOPE, help="Shell-style glob scope.")
    parser.add_argument("--max-file-passes", type=int, default=75, help="Maximum file/pass units to process this run. Use 0 for all.")
    parser.add_argument("--run-static", action="store_true", help="Run static-analysis tools and record outputs.")
    parser.add_argument("--fail-on-findings", action="store_true", help="Exit non-zero when heuristic candidates or static failures are present.")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    manifest_path = Path(args.manifest)
    reports_dir = Path(args.reports_dir)
    files = expand_scope(args.scope)
    if not files:
        print(f"No files matched scope: {args.scope}", file=sys.stderr)
        return 2

    manifest = load_manifest(manifest_path, args.scope, files)
    save_manifest(manifest, manifest_path)
    run_file_passes(manifest, manifest_path, reports_dir, args.max_file_passes)
    if args.run_static:
        run_static_tools(manifest, manifest_path, reports_dir)
    update_pass_status(manifest)
    save_manifest(manifest, manifest_path)
    shutil.copy2(manifest_path, reports_dir / "manifest.json")
    write_summary(manifest, reports_dir)

    if args.fail_on_findings:
        findings = manifest.get("totals", {}).get("findings", 0)
        static_failures = [
            tool_id
            for tool_id, state in manifest.get("static_tools", {}).items()
            if state.get("status") == "done" and state.get("exit_code") not in (0, None)
        ]
        if findings or static_failures:
            print(f"Audit found {findings} heuristic candidates and static failures: {', '.join(static_failures)}", file=sys.stderr)
            return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
