"""只用本地临时目录和随机项目验证 Compose，不操作已有容器或数据。"""
import argparse
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import urllib.request
import uuid


parser = argparse.ArgumentParser()
parser.add_argument('--image', default='ghcr.io/longafterx/mihomo-hub:latest')
args = parser.parse_args()
root = Path(__file__).resolve().parent.parent
environment = {key: value for key, value in os.environ.items() if not key.startswith('MIHOMOHUB_')}
project = 'mihomohub-compose-' + uuid.uuid4().hex[:10]
temporary = tempfile.TemporaryDirectory(prefix=project + '-')
fixture = Path(temporary.name)
override = fixture / 'override.yaml'
override.write_text('services:\n  mihomo-hub:\n    container_name: ' + project + '\n')
compose = [
    'docker', 'compose', '--env-file', '/dev/null', '--project-directory', str(fixture),
    '-p', project, '-f', str(root / 'compose.yaml'), '-f', str(override),
]
initializer_image = None


def run(arguments, env=environment):
    return subprocess.check_output(arguments, env=env, text=True).strip()


def helper(directory, script, *arguments, user='65532:65532'):
    # 仅挂载临时测试路径；应用文件检查以服务 UID 执行，不输出密钥内容。
    return run([
        'docker', 'run', '--rm', '--network', 'none', '--read-only', '--user', user,
        '--mount', 'type=bind,source=' + str(directory) + ',target=/fixture',
        initializer_image, '/bin/sh', '-ec', script, 'fixture', *arguments,
    ])


try:
    for relative in ['data', 'custom path/nested data']:
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            port = listener.getsockname()[1]
        origin = f'http://127.0.0.1:{port}'
        env = {
            **environment, 'MIHOMOHUB_IMAGE': args.image,
            'MIHOMOHUB_PORT': str(port), 'MIHOMOHUB_PUBLIC_ORIGIN': origin,
        }
        if relative != 'data':
            env['MIHOMOHUB_DATA_DIR'] = './' + relative
        directory = fixture / relative
        assert not directory.exists(), 'test must start with a missing data directory'
        config = json.loads(run(compose + ['config', '--format', 'json'], env))
        initializer_image = config['services']['init-data']['image']
        assert config['services']['mihomo-hub']['volumes'][0]['source'] == str(directory)
        run(compose + ['up', '-d', '--wait', '--wait-timeout', '45'], env)
        info = json.loads(run(['docker', 'inspect', project]))[0]
        mount = next(item for item in info['Mounts'] if item['Destination'] == '/var/lib/mihomo-hub')
        assert mount['Type'] == 'bind' and Path(mount['Source']).resolve() == directory.resolve()
        assert info['Config']['User'] == '65532:65532'
        assert info['HostConfig']['CapDrop'] == ['ALL']
        assert any(binding['HostIp'] == '0.0.0.0' for binding in info['NetworkSettings']['Ports']['11235/tcp'])
        init_id = run(compose + ['ps', '-a', '-q', 'init-data'], env)
        state = json.loads(run(['docker', 'inspect', init_id]))[0]['State']
        assert state['Status'] == 'exited' and state['ExitCode'] == 0
        assert helper(directory, 'stat -c "%u:%g %a" /fixture') == '65532:65532 700'
        jar = http.cookiejar.CookieJar()
        client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))

        def request(path, body=None, csrf=''):
            headers = {'Origin': origin, 'Content-Type': 'application/json', 'X-CSRF-Token': csrf}
            data = json.dumps(body).encode() if body is not None else None
            with client.open(urllib.request.Request(origin + path, data=data, headers=headers), timeout=5) as response:
                return json.load(response)['data']

        assert request('/api/v1/auth/setup')['setup_required']
        password = 'compose-directory-fixture-only'
        token = request('/api/v1/auth/setup', {'password': password, 'confirm_password': password})['csrf_token']
        item = request('/api/v1/instances', {
            'name': 'compose-persisted', 'controller_url': 'http://127.0.0.1:1',
            'secret': 'compose-controller-fixture-only',
        }, token)
        # 停止写入后重复初始化，核对数据库、密钥及文件权限均未变化。
        run(compose + ['stop', 'mihomo-hub'], env)
        fingerprint = 'sha256sum /fixture/mihomo-hub.db /fixture/secrets/master.key; stat -c "%u:%g %a" /fixture/mihomo-hub.db /fixture/secrets/master.key'
        before = hashlib.sha256(helper(directory, fingerprint).encode()).digest()
        helper(directory, 'echo unchanged > /fixture/untouched; chmod 640 /fixture/untouched')
        run(compose + ['run', '--rm', '--no-deps', 'init-data'], env)
        assert hashlib.sha256(helper(directory, fingerprint).encode()).digest() == before
        assert helper(directory, 'stat -c "%u:%g %a" /fixture/untouched; cat /fixture/untouched') == '65532:65532 640\nunchanged'
        run(compose + ['up', '-d', '--force-recreate', '--wait', '--wait-timeout', '45'], env)
        jar.clear()
        assert not request('/api/v1/auth/setup')['setup_required']
        request('/api/v1/auth/login', {'password': password})
        items = request('/api/v1/instances')
        assert len(items) == 1 and items[0]['id'] == item['id'] and items[0]['has_secret']
        run(compose + ['down'], env)

    # 目录准备失败时，不得启动主服务。
    override.write_text(
        'services:\n  mihomo-hub:\n    container_name: ' + project
        + '\n  init-data:\n    command: ["exit 23"]\n'
    )
    failed = subprocess.run(compose + ['up', '-d'], env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    assert failed.returncode != 0, 'initializer failure must prevent startup'
    main_id = run(compose + ['ps', '-a', '-q', 'mihomo-hub'], env)
    if main_id:
        assert not json.loads(run(['docker', 'inspect', main_id]))[0]['State']['Running']
    init_id = run(compose + ['ps', '-a', '-q', 'init-data'], env)
    assert json.loads(run(['docker', 'inspect', init_id]))[0]['State']['ExitCode'] == 23
    print('PASS: missing default/custom directories auto-created, all-interface publishing, nonroot main service, repeat initialization preserves data/key/modes, restart/login persistence, failed initializer blocks startup')
finally:
    subprocess.run(compose + ['down'], env=environment, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if initializer_image:
        # 仅恢复临时夹具所有权供宿主清理，不接触用户目录。
        helper(fixture, 'chown -R "$1:$2" /fixture; chmod -R u+rwX /fixture', str(os.getuid()), str(os.getgid()), user='0:0')
    temporary.cleanup()
