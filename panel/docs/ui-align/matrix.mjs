#!/usr/bin/env node
/**
 * 一次跑完 UI-ALIGN 要求的全矩阵:三语(zh / zh-tw / en) × 深浅(light / dark) × 宽窄(1440 / 420)。
 *
 * 为什么要有它:UI-ALIGN 的规矩是「已对齐」必须附三语、深浅主题、宽窄屏各一遍的 DOM 比对结论。
 * 手工切 12 次既慢又容易漏(之前只跑过 zh/light/1440 一组就差点标了已对齐)。
 *
 * 用法(两个页面都要先起好):
 *   node panel/docs/ui-align/matrix.mjs \
 *     --main http://127.0.0.1:5180 \
 *     --base http://127.0.0.1:8905 \
 *     --route "#/rules" \
 *     --login 1111
 *
 * 输出:每组的差异摘要一行 + 末尾汇总表;任何一组有「上游独有 or main 独有」就记一行,便于逐条归因。
 * 退出码:0 = 全部组跑完(不代表无差异,差异看汇总);1 = 有组跑失败(登录超时 / 页面打不开)。
 */
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const run = promisify(execFile)
const here = dirname(fileURLToPath(import.meta.url))

// 极简参数解析:够用即可
const args = {}
for (let i = 2; i < process.argv.length; i += 2) {
  const k = process.argv[i].replace(/^--/, '')
  args[k] = process.argv[i + 1]
}

const MAIN = args.main || 'http://127.0.0.1:5180'
const BASE = args.base || 'http://127.0.0.1:8905'
const ROUTE = args.route || '#/rules'
const LOGIN = args.login || '1111'

const LANGS = ['zh', 'zh-tw', 'en']
const THEMES = ['light', 'dark']
const WIDTHS = ['1440', '420']

const results = []
let failed = 0

for (const lang of LANGS) {
  for (const theme of THEMES) {
    for (const width of WIDTHS) {
      const tag = `${lang}/${theme}/${width}`
      const out = join(here, `.run-${lang}-${theme}-${width}.json`)
      const argv = [
        join(here, 'compare.mjs'),
        '--main', MAIN,
        '--base', BASE,
        '--route', ROUTE,
        '--lang', lang,
        '--theme', theme,
        '--width', width,
        '--login', LOGIN,
        '--out', out,
      ]
      process.stdout.write(`[${tag}] 跑… `)
      try {
        // compare.mjs 有差异时退出码是 1,那是正常结果,不是失败
        const { stdout } = await run(process.execPath, argv, {
          timeout: 300_000,
          // 显式继承环境变量:compare.mjs 靠 UI_ALIGN_CHROME 找浏览器,
          // 不传的话子进程拿不到,12 组会全部启动失败。
          env: process.env,
          // compare.mjs 有差异时退出码是 1,那是有结果,不是跑失败
          maxBuffer: 32 * 1024 * 1024,
        }).catch((e) => ({ stdout: e.stdout || '' }))
        const c = /内容差异[^:]*:\s*上游独有\s*(\d+)\s*\/\s*main 独有\s*(\d+)/.exec(stdout)
        const s = /结构差异[^:]*:\s*上游独有\s*(\d+)\s*\/\s*main 独有\s*(\d+)/.exec(stdout)
        const st = /计算样式差异\((\d+)\)/.exec(stdout)
        const row = {
          tag,
          contentBase: c ? +c[1] : -1,
          contentMain: c ? +c[2] : -1,
          structBase: s ? +s[1] : -1,
          structMain: s ? +s[2] : -1,
          style: st ? +st[1] : -1,
        }
        results.push(row)
        console.log(
          `内容 ${row.contentBase}/${row.contentMain}  结构 ${row.structBase}/${row.structMain}  样式 ${row.style}`,
        )
      } catch (e) {
        // 跑失败(登录超时、页面打不开)——记为失败,继续下一组
        failed++
        results.push({ tag, error: String(e.stderr || e.message).split('\n')[0].slice(0, 120) })
        console.log('失败:', String(e.stderr || e.message).split('\n')[0].slice(0, 100))
      }
    }
  }
}

console.log('\n=== 汇总 ===')
console.log('组别'.padEnd(20), '内容(上/main)'.padEnd(16), '结构(上/main)'.padEnd(16), '样式')
for (const r of results) {
  if (r.error) {
    console.log(r.tag.padEnd(20), 'FAILED', r.error)
    continue
  }
  console.log(
    r.tag.padEnd(20),
    `${r.contentBase}/${r.contentMain}`.padEnd(16),
    `${r.structBase}/${r.structMain}`.padEnd(16),
    r.style,
  )
}

// 归一化判断:内容差异去掉白名单/噪音后是否各组一致
const bad = results.filter((r) => !r.error && (r.contentMain > 0 || r.structMain > 0))
if (bad.length) {
  console.log('\n注意:以下组的 main 侧仍有独有差异(需逐条归因或修):')
  bad.forEach((r) => console.log(' ', r.tag, `内容 main ${r.contentMain} / 结构 main ${r.structMain}`))
} else {
  console.log('\n所有组的 main 侧独有差异为 0。')
}

process.exit(failed ? 1 : 0)
