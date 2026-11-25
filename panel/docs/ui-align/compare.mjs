#!/usr/bin/env node
/**
 * 界面逐页对齐的 DOM 比对工具(panel/docs/UI-ALIGN.md 里「比对方法」一节的实现)。
 *
 * 为什么要有它:截图判断在真实比对里误判过两次(把上游的 canary/随机标识、把不同后端实例的
 * 动态数字当成界面差异)。这里改用**规范化结构树 + 计算样式**做客观比对,截图只做线索。
 *
 * 用法(两个页面都要是能访问的地址):
 *   node panel/docs/ui-align/compare.mjs \
 *     --main   http://localhost:5180 \
 *     --base   http://127.0.0.1:8802 \
 *     --route  "#/proxies" \
 *     --lang   zh            # zh | zh-tw | en
 *     --theme  light         # light | dark
 *     --width  1440          # 桌面 1440,窄屏 420
 *     --login  1111          # 两边共用同一个密码,脚本自己登录
 *
 * 输出:差异摘要(标准输出)+ 完整差异写入 --out 指定的文件(默认不写)。
 * 退出码:0 = 无差异或只剩白名单/噪音;1 = 有需要处理的差异。
 *
 * 设计约束(见任务要求):
 *   - 保留标签、role、可见文本、图标标识、顺序、disabled/checked/折叠状态;
 *   - 丢弃哈希 class、data-v-*、自动生成 id、动态数字/时间/流量/延迟;
 *   - 内置例外 A/B/C 白名单,命中即忽略;
 *   - 不追像素级,剩余差异必须"可解释"并在 UI-ALIGN.md 记录。
 */

import { chromium } from 'playwright'
import { writeFileSync } from 'node:fs'

const CHROME =
  process.env.UI_ALIGN_CHROME ||
  'C:/Users/admin/.office-raccoon/agent/browsers/chromium-1226/chrome-win64/chrome.exe'

// ---------- 参数 ----------
function arg(name, def = '') {
  const i = process.argv.indexOf('--' + name)
  return i > 0 && process.argv[i + 1] ? process.argv[i + 1] : def
}
const MAIN = arg('main', 'http://127.0.0.1:5180')
const BASE = arg('base', 'http://127.0.0.1:8802')
const ROUTE = arg('route', '#/proxies')
const LANG = arg('lang', 'zh')
const THEME = arg('theme', 'light')
const WIDTH = Number(arg('width', '1440'))
const LOGIN = arg('login', '1111')
const OUT = arg('out', '')

// ---------- 例外 A/B/C 白名单(命中即忽略,不计入差异) ----------
// A:教程按钮/激活码/市场推广类; B:安格超市、DownMan、概览顶栏两处文案; C:业务优选整体。
const WHITELIST_TEXT = [
  /使用教程/,
  /教程/,
  /安格超市/,
  /安格商城/,
  /TOPIP/,
  /SUPERDOOR/,
  /OPENDOOR/,
  /DownMan/,
  /极速下载用/,
  /业务优选/,
  /机场、VPS/,
  /AI 中转站/,
  /Coming ?Soon/i,
  /激活/,
]
const WHITELIST_SELECTOR = [
  '[data-business-preference]',
  '[data-tutorial-button]',
  '[data-market-link]',
]
const isWhitelisted = (text) => WHITELIST_TEXT.some((re) => re.test(text || ''))

// ---------- 页面内规范化 ----------
// 在浏览器里跑:把 DOM 变成稳定的结构树。
const EXTRACT = () => {
  const DROP_TAGS = new Set(['SCRIPT', 'STYLE', 'LINK', 'META', 'NOSCRIPT', 'svg', 'SVG'])
  const KEEP_ATTRS = [
    'role',
    'aria-label',
    'aria-expanded',
    'aria-selected',
    'aria-checked',
    'aria-disabled',
    'disabled',
    'checked',
    'type',
    'href',
  ]

  // 动态值占位:数字、时间、流量、延迟、百分比、日期、版本号、UUID、长 hex
  const DYN = [
    [/\d{4}[-/年]\d{1,2}[-/月]\d{1,2}日?/g, '{DATE}'],
    [/\d{1,2}:\d{2}(:\d{2})?/g, '{TIME}'],
    [/\d+(\.\d+)?\s*(B|KB|KiB|MB|MiB|GB|GiB|TB)\/s/g, '{SPEED}'],
    [/\d+(\.\d+)?\s*(B|KB|KiB|MB|MiB|GB|GiB|TB)/g, '{SIZE}'],
    [/\d+(\.\d+)?\s*(ms|s|秒|分钟|小时|天)/g, '{DUR}'],
    [/\d+(\.\d+)?\s*%/g, '{PCT}'],
    [/\bv\d+\.\d+\.\d+[-.\w]*/g, '{VER}'],
    [/\b\d+(\.\d+){1,3}\b/g, '{NUM}'],
    [/\b\d+\b/g, '{N}'],
    [/\b[0-9a-f]{8,}\b/gi, '{HEX}'],
  ]
  const normText = (s) => {
    let t = (s || '').replace(/\s+/g, ' ').trim()
    for (const [re, rep] of DYN) t = t.replace(re, rep)
    return t
  }

  const iconKey = (el) => {
    // 图标标识:img 取文件名;svg 取 viewBox + 首个子元素的标签(形状大致可比)
    if (el.tagName === 'IMG') {
      const src = el.getAttribute('src') || ''
      return 'img:' + (src.split('/').pop() || '').replace(/[-.][\w-]*$/, '').slice(0, 24)
    }
    if (el.tagName === 'svg' || el.tagName === 'SVG') {
      return 'svg:' + (el.getAttribute('viewBox') || '') + ':' + (el.firstElementChild?.tagName || '')
    }
    return ''
  }

  const walk = (el) => {
    if (!el || DROP_TAGS.has(el.tagName)) return null
    if (el.tagName === 'svg' || el.tagName === 'SVG') {
      return { t: iconKey(el) }
    }
    const style = getComputedStyle(el)
    if (style.display === 'none' || style.visibility === 'hidden' || el.hidden) return null
    for (const sel of ['[data-business-preference]', '[data-tutorial-button]', '[data-market-link]']) {
      if (el.matches?.(sel)) return null
    }

    const node = { t: el.tagName.toLowerCase() }
    for (const a of KEEP_ATTRS) {
      if (a === 'role' && el.getAttribute(a)) node.role = el.getAttribute(a)
      else if (a === 'href' && el.getAttribute(a)) node.href = normText(el.getAttribute(a)).slice(0, 60)
      else if (a !== 'role' && a !== 'href' && el.hasAttribute(a)) node[a] = el.getAttribute(a) || true
    }
    const ik = iconKey(el)
    if (ik) node.icon = ik

    // 只收直接文本(子元素的文字各自成节点),避免文案被重复计入
    let own = ''
    for (const c of el.childNodes) if (c.nodeType === 3) own += c.textContent
    own = normText(own)
    if (own) node.text = own

    const kids = []
    for (const c of el.children) {
      const k = walk(c)
      if (k) kids.push(k)
    }
    if (kids.length) node.c = kids
    // 完全空的容器不参与比对(布局噪声)
    if (!node.text && !node.icon && !kids.length && !node.role) return null
    return node
  }

  return walk(document.body)
}

// 计算样式抽样(只抽关键容器,避免上百个节点的噪声)
const STYLES = () => {
  const pick = (el) => {
    const s = getComputedStyle(el)
    return {
      fontSize: s.fontSize,
      color: s.color,
      bg: s.backgroundColor,
      padding: s.padding,
      margin: s.margin,
      gap: s.gap,
      display: s.display,
      dir: s.flexDirection,
      radius: s.borderRadius,
    }
  }
  const out = {}
  const sel = {
    card: '.card',
    button: 'button.btn',
    tab: '[role=tab]',
    // 输入框取 .input 本身:padding 由它自己的类决定,取裸 input 会被 join 组的父级 padding 干扰
    input: 'input.input',
    heading: 'h1, h2, h3',
    menuItem: '.menu li',
  }
  for (const [k, s] of Object.entries(sel)) {
    // 取第一个**可见**的匹配元素:页面上常有多份同类节点(隐藏的旧路由、tab 面板、弹层),
    // 拿第一个容易抽到不可见的那个,导致样式比对出现假差异。
    const els = [...document.querySelectorAll(s)]
      .filter((e) => {
        const st = getComputedStyle(e)
        const r = e.getBoundingClientRect()
        return st.display !== 'none' && st.visibility !== 'hidden' && r.width > 0 && r.height > 0
      })
      // 取面积最大的那个:类名相同的元素在页面里可能有很多(小图标按钮 vs 主按钮),
      // 抽到哪个纯看 DOM 顺序,会让样式比对随机抖动。用面积挑「主要的那一个」。
      .sort((a, b) => {
        const ra = a.getBoundingClientRect()
        const rb = b.getBoundingClientRect()
        return rb.width * rb.height - ra.width * ra.height
      })
    if (els.length) out[k] = pick(els[0])
  }
  return out
}

// ---------- 序列化 ----------
function flatten(node, path = '', acc = []) {
  if (!node) return acc
  const key =
    path +
    '/' +
    node.t +
    (node.role ? `[role=${node.role}]` : '') +
    (node.icon ? `{${node.icon}}` : '') +
    (node.text ? `"${node.text}"` : '')
  acc.push(key)
  for (const c of node.c || []) flatten(c, key, acc)
  return acc
}

function diffLines(a, b) {
  const setA = new Map(a.map((l, i) => [l, i]))
  const setB = new Map(b.map((l, i) => [l, i]))
  const onlyBase = a.filter((l) => !setB.has(l))
  const onlyMain = b.filter((l) => !setA.has(l))
  return { onlyBase, onlyMain }
}

// ---------- 登录 ----------
async function login(page, origin) {
  await page.goto(origin + '/#/login', { waitUntil: 'domcontentloaded' })
  // main 的登录页是懒加载路由,首帧可能还没渲染出输入框:等它出现,不要只 sleep 一下就去数
  // 登录页的密码框在 input 上(:type 是 password),但首帧可能还没挂载;等它出现。
  // 用 input.input 兜底:有些实现把 class 放在外层 span 上,内层 input 只有类型。
  const input = page.locator('input[type=password], input.input, .input input')
  try {
    await input.first().waitFor({ state: 'visible', timeout: 20000 })
  } catch {
    return // 真的没有登录框(已登录),交给后面的页面判断
  }
  await input.first().fill(LOGIN)
  // 登录按钮的文案随语言变(简体「登录」/ 繁体「登入」/ 英文「Login」),用表单里的 submit 按钮定位,
  // 不靠文案(踩过:繁体下按「登录」找不到按钮,超时)。
  const btn = page.locator('button[type=submit], form button, button:has-text("登录"), button:has-text("登入"), button:has-text("Login")').first()
  await btn.click()
  // 登录成功后路由会跳到默认页;等它离开 /login 再继续,否则抓到的是登录页
  for (let i = 0; i < 20; i++) {
    await page.waitForTimeout(500)
    if (!/#\/login/.test(page.url())) break
  }
  const still = /#\/login/.test(page.url())
  if (still) {
    const msg = await page.locator('body').innerText().catch(() => '')
    throw new Error('登录失败(' + origin + '): ' + msg.slice(0, 120).replace(/\s+/g, ' '))
  }
}

async function applyEnv(page, base, lang, theme) {
  // 语言 / 主题存在 localStorage 里(键名沿用面板的约定),导航前就写好最稳
  await page.addInitScript(
    ({ lang, theme }) => {
      try {
        // 键名与取值跟 panel/src/store/settings.ts 一致:LANG 是 'zh-CN'|'zh-TW'|'en-US',
        // 主题存在 config/theme-mode(light|dark|system)。
        const langMap = { zh: 'zh-CN', 'zh-tw': 'zh-TW', en: 'en-US' }
        localStorage.setItem('config/language', langMap[lang] || lang)
        localStorage.setItem('config/theme-mode', theme)
      } catch {
        /* 读不到就算了,后面的 DOM 比对会体现出来 */
      }
    },
    { lang, theme },
  )
}

async function capture(browser, origin) {
  const ctx = await browser.newContext({
    viewport: { width: WIDTH, height: 900 },
    deviceScaleFactor: 1,
    locale: LANG === 'en' ? 'en-US' : LANG === 'zh-tw' ? 'zh-TW' : 'zh-CN',
  })
  const page = await ctx.newPage()
  await login(page, origin)
  // 语言 / 主题必须在登录之后写再 reload:addInitScript 在每次导航前都跑,登录页会把它们
  // 覆盖回默认值,只在导航前写一次是无效的(踩过:深色主题比对时两边都是浅色)。
  await page.evaluate(
    ({ lang, theme }) => {
      const langMap = { zh: 'zh-CN', 'zh-tw': 'zh-TW', en: 'en-US' }
      localStorage.setItem('config/language', langMap[lang] || lang)
      localStorage.setItem('config/theme-mode', theme)
    },
    { lang: LANG, theme: THEME },
  )
  await page.goto(origin + ROUTE, { waitUntil: 'domcontentloaded' })
  await page.reload({ waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(3500) // 等接口与图表稳定
  const tree = await page.evaluate(EXTRACT)
  const styles = await page.evaluate(STYLES)
  const title = await page.title()
  const url = page.url()
  await ctx.close()
  return { tree, styles, title, url }
}

// ---------- 主流程 ----------
const browser = await chromium.launch({ executablePath: CHROME })
const basePage = await capture(browser, BASE)
const mainPage = await capture(browser, MAIN)
await browser.close()

const flatBase = flatten(basePage.tree).filter((l) => !isWhitelisted(l))
const flatMain = flatten(mainPage.tree).filter((l) => !isWhitelisted(l))
const { onlyBase, onlyMain } = diffLines(flatBase, flatMain)

const styleDiff = []
for (const k of new Set([...Object.keys(basePage.styles), ...Object.keys(mainPage.styles)])) {
  const a = basePage.styles[k]
  const b = mainPage.styles[k]
  if (!a || !b) {
    styleDiff.push({ key: k, note: a ? '上游有、main 没有' : 'main 有、上游没有' })
    continue
  }
  for (const p of Object.keys(a)) {
    if (a[p] !== b[p]) styleDiff.push({ key: k, prop: p, base: a[p], main: b[p] })
  }
}

const report = {
  route: ROUTE,
  env: { lang: LANG, theme: THEME, width: WIDTH },
  title: { base: basePage.title, main: mainPage.title },
  url: { base: basePage.url, main: mainPage.url },
  dom: {
    baseLines: flatBase.length,
    mainLines: flatMain.length,
    onlyInBase: onlyBase,
    onlyInMain: onlyMain,
  },
  styles: styleDiff,
}

console.log(`# 比对 ${ROUTE}  lang=${LANG} theme=${THEME} width=${WIDTH}`)
console.log(`标题: 上游「${report.title.base}」 main「${report.title.main}」`)
console.log(`落在: 上游 ${report.url.base}  main ${report.url.main}`)
console.log(`结构行数: 上游 ${flatBase.length} / main ${flatMain.length}`)
console.log(`只在基准里有(${onlyBase.length}):`)
for (const l of onlyBase.slice(0, 40)) console.log('  -', l)
console.log(`只在 main 里有(${onlyMain.length}):`)
for (const l of onlyMain.slice(0, 40)) console.log('  +', l)
console.log(`计算样式差异(${styleDiff.length}):`)
for (const d of styleDiff.slice(0, 30)) console.log('  ~', JSON.stringify(d))

if (OUT) writeFileSync(OUT, JSON.stringify(report, null, 2))
const bad = onlyBase.length + onlyMain.length + styleDiff.length
process.exit(bad === 0 ? 0 : 1)
