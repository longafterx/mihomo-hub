// 使用真实 Nuxt 路由验证菜单点击；只检查 URL/aria-current 会漏掉视觉高亮停在旧页面的回归。
import assert from 'node:assert/strict'
import { execFileSync, spawn } from 'node:child_process'
import { once } from 'node:events'
import { mkdtempSync, mkdirSync, rmSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { createInterface } from 'node:readline'

const frontend = resolve(process.argv[2] || '../metacubexd')
const require = createRequire(join(frontend, 'packages/ui/package.json'))
const { chromium } = require('playwright')
const temporary = mkdtempSync(join(tmpdir(), 'mihomohub-navigation-'))
const binary = join(temporary, 'fixture-server')
const embedded = !!process.env.MIHOMOHUB_EMBED_SMOKE
execFileSync('go', ['build', embedded ? '-tags=integration,frontend' : '-tags=integration', '-o', binary, './tests/smoketest/server'], { stdio: 'inherit' })
const fixture = spawn(binary, embedded ? [] : ['-static', join(frontend, 'packages/ui/.output/public')], { stdio: ['ignore', 'pipe', 'inherit'] })
let browser
let page
const errors = []

try {
  const { url, ids } = await new Promise((resolve, reject) => {
    const lines = createInterface({ input: fixture.stdout })
    const timer = setTimeout(() => reject(new Error('Fixture startup timed out')), 15000)
    lines.once('line', line => { clearTimeout(timer); lines.close(); resolve(JSON.parse(line)) })
    fixture.once('exit', code => reject(new Error(`Fixture exited: ${code}`)))
  })
  browser = await chromium.launch({ headless: true })
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, locale: 'en-US', serviceWorkers: 'block' })
  await context.route('**/*', route => new URL(route.request().url()).origin === url ? route.continue() : route.abort())
  page = await context.newPage()
  page.on('pageerror', error => errors.push(error.message))
  await page.goto(url + '/login')
  await page.locator('[name="password"]').fill('p0-smoke-fixture-only')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.waitForURL(location => location.pathname === '/')
  await page.getByTestId('hub-instance-status').waitFor()
  await page.locator('#hub-endpoint').click()
  await page.locator(`[data-hub-menu-instance="${ids[0]}"]`).click()

  async function assertSidebar(path) {
    try {
      await page.waitForFunction(path => {
        const active = document.querySelectorAll('.nav-link.is-active')
        return active.length === 1 && active[0].getAttribute('href') === `#${path}`
      }, path, { timeout: 2000 })
    } catch {
      const active = await page.locator('.nav-link.is-active').evaluateAll(nodes => nodes.map(node => node.getAttribute('href')))
      throw new Error(`Menu selection did not follow ${path}: URL=${page.url()}, highlighted=${JSON.stringify(active)}`)
    }
  }

  async function navigate(path) {
    await page.locator(`a.nav-link[href="#${path}"]:visible`).click()
    await page.waitForURL(location => location.hash === `#${path}`)
    await assertSidebar(path)
  }

  // 一次点击就应同步页面与高亮；重复点击当前菜单也不能产生空白或旧选中项。
  for (const path of ['/rules', '/connections', '/traffic', '/logs', '/config', '/proxies', '/overview']) {
    await navigate(path)
    await navigate(path)
  }
  // 展开侧栏后重复录屏中的规则 → 连接 → 用量点击链。
  await page.locator('.drawer-side button:visible').first().click()
  await navigate('/rules')
  await navigate('/connections')
  await page.getByText('example.com', { exact: false }).first().waitFor()
  await navigate('/traffic')
  await page.getByTestId('hub-total').waitFor()

  // 快速连续点击最后应停在目标页面，不能因延迟更新 key 再挂载一次用量页面。
  await navigate('/rules')
  await page.evaluate(() => {
    const roots = new Set()
    const observer = new MutationObserver(records => {
      for (const record of records) {
        for (const node of record.addedNodes) {
          if (!(node instanceof Element)) continue
          if (node.matches('[data-testid="hub-history"]')) roots.add(node)
          for (const root of node.querySelectorAll('[data-testid="hub-history"]')) roots.add(root)
        }
      }
    })
    observer.observe(document.getElementById('__nuxt'), { childList: true, subtree: true })
    window.__navigationMounts = { roots, observer }
  })
  for (const path of ['/connections', '/logs', '/traffic']) {
    await page.locator(`a.nav-link[href="#${path}"]:visible`).click()
  }
  await page.waitForURL(location => location.hash === '#/traffic')
  await assertSidebar('/traffic')
  await page.getByTestId('hub-total').waitFor()
  await page.waitForTimeout(400)
  const mounts = await page.evaluate(() => {
    const { roots, observer } = window.__navigationMounts
    observer.disconnect()
    delete window.__navigationMounts
    return roots.size
  })
  assert.equal(mounts, 1, 'Navigating to usage mounted the page more than once')
  await navigate('/rules')
  await navigate('/connections')
  await page.goBack()
  await page.waitForURL(location => location.hash === '#/rules')
  await assertSidebar('/rules')
  await page.goForward()
  await page.waitForURL(location => location.hash === '#/connections')
  await assertSidebar('/connections')
  await page.reload()
  await assertSidebar('/connections')

  // 页面仍按实例隔离：修复路由同步后，同页切换实例仍需清空并刷新当前实例数据。
  await navigate('/rules')
  for (const id of ids) {
    await page.locator('#hub-endpoint').click()
    await page.locator(`[data-hub-menu-instance="${id}"]`).click()
    await assertSidebar('/rules')
    assert.equal(new URL(page.url()).hash, '#/rules')
    assert.equal(await page.locator(`[data-hub-instance-status="${id}"]`).getAttribute('aria-current'), 'true')
    await page.getByText('example.com', { exact: true }).first().waitFor()
  }
  mkdirSync('test-results', { recursive: true })
  // Playwright 的可见性判断不包含 opacity；补充断言，防止内容存在却一直透明。
  await page.waitForFunction(() => !document.querySelector('.page-enter-active, .page-leave-active'))
  const opacity = await page.getByText('example.com', { exact: true }).first().evaluate(element => {
    let opacity = 1
    for (let node = element; node; node = node.parentElement) {
      opacity *= Number(getComputedStyle(node).opacity)
    }
    return opacity
  })
  assert.equal(opacity, 1, 'Page content remained transparent after navigation')
  await page.screenshot({ path: 'test-results/hub-navigation-desktop.png' })

  await page.setViewportSize({ width: 390, height: 844 })
  await page.reload()
  const mobile = page.getByRole('navigation', { name: 'Mobile bottom navigation', exact: true })
  await mobile.waitFor()
  for (const path of ['/overview', '/proxies', '/rules', '/connections']) {
    await mobile.locator(`a[href="#${path}"]`).click()
    await page.waitForURL(location => location.hash === `#${path}`)
    await assertSidebar(path)
    const active = await mobile.locator('a.text-primary').getAttribute('href')
    assert.equal(active, `#${path}`, 'Mobile menu highlight did not follow the page')
  }
  await page.getByRole('button', { name: 'More menu', exact: true }).click()
  const usage = page.locator('div.fixed.inset-x-0 a[href="#/traffic"]:visible')
  await usage.click()
  await assertSidebar('/traffic')
  await usage.waitFor({ state: 'hidden' })
  assert((await page.getByRole('button', { name: 'More menu', exact: true }).getAttribute('class')).split(' ').includes('bg-primary'))
  await page.getByTestId('hub-total').waitFor()
  await page.screenshot({ path: 'test-results/hub-navigation-mobile.png' })
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)

  // 使用抽屉导航时，选中页面后应立即关闭抽屉，不等待下一次点击。
  await page.evaluate(() => localStorage.setItem('useMobileBottomNav', 'false'))
  await page.reload()
  await page.locator('label[aria-label="open sidebar"]').click()
  await navigate('/rules')
  await page.waitForFunction(() => !document.getElementById('main-drawer').checked)
  assert.deepEqual(errors, [])
  console.log('PASS: desktop collapsed/expanded menus, rapid clicks without duplicate page mounts, visual highlight, back/forward/reload, same-page instance switching, mobile bottom/secondary/drawer navigation')
} catch (error) {
  if (page) {
    mkdirSync('test-results', { recursive: true })
    await page.screenshot({ path: 'test-results/hub-navigation-failure.png' })
  }
  throw error
} finally {
  await browser?.close()
  if (fixture.exitCode === null) {
    const exited = once(fixture, 'exit')
    fixture.kill('SIGTERM')
    await exited
  }
  rmSync(temporary, { recursive: true, force: true })
}
