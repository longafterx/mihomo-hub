"""只读校验 Compose 的端口、Origin 和用户数据目录；不启动容器或连接 Docker daemon。"""
import json
import os
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parent.parent
for port, origin, directory in [
    (None, None, None),
    ('18081', None, None),
    ('18081', 'https://hub.example', None),
    (None, None, '/srv/mihomo-hub/data'),
    (None, None, './custom data'),
]:
    environment = {key: value for key, value in os.environ.items() if not key.startswith('MIHOMOHUB_')}
    if port:
        environment['MIHOMOHUB_PORT'] = port
    if origin:
        environment['MIHOMOHUB_PUBLIC_ORIGIN'] = origin
    if directory:
        environment['MIHOMOHUB_DATA_DIR'] = directory
    # 忽略部署者本地 .env，校验仓库中的默认值及显式覆盖。
    config = json.loads(subprocess.check_output(
        ['docker', 'compose', '--env-file', '/dev/null', '-f', str(root / 'compose.yaml'), 'config', '--format', 'json'],
        env=environment,
    ))
    service = config['services']['mihomo-hub']
    assert service['image'] == 'ghcr.io/longafterx/mihomo-hub:latest'
    assert service['environment']['MIHOMOHUB_LISTEN'] == '0.0.0.0:11235'
    assert service['environment']['MIHOMOHUB_PUBLIC_ORIGIN'] == (origin or f'http://127.0.0.1:{port or "11235"}')
    assert str(service['ports'][0]['published']) == (port or '11235')
    assert service['ports'][0]['target'] == 11235
    assert service['ports'][0].get('host_ip') in (None, '', '0.0.0.0')
    assert service['depends_on']['init-data']['condition'] == 'service_completed_successfully'
    mount = service['volumes'][0]
    expected = Path(directory or './data')
    if not expected.is_absolute():
        expected = root / expected
    assert mount['type'] == 'bind'
    assert mount['source'] == str(expected.resolve())
    assert mount['target'] == '/var/lib/mihomo-hub'
    assert mount.get('bind', {}).get('create_host_path', True)
    initializer = config['services']['init-data']
    assert initializer['volumes'][0]['source'] == mount['source']
    assert initializer['volumes'][0]['target'] == mount['target']
    assert initializer['volumes'][0].get('bind', {}).get('create_host_path', True)
    assert initializer['user'] == '0:0'
    assert initializer['network_mode'] == 'none'
    assert initializer['read_only']
    assert set(initializer['cap_add']) == {'CHOWN', 'FOWNER'}
    assert initializer['cap_drop'] == ['ALL']
    assert not config.get('volumes')
print('PASS: all-interface port mapping, port/Origin overrides, shared default/custom data directories, one-time directory preparation before nonroot service')
