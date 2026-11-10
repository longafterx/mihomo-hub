"""只读校验 Compose 的端口/Origin 默认值；不启动容器或连接 Docker daemon。"""
import json
import os
import subprocess

for port, origin in [(None, None), ('18081', None), ('18081', 'https://hub.example')]:
    environment = {key: value for key, value in os.environ.items() if not key.startswith('MIHOMOHUB_')}
    if port:
        environment['MIHOMOHUB_PORT'] = port
    if origin:
        environment['MIHOMOHUB_PUBLIC_ORIGIN'] = origin
    config = json.loads(subprocess.check_output(['docker', 'compose', 'config', '--format', 'json'], env=environment))
    service = config['services']['mihomo-hub']
    assert service['environment']['MIHOMOHUB_PUBLIC_ORIGIN'] == (origin or f'http://127.0.0.1:{port or "8080"}')
    assert str(service['ports'][0]['published']) == (port or '8080')
print('PASS: default/custom Compose port matches Origin; explicit HTTPS Origin preserved')
