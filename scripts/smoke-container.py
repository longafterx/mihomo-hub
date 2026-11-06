"""隔离容器验收：只创建随机命名的测试容器/卷，不接触已有服务。"""
import argparse
import http.cookiejar
import json
import os
import pty
import select
import socket
import subprocess
import tempfile
import time
import urllib.request
import urllib.error
import uuid

parser=argparse.ArgumentParser()
parser.add_argument('--remote', help='optional SSH Docker host, e.g. root@host')
parser.add_argument('--image', default='mihomo-hub:0.3.0')
args=parser.parse_args()
environment=dict(os.environ)
if args.remote: environment['DOCKER_HOST']='ssh://'+args.remote
name='mihomohub-smoke-'+uuid.uuid4().hex[:12]
volume=name+'-data'
password='12345678'
tunnel=None

def docker(*arguments, **kwargs):
    return subprocess.check_output(['docker', *arguments],env=environment,text=True,**kwargs).strip()

def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1',0))
        return sock.getsockname()[1]

try:
    docker('volume','create',volume)
    master,slave=pty.openpty()
    initializer=subprocess.Popen(['docker','run','--rm','-it','--read-only','--cap-drop=ALL','--security-opt=no-new-privileges:true','-v',volume+':/var/lib/mihomo-hub',args.image,'init'],stdin=slave,stdout=slave,stderr=slave,env=environment)
    os.close(slave)
    transcript=b''
    try:
        for prompt in (b'Admin password',b'Confirm password'):
            deadline=time.monotonic()+30
            while prompt not in transcript:
                if time.monotonic()>deadline: raise AssertionError('container init timed out')
                if select.select([master],[],[],0.1)[0]: transcript+=os.read(master,4096)
            time.sleep(0.1)
            os.write(master,(password+'\n').encode())
        assert initializer.wait(timeout=30)==0, transcript.decode(errors='replace')
        assert password.encode() not in transcript, 'password echoed'
    finally:
        if initializer.poll() is None: initializer.kill(); initializer.wait()
        os.close(master)
    # 先确定随机映射端口，再以相同端口重建服务；Origin 校验必须与访问入口完全一致。
    docker('run','-d','--name',name,'--read-only','--cap-drop=ALL','--security-opt=no-new-privileges:true','--pids-limit=128','--memory=512m','-p','127.0.0.1::8080','-v',volume+':/var/lib/mihomo-hub',args.image)
    port=int(docker('port',name,'8080/tcp').rsplit(':',1)[1])
    local_port=free_port() if args.remote else port
    origin=f'http://127.0.0.1:{local_port}'
    docker('rm','-f',name)
    docker('run','-d','--name',name,'--read-only','--cap-drop=ALL','--security-opt=no-new-privileges:true','--pids-limit=128','--memory=512m','-p',f'127.0.0.1:{port}:8080','-e','MIHOMOHUB_PUBLIC_ORIGIN='+origin,'-v',volume+':/var/lib/mihomo-hub',args.image)
    if args.remote:
        tunnel=subprocess.Popen(['ssh','-o','BatchMode=yes','-o','ExitOnForwardFailure=yes','-N','-L',f'127.0.0.1:{local_port}:127.0.0.1:{port}',args.remote],stdout=subprocess.DEVNULL,stderr=subprocess.PIPE)
    cookies=http.cookiejar.CookieJar()
    opener=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cookies))
    def request(path,body=None,method=None,csrf=''):
        data=json.dumps(body).encode() if body is not None else None
        headers={'Origin':origin,'Content-Type':'application/json','X-CSRF-Token':csrf}
        response=opener.open(urllib.request.Request(origin+path,data=data,method=method,headers=headers),timeout=5)
        with response: return response.status,response.read(),response.headers
    def ready():
        deadline=time.monotonic()+30
        while True:
            try:
                assert request('/readyz')[0]==200
                return
            except (OSError,AssertionError):
                if time.monotonic()>deadline: raise
                time.sleep(0.2)
    def login():
        return json.loads(request('/api/v1/auth/login',{'username':'admin','password':password})[1])['data']['csrf_token']
    ready()
    info=json.loads(docker('inspect',name))[0]
    assert info['Config']['User']=='65532:65532'
    assert info['HostConfig']['ReadonlyRootfs']
    assert info['HostConfig']['CapDrop']==['ALL']
    version=json.loads(docker('exec',name,'/mihomo-hub','version'))
    assert version['frontend_embedded'] is True
    assert len(version['frontend_commit'])==40
    assert version['schema_version']==3
    assert '__NUXT' in request('/')[1].decode()
    assert 'no-store' in request('/')[2]['Cache-Control']
    assert 'unregister' in request('/sw.js')[1].decode()
    token=login()
    item=json.loads(request('/api/v1/instances',{'name':'container-persisted','controller_url':'http://127.0.0.1:1','secret':'container-controller-fixture','enabled':False,'collect_enabled':False},csrf=token)[1])['data']
    assert item['has_secret'] and 'secret' not in item
    docker('exec',name,'/mihomo-hub','backup','--output','/var/lib/mihomo-hub/fixture-backup.db')
    docker('restart','-t','10',name)
    ready()
    cookies.clear()
    token=login()
    items=json.loads(request('/api/v1/instances')[1])['data']
    assert len(items)==1 and items[0]['id']==item['id'] and items[0]['has_secret']
    # 保留历史不再占用名称；同名重建按新身份保存，且允许明确不配置 Secret。
    request('/api/v1/instances/'+item['id']+'?history=retain',method='DELETE',csrf=token)
    replacement=json.loads(request('/api/v1/instances',{'name':'container-persisted','controller_url':'http://127.0.0.1:1','secret':'','enabled':False,'collect_enabled':False},csrf=token)[1])['data']
    assert replacement['id']!=item['id'] and not replacement['has_secret']
    # scratch 镜像中只有 Go 可执行文件、CA 与许可证，没有 Node/shell。
    assert subprocess.run(['docker','exec',name,'/usr/bin/node','--version'],env=environment,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode!=0
    print('PASS: nonroot/read-only container, embedded UI, init/login, encrypted persistence, restart, backup, retained name reuse/new UUID/empty Secret, no Node runtime')
finally:
    if tunnel: tunnel.terminate(); tunnel.wait(timeout=10)
    subprocess.run(['docker','rm','-f',name],env=environment,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    subprocess.run(['docker','volume','rm',volume],env=environment,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
