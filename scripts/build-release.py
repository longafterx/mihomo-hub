"""从两个固定仓库版本生成无 CGO 的发布包、版本清单和 SHA256。"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import time
import uuid
from urllib.parse import quote

ROOT = Path(__file__).resolve().parent.parent
parser = argparse.ArgumentParser()
parser.add_argument('--frontend', default=str(ROOT.parent / 'metacubexd'))
parser.add_argument('--version', default='0.3.0')
parser.add_argument('--targets', default='linux/amd64,linux/arm64')
parser.add_argument('--prepared', action='store_true', help='reuse the verified frontend produced in this CI job')
args = parser.parse_args()
if not re.fullmatch(r'[0-9A-Za-z][0-9A-Za-z._-]{0,63}', args.version):
    raise SystemExit('invalid release version')

def run(arguments, **kwargs):
    return subprocess.check_output(arguments, cwd=ROOT, text=True, **kwargs).strip()

lock = (ROOT / 'build/frontend.lock.yaml').read_text()
frontend_commit = re.search(r'^ref: "([a-f0-9]{40})"$', lock, re.M)[1]
if not args.prepared:
    subprocess.run(['sh', 'scripts/build-frontend.sh', args.frontend], cwd=ROOT, check=True)
manifest = json.loads((ROOT / 'internal/web/dist/mihomohub-build.json').read_text())
if manifest['frontend_commit'] != frontend_commit:
    raise SystemExit('prepared frontend differs from lock')
try:
    backend_commit = run(['git', 'rev-parse', 'HEAD'])
except subprocess.CalledProcessError:
    raise SystemExit('commit backend implementation before creating a release')
# 只校验构建输入；用户尚未提交的设计笔记不会进入发布二进制。
inputs = ['cmd', 'internal', 'migrations', 'queries', 'scripts', 'go.mod', 'go.sum', 'build/frontend.lock.yaml', 'Dockerfile', 'Dockerfile.release']
if run(['git', 'status', '--porcelain', '--untracked-files=normal', '--', *inputs]):
    raise SystemExit('backend build inputs must be committed')
epoch = int(os.environ.get('SOURCE_DATE_EPOCH') or run(['git', 'show', '-s', '--format=%ct', 'HEAD']))
build_time = time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime(epoch))
output = ROOT / 'dist'
output.mkdir(exist_ok=True)
notices = ROOT / 'build/notices'
notices.mkdir(parents=True, exist_ok=True)
# 收集 Go 依赖实际许可证，连同依赖版本一起发布；不把模块缓存绝对路径写入产物。
raw = run(['go', 'list', '-m', '-json', 'all'])
decoder = json.JSONDecoder()
modules = []
licenses = []
while raw.strip():
    item, end = decoder.raw_decode(raw.lstrip())
    raw = raw.lstrip()[end:]
    modules.append({key: item[key] for key in ('Path', 'Version', 'Sum') if key in item})
    if item.get('Main') or not item.get('Dir'):
        continue
    directory = Path(item['Dir'])
    candidates = [p for p in directory.iterdir() if p.is_file() and re.match(r'^(licen[cs]e|copying|notice)(\.|$)', p.name, re.I)]
    for file in sorted(candidates):
        licenses.append(f"\n--- {item['Path']}@{item.get('Version', '')} / {file.name} ---\n{file.read_text(errors='replace')}")
licenses.append('\n--- Go standard library ---\n' + (Path(run(['go', 'env', 'GOROOT'])) / 'LICENSE').read_text())
(notices / 'go-LICENSES.txt').write_text('\n'.join(licenses))
(notices / 'go-modules.json').write_text(json.dumps(modules, indent=2) + '\n')
shutil.copy2(ROOT / 'docs/THIRD-PARTY-NOTICES.md', notices / 'THIRD-PARTY-NOTICES.md')
# Node 仅用于构建：从锁定版本导出其随版 Mozilla 根证书，scratch 镜像不依赖基础镜像下载。
if run(['node', '--version']) != 'v' + manifest['node']:
    raise SystemExit('use the locked Node version to export the CA bundle')
ca_bundle = run(['node', '-e', "process.stdout.write(require('node:tls').rootCertificates.join('\\n')+'\\n')"])
artifacts = []
for target in args.targets.split(','):
    if target not in ('linux/amd64', 'linux/arm64', 'darwin/arm64', 'darwin/amd64', 'windows/amd64'):
        raise SystemExit('unsupported target')
    goos, goarch = target.split('/')
    name = f'mihomo-hub_{args.version}_{goos}_{goarch}'
    stage = output / name
    if stage.exists():
        shutil.rmtree(stage)
    stage.mkdir()
    (stage / 'data').mkdir(mode=0o700)
    (stage / 'ca-certificates.crt').write_text(ca_bundle + '\n')
    binary = stage / ('mihomo-hub.exe' if goos == 'windows' else 'mihomo-hub')
    ldflags = f'-s -w -X github.com/longafterx/mihomo-hub/internal/app.Version={args.version} -X github.com/longafterx/mihomo-hub/internal/app.Commit={backend_commit} -X github.com/longafterx/mihomo-hub/internal/app.FrontendCommit={frontend_commit} -X github.com/longafterx/mihomo-hub/internal/app.BuildTime={build_time}'
    subprocess.run(['go', 'build', '-trimpath', '-buildvcs=false', '-tags=frontend', '-ldflags', ldflags, '-o', str(binary), './cmd/mihomo-hub'], cwd=ROOT, env={**os.environ, 'CGO_ENABLED':'0', 'GOOS':goos, 'GOARCH':goarch}, check=True)
    version = {**manifest, 'version': args.version, 'backend_commit': backend_commit, 'schema_version': 6, 'build_time':build_time, 'go':run(['go','version']), 'target':target, 'cgo_enabled':False, 'binary_sha256':hashlib.sha256(binary.read_bytes()).hexdigest(), 'ca_bundle_source':'Node '+manifest['node']+' bundled Mozilla rootCertificates', 'ca_bundle_sha256':hashlib.sha256((stage / 'ca-certificates.crt').read_bytes()).hexdigest()}
    (stage / 'manifest.json').write_text(json.dumps(version, indent=2) + '\n')
    components = []
    for module in modules:
        if 'Version' not in module: continue
        purl = 'pkg:golang/' + quote(module['Path'], safe='/') + '@' + quote(module['Version'], safe='')
        components.append({'type':'library', 'bom-ref':purl, 'name':module['Path'], 'version':module['Version'], 'purl':purl})
    dependencies = json.loads((notices / 'frontend-dependencies.json').read_text())
    for packages in dependencies.values():
        for dependency in packages:
            for dependency_version in dependency['versions']:
                purl = 'pkg:npm/' + quote(dependency['name'], safe='/') + '@' + quote(dependency_version, safe='')
                components.append({'type':'library', 'bom-ref':purl, 'name':dependency['name'], 'version':dependency_version, 'purl':purl, 'licenses':[{'license':{'name':dependency.get('license','Unknown')}}]})
    components = sorted({item['bom-ref']:item for item in components}.values(), key=lambda item:item['bom-ref'])
    bom = {'bomFormat':'CycloneDX', 'specVersion':'1.6', 'serialNumber':'urn:uuid:'+str(uuid.uuid5(uuid.NAMESPACE_URL, backend_commit+frontend_commit+target)), 'version':1, 'metadata':{'timestamp':build_time, 'component':{'type':'application','name':'MihomoHub','version':args.version}}, 'components':components}
    (stage / 'sbom.cdx.json').write_text(json.dumps(bom, indent=2) + '\n')
    for file in ['README.md', 'config.example.yaml']:
        shutil.copy2(ROOT / file, stage / file)
    # 发布包只附带使用与运维资料，本地开发档案不进入交付物。
    shutil.copytree(ROOT / 'docs', stage / 'docs', ignore=shutil.ignore_patterns('archive', 'MihomoHub-DESIGN*', 'MihomoHub-TECH_STACK.md'))
    shutil.copytree(notices, stage / 'notices')
    archive = output / (name + '.tar.gz')
    # 固定 tar 的身份与时间戳；gzip 同样使用 SOURCE_DATE_EPOCH。
    import gzip
    with archive.open('wb') as destination:
        with gzip.GzipFile(filename='', mode='wb', fileobj=destination, mtime=epoch) as compressed:
            with tarfile.open(fileobj=compressed, mode='w') as tar:
                for file in sorted(stage.rglob('*')):
                    info = tar.gettarinfo(str(file), arcname=str(Path(name) / file.relative_to(stage)))
                    info.uid = info.gid = 0
                    info.uname = info.gname = ''
                    info.mtime = epoch
                    if file.is_file():
                        with file.open('rb') as source: tar.addfile(info, source)
                    else: tar.addfile(info)
    artifacts.append(archive)
(output / 'SHA256SUMS').write_text(''.join(f'{hashlib.sha256(file.read_bytes()).hexdigest()}  {file.name}\n' for file in artifacts))
print('Built: ' + ', '.join(file.name for file in artifacts))
