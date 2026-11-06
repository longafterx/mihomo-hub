"""Exercise the real production CLI with ephemeral local credentials and data."""
import errno
import http.cookiejar
import json
import os
from pathlib import Path
import pty
import select
import signal
import socket
import sqlite3
import subprocess
import tempfile
import termios
import time
import urllib.error
import urllib.request


with tempfile.TemporaryDirectory(prefix="mihomohub-cli-") as directory:
    root = Path(directory)
    binary = root / "mihomo-hub"
    supplied = os.environ.get("MIHOMOHUB_TEST_BINARY")
    if supplied:
        import shutil
        shutil.copy2(supplied, binary)
    else:
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/mihomo-hub"], check=True)
    version = json.loads(subprocess.check_output([str(binary), "version"]))
    assert version["schema_version"] == 4
    if supplied:
        assert version["frontend_embedded"] is True
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    origin = f"http://127.0.0.1:{port}"
    config = root / "config.yaml"
    config.write_text(
        f'server:\n  listen: "127.0.0.1:{port}"\n  public_origin: "{origin}"\n'
        f'database:\n  path: "{root / "hub.db"}"\n'
        f'security:\n  master_key_file: "{root / "master.key"}"\n'
    )
    environment = {key: value for key, value in os.environ.items() if not key.startswith("MIHOMOHUB_")}
    fixture_password = "12345678"
    master, slave = pty.openpty()
    initializer = subprocess.Popen(
        [str(binary), "init", "--config", str(config)],
        stdin=slave, stdout=slave, stderr=slave, env=environment,
    )
    os.close(slave)
    transcript = b""
    try:
        for prompt in (b"Admin password", b"Confirm password"):
            deadline = time.monotonic() + 10
            while prompt not in transcript:
                assert time.monotonic() < deadline, "init prompt timed out"
                if select.select([master], [], [], 0.1)[0]:
                    transcript += os.read(master, 4096)
            while termios.tcgetattr(master)[3] & termios.ECHO:
                assert time.monotonic() < deadline, "password input echo remained enabled"
                time.sleep(0.001)
            os.write(master, (fixture_password + "\n").encode())
        assert initializer.wait(timeout=10) == 0
        while select.select([master], [], [], 0.1)[0]:
            try:
                chunk = os.read(master, 4096)
                if not chunk:
                    break
                transcript += chunk
            except OSError as error:
                if error.errno == errno.EIO:
                    break
                raise
        assert fixture_password.encode() not in transcript, "init echoed the password"
        assert (root / "master.key").stat().st_mode & 0o777 == 0o600
    finally:
        if initializer.poll() is None:
            initializer.kill()
            initializer.wait()
        os.close(master)

    server = subprocess.Popen(
        [str(binary), "serve", "--config", str(config)],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=environment,
    )
    try:
        deadline = time.monotonic() + 10
        while True:
            try:
                with urllib.request.urlopen(origin + "/readyz", timeout=0.5) as response:
                    assert response.status == 200
                break
            except (urllib.error.URLError, TimeoutError):
                assert server.poll() is None and time.monotonic() < deadline, "server startup failed"
                time.sleep(0.05)
        try:
            urllib.request.urlopen(origin + "/api/v1/instances", timeout=2)
            raise AssertionError("anonymous access accepted")
        except urllib.error.HTTPError as error:
            assert error.code == 401
        jar = http.cookiejar.CookieJar()
        client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
        request = urllib.request.Request(
            origin + "/api/v1/auth/login",
            data=json.dumps({"password": fixture_password}).encode(),
            headers={"Origin": origin, "Content-Type": "application/json"},
        )
        with client.open(request, timeout=5) as response:
            assert response.status == 200
        with client.open(origin + "/api/v1/instances", timeout=2) as response:
            assert json.load(response)["data"] == []
        if supplied:
            with client.open(origin + "/", timeout=2) as response:
                assert b"__NUXT" in response.read(), "release UI missing"
            with client.open(origin + "/api/v1/meta", timeout=2) as response:
                metadata = json.load(response)["data"]
                assert metadata["frontend_embedded"] is True
                assert metadata["frontend_commit"] == version["frontend_commit"]
                assert metadata["backend_commit"] == version["backend_commit"]
        backup = root / "backup.db"
        subprocess.run([str(binary), "backup", "--config", str(config), "--output", str(backup)], check=True, env=environment, capture_output=True)
        with sqlite3.connect(backup) as restored:
            assert restored.execute("SELECT COUNT(*) FROM admin").fetchone()[0] == 1
        assert backup.stat().st_mode & 0o777 == 0o600
        refused = subprocess.run([str(binary), "backup", "--config", str(config), "--output", str(backup)], env=environment, capture_output=True)
        assert refused.returncode != 0, "backup overwrote an existing file"
        server.send_signal(signal.SIGTERM)
        assert server.wait(timeout=5) == 0, "SIGTERM did not exit cleanly"
        _, logs = server.communicate()
        assert fixture_password.encode() not in logs, "password leaked in server logs"
        # 用同一主密钥和新路径启动备份快照，验证真实二进制恢复，而非只读 SQL 检查。
        restore_config = root / "restore.yaml"
        restore_config.write_text(config.read_text().replace(str(root / "hub.db"), str(backup)))
        restored_server = subprocess.Popen([str(binary), "serve", "--config", str(restore_config)], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, env=environment)
        try:
            deadline = time.monotonic() + 10
            while True:
                try:
                    with urllib.request.urlopen(origin + "/readyz", timeout=0.5) as response:
                        assert response.status == 200
                    break
                except (urllib.error.URLError, TimeoutError):
                    assert restored_server.poll() is None and time.monotonic() < deadline, "restore failed"
                    time.sleep(0.05)
            jar.clear()
            with client.open(request, timeout=5) as response:
                assert response.status == 200
            with client.open(origin + "/api/v1/instances", timeout=2) as response:
                assert json.load(response)["data"] == []
            subprocess.run([str(binary), "healthcheck", "--config", str(restore_config)], check=True, env=environment, capture_output=True)
        finally:
            restored_server.send_signal(signal.SIGTERM)
            assert restored_server.wait(timeout=5) == 0
        print("PASS: CLI init/Session/version/healthcheck, 0600 key/backup, no overwrite, SIGTERM, snapshot restore and re-login")
    finally:
        if server.poll() is None:
            server.kill()
            server.wait()
