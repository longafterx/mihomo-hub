import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { execFileSync, spawn } from 'node:child_process'
import { mkdtempSync, mkdirSync, rmSync, existsSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { resolve, join } from 'node:path'
import { createInterface } from 'node:readline'

const frontend = resolve(process.argv[2] || '../metacubexd')
const pinned = readFileSync(new URL('../build/frontend.lock.yaml', import.meta.url), 'utf8').match(/ref: "([a-f0-9]{40})"/)[1]
// 前端固定 SHA 在正式构建脚本中校验；此脚本也支持提交前工作区验收。
const staticDir = resolve(frontend, 'packages/ui/.output/public')
assert(existsSync(join(staticDir, 'index.html')), 'Build the pinned XD static UI first')
const require = createRequire(resolve(frontend, 'packages/ui/package.json'))
const { chromium } = require('playwright')
const temporary = mkdtempSync(join(tmpdir(), 'mihomohub-browser-'))
const binary = join(temporary, 'fixture-server')
execFileSync('go', ['build', process.env.MIHOMOHUB_EMBED_SMOKE ? '-tags=integration,frontend' : '-tags=integration', '-o', binary, './tests/smoketest/server'], { stdio: 'inherit' })
const fixture = spawn(binary, process.env.MIHOMOHUB_EMBED_SMOKE ? [] : ['-static', staticDir], { stdio: ['ignore', 'pipe', 'inherit'] })
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
  await page.locator('input[name="username"]').fill('admin')
  await page.locator('input[name="password"]').fill('p0-smoke-fixture-only')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.waitForURL(u => u.pathname === '/')
  assert.equal((await context.request.get(`${url}/api/v1/auth/me`)).status(), 200)
  const meta = await (await context.request.get(`${url}/api/v1/meta`)).json()
  if (process.env.MIHOMOHUB_EMBED_SMOKE) assert.equal(meta.data.frontend_embedded, true)
  await page.evaluate(() => localStorage.setItem('collapsedMap', JSON.stringify({ GLOBAL: true, '测试策略': true })))
  for (const [index, id] of ids.entries()) {
    await page.locator('#hub-endpoint').selectOption(id)
    await page.goto(`${url}/#/overview`)
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
  await page.goto(`${url}/#/traffic`)
  await page.getByTestId('hub-total').waitFor()
  await page.getByText('history.example', { exact: true }).first().waitFor()
  const total = await page.getByTestId('hub-total').innerText()
  assert(!total.startsWith('0 '), 'SQLite fixture history should be visible')
  await page.locator('#hub-history-instance').selectOption(ids[0])
  await page.getByTestId('hub-total').waitFor()
  assert.notEqual(await page.getByTestId('hub-total').innerText(), total)
  for (const dimension of ['proxy', 'rule', 'process', 'src_ip', 'host']) {
    await page.locator('#hub-dimension').selectOption(dimension)
    await page.getByTestId('hub-total').waitFor()
  }
  await page.evaluate(() => localStorage.setItem('endpointList', '[]'))
  await page.goto(`${url}/#/setup`)
  await page.getByRole('button', { name: 'Keep local data', exact: true }).click()
  await page.locator('#hub-name').fill('browser-created')
  await page.locator('#hub-url').fill('http://127.0.0.1:1')
  await page.locator('form input[type="checkbox"]').first().uncheck()
  await page.getByRole('button', { name: 'Add instance', exact: true }).click()
  const created = page.locator('article').filter({ hasText: 'browser-created' })
  await created.waitFor()
  await page.reload()
  await created.waitFor()
  await created.getByRole('button', { name: 'Edit', exact: true }).click()
  await page.locator('#hub-name').fill('browser-edited')
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  const edited = page.locator('article').filter({ hasText: 'browser-edited' })
  await edited.waitFor()
  await edited.getByRole('button', { name: 'Enable', exact: true }).click()
  await edited.getByRole('button', { name: 'Disable', exact: true }).waitFor()
  await edited.getByRole('button', { name: 'Disable', exact: true }).click()
  await edited.getByRole('button', { name: 'Delete', exact: true }).click()
  await edited.locator('select').selectOption('purge')
  await edited.getByRole('button', { name: 'Confirm deletion', exact: true }).click()
  await edited.waitFor({ state: 'detached' })
  const retained = page.locator(`[data-instance="${ids[1]}"]`)
  await retained.getByRole('button', { name: 'Delete', exact: true }).click()
  await retained.getByRole('button', { name: 'Confirm deletion', exact: true }).click()
  await retained.waitFor({ state: 'detached' })
  await page.goto(`${url}/#/traffic`)
  await page.getByTestId('hub-total').waitFor()
  assert.equal(await page.getByTestId('hub-total').innerText(), total)
  await page.locator('#hub-history-instance').selectOption(ids[1])
  await page.getByTestId('hub-total').waitFor()
  await page.setViewportSize({ width: 390, height: 844 })
  assert(await page.getByTestId('hub-total').isVisible())
  await page.route('**/api/v1/analytics/**', route => route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { message: 'fixture unavailable' } }) }))
  await page.getByRole('button', { name: 'Refresh', exact: true }).click()
  await page.getByRole('alert').filter({ hasText: 'fixture unavailable' }).waitFor()
  assert.equal(await page.getByTestId('hub-total').count(), 0)
  await page.unroute('**/api/v1/analytics/**')
  await page.getByRole('button', { name: 'Retry', exact: true }).click()
  await page.getByTestId('hub-total').waitFor()
  assert.equal(await page.evaluate(() => localStorage.getItem('endpointList')), '[]')
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
  await page.screenshot({ path: 'test-results/hub-smoke.png', fullPage: true })
  // Cookie 失效后的下一次平台请求必须回到登录页，不能留下旧历史数值。
  await context.clearCookies()
  await page.getByRole('button', { name: 'Refresh', exact: true }).click()
  await page.waitForURL('**/login')
  await page.locator('input[name="username"]').fill('admin')
  await page.locator('input[name="password"]').fill('p0-smoke-fixture-only')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.waitForURL(u => u.pathname === '/')
  await page.getByTestId('hub-session').waitFor()
  await page.getByRole('button', { name: 'Log out', exact: true }).click()
  await page.waitForURL('**/login')
  assert.equal((await context.request.get(`${url}/api/v1/instances`)).status(), 401)
  console.log('PASS: Hub CRUD/persistence/retain/purge, SQLite single/all history, five TOP dimensions, mobile/error/retry/logout, runtime routes, 4 WS, secret isolation')
} catch (error) {
  if (page) {
    console.error('Smoke failed at', page.url(), (await page.locator('body').innerText()).slice(0, 3000))
    mkdirSync('test-results', { recursive: true })
    await page.screenshot({ path: 'test-results/hub-smoke-failure.png', fullPage: true })
  }
  throw error
} finally {
  await browser?.close()
  fixture.kill('SIGTERM')
  await new Promise(resolve => { fixture.once('exit', resolve); if (fixture.exitCode !== null) resolve() })
  rmSync(temporary, { recursive: true, force: true })
}
