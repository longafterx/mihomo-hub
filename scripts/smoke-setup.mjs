// 真实生产入口的首次使用验收，数据与端口均为本地临时资源。
import assert from 'node:assert/strict'
import { execFileSync, spawn } from 'node:child_process'
import { once } from 'node:events'
import { createRequire } from 'node:module'
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

const frontend = resolve(process.argv[2] || '../metacubexd')
const require = createRequire(join(frontend, 'packages/ui/package.json'))
const { chromium } = require('playwright')
const temporary = mkdtempSync(join(tmpdir(), 'mihomohub-setup-'))
const binary = join(temporary, 'mihomo-hub')
execFileSync('go', ['build', ...(process.env.MIHOMOHUB_EMBED_SMOKE ? ['-tags=frontend'] : []), '-o', binary, './cmd/mihomo-hub'], { stdio: 'inherit' })
const socket = createServer()
socket.listen(0, '127.0.0.1')
await once(socket, 'listening')
const port = socket.address().port
await new Promise(resolve => socket.close(resolve))
const origin = `http://127.0.0.1:${port}`
const environment = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('MIHOMOHUB_')))
let server
let logs = ''
let browser
const errors = []
const password = 'browser-setup-fixture'

async function start(name) {
  const directory = join(temporary, name)
  mkdirSync(directory, { recursive: true })
  const config = join(directory, 'config.yaml')
  writeFileSync(config, `server:\n  listen: "127.0.0.1:${port}"\n  public_origin: "${origin}"\ndatabase:\n  path: ${JSON.stringify(join(directory, 'data/hub.db'))}\nsecurity:\n  master_key_file: ${JSON.stringify(join(directory, 'data/secrets/master.key'))}\n`, { mode: 0o600 })
  server = spawn(binary, ['serve', '--config', config], { env: environment, stdio: ['ignore', 'ignore', 'pipe'] })
  server.stderr.on('data', chunk => { logs += chunk.toString() })
  const deadline = Date.now() + 15000
  while (true) {
    try {
      assert.equal((await fetch(origin + '/readyz')).status, 200)
      return directory
    } catch {
      assert.equal(server.exitCode, null, 'Production server failed to start')
      if (Date.now() > deadline) throw new Error('Production server startup timed out')
      await new Promise(resolve => setTimeout(resolve, 50))
    }
  }
}

async function stop() {
  if (!server || server.exitCode !== null) return
  const exited = once(server, 'exit')
  server.kill('SIGTERM')
  const [code] = await exited
  assert.equal(code, 0, 'Production server did not stop cleanly')
}

async function open(context, path = '/') {
  const page = await context.newPage()
  page.on('pageerror', error => errors.push(error.message))
  page.on('request', request => {
    assert(!request.url().includes(password), 'Password appeared in browser URL')
    assert.equal(new URL(request.url()).origin, origin, 'Setup requested an external resource')
  })
  await page.goto(origin + path)
  return page
}

async function fill(page, value, confirmation = value) {
  await page.locator('[name="password"]').fill(value)
  await page.locator('[name="confirm_password"]').fill(confirmation)
}

try {
  const directory = await start('desktop')
  const keyPath = join(directory, 'data/secrets/master.key')
  const originalKey = readFileSync(keyPath)
  // 尚未设置密码时重启仍进入引导，并保留自动生成的主密钥。
  await stop()
  await start('desktop')
  assert.deepEqual(readFileSync(keyPath), originalKey)
  browser = await chromium.launch({ headless: true })
  const desktop = await browser.newContext({ viewport: { width: 1280, height: 900 }, serviceWorkers: 'block' })
  const page = await open(desktop)
  await page.getByRole('button', { name: '设置密码并进入', exact: true }).waitFor()
  assert.equal(new URL(page.url()).pathname, '/login')
  assert.equal(await page.locator('[name="username"]').count(), 0)
  assert.equal((await desktop.request.get(origin + '/api/v1/instances')).status(), 401)
  const other = await browser.newContext({ serviceWorkers: 'block' })
  const stale = await open(other)
  await stale.getByRole('button', { name: '设置密码并进入', exact: true }).waitFor()
  await fill(page, password, 'different-password')
  await page.getByRole('button', { name: '设置密码并进入', exact: true }).click()
  await page.getByRole('status').filter({ hasText: '两次输入的密码不一致' }).waitFor()
  assert.equal((await (await desktop.request.get(origin + '/api/v1/auth/setup')).json()).data.setup_required, true)
  await fill(page, password)
  mkdirSync('test-results', { recursive: true })
  await page.screenshot({ path: 'test-results/hub-first-run-desktop.png' })
  await page.getByRole('button', { name: '设置密码并进入', exact: true }).click()
  await page.waitForURL(url => url.pathname === '/')
  assert.equal((await desktop.request.get(origin + '/api/v1/auth/me')).status(), 200)
  assert.deepEqual((await (await desktop.request.get(origin + '/api/v1/instances')).json()).data, [])
  // 已打开的旧引导页遇到其他浏览器完成设置后，切回登录，不覆盖新密码。
  await fill(stale, 'different-password')
  await stale.getByRole('button', { name: '设置密码并进入', exact: true }).click()
  await stale.getByRole('button', { name: '登录', exact: true }).waitFor()
  await stale.getByRole('status').filter({ hasText: '密码已设置' }).waitFor()
  await stale.locator('[name="password"]').fill('different-password')
  await stale.getByRole('button', { name: '登录', exact: true }).click()
  await stale.getByRole('status').filter({ hasText: '密码错误' }).waitFor()
  await stale.locator('[name="password"]').fill(password)
  await stale.getByRole('button', { name: '登录', exact: true }).click()
  await stale.waitForURL(url => url.pathname === '/')
  await stop()
  await start('desktop')
  assert.deepEqual(readFileSync(keyPath), originalKey)
  const returning = await open(other, '/login')
  await returning.getByRole('button', { name: '登录', exact: true }).waitFor()
  assert.equal(await returning.locator('[name="confirm_password"]').isVisible(), false)
  await returning.locator('[name="password"]').fill(password)
  await returning.getByRole('button', { name: '登录', exact: true }).click()
  await returning.waitForURL(url => url.pathname === '/')
  await desktop.close()
  await other.close()
  await stop()
  await start('mobile')
  const mobile = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, serviceWorkers: 'block' })
  const phone = await open(mobile)
  await phone.getByRole('button', { name: '设置密码并进入', exact: true }).waitFor()
  assert.equal(await phone.evaluate(() => document.documentElement.scrollWidth > window.innerWidth), false)
  await phone.screenshot({ path: 'test-results/hub-first-run-mobile.png' })
  // 模拟服务端已提交，但设置响应在网络中丢失；页面应切换到登录。
  await phone.route('**/api/v1/auth/setup', async route => {
    if (route.request().method() !== 'POST') return route.continue()
    const response = await route.fetch()
    assert.equal(response.status(), 200)
    await route.abort('failed')
  })
  await fill(phone, password)
  await phone.getByRole('button', { name: '设置密码并进入', exact: true }).click()
  await phone.getByRole('button', { name: '登录', exact: true }).waitFor()
  await phone.getByRole('status').filter({ hasText: '密码已设置' }).waitFor()
  await phone.unroute('**/api/v1/auth/setup')
  await phone.locator('[name="password"]').fill(password)
  await phone.getByRole('button', { name: '登录', exact: true }).click()
  await phone.waitForURL(url => url.pathname === '/')
  assert.equal((await mobile.request.get(origin + '/api/v1/auth/me')).status(), 200)
  await mobile.close()
  assert.deepEqual(errors, [])
  assert(!logs.includes(password), 'Password appeared in server logs')
  console.log('PASS: desktop/mobile first run, automatic database/key initialization, confirmation, automatic Session, stale setup page/lost response recovery, restart/login and key preservation')
} finally {
  await browser?.close()
  await stop()
  rmSync(temporary, { recursive: true, force: true })
}
