import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { execFileSync, spawn } from 'node:child_process'
import { mkdtempSync, mkdirSync, rmSync, existsSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { resolve, join } from 'node:path'
import { createInterface } from 'node:readline'

const frontend = resolve(process.argv[2] || '../metacubexd')
const pinned = readFileSync(new URL('../build/frontend.lock.yaml', import.meta.url), 'utf8').match(/upstream_ref: "([a-f0-9]{40})"/)[1]
assert.equal(execFileSync('git', ['rev-parse', 'HEAD'], { cwd: frontend, encoding: 'utf8' }).trim(), pinned)
const staticDir = resolve(frontend, 'packages/ui/.output/public')
assert(existsSync(join(staticDir, 'index.html')), 'Build the pinned XD static UI first')
const require = createRequire(resolve(frontend, 'packages/ui/package.json'))
const { chromium } = require('playwright')
const temporary = mkdtempSync(join(tmpdir(), 'mihomohub-browser-'))
const binary = join(temporary, 'fixture-server')
execFileSync('go', ['build', '-tags=integration', '-o', binary, './tests/smoketest/server'], { stdio: 'inherit' })
const fixture = spawn(binary, ['-static', staticDir], { stdio: ['ignore', 'pipe', 'inherit'] })
let browser
let page
try {
  const { url, ids } = await new Promise((resolve, reject) => {
    const lines = createInterface({ input: fixture.stdout })
    const timer = setTimeout(() => reject(new Error('Fixture startup timed out')), 15000)
    lines.once('line', line => { clearTimeout(timer); lines.close(); resolve(JSON.parse(line)) })
    fixture.once('exit', code => reject(new Error(`Fixture exited: ${code}`)))
  })
  browser = await chromium.launch({ headless: true })
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, locale: 'en-US', serviceWorkers: 'block' })
  await context.route('**/*', route => {
    if (new URL(route.request().url()).origin !== url) return route.abort()
    return route.continue()
  })
  page = await context.newPage()
  const pageErrors = []
  const gatewayErrors = []
  const observed = []
  page.on('pageerror', error => pageErrors.push(error.message))
  page.on('request', request => {
    assert(!request.url().includes('p0-smoke-fixture-only'), 'Password reached browser URL')
    const serialized = JSON.stringify({ url: request.url(), headers: request.headers(), body: request.postData() })
    assert(!serialized.includes('never-in-browser-secret-'), 'Controller secret reached browser request')
    observed.push(request.url())
  })
  page.on('response', response => {
    if (response.url().includes('/mihomo/') && response.status() >= 400) gatewayErrors.push(`${response.status()} ${response.url()}`)
  })
  page.on('websocket', ws => {
    assert(!ws.url().includes('token='), 'Browser websocket contains an upstream token')
    observed.push(ws.url())
  })
  await page.goto(`${url}/login`)
  await page.locator('input[name="password"]').fill('p0-smoke-fixture-only')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.waitForURL(u => u.pathname === '/')
  assert.equal((await context.request.get(`${url}/api/v1/auth/me`)).status(), 200)
  const meta = await (await context.request.get(`${url}/api/v1/meta`)).json()
  assert.equal(meta.data.api_version, 'v1')
  await page.evaluate(() => localStorage.setItem('collapsedMap', JSON.stringify({ GLOBAL: true, '测试策略': true })))
  for (const [index, id] of ids.entries()) {
    if (index > 0) await page.goto(`${url}/#/setup`)
    await page.locator('#url').fill(`${url}/mihomo/${id}`)
    await page.locator('#secret').fill('')
    await page.locator('form button[type="submit"]').click()
    await page.waitForURL('**/#/overview')
    await page.goto(`${url}/#/connections`)
    await page.getByText('example.com', { exact: false }).first().waitFor()
    await page.goto(`${url}/#/rules`)
    await page.getByText('example.com', { exact: false }).first().waitFor()
    await page.goto(`${url}/#/logs`)
    await page.getByText(`smoke-${index} online`, { exact: true }).first().waitFor()
    await page.goto(`${url}/#/proxies`)
    const reject = page.locator('[data-proxy-group="测试策略"]').getByRole('button', { name: 'REJECT', exact: true })
    await reject.first().waitFor()
    const switched = page.waitForResponse(r => r.request().method() === 'PUT' && r.url().includes(`/mihomo/${id}/proxies/`))
    await reject.first().click()
    assert.equal((await switched).status(), 204)
    const proxies = await context.request.get(`${url}/mihomo/${id}/proxies`)
    assert.equal((await proxies.json()).proxies['测试策略'].now, 'REJECT')
  }
  const saved = await page.evaluate(() => JSON.parse(localStorage.getItem('endpointList') || '[]'))
  assert.equal(saved.length, 2)
  for (const endpoint of saved) {
    assert.equal(endpoint.secret, '')
    assert(endpoint.url.startsWith(`${url}/mihomo/`))
  }
  const browserStorage = await context.storageState({ indexedDB: true })
  assert(!JSON.stringify(browserStorage).includes('never-in-browser-secret-'))
  for (const id of ids) {
    for (const stream of ['connections', 'traffic', 'memory', 'logs']) {
      assert(observed.some(u => u.startsWith(`ws${url.slice(4)}/mihomo/${id}/${stream}`)), `Missing ${stream} WS for ${id}`)
    }
  }
  assert.deepEqual(gatewayErrors, [])
  assert.deepEqual(pageErrors, [])
  mkdirSync('test-results', { recursive: true })
  await page.screenshot({ path: 'test-results/xd-smoke.png', fullPage: true })
  console.log('PASS: unchanged XD, 2 endpoints, connections, rules, logs, proxy switching, 4 WS streams, empty browser secrets')
} catch (error) {
  if (page) {
    console.error('Smoke failed at', page.url(), (await page.locator('body').innerText()).slice(0, 3000))
    mkdirSync('test-results', { recursive: true })
    await page.screenshot({ path: 'test-results/xd-smoke-failure.png', fullPage: true })
  }
  throw error
} finally {
  await browser?.close()
  fixture.kill('SIGTERM')
  await new Promise(resolve => { fixture.once('exit', resolve); if (fixture.exitCode !== null) resolve() })
  rmSync(temporary, { recursive: true, force: true })
}
