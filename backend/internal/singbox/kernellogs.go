package singbox

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// LogEntry 是内核日志流里的一行:Level 是 debug / info / warn / error,Payload 是内核格式化好的正文
// (例如 "[2751904522 0ms] dns: exchange x.a.test. IN A",开头方括号里是这条连接 / 查询的编号和已经过的时间)。
type LogEntry struct {
	Level   string
	Payload string
}

const (
	logTapMaxEntries = 20000   // 收满就丢后面的(一次测试只开一两秒,正常到不了)
	logMaxFrame      = 1 << 20 // 单帧上限
	wsGUID           = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
)

// LogTap 是一次开着的内核日志订阅。Stop 之前所有收到的行都留着,Stop 关掉订阅并把它们交出来。
type LogTap struct {
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	entries []LogEntry
}

// OpenKernelLogs 订阅正在运行的 sing-box 的日志流(只读,Clash API 的 GET /logs?level=debug,走 WebSocket)。
// 控制地址和 secret 取自正式配置。内核的日志流只给订阅期间产生的行,所以必须先订阅、再发探测;
// WebSocket 握手完成(101)就说明内核那边已经订上了,返回时不会再漏掉之后的行。
// 只要开着控制接口,内核就会把 debug 行交给订阅者,不受配置里 log.level 的限制;订阅只在测试期间开,用完立刻 Stop。
// 内核没在跑、没开控制接口、鉴权失败、握手失败都返回 error。
func OpenKernelLogs(ctx context.Context, configFile string) (*LogTap, error) {
	addr, secret, err := ControllerFromFile(configFile)
	if err != nil {
		return nil, err
	}
	return openLogTap(ctx, addr, secret, "debug")
}

func openLogTap(ctx context.Context, addr, secret, level string) (*LogTap, error) {
	keyRaw := make([]byte, 16)
	if _, err := rand.Read(keyRaw); err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyRaw)
	cctx, cancel := context.WithCancel(ctx)
	u := url.URL{Scheme: "http", Host: addr, Path: "/logs", RawQuery: url.Values{"level": {level}}.Encode()}
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, u.String(), nil)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", key)
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 5 * time.Second}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		cancel()
		tr.CloseIdleConnections()
		return nil, errors.New("控制接口连不上(内核没在运行?)")
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = resp.Body.Close()
		cancel()
		if resp.StatusCode == http.StatusNoContent {
			return nil, errors.New("内核不提供日志订阅")
		}
		return nil, fmt.Errorf("控制接口的日志订阅返回 %d", resp.StatusCode)
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	if resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		_ = resp.Body.Close()
		cancel()
		return nil, errors.New("日志订阅的握手应答不对")
	}
	rwc, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		_ = resp.Body.Close()
		cancel()
		return nil, errors.New("日志订阅没能切换到 WebSocket")
	}
	t := &LogTap{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(t.done)
		defer rwc.Close()
		go func() { <-cctx.Done(); _ = rwc.Close() }() // 取消时把阻塞的读放掉
		t.read(rwc)
	}()
	return t, nil
}

// read 一帧一帧读服务端发来的文本帧(服务端发的帧不加掩码),每帧是 {"type":"debug","payload":"..."}。
func (t *LogTap) read(r io.Reader) {
	br := bufio.NewReader(r)
	var msg []byte
	for {
		var h [2]byte
		if _, err := io.ReadFull(br, h[:]); err != nil {
			return
		}
		fin, op := h[0]&0x80 != 0, h[0]&0x0f
		n := uint64(h[1] & 0x7f)
		switch n {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(br, b[:]); err != nil {
				return
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(br, b[:]); err != nil {
				return
			}
			n = binary.BigEndian.Uint64(b[:])
		}
		if n > logMaxFrame {
			return
		}
		if h[1]&0x80 != 0 { // 服务端不该加掩码,有就跳过掩码键
			if _, err := io.CopyN(io.Discard, br, 4); err != nil {
				return
			}
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(br, payload); err != nil {
			return
		}
		switch op {
		case 0x8: // 关闭
			return
		case 0x9, 0xA: // ping / pong:不用管
			continue
		case 0x1, 0x0: // 文本 / 续帧
			msg = append(msg, payload...)
			if len(msg) > logMaxFrame {
				return
			}
			if fin {
				t.add(msg)
				msg = nil
			}
		}
	}
}

func (t *LogTap) add(b []byte) {
	var l struct {
		Type    string `json:"type"`
		Payload string `json:"payload"`
	}
	if json.Unmarshal(b, &l) != nil {
		return
	}
	t.mu.Lock()
	if len(t.entries) < logTapMaxEntries {
		t.entries = append(t.entries, LogEntry{Level: l.Type, Payload: l.Payload})
	}
	t.mu.Unlock()
}

// Stop 关掉订阅,等读循环退出,返回订阅期间收到的全部行。可以重复调用。
func (t *LogTap) Stop() []LogEntry {
	t.cancel()
	<-t.done
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]LogEntry(nil), t.entries...)
}
