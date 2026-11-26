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
 *     --click  "key:panel"   # 可选:落地后切页签;文案 "内核" 或 data-key "key:panel"
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
// --dump <前缀> 把两侧规范化结构树各写一份(前缀-base.txt / 前缀-main.txt),用来查
// 「树整体错位一层」这类 diff 看不出根因的问题(踩过:main 多一层包裹 div 时全页都报差异)。
const DUMP = arg('dump', '')
// 子页签的对齐:默认「落地同一入口后按文案点同名页签」,--click "内核" 或 --click "设置,内核"
// (逗号分隔,依次点)。个别分类页两侧文案相同、按文案点却「点了没变化」(它本来就是默认页),
// 这时用 key:<data-key> 直接按 data-key 点,侧边栏/抽屉两个实例也能精确选中:
// --click "key:panel"。
const CLICK = arg('click', '')
  .split(',')
  .map((s) => s.trim())
  .filter(Boolean)

// ---------- 例外 A/B/C 白名单(命中即忽略,不计入差异) ----------
// A:教程按钮/激活码/市场推广类; B:安格超市、DownMan、概览顶栏两处文案; C:业务优选整体。
const WHITELIST_TEXT = [
  /使用教程/,
  /教程/,
  // 教程按钮在树里只有图标、没有文字(标题行右侧那个 youtube 图标),单靠文案匹配不到:
  // 例外 A 覆盖它,所以直接按图标标识忽略。
  /img:youtube/,
  /安格超市/,
  /安格商城/,
  /TOPIP/,
  /SUPERDOOR/,
  /OPENDOOR/,
  /DownMan/,
  /极速下载用/,
  /业务优选/,
  /業務優選/,
  /Business preference/i,
  // 例外 C 的概览卡:内核状态读不到时它自己有一句提示,main 整块卡都没有
  /暂时无法读取内核状态/,
  /暫時無法讀取核心狀態/,
  /Kernel status unavailable/i,
  // 例外 C 的组设置字段(综合评分):main 没有这一组开关
  /可靠性衰减/,
  /可信度效期/,
  // 上面两个字段的单位(「分钟」「秒」)单独成节点,只在这张卡的 label>span 里出现,
  // 按路径限定,避免把别处同名的单位一起放过
  /\/label\/span"(分钟|秒)"$/,
  /^span"(分钟|秒)"$/,
  /机场、VPS/,
  /AI 中转站/,
  /Coming ?Soon/i,
  /激活/,
  // 平台磁贴的三个图标(OpenWrt / Linux / Docker):上游把图标当独立 svg 文件打包
  // (openwrt-<hash>.svg),main 走品牌图标库的内联 data URI。逐像素截图核对过外观一致,
  // 差异只在「图片从哪来」,不是界面差异。
  /img\{img:(data-svg|openwrt|linux-color|docker)\}/,
  // 站点测速 / 客户端下载平台 / 路由器标识的图标:同样是「上游独立 svg 文件 vs main 内联」,
  // 逐个截图核对过一致(例外 A)
  /img\{img:(baidu|google|openai-light|telegram|android|windows|harmonyos|openbox)\}/,
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
      // 内联 data URI(品牌图标在 main 里是内联 svg 字符串,上游是打包出来的 svg 文件):
      // 只留一个统一标识,别把整段 URI 当 key——URI 里带 svg 源码,两边必然不同。
      if (src.startsWith('data:')) return 'img:data-svg'
      // 打包产物带 vite 的 hash 后缀(logo-CI46uiNr.svg),dev 下没有(logo.svg):
      // 不剥掉会让每个 img 都成为一条差异。但只能剥「像 hash 的」——纯小写单词
      // (earth-meridians.svg)不是 hash,剥了会变成 earth,和 upstream 的 earth-meridians 对不上。
      const stem = (src.split('/').pop() || '').replace(/\.[a-z0-9]+$/i, '')
      const name = stem.replace(/-([A-Za-z0-9_]{6,})$/, (m, tail) => (/[A-Z0-9]/.test(tail) ? '' : m))
      return 'img:' + name.slice(0, 24)
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
    // 例外 A 的「使用教程」按钮:上游没有 data-tutorial-button 标记,只能靠里面的 youtube 图标认。
    // 连外壳一起丢掉,否则 button/span/span 这几层空壳会各算一条差异。
    if (el.tagName === 'BUTTON' && el.querySelector('img[src*="youtube"]')) return null

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
      // 抽到的是哪个元素:同名元素在页面上可能有好几个(小图标按钮 vs 主按钮),
      // 只报 key 的话差异出来了也没法定位,带上标签和开头几个字
      hint: el.tagName.toLowerCase() + '|' + (el.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 18),
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
// 完整结构树:保留每一层容器,能看出包裹层级(上游多包一层 div/span 会体现出来)。
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

// 只看「看得见的东西」:有文字 / 有图标 / 有 role 的节点,不带路径。
// 容器包几层 div、span 不影响这一组——对齐界面时真正要对的是文案、控件、图标顺序,
// 纯粹多包一层壳属于「实现差异」,会写在 UI-ALIGN.md 的已解释差异里,不在这里刷屏。
function flattenContent(node, acc = []) {
  if (!node) return acc
  if (node.text || node.icon || node.role) {
    acc.push(
      node.t +
        (node.role ? `[role=${node.role}]` : '') +
        (node.icon ? `{${node.icon}}` : '') +
        (node.text ? `"${node.text}"` : ''),
    )
  }
  for (const c of node.c || []) flattenContent(c, acc)
  return acc
}


// 行对齐的关键:直接按「整行文本」做集合比对时,只要两边的行序不同(比如表格按命中数排序、
// 或某个节点在某侧还没测出延迟),整片行都会被算成「独有」——这不是界面差异,是比对方法的问题
// (踩过:规则页报 1268/875 条结构差异,实际逐项核对两边 img 都 210 个、TikTok 都出现 2 次)。
//
// 所以这里先给每行算一个「结构签名」:去掉行号前缀、把数字/百分比/大小占位统一成 {N},
// 再按签名做**多重集**比对(同一签名出现几次就要对几次),这样顺序不再影响结论。
// 签名相同但次数不同的,按差额报出来;签名完全对不上的才是真的结构差异。
const NUM_RE = /\d+(?:[.,]\d+)*/g
function signature(line) {
  return line
    // 去掉路径里的行号前缀序号(例如 "32." / "12)"),那是列表顺序,不是界面结构
    .replace(/^\s*\d+[.)]\s*/, '')
    .replace(NUM_RE, '{N}')
    .replace(/\{N\}+/g, '{N}')
}

function diffLines(a, b) {
  const sigA = a.map(signature)
  const sigB = b.map(signature)
  // 多重集:同签名出现几次就算几次
  const count = (arr) => {
    const m = new Map()
    for (const s of arr) m.set(s, (m.get(s) || 0) + 1)
    return m
  }
  const ca = count(sigA)
  const cb = count(sigB)
  const onlyBase = []
  const onlyMain = []
  // 上游独有:签名在 main 里出现次数不够的部分
  for (let i = 0; i < sigA.length; i++) {
    const s = sigA[i]
    const left = cb.get(s) || 0
    const usedBefore = sigA.slice(0, i).filter((x) => x === s).length
    if (usedBefore >= left) onlyBase.push(a[i])
  }
  for (let i = 0; i < sigB.length; i++) {
    const s = sigB[i]
    const left = ca.get(s) || 0
    const usedBefore = sigB.slice(0, i).filter((x) => x === s).length
    if (usedBefore >= left) onlyMain.push(b[i])
  }
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
  // 顺序很重要:先找真正提交表单的按钮;main 的登录页有个空内容的普通 button 排在前面,
  // 用 `.first()` 会点到它(踩过:规则页比对一直卡在登录超时)。
  const submit = page.locator('button[type=submit], form button[type=submit]')
  const byText = page.locator('button:has-text("登录"), button:has-text("登入"), button:has-text("Login")')
  const btn = (await submit.count()) ? submit.first() : byText.first()
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
  await page.waitForTimeout(2500) // 等接口与图表稳定
  // 子页签:按可见文案点(key:<data-key> 时按 data-key 点),点完等一等再抓
  for (const spec of CLICK) {
    const byKey = spec.startsWith('key:') ? spec.slice(4).trim() : ''
    const text = byKey ? '' : spec
    // 同一个文案往往有两个实例(桌面菜单 + 手机抽屉/顶部横滑),.first() 可能挑到隐藏的那个,
    // 于是 waitFor(visible) 超时、页面没切,却看起来「零差异」。逐个试,点第一个可见的。
    const candidates = byKey
      ? page.locator(`[data-key="${byKey}"]`)
      : page
          .locator('[role=tab], button, a')
          .filter({
            hasText: new RegExp('^\\s*' + text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '\\s*$'),
          })
    const total = await candidates.count()
    // 点之前记一份正文指纹:点完没变化说明这次点击是无效的(点到了当前已选中的项、
    // 或者点到的是顶部分段控件而不是真正的页签),不能当成「这个页签没差异」。
    const before = await page.evaluate(() => document.body.innerText.replace(/\s+/g, ' '))
    let clicked = false
    let didClick = false
    for (let i = 0; i < total; i += 1) {
      const el = candidates.nth(i)
      if (!(await el.isVisible().catch(() => false))) continue
      // 已经是当前选中项就没必要点,更不该因此告警:抓到的页面本来就是目标页
      const alreadyActive = await el
        .evaluate((n) => n.classList.contains('menu-active') || n.getAttribute('aria-selected') === 'true')
        .catch(() => false)
      if (alreadyActive) {
        clicked = true
        break
      }
      await el.click()
      await page.waitForTimeout(1800)
      clicked = true
      didClick = true
      break
    }
    if (clicked) {
      if (!didClick) continue
      const after = await page.evaluate(() => document.body.innerText.replace(/\s+/g, ' '))
      if (after === before) {
        console.log(`! 点了「${spec}」但页面没变化(${origin})——这次点击无效,抓到的是原页`)
      }
      continue
    }
    {
      // 找不到/点不动就照原样抓,但必须喊一声:否则两侧抓的是同一个页签,
      // 结果看起来「零差异」,其实是压根没切过去。
      console.log(`! 页签没有点到:「${spec}」(${origin})——抓到的仍是当前页`)
    }
  }
  await page.waitForTimeout(1500) // 等接口与图表稳定
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

// 内容层比对:只看文案 / 控件 / 图标,不看容器包了几层
const contentBase = flattenContent(basePage.tree).filter((l) => !isWhitelisted(l))
const contentMain = flattenContent(mainPage.tree).filter((l) => !isWhitelisted(l))
const contentDiff = diffLines(contentBase, contentMain)

const styleDiff = []
for (const k of new Set([...Object.keys(basePage.styles), ...Object.keys(mainPage.styles)])) {
  const a = basePage.styles[k]
  const b = mainPage.styles[k]
  if (!a || !b) {
    styleDiff.push({ key: k, note: a ? '上游有、main 没有' : 'main 有、上游没有' })
    continue
  }
  for (const p of Object.keys(a)) {
    if (p === 'hint') continue
    if (a[p] !== b[p]) styleDiff.push({ key: k, prop: p, base: a[p], main: b[p], what: a.hint })
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
  content: {
    baseLines: contentBase.length,
    mainLines: contentMain.length,
    onlyInBase: contentDiff.onlyBase,
    onlyInMain: contentDiff.onlyMain,
  },
  styles: styleDiff,
}

console.log(`# 比对 ${ROUTE}  lang=${LANG} theme=${THEME} width=${WIDTH}`)
console.log(`标题: 上游「${report.title.base}」 main「${report.title.main}」`)
console.log(`落在: 上游 ${report.url.base}  main ${report.url.main}`)
console.log(
  `内容差异(文案/控件/图标): 上游独有 ${contentDiff.onlyBase.length} / main 独有 ${contentDiff.onlyMain.length}`,
)
for (const l of contentDiff.onlyBase.slice(0, 60)) console.log('  -', l)
for (const l of contentDiff.onlyMain.slice(0, 60)) console.log('  +', l)
console.log(`结构行数: 上游 ${flatBase.length} / main ${flatMain.length}`)
console.log(`结构差异(含容器层级): 上游独有 ${onlyBase.length} / main 独有 ${onlyMain.length}`)
for (const l of onlyBase.slice(0, 20)) console.log('  -', l)
for (const l of onlyMain.slice(0, 20)) console.log('  +', l)
console.log(`计算样式差异(${styleDiff.length}):`)
for (const d of styleDiff.slice(0, 30)) console.log('  ~', JSON.stringify(d))

if (OUT) writeFileSync(OUT, JSON.stringify(report, null, 2))
if (DUMP) {
  writeFileSync(`${DUMP}-base.txt`, flatBase.join('\n') + '\n')
  writeFileSync(`${DUMP}-main.txt`, flatMain.join('\n') + '\n')
  writeFileSync(`${DUMP}-content-base.txt`, contentBase.join('\n') + '\n')
  writeFileSync(`${DUMP}-content-main.txt`, contentMain.join('\n') + '\n')
}
const bad = contentDiff.onlyBase.length + contentDiff.onlyMain.length + styleDiff.length
process.exit(bad === 0 ? 0 : 1)
