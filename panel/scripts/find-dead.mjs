#!/usr/bin/env node
/**
 * 扫 panel/src,找出「文件存在但没人 import」的组件 / 工具模块。
 *
 * 为什么要有它:任务要求清理 main 里上游没有的键与配套代码,反向也要确认自研代码里没有
 * 早期试验遗留、已经不被任何页面引用的死文件(死组件会让 build 变大、也容易误改)。
 *
 * 用法:node panel/scripts/find-dead.mjs
 * 输出:每个可疑文件一行,末尾给汇总。退出码恒为 0(这是审计工具,不是门禁)。
 */
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative, basename, extname } from 'node:path'

const root = 'panel/src'
const files = []
;(function walk(dir) {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name)
    if (e.isDirectory()) walk(p)
    else if (/\.(vue|tsx?|js)$/.test(e.name)) files.push(p)
  }
})(root)

// 入口与路由不算死代码,以及 vite 的 config、类型声明
const ALWAYS_USED = /(^|\/)(main\.ts|App\.vue|router\/|env\.d\.ts$)/

const contents = new Map()
for (const f of files) contents.set(f, readFileSync(f, 'utf8'))

const dead = []
for (const f of files) {
  if (ALWAYS_USED.test(f)) continue
  const name = basename(f, extname(f))
  // 该名字是否在别的文件里出现过(import 路径、动态组件、字符串引用)
  let used = false
  for (const [g, text] of contents) {
    if (g === f) continue
    if (text.includes(name)) {
      used = true
      break
    }
  }
  if (!used) dead.push(f)
}

console.log('=== 疑似未被引用 ===')
for (const d of dead) console.log(' ', relative('.', d))
console.log(`\n共 ${files.length} 个源文件,疑似未引用 ${dead.length} 个`)
