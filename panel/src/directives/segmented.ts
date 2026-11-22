import type { Directive, DirectiveBinding } from 'vue'

// v-segmented:分段选择器 / 标签条 / 菜单 / 卡片组 / 底部 dock 的「滑动高亮」。
// 对齐上游 v0.1.477 的同名全局指令(上游只发布编译后的 panel/dist,这里按其行为还原)。
//   v-segmented                          标签条(默认 variant = tabs,选中项是 :scope > .tab-active)
//   v-segmented="{ variant: 'menu' }"    菜单(ul > li)
//   v-segmented="{ variant: 'card' }"    卡片组
//   v-segmented="{ variant: 'dock' }"    底部 dock(用 ::before 做高亮,不复制子元素)
//   active:  选中项的选择器,默认 ':scope > .tab-active'
//   content: 切换时是否让后面的内容同步轻推(menu / dock 默认关,其余默认开)
//   disabled: 为真时不启用
export interface SegmentedOptions {
  variant?: string
  active?: string
  content?: boolean
  disabled?: boolean
}

interface SegState {
  active: HTMLElement | null
  geometry: string
  selector: string
  variant: string
  ro: ResizeObserver
  mo: MutationObserver
  observed: WeakSet<Element>
  onClick: (e: Event) => void
  content: boolean
  highlight: HTMLElement | null
  copy: HTMLElement | null
  copies: Map<HTMLElement, HTMLElement>
  motionUntil: number
}
type SegEl = HTMLElement & { __seg?: SegState; __tabAnim?: Animation }

const DEFAULT_ACTIVE = ':scope > .tab-active'
const TAB_SHIFT = 12
const NUDGE_STOP = '.card, .card-body, .modal-box, .tippy-content, .collapse-content, #app-content'

// 最近一次点击造成的切换方向,供 v-tab-content 做内容的横向轻推
const motion = { dir: 0, at: 0, motion: false }

export const setTabMotion = (dir: number) => {
  motion.dir = Math.sign(dir)
  motion.at = Date.now()
  motion.motion = true
}

const reduced = () => matchMedia('(prefers-reduced-motion: reduce)').matches
const slideDuration = () => (reduced() ? 150 : 250)

const childOfRoot = (el: Element | null, root: Element): HTMLElement | null => {
  let cur: Element | null = el
  while (cur && cur.parentElement !== root) cur = cur.parentElement
  return cur instanceof HTMLElement ? cur : null
}

const itemsOf = (root: SegEl) =>
  Array.from(root.children).filter(
    (c): c is HTMLElement => c instanceof HTMLElement && c !== root.__seg?.highlight,
  )

const findActive = (root: SegEl, st: SegState) =>
  (Array.from(root.querySelectorAll<HTMLElement>(st.selector)).find(
    (el) => !st.highlight?.contains(el),
  ) ?? null)

const offsetIn = (el: HTMLElement, root: HTMLElement) => {
  let x = 0
  let y = 0
  let cur: HTMLElement | null = el
  while (cur && cur !== root) {
    x += cur.offsetLeft
    y += cur.offsetTop
    cur = cur.offsetParent as HTMLElement | null
  }
  if (cur === root) return { x, y }
  const a = el.getBoundingClientRect()
  const b = root.getBoundingClientRect()
  return {
    x: a.left - b.left + root.scrollLeft - root.clientLeft,
    y: a.top - b.top + root.scrollTop - root.clientTop,
  }
}

let copyId = 0
// 复制一份子元素放进高亮层(高亮层里的文字用选中色),id 与引用要改名避免重复
const cloneForCopy = (el: HTMLElement) => {
  const clone = el.cloneNode(true) as HTMLElement
  const all = [clone, ...Array.from(clone.querySelectorAll<HTMLElement>('*'))]
  const ids = new Map<string, string>()
  for (const n of all) if (n.id) ids.set(n.id, `seg-copy-${++copyId}`)
  for (const n of all) {
    if (n.id) n.id = ids.get(n.id) as string
    n.classList.remove('seg-on')
    for (const attr of Array.from(n.attributes)) {
      let v = attr.value.replace(/url\((['"]?)#([^)'"\s]+)\1\)/g, (m, q, id) =>
        ids.has(id) ? `url(${q}#${ids.get(id)}${q})` : m,
      )
      if (['href', 'xlink:href'].includes(attr.name) && v.startsWith('#') && ids.has(v.slice(1))) {
        v = `#${ids.get(v.slice(1))}`
      }
      if (v !== attr.value) n.setAttribute(attr.name, v)
    }
    if (n.matches('a, button, input, select, textarea, [tabindex]')) n.setAttribute('tabindex', '-1')
  }
  return clone
}

const syncCopies = (root: SegEl, dirty: Set<HTMLElement> = new Set()) => {
  const st = root.__seg
  if (!st?.highlight || !st.copy) return
  const items = itemsOf(root)
  const alive = new Set(items)
  for (const [item, copy] of st.copies) {
    if (alive.has(item)) continue
    copy.remove()
    st.ro.unobserve(item)
    st.observed.delete(item)
    st.copies.delete(item)
  }
  const cw = root.clientWidth
  const ch = root.clientHeight
  let w = cw
  let h = ch
  const cs = getComputedStyle(root)
  const padR = parseFloat(cs.paddingRight) || 0
  const padB = parseFloat(cs.paddingBottom) || 0
  const boxes = items.map((item) => {
    const { x, y } = offsetIn(item, root)
    const iw = item.offsetWidth
    const ih = item.offsetHeight
    w = Math.max(w, x + iw + padR)
    h = Math.max(h, y + ih + padB)
    return { item, left: `${x}px`, top: `${y}px`, width: `${iw}px`, height: `${ih}px` }
  })
  const frag = document.createDocumentFragment()
  for (const { item, ...style } of boxes) {
    let copy = st.copies.get(item)
    if (!copy || dirty.has(item)) {
      const fresh = cloneForCopy(item)
      if (copy) copy.replaceWith(fresh)
      else frag.append(fresh)
      copy = fresh
      st.copies.set(item, copy)
    }
    if (Object.entries(style).some(([k, v]) => copy!.style.getPropertyValue(k) !== v)) {
      Object.assign(copy.style, { position: 'absolute', ...style, margin: '0' })
    }
  }
  st.copy.append(frag)
  if (st.highlight.style.width !== `${w}px`) st.highlight.style.width = `${w}px`
  if (st.highlight.style.height !== `${h}px`) st.highlight.style.height = `${h}px`
  st.highlight.style.maxWidth = w <= cw + 1 ? '100%' : 'none'
  st.highlight.style.maxHeight = h <= ch + 1 ? '100%' : 'none'
}

// 切换时,把紧跟在条下面的内容也轻推一下(v-tab-content 的同一动画)
const animateTab = (el: SegEl) => {
  const recent = Date.now() - motion.at < 1500
  el.__tabAnim?.cancel()
  if (!recent || !motion.motion) return
  const r = reduced()
  const from = -motion.dir * (r ? 4 : TAB_SHIFT)
  el.__tabAnim = el.animate([{ transform: `translateX(${from}px)` }, { transform: 'none' }], {
    duration: r ? 150 : 220,
    easing: 'cubic-bezier(0.23, 1, 0.32, 1)',
  })
}

const nudgeFollowing = (root: HTMLElement) => {
  const bottom = root.getBoundingClientRect().bottom
  let cur: HTMLElement | null = root
  for (let depth = 0; depth < 4 && cur; depth++) {
    if (depth > 0 && cur.matches(NUDGE_STOP)) return
    const following: HTMLElement[] = []
    for (let s = cur.nextElementSibling; s; s = s.nextElementSibling) {
      if (!(s instanceof HTMLElement) || !s.getClientRects().length) continue
      if (s.getBoundingClientRect().top >= bottom - 1) following.push(s)
    }
    if (following.length) {
      following.forEach((f) => animateTab(f as SegEl))
      return
    }
    cur = cur.parentElement
  }
}

const layout = (root: SegEl, animate: boolean) => {
  const st = root.__seg
  if (!st) return
  const active = findActive(root, st)
  if (!active || !active.offsetWidth) {
    if (root.classList.contains('seg-dock')) root.style.setProperty('--seg-o', '0')
    if (st.highlight) st.highlight.style.visibility = 'hidden'
    if (st.active?.classList.contains('seg-on')) st.active.classList.remove('seg-on')
    st.active = active
    st.geometry = ''
    return
  }
  const { x, y } = offsetIn(active, root)
  const key = () =>
    `${x},${y},${active.offsetWidth},${active.offsetHeight},${root.clientWidth},${root.clientHeight},${st.highlight?.style.width},${st.highlight?.style.height}`
  if (key() === st.geometry && active === st.active) return
  const moved = animate && st.active !== null && st.active !== active
  if (moved) st.motionUntil = performance.now() + slideDuration()
  const animating = moved || (st.active === active && performance.now() < st.motionUntil)
  if (root.classList.contains('seg-animate') !== animating) root.classList.toggle('seg-animate', animating)
  if (moved && st.content) nudgeFollowing(root)
  if (st.active !== active && st.active?.classList.contains('seg-on')) st.active.classList.remove('seg-on')
  if (!active.classList.contains('seg-on')) active.classList.add('seg-on')
  syncCopies(root)
  if (st.highlight) {
    const w = parseFloat(st.highlight.style.width)
    const h = parseFloat(st.highlight.style.height)
    st.highlight.style.visibility = 'visible'
    st.highlight.style.clipPath = `inset(${y}px ${w - x - active.offsetWidth}px ${h - y - active.offsetHeight}px ${x}px round var(--seg-r))`
  }
  if (root.classList.contains('seg-dock')) {
    root.style.setProperty('--seg-x', `${x}px`)
    root.style.setProperty('--seg-y', `${y}px`)
    root.style.setProperty('--seg-w', `${active.offsetWidth}px`)
    root.style.setProperty('--seg-h', `${active.offsetHeight}px`)
    root.style.setProperty('--seg-o', '1')
  }
  st.active = active
  st.geometry = key()
}

const observeItems = (root: SegEl, st: SegState) => {
  for (const item of itemsOf(root)) {
    if (st.observed.has(item)) continue
    st.observed.add(item)
    st.ro.observe(item)
  }
}

const setup = (root: SegEl, binding: DirectiveBinding<SegmentedOptions | undefined>) => {
  const variant = binding.value?.variant || 'tabs'
  root.classList.add('seg', `seg-${variant}`)
  let highlight: HTMLElement | null = null
  let copy: HTMLElement | null = null
  if (variant !== 'dock' && CSS.supports('clip-path', 'inset(0)')) {
    highlight = document.createElement(root.matches('ul, ol') ? 'li' : 'div')
    highlight.className = 'seg-highlight'
    highlight.setAttribute('aria-hidden', 'true')
    highlight.setAttribute('inert', '')
    copy = root.cloneNode(false) as HTMLElement
    copy.removeAttribute('id')
    copy.removeAttribute('style')
    copy.classList.remove('seg', `seg-${variant}`, 'seg-animate')
    copy.classList.add('seg-copy', `seg-copy-${variant}`)
    highlight.append(copy)
    root.append(highlight)
    root.classList.add('seg-clipped')
  }
  const ro = new ResizeObserver(() => layout(root, false))
  const mo = new MutationObserver((records) => {
    const st = root.__seg
    if (!st) return
    const real = records.filter(
      (r) =>
        !st.highlight?.contains(r.target) &&
        (r.target !== root ||
          (r.type === 'childList' &&
            [...r.addedNodes, ...r.removedNodes].some((n) => n !== st.highlight))),
    )
    if (!real.length) return
    if (records.some((r) => r.type === 'childList')) observeItems(root, st)
    const dirty = new Set<HTMLElement>()
    for (const r of real) {
      const el = childOfRoot(r.target instanceof Element ? r.target : r.target.parentElement, root)
      if (el) dirty.add(el)
    }
    syncCopies(root, dirty)
    layout(root, true)
  })
  const onClick = (e: Event) => {
    const st = root.__seg
    const target = childOfRoot(e.target as Element, root)
    const current = st?.active ? childOfRoot(st.active, root) : null
    if (!target || !current || target === current) return
    const a = offsetIn(current, root)
    const b = offsetIn(target, root)
    const byMouse = e instanceof MouseEvent && e.detail > 0
    motion.dir = Math.sign(b.x - a.x) || Math.sign(b.y - a.y)
    motion.at = Date.now()
    motion.motion = byMouse
    root.classList.toggle('seg-instant', !byMouse)
  }
  const st: SegState = {
    active: null,
    geometry: '',
    selector: binding.value?.active || DEFAULT_ACTIVE,
    variant,
    ro,
    mo,
    observed: new WeakSet(),
    onClick,
    content: binding.value?.content ?? !['menu', 'dock'].includes(variant),
    highlight,
    copy,
    copies: new Map(),
    motionUntil: 0,
  }
  root.__seg = st
  root.addEventListener('click', onClick, true)
  ro.observe(root)
  observeItems(root, st)
  mo.observe(root, {
    subtree: true,
    childList: true,
    characterData: true,
    attributes: true,
    attributeFilter: ['class', 'aria-selected', 'src', 'style'],
  })
  layout(root, false)
}

const teardown = (root: SegEl) => {
  const st = root.__seg
  if (!st) return
  st.ro.disconnect()
  st.mo.disconnect()
  root.removeEventListener('click', st.onClick, true)
  st.highlight?.remove()
  st.active?.classList.remove('seg-on')
  root.classList.remove('seg', `seg-${st.variant}`, 'seg-clipped', 'seg-instant', 'seg-animate')
  delete root.__seg
}

export const vSegmented: Directive<SegEl, SegmentedOptions | undefined> = {
  mounted(el, binding) {
    if (!binding.value?.disabled) setup(el, binding)
  },
  updated(el, binding) {
    if (binding.value?.disabled) {
      teardown(el)
      return
    }
    if (!el.__seg) {
      setup(el, binding)
      return
    }
    observeItems(el, el.__seg)
    layout(el, true)
  },
  unmounted: teardown,
}

// v-tab-content="key":key 变化时(且刚点过分段选择器),内容横向轻推一下
export const vTabContent: Directive<SegEl, unknown> = {
  updated(el, binding) {
    if (binding.value !== binding.oldValue) animateTab(el)
  },
}
