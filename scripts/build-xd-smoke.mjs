// P0 offline smoke helper. Application source and lockfiles remain untouched.
// Normal builds should use the frontend repository's `pnpm build:ui`.
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'

const frontend = resolve(process.argv[2] || '../metacubexd')
const pinned = readFileSync(new URL('../build/frontend.lock.yaml', import.meta.url), 'utf8').match(/upstream_ref: "([a-f0-9]{40})"/)[1]
const actual = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: frontend, encoding: 'utf8' }).trim()
if (actual !== pinned) throw new Error(`Expected XD ${pinned}, found ${actual}`)
process.env.NODE_ENV = 'production'
const ui = resolve(frontend, 'packages/ui')
const { loadNuxt, build } = await import(pathToFileURL(resolve(ui, 'node_modules/nuxt/dist/index.mjs')))
const nuxt = await loadNuxt({
  cwd: ui, ready: false, dev: false,
  overrides: { _generate: true, nitro: { static: true, preset: 'static' } },
})
// The test environment cannot reach Google Fonts. Disable only this build module;
// the original Vue/TS application runs with CSS font fallbacks. No mock UI mode.
nuxt.options.modules = nuxt.options.modules.filter(m => m !== '@nuxt/fonts')
for (const layer of nuxt.options._layers) {
  layer.config.modules = layer.config.modules?.filter(m => m !== '@nuxt/fonts')
}
try {
  await nuxt.ready()
  await build(nuxt)
} finally {
  await nuxt.close()
}
