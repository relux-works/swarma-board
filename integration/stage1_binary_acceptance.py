#!/usr/bin/env python3
"""Integrator final-binary synthetic gate, derived from the frozen deployment handoff.

Example (integrator supplies synthetic dev8 schema, current UID, one admin grant):
 GOMAXPROCS=2 python3 docs/deployment/binary_acceptance.py \
   --server /abs/final/swarma-board-server --client /abs/final/swarma-board \
   --policy-fixture /abs/synthetic-dev8-policy.json \
   --project synthetic-board --account shared-agent --commit <40-hex-final-pin>

Uses private temporary paths, synthetic records, current nonprivileged UID only.
Never pass a live policy. No privileged actions or real deployment.
"""
import argparse
import hashlib
import shutil
import http.client
import json
import os
from pathlib import Path
import re
import socket
import stat
import subprocess
import tempfile
import time

PROFILE = "single-trust-unit"
HEADER = "Swarma-Board-Deployment"
OBSERVATIONS = []
EXPECTED = {
    "profile": PROFILE,
    "use": "first-dogfood",
    "transport": "uds-only",
    "identity": "shared-agent-uid; no per-agent isolation",
    "per_agent_isolation": False,
    "production_admitted": False,
    "production_blockers": ["A2-035: O/S/C principals and UID-helper", "A2-036: protected per-request policy publisher"],
    "policy": "release-static-owner-private-0600",
    "lifecycle_owner": "agents2 board orchestrator",
    "client_self_spawn": False,
    "client_auto_recovery": False,
    "remote_tls_qualification_supported": True,
    "remote_tls_activation": False,
}


def run(binary, args, code):
    result = subprocess.run([str(binary), *args], capture_output=True, text=True, timeout=8)
    OBSERVATIONS.append({"argv": [str(binary), *args], "expected_exit": code, "exit": result.returncode,
                         "stdout": result.stdout, "stderr": result.stderr})
    assert result.returncode == code, (binary.name, args[:2], result.returncode, "unexpected exit")
    return result


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("swarma-board.local", timeout=2)
        self.path = str(path)

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


def request(sock, route):
    conn = UnixHTTP(sock)
    try:
        conn.request("GET", route)
        resp = conn.getresponse()
        body = json.loads(resp.read())
        assert json.loads(resp.getheader(HEADER, "null")) == EXPECTED, "missing/unsafe live descriptor"
        return resp.status, body
    finally:
        conn.close()


def check_socket_startup_refusals(server, base, root, evidence):
    witnesses = []
    for case in ("socket-parent-0755", "occupied-live-socket"):
        runtime = root / (case + "-run")
        runtime.mkdir(mode=0o700)
        # Explicit chmod: a restrictive process umask must not mask this probe.
        runtime.chmod(0o755 if case == "socket-parent-0755" else 0o700)
        sock, data, exports = runtime / "s", root / (case + "-data"), root / (case + "-exports")
        changed = [x for x in base if not x.startswith(("--unix-socket=", "--data-dir=", "--audit-export-dir="))]
        changed += ["--unix-socket=" + str(sock), "--data-dir=" + str(data), "--audit-export-dir=" + str(exports)]
        foreign = None
        before = None
        try:
            if case == "occupied-live-socket":
                foreign = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
                foreign.settimeout(2)
                foreign.bind(str(sock))
                foreign.listen(1)
                before = sock.lstat()
            assert not os.path.lexists(data) and not os.path.lexists(exports), "probe requires absent state"
            assert stat.S_IMODE(runtime.stat().st_mode) == (0o755 if foreign is None else 0o700)
            result = run(server, changed, 1)
            witness = {"case": case, "exit": result.returncode,
                       "runtime_mode": oct(stat.S_IMODE(runtime.stat().st_mode)),
                       "data_created": os.path.lexists(data),
                       "owner_lock_created": os.path.lexists(data / ".owner.lock"),
                       "database_files": sorted(p.name for p in data.glob("*.db")),
                       "exports_created": os.path.lexists(exports)}
            if foreign is None:
                witness["socket_created"] = os.path.lexists(sock)
            else:
                after = sock.lstat()
                witness["foreign_socket_before"] = {"device": before.st_dev, "inode": before.st_ino,
                                                    "mode": before.st_mode, "uid": before.st_uid,
                                                    "mtime_ns": before.st_mtime_ns}
                witness["foreign_socket_after"] = {"device": after.st_dev, "inode": after.st_ino,
                                                   "mode": after.st_mode, "uid": after.st_uid,
                                                   "mtime_ns": after.st_mtime_ns}
                with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as peer:
                    peer.settimeout(2)
                    peer.connect(str(sock))
                    accepted, _ = foreign.accept()
                    with accepted:
                        accepted.sendall(b"foreign-listener-preserved")
                        witness["foreign_listener_reachable"] = peer.recv(64) == b"foreign-listener-preserved"
            # Retain observations even when the old binary exhibits F1.
            (evidence / (case + ".json")).write_text(json.dumps(witness, indent=2) + "\n")
            witnesses.append(witness)
        finally:
            if foreign is not None:
                foreign.close()
                if before is not None and os.path.lexists(sock) and os.path.samestat(before, sock.lstat()):
                    sock.unlink()  # only this synthetic probe's own socket
    for witness in witnesses:
        assert not witness["data_created"] and not witness["owner_lock_created"] and not witness["database_files"], (witness["case"], "socket refusal created board data")
        assert not witness["exports_created"], (witness["case"], "socket refusal created exports")
        if witness["case"] == "socket-parent-0755":
            assert not witness["socket_created"], "invalid parent created socket"
        else:
            assert witness["foreign_socket_before"] == witness["foreign_socket_after"], "foreign socket replaced or changed"
            assert witness["foreign_listener_reachable"], "foreign listener disrupted"
    return witnesses


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    for name in ("server", "client", "policy-fixture", "project", "account", "commit"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--evidence-dir", required=True)
    args = parser.parse_args()
    assert re.fullmatch(r"[0-9a-f]{40}", args.commit), "exact final binary commit required"
    server, client = Path(args.server).resolve(), Path(args.client).resolve()
    fixture = Path(args.policy_fixture).read_bytes()
    policy_version = json.loads(fixture)["policy_version"]
    evidence = Path(args.evidence_dir).resolve()
    evidence.mkdir(mode=0o700)
    assert len(fixture) <= 1 << 20, "synthetic policy fixture too large"
    for binary in (server, client):
        status = run(binary, ["--deployment-profile=" + PROFILE, "--deployment-status"], 0)
        assert json.loads(status.stdout) == EXPECTED, "static binary descriptor mismatch"
        run(binary, ["--deployment-status"], 2)
        run(binary, ["--deployment-profile=unknown", "--deployment-status"], 2)
        refused = run(binary, ["--deployment-profile=production", "--deployment-status"], 2)
        diagnostic = refused.stdout + refused.stderr
        assert "A2-035" in diagnostic and "A2-036" in diagnostic, "production gate diagnostics missing"
        run(binary, ["--deployment-profile=" + PROFILE, "--deployment-profile=" + PROFILE, "--deployment-status"], 2)
        run(binary, ["--deployment-profile=", "--deployment-profile=" + PROFILE, "--deployment-status"], 2)

    with tempfile.TemporaryDirectory(prefix="bd-final-", dir="/tmp") as temporary:
        root = Path(temporary)
        runtime = root / "run"
        runtime.mkdir(mode=0o700)
        policy = root / "policy" / "release.json"
        policy.parent.mkdir(mode=0o700)
        policy.write_bytes(fixture)
        policy.chmod(0o600)
        sock, data, exports = runtime / "s", root / "data", root / "exports"
        base = ["--deployment-profile=" + PROFILE, "--unix-socket=" + str(sock),
                "--data-dir=" + str(data), "--policy-file=" + str(policy),
                "--audit-export-dir=" + str(exports), "--policy-version=" + policy_version]
        invalid = [base[1:], base + ["--listen=127.0.0.1:0"], base + ["--tls-cert=missing"],
                   base + ["--tls-key=missing"], base + ["--remote-tls"],
                   base + ["--synthetic-fixture=missing"],
                   [x for x in base if not x.startswith("--audit-export-dir=")] + ["--audit-export-dir=" + str(data / "exports")]]
        invalid.extend([base + ["--deployment-status"], base + ["--policy-version=other"],
                        base + ["--unix-socket="], base + ["--remote-tls=false"],
                        [x for x in base if not x.startswith("--policy-version=")]])
        for invocation in invalid:
            run(server, invocation, 2)
            assert not data.exists() and not sock.exists(), "invalid config wrote state/listened"

        for bad in (["--policy-version=wrong"],):
            changed = [x for x in base if not x.startswith("--policy-version=")] + bad
            run(server, changed, 1)
            assert not data.exists() and not sock.exists(), "policy pin refusal wrote state"
        socket_refusals = check_socket_startup_refusals(server, base, root, evidence)
        projection = root / "projection.json"
        projection.write_text(json.dumps({"endpoint": "unix://" + str(sock), "project_id": args.project, "account_id": args.account}))
        projection.chmod(0o600)
        client_args = ["--deployment-profile=" + PROFILE, "--deployment-projection=" + str(projection), "--json", "version", "--server"]
        remote = root / "remote.json"
        remote.write_text(json.dumps({"endpoint": "https://example.invalid", "project_id": args.project, "account_id": args.account}))
        run(client, ["--deployment-profile=" + PROFILE, "--deployment-projection=" + str(remote), "version", "--server"], 2)

        # Child process is the final server under test with synthetic policy only.
        # Logs stay in private temp files, never copied to the acceptance report.
        with (root / "server.log").open("wb") as log:
            process = subprocess.Popen([str(server), *base], stdout=log, stderr=log)
            try:
                deadline = time.monotonic() + 5
                while True:
                    assert process.poll() is None, "final server refused valid synthetic dev8 policy"
                    try:
                        status, _ = request(sock, "/ready")
                        if status == 200:
                            break
                    except (OSError, http.client.HTTPException):
                        pass
                    assert time.monotonic() < deadline, "final readiness deadline"
                    time.sleep(0.03)
                status, version = request(sock, "/v1/version")
                assert status == 200 and version["commit"] == args.commit, "final server pin mismatch"
                verified = run(client, client_args, 0)
                assert json.loads(verified.stdout)["commit"] == args.commit, "client server preflight pin mismatch"
                local_version = run(client, ["--json", "version"], 0)
                assert json.loads(local_version.stdout)["commit"] == args.commit, "final client pin mismatch"
                status, identity = request(sock, "/v1/me")
                assert status == 200 and identity["account_id"] == args.account, "kernel/policy identity mismatch"
                selected = client_args[:-2] # retains explicit projection and --json
                created = json.loads(run(client, selected + ["m", 'create(type=epic,name="Synthetic final binary epic")'], 0).stdout)
                epic = next(iter(created.values()))["id"]
                # The CLI and final service persist a real synthetic mutation.
                assert epic.startswith("EPIC-")
                source = data / (args.project + ".db")
                export_args = ["maintenance-export", "--database=" + str(source), "--project=" + args.project,
                               "--writer-stopped", "--output=" + str(root / "busy")]
                run(server, export_args, 1)
                assert not (root / "busy").exists(), "live writer export succeeded"
                policy.chmod(0o644)
                status, _ = request(sock, "/ready")
                assert status == 503, "policy loss left readiness green"
                policy.chmod(0o600)
                status, _ = request(sock, "/ready")
                assert status == 503, "policy restoration cleared restart latch"
            finally:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
                    raise AssertionError("server did not drain/stop")
        assert process.returncode == 0, "server did not drain cleanly"
        source = data / (args.project + ".db")
        original = source.read_bytes()
        original_stat = source.stat()
        exports.mkdir(mode=0o700)
        checkpoint = exports / "checkpoint"
        export_args = ["maintenance-export", "--database=" + str(source), "--project=" + args.project,
                       "--writer-stopped", "--output=" + str(checkpoint)]
        exported = run(server, export_args, 0)
        assert "unsigned" in exported.stderr and "writer remains stopped" in exported.stderr
        manifest = json.loads((checkpoint / "manifest.json").read_bytes())
        assert manifest["version"] == 1 and manifest["status"] == "unsigned"
        assert manifest["exporter_commit"] == args.commit and manifest["revision"] == 1
        assert manifest["declared_signer_key_id"] == ""
        assert sorted(x.name for x in checkpoint.iterdir()) == ["manifest.json", "snapshot.tar.gz", "task-audit.ndjson"]
        for key in ("snapshot", "audit"):
            artifact = checkpoint / manifest[key]["name"]
            payload = artifact.read_bytes()
            assert len(payload) == manifest[key]["bytes"] and hashlib.sha256(payload).hexdigest() == manifest[key]["sha256"]
            assert artifact.stat().st_mode & 0o777 == 0o600
        run(server, export_args, 1)
        run(server, ["maintenance-import", "--database=" + str(source), "--input=" + str(checkpoint)], 2)
        assert source.read_bytes() == original and source.stat().st_ino == original_stat.st_ino and source.stat().st_mtime_ns == original_stat.st_mtime_ns
        shutil.copytree(checkpoint, evidence / "checkpoint")
        shutil.copyfile(source, evidence / "synthetic-source.db")
        refused = run(client, client_args, 1)
        assert "Host/launcher" in refused.stderr, "dead-service recovery guidance missing"
        assert not sock.exists(), "dead-service client resurrected socket or cleanup failed"
    result = {"gate": "single-trust-unit-final-binaries", "status": "pass", "commit": args.commit,
                      "production_admitted": False, "per_agent_isolation": False,
                      "synthetic_only": True, "policy_pin_checked": True, "policy_drift_latched": True,
                      "actual_cli_mutation_export": True, "manifest_status": "unsigned",
                      "source_bytes_inode_mtime_preserved": True, "live_writer_export_refused": True,
                      "socket_refusals_no_data_created": socket_refusals,
                      "import_refused": True, "no_resurrection": True}
    (evidence / "acceptance.json").write_text(json.dumps(result, indent=2) + "\n")
    (evidence / "process.json").write_text(json.dumps(OBSERVATIONS, indent=2) + "\n")
    print(json.dumps(result))


if __name__ == "__main__":
    main()
