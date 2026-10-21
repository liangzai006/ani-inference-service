#!/usr/bin/env python3
"""Verify the pending API release and consumer migration in an isolated checkout.

The real root go.mod keeps the published API pin. This check applies the release
import patch and a local API replacement only inside a temporary directory.
"""

import os
from pathlib import Path
import shutil
import subprocess
import tempfile


def main():
    root = Path(__file__).resolve().parent.parent
    env = {**os.environ, "GOWORK": "off", "GOLANG_PROTOBUF_REGISTRATION_CONFLICT": "panic"}
    files = subprocess.check_output(
        ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"], cwd=root
    ).split(b"\0")
    with tempfile.TemporaryDirectory(prefix="inference-api-compatibility.") as temporary:
        stage = Path(temporary)
        for name in files:
            if not name:
                continue
            relative = Path(os.fsdecode(name))
            source = root / relative
            if not source.is_file():  # Deleted files in the working tree.
                continue
            destination = stage / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source, destination)

        def run(*args):
            subprocess.run(args, cwd=stage, env=env, check=True)

        patch = stage / "docs/execution/patches/2026-10-21-inference-contract-consumers.patch"
        # The consumer migration may already be present in the working tree.
        # Apply the preserved patch only when it still applies cleanly; this
        # keeps the compatibility check useful both before and after landing
        # the import migration.
        if patch.exists() and subprocess.run(
            ["git", "apply", "--check", str(patch)], cwd=stage, env=env,
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        ).returncode == 0:
            run("git", "apply", str(patch))
        run("go", "mod", "edit", f"-replace=github.com/liangzai006/ani-inference-service/api={stage / 'api'}")
        run("go", "test", "-mod=readonly", "-count=1", "./...")
        run("go", "vet", "-mod=readonly", "./...")
        run("go", "build", "-mod=readonly", "./...")
        print("PASS: local API and pending consumer imports; repository dependencies unchanged")


if __name__ == "__main__":
    main()
