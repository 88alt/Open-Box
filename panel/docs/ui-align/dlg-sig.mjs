// 弹窗层对比工具:DOM 比对(compare.mjs)只走页面,进不了弹窗。
// 这个脚本在两侧各打开同一个弹窗,导出「标签 + 类名 + 文案 + 控件属性」的结构签名,
// 再逐行 diff。用法:
//   node panel/docs/ui-align/dlg-sig.mjs --route "#/subscriptions" --open "添加订阅"
//   node panel/docs/ui-align/dlg-sig.mjs --route "#/settings?tab=dns" --open "aria=添加:0"
//   node panel/docs/ui-align/dlg-sig.mjs --route "#/proxies" --open "css=.btn-ghost:2"
// --open 支持三种写法:
//   <文本>            按可见文案找可点元素(取第一个可见的)
//   文本:2            同上,取第 3 个
//   aria=<label>:N    按 aria-label 找
//   css=<选择器>:N    按 CSS 选择器找
// 其余参数沿用 compare.mjs:--main/--base/--lang/--theme/--width/--login。
import { chromium } from 'playwright'

const CHROME =
  process.env.CHROME_PATH ||
  'C:/Users/admin/.office-raccoon/agent/browsers/chromium-1226/chrome-win64/chrome.exe'

function parseArgs(argv) {
  const out = {
    main: 'http://127.0.0.1:5174',
    base: 'http://127.0.0.1:8802',
    route: '#/',
    open: '',
    lang: 'zh',
    theme: 'light',
    width: '1440',
    height: '1000',
    login: '1111',
    wait: '1500',
    then: '',
    keep: '',
    maxdiff: '400',
  }
  for (let i = 2; i < argv.length; i += 2) {
    const k = argv[i].replace(/^--/, '')
    if (k in out) out[k] = argv[i + 1]
  }
  return out
}

const args = parseArgs(process.argv)

// 结构签名里只保留「有对齐意义」的类名,丢掉纯工具类噪声(key 顺序、颜色渐变等)
const KEEP_CLASS =
  /^(flex|grid|gap-[\w.]+|w-[\w.]+|min-w-[\w.]+|max-w-[\w.]+|shrink-?[\w]*|grow|items-[\w-]+|justify-[\w-]+|text-[\w./-]+|font-[\w]+|input|input-[\w]+|select|select-[\w]+|textarea|textarea-[\w]+|btn|btn-[\w]+|badge|badge-[\w]+|toggle|toggle-[\w]+|checkbox|radio|label|card|card-[\w]+|modal|modal-[\w]+|dialog|tabs|tab|tab-[\w]+|join|join-item|divider|loading|loading-[\w]+|collapse|alert|alert-[\w]+|link|link-[\w]+|table|table-[\w]+|mono|font-mono|truncate|break-all|whitespace-[\w]+|overflow-[\w-]+|rounded[\w-]*|border[\w-]*|p[xytblr]?-[\w.]+|m[xytblr]?-[\w.]+|sr-only|peer|hidden|block|inline-flex|absolute|relative|top-[\w.]+|right-[\w.]+|bottom-[\w.]+|left-[\w.]+|z-[\w.]+|opacity-[\w]+|col-span-[\w]+|row-span-[\w]+|hidden|md:|sm:|lg:)$/

function cleanClass(el) {
  const raw = typeof el.className === 'string' ? el.className : el.getAttribute('class') || ''
  return raw
    .split(/\s+/)
    .filter(Boolean)
    .filter((c) => KEEP_CLASS.test(c) || c.startsWith('md:') || c.startsWith('sm:') || c.startsWith('lg:'))
    .join(' ')
}

// 在页面里跑:从弹窗根节点开始,把「有几何尺寸」的节点按先序遍历输出成文本行
function dumpSig() {
  const inView = (el) => {
    const r = el.getBoundingClientRect()
    return r.width > 0 && r.height > 0
  }
  // 弹窗根节点:优先 role=dialog,其次 dialog[open],最后 .modal-box
  const roots = [
    ...document.querySelectorAll('[role=dialog]'),
    ...document.querySelectorAll('dialog[open]'),
    ...document.querySelectorAll('.modal-box'),
  ].filter(inView)
  if (!roots.length) return { err: '没有可见弹窗' }
  // 取面积最大的那个(嵌套弹窗时外层也算,取最大的通常就是主弹窗)
  const root = roots.sort((a, b) => {
    const ra = a.getBoundingClientRect()
    const rb = b.getBoundingClientRect()
    return rb.width * rb.height - ra.width * ra.height
  })[0]

  const lines = []
  const walk = (el, depth) => {
    if (!inView(el)) return
    const tag = el.tagName.toLowerCase()
    const indent = '  '.repeat(depth)
    const kids = [...el.children]
    const cls = (typeof el.className === 'string' ? el.className : '')
      .split(/\s+/)
      .filter(Boolean)
      .filter((c) =>
        /^(flex|grid|gap-|w-|min-w-|max-w-|shrink|grow|items-|justify-|text-|font-|input|select|textarea|btn|badge|toggle|checkbox|radio|label|card|modal|dialog|tabs|tab|join|divider|loading|collapse|alert|link|table|mono|truncate|break-all|whitespace-|overflow-|rounded|border|p[xytblr]?-|m[xytblr]?-|sr-only|hidden|block|inline-flex|absolute|relative|top-|right-|bottom-|left-|z-|opacity-|col-span-|row-span-|app-[\w-]+|md:|sm:|lg:)/.test(c),
      )
      // 类名顺序不影响渲染,排序后比较,免得满屏都是「同一个类名挪了位置」
      .sort()
      .join(' ')
    const attrs = []
    for (const a of ['type', 'placeholder', 'aria-label', 'role', 'maxlength', 'min', 'max', 'rows', 'disabled', 'checked', 'href', 'target']) {
      const v = el.getAttribute(a)
      if (v !== null && v !== '' && v !== 'false') attrs.push(`${a}=${JSON.stringify(v)}`)
    }
    const rect = el.getBoundingClientRect()
    const size = `${Math.round(rect.width)}x${Math.round(rect.height)}`
    if (!kids.length) {
      const t = (el.textContent || '').trim().replace(/\s+/g, ' ')
      lines.push(`${indent}${tag}${cls ? ' .' + cls.replace(/ /g, '.') : ''}[${size}]${attrs.length ? ' ' + attrs.join(' ') : ''}${t ? ' «' + t.slice(0, 80) + '»' : ''}`)
    } else {
      lines.push(`${indent}${tag}${cls ? ' .' + cls.replace(/ /g, '.') : ''}[${size}]${attrs.length ? ' ' + attrs.join(' ') : ''}`)
    }
    for (const k of kids) walk(k, depth + 1)
  }
  walk(root, 0)
  return { lines, text: (root.innerText || '').replace(/\n{2,}/g, '\n').trim() }
}

function parseOpen(spec) {
  const m = spec.match(/^(aria|css)=(.+?)(?::(\d+))?$/)
  if (m) return { kind: m[1], value: m[2], index: Number(m[3] || 0) }
  const t = spec.match(/^(.+?):(\d+)$/)
  if (t) return { kind: 'text', value: t[1], index: Number(t[2]) }
  return { kind: 'text', value: spec, index: 0 }
}

async function run(browser, name, base) {
  const ctx = await browser.newContext({
    viewport: { width: Number(args.width), height: Number(args.height) },
  })
  const page = await ctx.newPage()
  const langMap = { zh: 'zh-CN', en: 'en-US', 'zh-tw': 'zh-TW' }
  await page.addInitScript(
    ([theme, lang]) => {
      localStorage.setItem('config/theme-mode', theme)
      localStorage.setItem('config/language', lang)
    },
    [args.theme, langMap[args.lang] || 'zh-CN'],
  )
  await page.goto(base + '/#/login')
  await page.waitForSelector('input[type=password]', { timeout: 20000 })
  await page.fill('input[type=password]', args.login)
  await page.keyboard.press('Enter')
  // 等登录真的跳走再导航:固定 sleep 会撞上「跳转还没完成就又 goto」,结果是停在登录页、
  // 后面所有点击都落空(表现为「没点到 xxx」)。轮询 hash 比定时稳。
  await page
    .waitForFunction(() => !location.hash.startsWith('#/login'), null, { timeout: 20000 })
    .catch(() => {})
  await page.waitForTimeout(1200)
  // 登录成功后应用自己还会再 push 一次默认路由(常常把刚 goto 的目标顶掉),
  // 所以这里「导航 → 校验 hash → 不行就重来」,别指望一次 goto 到位。
  const want = args.route.replace(/^#/, '')
  for (let i = 0; i < 6; i++) {
    await page.goto(base + '/' + args.route)
    await page.waitForTimeout(1200)
    if (page.url().includes(want)) break
  }
  await page.waitForTimeout(Number(args.wait) + 1500)
  await page.waitForLoadState('networkidle').catch(() => {})

  const spec = parseOpen(args.open)
  let clicked = false
  if (spec.kind === 'css') {
    const loc = page.locator(spec.value).nth(spec.index)
    await loc.waitFor({ state: 'visible', timeout: 8000 }).catch(() => {})
    if (await loc.count()) {
      await loc.click()
      clicked = true
    }
  } else if (spec.kind === 'aria') {
    const loc = page.locator(`[aria-label="${spec.value}"]`).nth(spec.index)
    await loc.waitFor({ state: 'visible', timeout: 8000 }).catch(() => {})
    if (await loc.count()) {
      await loc.click()
      clicked = true
    }
  } else {
    const loc = page
      .locator('button, a, [role=tab], [role=button], label, .btn')
      .filter({ hasText: spec.value })
    await loc.first().waitFor({ state: 'attached', timeout: 8000 }).catch(() => {})
    const n = await loc.count()
    for (let i = spec.index; i < n; i++) {
      const el = loc.nth(i)
      if (await el.isVisible()) {
        await el.click()
        clicked = true
        break
      }
    }
  }
  if (!clicked) {
    let where = ''
    try {
      where = await page.evaluate(() => (document.body.innerText || '').replace(/\s+/g, ' ').slice(0, 120))
    } catch {
      where = '(取不到文本)'
    }
    console.log(`${name}: 没点到 ${args.open} —— 当前页面: ${page.url()} | ${where}`)
    await ctx.close()
    return null
  }
  await page.waitForTimeout(Number(args.wait))

  // --then:弹窗打开后再点一次(切页签、展开折叠块等),写法同 --open。
  // 搜索范围限定在弹窗内:弹窗外(侧栏菜单、页面按钮)隔着遮罩点不到,也会点错东西。
  if (args.then) {
    const t = parseOpen(args.then)
    const scope = page.locator('[role=dialog]:visible').last()
    let ok = false
    if (t.kind === 'css') {
      const loc = scope.locator(t.value).nth(t.index)
      if (await loc.count()) { await loc.click(); ok = true }
    } else if (t.kind === 'aria') {
      const loc = scope.locator(`[aria-label="${t.value}"]`).nth(t.index)
      if (await loc.count()) { await loc.click(); ok = true }
    } else {
      const loc = scope.locator('[role=tab], button, a, .btn, label').filter({ hasText: t.value })
      await loc.first().waitFor({ state: 'attached', timeout: 8000 }).catch(() => {})
      const n = await loc.count()
      for (let i = t.index; i < n; i++) {
        const el = loc.nth(i)
        if (await el.isVisible()) { await el.click(); ok = true; break }
      }
    }
    if (!ok) console.log(`${name}: 没点到 --then ${args.then}`)
    await page.waitForTimeout(Number(args.wait))
  }

  const sig = await page.evaluate(dumpSig)
  if (args.keep) {
    await page.screenshot({ path: `${args.keep}-${name}.png`, fullPage: false })
  }
  await ctx.close()
  return sig
}

const browser = await chromium.launch({ executablePath: CHROME })
const a = await run(browser, 'main', args.main)
const b = await run(browser, 'base', args.base)
await browser.close()

if (!a || !b) process.exit(2)
if (a.err || b.err) {
  console.log('main:', a.err || 'ok', 'base:', b.err || 'ok')
  if (a.err || b.err) process.exit(2)
}

const la = a.lines
const lb = b.lines
console.log(`===== 结构签名 main ${la.length} 行 / base ${lb.length} 行 =====`)

// 逐行对比:共同前缀/后缀之外的中段就是差异区
let p = 0
while (p < la.length && p < lb.length && la[p] === lb[p]) p++
let s = 0
while (s < la.length - p && s < lb.length - p && la[la.length - 1 - s] === lb[lb.length - 1 - s]) s++

if (p === la.length && p === lb.length) {
  console.log('结构签名完全一致')
} else {
  const midA = la.slice(p, la.length - s)
  const midB = lb.slice(p, lb.length - s)
  console.log(`相同前缀 ${p} 行,相同后缀 ${s} 行;差异区 main ${midA.length} 行 / base ${midB.length} 行`)
  console.log('--- main ---')
  for (const l of midA.slice(0, Number(args.maxdiff || 400))) console.log('  ' + l)
  console.log('--- base ---')
  for (const l of midB.slice(0, Number(args.maxdiff || 400))) console.log('  ' + l)
}

// 文案差异单独列(结构相同但文字不同时上面看不出来)
const ta = a.text.split('\n').map((x) => x.trim()).filter(Boolean)
const tb = b.text.split('\n').map((x) => x.trim()).filter(Boolean)
const onlyA = ta.filter((x) => !tb.includes(x))
const onlyB = tb.filter((x) => !ta.includes(x))
if (onlyA.length || onlyB.length) {
  console.log('--- 文案:只在 main ---')
  for (const l of onlyA.slice(0, 30)) console.log('  «' + l.slice(0, 110) + '»')
  console.log('--- 文案:只在 base ---')
  for (const l of onlyB.slice(0, 30)) console.log('  «' + l.slice(0, 110) + '»')
}
