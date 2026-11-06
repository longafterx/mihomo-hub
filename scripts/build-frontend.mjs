import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { cpSync, existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const backend = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const frontend = resolve(process.argv[2] || resolve(backend, '../metacubexd'))
const lock = readFileSync(resolve(backend, 'build/frontend.lock.yaml'), 'utf8')
const field = key => lock.match(new RegExp(`^${key}: "?([^"\\n]+)"?$`, 'm'))?.[1]
const run = (command, args, cwd = frontend) => execFileSync(command, args, { cwd, encoding: 'utf8' }).trim()
const commit = field('ref')
assert.match(commit, /^[a-f0-9]{40}$/)
assert.equal(run('git', ['rev-parse', 'HEAD']), commit, 'Frontend HEAD differs from lock')
// 不允许把工作区修改冒充固定 SHA；本地工具元数据不参与前端构建。
assert.equal(run('git', ['status', '--porcelain', '--untracked-files=normal', '--', 'packages', 'package.json', 'pnpm-lock.yaml', 'pnpm-workspace.yaml', 'nuxt.config.ts']), '', 'Frontend build inputs must be clean')
run('git', ['diff', '--exit-code', 'HEAD'])
assert.equal(process.versions.node, field('node'), 'Use the locked Node version')
assert.equal(run('pnpm', ['--version']), field('pnpm'), 'Use the locked pnpm version')
execFileSync('pnpm', ['install', '--frozen-lockfile'], { cwd: frontend, stdio: 'inherit' })
execFileSync('pnpm', ['build:ui'], { cwd: frontend, stdio: 'inherit', env: { ...Object.fromEntries(Object.entries(process.env).filter(([key]) => !/^(NUXT_|MOCK_MODE|MCXD_|MIHOMOHUB_)/.test(key))), MIHOMOHUB_BUILD: 'true', MOCK_MODE: 'false', NUXT_APP_BASE_URL: '/' } })
const output = resolve(frontend, 'packages/ui/.output/public')
assert(existsSync(resolve(output, 'index.html')))
assert(!existsSync(resolve(output, 'sw.js')), 'Hub build must disable PWA')
const destination = resolve(backend, 'internal/web/dist')
rmSync(destination, { recursive: true, force: true })
mkdirSync(destination, { recursive: true })
cpSync(output, destination, { recursive: true })
const manifest = {
  frontend_commit: commit, upstream_xd_commit: field('upstream_ref'),
  api_contract: field('expected_api_contract'), node: field('node'), pnpm: field('pnpm'),
  lockfile_sha256: createHash('sha256').update(readFileSync(resolve(frontend, 'pnpm-lock.yaml'))).digest('hex'),
}
writeFileSync(resolve(destination, 'mihomohub-build.json'), JSON.stringify(manifest, null, 2) + '\n')
const notices = resolve(backend, 'build/notices')
mkdirSync(notices, { recursive: true })
cpSync(resolve(frontend, 'LICENSE'), resolve(notices, 'MetaCubeXD-LICENSE'))
cpSync(resolve(frontend, 'packages/ui/public/fonts/ubuntu/LICENSE'), resolve(notices, 'Ubuntu-LICENSE'))
const dependencies = JSON.parse(execFileSync('pnpm', ['licenses', 'list', '--prod', '--json'], { cwd: frontend, maxBuffer: 16 * 1024 * 1024 }))
const licenseText = []
for (const packages of Object.values(dependencies)) {
  for (const dependency of packages) {
    for (const directory of dependency.paths || []) {
      for (const entry of readdirSync(directory, { withFileTypes: true })) {
        if (entry.isFile() && /^(licen[cs]e|copying|notice)(\.|$)/i.test(entry.name)) {
          licenseText.push(`\n--- ${dependency.name}@${dependency.versions.join(',')} / ${entry.name} ---\n${readFileSync(resolve(directory, entry.name), 'utf8')}`)
        }
      }
    }
    // 不将构建机绝对路径泄露到发布包的依赖清单。
    delete dependency.paths
  }
}
writeFileSync(resolve(notices, 'frontend-dependencies.json'), JSON.stringify(dependencies, null, 2) + '\n')
writeFileSync(resolve(notices, 'frontend-LICENSES.txt'), licenseText.join('\n'))
console.log(`Embedded frontend prepared: ${commit}`)
