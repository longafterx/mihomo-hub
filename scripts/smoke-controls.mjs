// 只启动本地隔离容器和临时 Hub。所有下载/DNS 都来自本脚本，不接触已注册设备。
import assert from 'node:assert/strict'
import { execFileSync, spawn } from 'node:child_process'
import { createSocket } from 'node:dgram'
import { createServer } from 'node:http'
import { createRequire } from 'node:module'
import { mkdtempSync, readFileSync, writeFileSync, mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { createInterface } from 'node:readline'

const frontend = resolve(process.argv[2] || '../metacubexd')
const require = createRequire(join(frontend, 'packages/ui/package.json'))
const { chromium } = require('playwright')
const temporary = mkdtempSync(join(tmpdir(), 'mihomohub-controls-'))
const container = `mihomohub-controls-${process.pid}`
const docker = args => execFileSync('docker', ['--context', 'desktop-linux', ...args], { encoding: 'utf8' }).trim()
const hostAddress = docker(['run','--rm','--entrypoint','/bin/sh','metacubex/mihomo:latest','-c','nslookup host.docker.internal']).match(/Address:\s+(\d+\.\d+\.\d+\.\d+)\s*$/m)?.[1]
assert(hostAddress, 'Docker host address unavailable')
const sockets = []
let fixture, browser, assets
let proxyUpdated = false, ruleUpdated = false
const assetRequests = []

async function dnsServer(lastByte) {
  const socket = createSocket('udp4')
  sockets.push(socket)
  socket.on('message', (message, peer) => {
    // 合成的无压缩 A 记录应答，避免 DNS 测试访问公共解析器。
    if (message.length < 17) return
    const response = Buffer.concat([Buffer.from(message), Buffer.from([0xc0,0x0c,0,1,0,1,0,0,0,1,0,4,192,0,2,lastByte])])
    response.writeUInt16BE(0x8180, 2)
    response.writeUInt16BE(1, 6)
    response.writeUInt16BE(0, 8)
    response.writeUInt16BE(0, 10)
    socket.send(response, peer.port, peer.address)
  })
  await new Promise(resolve => socket.bind(0, '0.0.0.0', resolve))
  return `udp://${hostAddress}:${socket.address().port}`
}

async function waitFor(check, label) {
  const deadline = Date.now()+20000
  while (Date.now()<deadline) {
    try { const result = await check(); if (result) return result } catch {}
    await new Promise(resolve=>setTimeout(resolve,200))
  }
  throw new Error(`Timed out: ${label}`)
}

try {
  const dnsA = await dnsServer(11), dnsB = await dnsServer(22)
  assets = createServer((request,response)=>{
    assetRequests.push(request.url)
    if (request.url === '/proxies') return response.end(JSON.stringify({proxies:[{name:proxyUpdated?'updated-node':'initial-node',type:'direct'}]}))
    if (request.url === '/rules') return response.end(ruleUpdated?'payload:\n  - DOMAIN,updated.example\n  - DOMAIN,second.example\n':'payload:\n  - DOMAIN,initial.example\n')
    const name = request.url?.slice(1)
    if (['geoip.metadb','geoip.dat','geosite.dat'].includes(name)) return response.end(readFileSync(join(temporary,name)))
    response.writeHead(404).end()
  })
  await new Promise(resolve=>assets.listen(0,'0.0.0.0',resolve))
  const source = `http://${hostAddress}:${assets.address().port}`
  const config = {
    'mixed-port':7890,'allow-lan':false,mode:'rule','log-level':'info',
    'external-controller':'0.0.0.0:9090',secret:'local-control-fixture-only',
    'geodata-mode':false,'geo-auto-update':false,
    'geox-url':{mmdb:`${source}/geoip.metadb`,geoip:`${source}/geoip.dat`,geosite:`${source}/geosite.dat`,asn:`${source}/geoip.metadb`},
    dns:{enable:true,listen:'127.0.0.1:1053','enhanced-mode':'redir-host',nameserver:[dnsA], 'use-hosts':false},
    tun:{enable:false,stack:'mixed',device:'hubtest'},
    'proxy-providers':{'local-proxies':{type:'http',url:`${source}/proxies`,path:'./providers/proxies.yaml',interval:86400}},
    'rule-providers':{'local-rules':{type:'http',behavior:'classical',format:'yaml',url:`${source}/rules`,path:'./providers/rules.yaml',interval:86400}},
    'proxy-groups':[{name:'local-selector',type:'select',use:['local-proxies']}],
    rules:['RULE-SET,local-rules,DIRECT','GEOIP,CN,DIRECT','GEOSITE,cn,DIRECT','MATCH,DIRECT'],
  }
  writeFileSync(join(temporary,'config.yaml'),JSON.stringify(config))
  docker(['run','-d','--name',container,'--memory','256m','--cpus','1','--pids-limit','64',
    '--cap-add','NET_ADMIN','--device','/dev/net/tun',
    '-p','127.0.0.1::9090','-v',`${join(temporary,'config.yaml')}:/root/.config/mihomo/config.yaml:ro`,
    'metacubex/mihomo:latest'])
  for (const name of ['geoip.metadb','geoip.dat','geosite.dat']) docker(['cp',`${container}:/root/.config/mihomo/${name}`,join(temporary,name)])
  const port = docker(['port',container,'9090/tcp']).split(':').at(-1)
  const controller = `http://127.0.0.1:${port}`
  const version = await waitFor(async()=>{
    const response=await fetch(controller+'/version',{headers:{Authorization:'Bearer local-control-fixture-only'}})
    return response.ok && (await response.json()).version
  },'real controller startup')
  execFileSync('go',['build','-tags=integration','-o',join(temporary,'fixture'),'./tests/smoketest/server'],{stdio:'inherit'})
  fixture=spawn(join(temporary,'fixture'),['-static',join(frontend,'packages/ui/.output/public'),'-local-controller',controller],{stdio:['ignore','pipe','inherit']})
  const {url,local_id:id}=await new Promise((resolve,reject)=>{
    const lines=createInterface({input:fixture.stdout})
    const timer=setTimeout(()=>reject(new Error('Hub fixture startup timed out')),15000)
    lines.once('line',line=>{clearTimeout(timer);lines.close();resolve(JSON.parse(line))})
    fixture.once('exit',()=>reject(new Error('Hub fixture stopped')))
  })
  browser=await chromium.launch({headless:true})
  const context=await browser.newContext({viewport:{width:1440,height:1000},serviceWorkers:'block'})
  await context.route('**/*',route=>new URL(route.request().url()).origin===url?route.continue():route.abort())
  const page=await context.newPage()
  const pageErrors=[]
  page.on('pageerror',error=>pageErrors.push(error.message))
  await page.goto(url+'/login')
  await page.locator('input[name="password"]').fill('p0-smoke-fixture-only')
  await page.getByRole('button',{name:'登录',exact:true}).click()
  await page.waitForURL(u=>u.pathname==='/')
  await page.locator('#hub-endpoint').click()
  await page.locator(`[data-hub-menu-instance="${id}"]`).click()
  const native=async(method,path,body)=>{
    const response=await context.request.fetch(url+`/mihomo/${id}`+path,{method,headers:{Origin:url},...(body===undefined?{}:{data:body}),timeout:190000})
    assert.equal(response.status(),method==='GET'||path==='/restart'?200:204,`${method} ${path}: ${response.status()} ${await response.text()}`)
    return method==='GET'?response.json():undefined
  }
  await native('PATCH','/configs',{'mixed-port':7898,'allow-lan':true})
  let running=await native('GET','/configs')
  assert.equal(running['mixed-port'],7898)
  assert.equal(running['allow-lan'],true)
  proxyUpdated=true;ruleUpdated=true
  await native('PUT','/providers/proxies/local-proxies')
  assert.equal((await native('GET','/providers/proxies')).providers['local-proxies'].proxies[0].name,'updated-node')
  await native('PUT','/providers/rules/local-rules')
  assert.equal((await native('GET','/providers/rules')).providers['local-rules'].ruleCount,2)
  await native('PATCH','/configs',{tun:{enable:true,stack:'mixed',device:'hubtest','auto-route':false,'auto-detect-interface':false}})
  assert.equal((await native('GET','/configs')).tun.enable,true)
  assert(docker(['exec',container,'ip','link','show','hubtest']).includes('hubtest'),'TUN device missing')
  await native('PATCH','/configs',{tun:{enable:false}})
  await page.goto(url+'/#/config')
  await page.reload() // 前面的外部 API 测试修改了配置，丢弃页面先前的查询缓存。
  const card=page.getByTestId('hub-dns-settings')
  await card.getByText('Import a complete YAML first',{exact:true}).waitFor()
  await page.waitForFunction(()=>document.querySelector('#mixedPort')?.value==='7898')
  // 直接操作 XD 的端口和 LAN 控件，随后从真实内核读回结果。
  const portSaved=page.waitForResponse(r=>r.url().endsWith(`/mihomo/${id}/configs`)&&r.request().method()==='PATCH')
  await page.locator('#mixedPort').fill('7899')
  await page.locator('#mixedPort').blur()
  assert.equal((await portSaved).status(),204)
  const lanSaved=page.waitForResponse(r=>r.url().endsWith(`/mihomo/${id}/configs`)&&r.request().method()==='PATCH')
  await page.locator('#enable-allow-lan').uncheck()
  assert.equal((await lanSaved).status(),204)
  running=await native('GET','/configs')
  assert.equal(running['mixed-port'],7899)
  assert.equal(running['allow-lan'],false)
  const imported=page.waitForResponse(r=>r.url().endsWith(`/instances/${id}/runtime-config`)&&r.request().method()==='PUT')
  await page.getByTestId('hub-dns-yaml').setInputFiles(join(temporary,'config.yaml'))
  assert.equal((await imported).status(),200)
  await card.getByText('Full YAML available',{exact:true}).waitFor()
  await page.locator('#hub-dns-nameserver').fill(dnsB)
  const applied=page.waitForResponse(r=>r.url().endsWith('/runtime-config/dns')&&r.request().method()==='PATCH')
  await card.getByRole('button',{name:'Apply DNS and hot-load',exact:true}).click()
  assert.equal((await applied).status(),200)
  const answer=await native('GET','/dns/query?name=runtime-dns.example&type=A')
  assert(answer.Answer.some(item=>item.data==='192.0.2.22'),'DNS resolver did not use the edited nameserver')
  for(const [label,path] of [['Flush Fake-IP','/cache/fakeip/flush'],['Flush DNS Cache','/cache/dns/flush'],['Update GEO Databases','/configs/geo']]){
    const done=page.waitForResponse(r=>r.url().endsWith(`/mihomo/${id}${path}`)&&r.request().method()==='POST',{timeout:190000})
    await page.getByRole('button',{name:label,exact:true}).click()
    assert.equal((await done).status(),204,label)
  }
  assert(assetRequests.includes('/geoip.metadb'),'Geo update did not fetch the configured database')
  await native('PUT','/configs?force=true',{path:'',payload:''})
  running=await native('GET','/configs')
  assert.equal(running['mixed-port'],7890,'reload did not restore the device file')
  await native('POST','/restart')
  await new Promise(resolve=>setTimeout(resolve,500))
  await waitFor(async()=>{
    const response=await context.request.get(url+`/mihomo/${id}/version`)
    return response.status()===200
  },'controller restart')
  await page.reload()
  await page.locator('#hub-dns-nameserver').waitFor()
  assert.equal(await page.locator('#hub-dns-nameserver').inputValue(),dnsB)
  mkdirSync('test-results',{recursive:true})
  await card.screenshot({path:'test-results/hub-controls-desktop.png',animations:'disabled'})
  await page.setViewportSize({width:390,height:844})
  assert(await card.evaluate(el=>el.scrollWidth<=el.clientWidth),'DNS card overflows on mobile')
  await card.screenshot({path:'test-results/hub-controls-mobile.png',animations:'disabled'})
  assert.deepEqual(pageErrors,[])
  console.log(JSON.stringify({result:'PASS',mihomo:version,image:docker(['image','inspect','metacubex/mihomo:latest','--format','{{.Id}}']),checks:['port/LAN readback','proxy/rule provider update','TUN device creation','full YAML import','DNS resolver change','cache/Geo actions','reload file','restart recovery','browser desktop/mobile']}))
} finally {
  await browser?.close()
  if(fixture){fixture.kill('SIGTERM');await new Promise(resolve=>fixture.once('exit',resolve))}
  for(const socket of sockets) socket.close()
  if(assets) await new Promise(resolve=>assets.close(resolve))
  try { docker(['rm','-f',container]) } catch {}
  rmSync(temporary,{recursive:true,force:true})
}
