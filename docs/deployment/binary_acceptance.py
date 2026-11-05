#!/usr/bin/env python3
"""Final-binary gate, NOT run by this unwired module candidate.

Example (integrator supplies synthetic dev8 schema, current UID, one admin grant):
 GOMAXPROCS=2 python3 docs/deployment/binary_acceptance.py \
   --server /abs/final/swarma-board-server --client /abs/final/swarma-board \
   --policy-fixture /abs/synthetic-dev8-policy.json \
   --project synthetic-board --account shared-agent --commit <40-hex-final-pin>

Uses private temporary paths, synthetic records, current nonprivileged UID only.
Never pass a live policy. No privileged actions or real deployment.
"""
import argparse
import http.client
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import tempfile
import time

PROFILE = "single-trust-unit"
HEADER = "Swarma-Board-Deployment"
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


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    for name in ("server", "client", "policy-fixture", "project", "account", "commit"):
        parser.add_argument("--" + name, required=True)
    args = parser.parse_args()
    assert re.fullmatch(r"[0-9a-f]{40}", args.commit), "exact final binary commit required"
    server, client = Path(args.server).resolve(), Path(args.client).resolve()
    fixture = Path(args.policy_fixture).read_bytes()
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
                "--audit-export-dir=" + str(exports)]
        invalid = [base[1:], base + ["--listen=127.0.0.1:0"], base + ["--tls-cert=missing"],
                   base + ["--tls-key=missing"], base + ["--remote-tls"],
                   base + ["--synthetic-fixture=missing"],
                   [x for x in base if not x.startswith("--audit-export-dir=")] + ["--audit-export-dir=" + str(data / "exports")]]
        for invocation in invalid:
            run(server, invocation, 2)
            assert not data.exists() and not sock.exists(), "invalid config wrote state/listened"

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
                policy.chmod(0o644)
                status, _ = request(sock, "/ready")
                assert status == 503, "policy loss left readiness green"
            finally:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
                    raise AssertionError("server did not drain/stop")
        refused = run(client, client_args, 1)
        assert "Host/launcher" in refused.stderr, "dead-service recovery guidance missing"
        assert not sock.exists(), "dead-service client resurrected socket or cleanup failed"
    print(json.dumps({"gate": "single-trust-unit-final-binaries", "status": "pass", "commit": args.commit,
                      "production_admitted": False, "per_agent_isolation": False}))


if __name__ == "__main__":
    main()
