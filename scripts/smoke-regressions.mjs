// 六项修复的浏览器回归：全部使用本地临时服务、测试证书与数据库，不连接已有实例。
import assert from 'node:assert/strict'
import { execFileSync, spawn } from 'node:child_process'
import { once } from 'node:events'
import { mkdtempSync, mkdirSync, readFileSync, rmSync } from 'node:fs'
import { createServer } from 'node:https'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { createInterface } from 'node:readline'

const frontend = resolve(process.argv[2] || '../metacubexd')
const require = createRequire(join(frontend, 'packages/ui/package.json'))
const { chromium, expect } = require('@playwright/test')
const temporary = mkdtempSync(join(tmpdir(), 'mihomohub-regressions-'))
const binary = join(temporary, 'fixture')
const embedded = !!process.env.MIHOMOHUB_EMBED_SMOKE
let fixture, browser, tlsServer, page
try {
  const key = join(temporary, 'fixture.key'), cert = join(temporary, 'fixture.crt')
  execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1', '-subj', '/CN=127.0.0.1', '-addext', 'subjectAltName=IP:127.0.0.1', '-keyout', key, '-out', cert], { stdio: 'ignore' })
  tlsServer = createServer({ key: readFileSync(key), cert: readFileSync(cert) }, (request, response) => {
    response.setHeader('Content-Type', 'application/json')
    if (request.url === '/version') return response.end('{"version":"v1.19.33"}')
    response.writeHead(404).end('{}')
  })
  await new Promise(resolve => tlsServer.listen(0, '127.0.0.1', resolve))
  const tlsPort = String(tlsServer.address().port)
  execFileSync('go', ['build', embedded ? '-tags=integration,frontend' : '-tags=integration', '-o', binary, './tests/smoketest/server'], { stdio: 'inherit' })
  // 仅测试进程信任这张本地证书；实际部署继续使用系统 CA 验证。
  fixture = spawn(binary, [...(embedded ? [] : ['-static', join(frontend, 'packages/ui/.output/public')]), '-tls-ca', cert], { env: { ...process.env, GODEBUG: `${process.env.GODEBUG ? process.env.GODEBUG + ',' : ''}x509usefallbackroots=1` }, stdio: ['ignore', 'pipe', 'inherit'] })
  const { url, ids } = await new Promise((resolve, reject) => {
    const lines = createInterface({ input: fixture.stdout })
    const timer = setTimeout(() => reject(new Error('Fixture startup timed out')), 15000)
    lines.once('line', line => { clearTimeout(timer); lines.close(); resolve(JSON.parse(line)) })
    fixture.once('exit', code => reject(new Error(`Fixture exited: ${code}`)))
  })
  browser = await chromium.launch({ headless: true })
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, locale: 'en-US', serviceWorkers: 'block' })
  await context.route('**/*', route => new URL(route.request().url()).origin === url ? route.continue() : route.abort())
  const errors = []
  const open = async () => {
    const tab = await context.newPage()
    tab.on('pageerror', error => errors.push(error.message))
    await tab.goto(url + '/login')
    await tab.locator('[name="password"]').fill('p0-smoke-fixture-only')
    await tab.getByRole('button', { name: '登录', exact: true }).click()
    await tab.waitForURL(location => location.pathname === '/')
    await tab.getByTestId('hub-instance-status').waitFor()
    return tab
  }
  page = await open()
  const initialProbe = page.waitForResponse(response => response.url().endsWith(`/instances/${ids[0]}/test`))
  await page.locator(`[data-hub-instance-status="${ids[0]}"]`).hover()
  assert.equal((await initialProbe).status(), 200)
  const other = await open()
  const statuses = []
  page.on('response', response => {
    if (response.url().endsWith(`/instances/${ids[1]}/test`)) statuses.push(response.status())
  })
  await page.locator(`[data-hub-instance-status="${ids[1]}"]`).hover()
  await expect.poll(() => statuses).toEqual([403, 200])
  await other.close()

  // 首页与管理页都显示实例列表；等待旧页面卸载后再操作，避免点击到路由过渡中的旧按钮。
  const previousInstances = await page.getByTestId('hub-instances').elementHandle()
  await page.locator('#hub-endpoint').click()
  await page.getByRole('menuitem', { name: 'Manage instances', exact: true }).click()
  await page.waitForFunction(element => !element.isConnected, previousInstances)
  await page.getByTestId('hub-instances').waitFor()
  await page.getByRole('button', { name: 'Add instance', exact: true }).click()
  await page.locator('#hub-name').fill('auto-https')
  await page.locator('#hub-host').fill('127.0.0.1')
  await page.locator('#hub-port').fill(tlsPort)
  await page.getByRole('dialog').getByRole('button', { name: 'Test and add', exact: true }).click()
  await page.getByRole('dialog').waitFor({ state: 'hidden' })
  const created = page.locator('article[data-instance]').filter({ hasText: 'auto-https' })
  await created.waitFor()
  const tlsId = await created.getAttribute('data-instance')
  const config = await (await context.request.get(`${url}/api/v1/instances/${tlsId}/config`)).json()
  assert.equal(config.data.controller_url, `https://127.0.0.1:${tlsPort}`)
  assert.equal(await page.locator('#hub-protocol').count(), 0)

  const retainedId = ids[1]
  await page.locator(`[data-instance="${retainedId}"]`).getByRole('button', { name: 'Delete', exact: true }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Confirm deletion', exact: true }).click()
  await page.getByRole('dialog').waitFor({ state: 'hidden' })
  const retained = page.getByTestId('hub-retained')
  await retained.locator('summary').click()
  const old = page.locator(`[data-retained-instance="${retainedId}"]`)
  await old.waitFor()
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await old.getByRole('button', { name: 'Delete history', exact: true }).click()
    await expect(page.getByRole('dialog')).toContainText('cannot be undone')
    // 原生 dialog 已打开时，内部面板可能仍在入场动画中；截图前确认实际可见。
    await expect(page.getByRole('dialog').locator('.modal-shell')).toHaveCSS('opacity', '1')
    assert.equal(await page.locator('input[name="hub-delete-history"]').count(), 0)
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)
    mkdirSync('test-results', { recursive: true })
    await page.screenshot({ path: `test-results/hub-history-purge-${width}.png` })
    await page.getByRole('dialog').getByRole('button', { name: 'Cancel', exact: true }).click()
    await page.getByRole('dialog').waitFor({ state: 'hidden' })
    assert.equal((await context.request.get(`${url}/api/v1/instances/retained`)).status(), 200)
  }
  await old.getByRole('button', { name: 'Delete history', exact: true }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Confirm deletion', exact: true }).click()
  await old.waitFor({ state: 'detached' })
  const catalog = (await (await context.request.get(`${url}/api/v1/instances/retained`)).json()).data
  assert(!catalog.some(item => item.id === retainedId))
  assert.equal((await context.request.get(`${url}/api/v1/instances/${ids[0]}`)).status(), 200)
  assert.equal((await context.request.get(`${url}/api/v1/instances/${tlsId}`)).status(), 200)
  assert.deepEqual(errors, [])
  console.log('PASS: cross-tab CSRF recovery without reload, automatic HTTPS registration with certificate verification, retained history listing/cancel/purge on desktop and mobile, isolation of other instances')
} catch (error) {
  if (page) {
    mkdirSync('test-results', { recursive: true })
    await page.screenshot({ path: 'test-results/hub-regressions-failure.png', fullPage: true })
    console.error('Regression failed at', page.url(), (await page.locator('body').innerText()).slice(0, 2200))
  }
  throw error
} finally {
  await browser?.close()
  if (fixture && fixture.exitCode === null) { const done = once(fixture, 'exit'); fixture.kill('SIGTERM'); await done }
  if (tlsServer) { tlsServer.closeAllConnections(); await new Promise(resolve => tlsServer.close(resolve)) }
  rmSync(temporary, { recursive: true, force: true })
}
