import hashlib
import os
import pathlib
import sys
import tarfile
import tempfile
import urllib.parse
import urllib.request


def main():
    root = pathlib.Path(os.environ.get("MODEL_ROOT", "/model"))
    expected = os.environ["MODEL_SHA256"].lower()
    limit = 512 << 20
    target = root / "data"
    if target.exists():
        if (target / ".artifact-sha256").read_text().strip() == expected:
            print("verified existing model bundle sha256=" + expected)
            return
        raise ValueError("existing model does not match expected digest")
    url = os.environ["MODEL_DOWNLOAD_URL"]
    scheme = urllib.parse.urlsplit(url).scheme
    if scheme != "https":
        raise ValueError("download transport is not allowed")
    digest, size = hashlib.sha256(), 0
    with tempfile.TemporaryFile(dir=root) as archive:
        with urllib.request.urlopen(url, timeout=60) as source:
            while True:
                chunk = source.read(1 << 20)
                if not chunk:
                    break
                size += len(chunk)
                if size > limit:
                    raise ValueError("download exceeds size limit")
                digest.update(chunk)
                archive.write(chunk)
        if digest.hexdigest() != expected:
            raise ValueError("artifact checksum mismatch")
        archive.seek(0)
        stage = pathlib.Path(tempfile.mkdtemp(prefix="verified-", dir=root))
        names, extracted = set(), 0
        with tarfile.open(fileobj=archive, mode="r:") as bundle:
            for member in bundle:
                path = pathlib.PurePosixPath(member.name)
                if not member.isfile() or path.is_absolute() or ".." in path.parts or "\\" in member.name or member.name in names or member.name == ".artifact-sha256":
                    raise ValueError("unsafe bundle member")
                if not member.name or str(path) != member.name:
                    raise ValueError("noncanonical bundle member")
                extracted += member.size
                if extracted > limit or len(names) >= 1024:
                    raise ValueError("bundle exceeds extraction limit")
                names.add(member.name)
                dest = stage.joinpath(*path.parts)
                dest.parent.mkdir(parents=True, exist_ok=True)
                with bundle.extractfile(member) as source, dest.open("xb") as output:
                    while chunk := source.read(1 << 20):
                        output.write(chunk)
                dest.chmod(0o444)
        if not {"config.json", "tokenizer.json"}.issubset(names) or not any(x.endswith(".safetensors") for x in names):
            raise ValueError("bundle lacks required model files")
        (stage / ".artifact-sha256").write_text(expected + "\n")
        stage.rename(target)
        print(f"model bundle verified sha256={expected} bytes={size} files={len(names)}")


try:
    main()
except Exception as error:
    # HTTP and URL exceptions include credentials in their text. Emit only a
    # bounded class; checksum/archive errors are our own constant messages.
    message = str(error) if type(error) is ValueError else type(error).__name__
    print("model materialization failed: " + message, file=sys.stderr)
    sys.exit(1)
