// 临时:把上游 panel/dist 挂成基准页,并把 /api 与 /api/controller-ws 反代到真机后端,
// 这样上游 dist 也能登录、也能拿到真实数据(和 main 的 dev 服务同一套后端)。
const http = require('http')
const fs = require('fs')
const path = require('path')

const root = process.argv[2]
const port = Number(process.argv[3] || 8903)
const target = process.argv[4] || 'http://192.168.2.5:3036'
const t = new URL(target)

const types = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'application/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.ico': 'image/x-icon',
  '.json': 'application/json; charset=utf-8',
  '.gz': 'application/gzip',
  '.woff2': 'font/woff2',
  '.webp': 'image/webp',
}

function proxy(req, res) {
  const opts = {
    host: t.hostname,
    port: t.port || 80,
    method: req.method,
    path: req.url,
    headers: { ...req.headers, host: t.host },
  }
  const up = http.request(opts, (ur) => {
    // 上游或客户端中途断开会抛 ECONNRESET 之类的错误;不接住会让整个进程崩掉,
    // 后面的比对组全都连不上(2026-11-27 实际踩到:12 组里 11 组 -1)。
    ur.on('error', () => res.destroy())
    res.on('error', () => ur.destroy())
    res.writeHead(ur.statusCode || 502, ur.headers)
    ur.pipe(res)
  })
  up.on('error', () => {
    if (!res.headersSent) {
      res.writeHead(502)
      res.end('bad gateway')
    } else {
      res.destroy()
    }
  })
  req.on('error', () => up.destroy())
  req.pipe(up)
}

// WebSocket 升级也要转发(流量 / 日志走 /api/controller-ws)
function proxyWs(req, socket, head) {
  const opts = {
    host: t.hostname,
    port: t.port || 80,
    method: req.method,
    path: req.url,
    headers: { ...req.headers, host: t.host },
  }
  const up = http.request(opts)
  up.on('upgrade', (ur, upsock, uphead) => {
    socket.write(
      'HTTP/1.1 101 Switching Protocols\r\n' +
        Object.entries(ur.headers)
          .map(([k, v]) => `${k}: ${v}`)
          .join('\r\n') +
        '\r\n\r\n',
    )
    if (uphead && uphead.length) socket.unshift(uphead)
    upsock.pipe(socket)
    socket.pipe(upsock)
  })
  up.on('error', () => socket.destroy())
  socket.on('error', () => up.destroy())
  up.end()
}

// 兜底:代理链路上任何漏网的 socket 错误都不该让基准服务退出
process.on('uncaughtException', (e) => {
  if (e && (e.code === 'ECONNRESET' || e.code === 'EPIPE')) return
  console.error('[serve-base] uncaught:', e && e.message)
})

const server = http.createServer((req, res) => {
  if (req.url.startsWith('/api')) return proxy(req, res)
  let p = decodeURIComponent(req.url.split('?')[0])
  if (p === '/') p = '/index.html'
  const file = path.join(root, p)
  fs.readFile(file, (err, buf) => {
    if (err) {
      // 上游 dist 里很多资源只有预压缩的 .gz(HTML 引用的是不带 .gz 的名字),
      // 先回落同名 .gz 并解压返回,否则整页白屏。
      const gz = file + '.gz'
      return fs.readFile(gz, (eg, gbuf) => {
        if (!eg) {
          const zlib = require('zlib')
          return zlib.gunzip(gbuf, (e3, plain) => {
            if (e3) {
              res.writeHead(500)
              res.end('gunzip failed')
              return
            }
            res.writeHead(200, { 'Content-Type': types[path.extname(file)] || 'application/octet-stream' })
            res.end(plain)
          })
        }
        fs.readFile(path.join(root, 'index.html'), (e2, html) => {
          if (e2) {
            res.writeHead(404)
            res.end('not found')
            return
          }
          res.writeHead(200, { 'Content-Type': types['.html'] })
          res.end(html)
        })
      })
    }
    res.writeHead(200, { 'Content-Type': types[path.extname(file)] || 'application/octet-stream' })
    res.end(buf)
  })
})
server.on('upgrade', proxyWs)
server.listen(port, '127.0.0.1', () => console.log('base on', port, '-> api', target))
