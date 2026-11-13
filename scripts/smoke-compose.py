"""只用本地临时目录和随机项目验证 Compose，不操作已有容器或数据。"""
import argparse
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
helper_image = 'alpine:3.22.4@sha256:310c62b5e7ca5b08167e4384c68db0fd2905dd9c7493756d356e893909057601'


def run(arguments, env=environment):
    return subprocess.check_output(arguments, env=env, text=True).strip()


def helper(directory, script, *arguments):
    # 测试辅助容器仅挂载临时夹具，用于检查权限和清理；不属于部署服务，也不输出密钥内容。
    return run([
        'docker', 'run', '--rm', '--network', 'none', '--read-only', '--user', '0:0',
        '--mount', 'type=bind,source=' + str(directory) + ',target=/fixture',
        helper_image, '/bin/sh', '-ec', script, 'fixture', *arguments,
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
        assert set(config['services']) == {'mihomo-hub'}
        assert not config['services']['mihomo-hub'].get('depends_on')
        assert config['services']['mihomo-hub']['volumes'][0]['source'] == str(directory)
        run(compose + ['up', '-d', '--wait', '--wait-timeout', '45'], env)
        info = json.loads(run(['docker', 'inspect', project]))[0]
        mount = next(item for item in info['Mounts'] if item['Destination'] == '/var/lib/mihomo-hub')
        assert mount['Type'] == 'bind' and Path(mount['Source']).resolve() == directory.resolve()
        assert info['Config']['User'] == '0:0'
        assert info['HostConfig']['ReadonlyRootfs']
        assert info['HostConfig']['CapDrop'] == ['ALL']
        # Docker inspect 可能补全 CAP_ 前缀，按同一能力名称校验。
        assert [cap.removeprefix('CAP_') for cap in info['HostConfig']['CapAdd']] == ['DAC_OVERRIDE']
        assert info['HostConfig']['SecurityOpt'] == ['no-new-privileges:true']
        assert any(binding['HostIp'] == '0.0.0.0' for binding in info['NetworkSettings']['Ports']['11235/tcp'])
        assert run(compose + ['ps', '-a', '--services'], env) == 'mihomo-hub'
        assert helper(directory, 'stat -c "%u:%g %a" /fixture/mihomo-hub.db /fixture/secrets/master.key') == '0:0 600\n0:0 600'
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
        # 重建后核对登录、实例、密钥及权限；数据库会正常写入，不比较数据库内容哈希。
        run(compose + ['stop', 'mihomo-hub'], env)
        helper(directory, 'echo unchanged > /fixture/untouched; chmod 640 /fixture/untouched')
        fingerprint = (
            'sha256sum /fixture/secrets/master.key; '
            'stat -c "%u:%g %a" /fixture /fixture/secrets /fixture/mihomo-hub.db '
            '/fixture/secrets/master.key /fixture/untouched; cat /fixture/untouched'
        )
        for legacy in [False, True]:
            if legacy:
                # 模拟旧版非 root 服务留下的私有目录，验证升级无需 chown 或重建数据。
                helper(directory, 'chown -R 65532:65532 /fixture; chmod 0700 /fixture')
                assert helper(directory, 'stat -c "%u:%g %a" /fixture') == '65532:65532 700'
            before = helper(directory, fingerprint)
            run(compose + ['up', '-d', '--force-recreate', '--wait', '--wait-timeout', '45'], env)
            jar.clear()
            assert not request('/api/v1/auth/setup')['setup_required']
            request('/api/v1/auth/login', {'password': password})
            items = request('/api/v1/instances')
            assert len(items) == 1 and items[0]['id'] == item['id'] and items[0]['has_secret']
            run(compose + ['stop', 'mihomo-hub'], env)
            assert helper(directory, fingerprint) == before, 'restart must preserve key, owners and modes'
        run(compose + ['down'], env)

    print('PASS: single-service deployment, missing default/custom directories auto-created, '
          'all-interface publishing, restart/login persistence, legacy UID 65532 data accessible, '
          'key/owners/modes preserved')
finally:
    subprocess.run(compose + ['down'], env=environment, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    # 仅恢复临时夹具所有权供宿主清理，不接触用户目录。
    helper(fixture, 'chown -R "$1:$2" /fixture; chmod -R u+rwX /fixture', str(os.getuid()), str(os.getgid()))
    temporary.cleanup()
