package probe

// DNS 上游测试(M2 dns/upstream-test)。
//
// 做法(自己的设计,依据 sing-box 官方文档的 direct 入站、sniff / hijack-dns 路由动作、DNS server 的 detour):
// 起一个临时 sing-box,只开一个仅监听 127.0.0.1 的 direct 入站,路由把进来的 DNS 查询劫持给内核自己的 DNS 模块,
// 内核的 DNS 里只有「被测的那一台上游」(可带 detour,走指定出站)。本进程向这个入站发一个 A 查询并计时。
// 这样测的是「内核按所选协议、经所选线路真的去问这台服务器」,而不是本进程自己去连它。
// 临时进程不碰系统(没有 tun、不改 DNS / 防火墙),结束即杀,配置文件 0600 用完删除。

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	dnsTestTag   = "ob-dns-test"
	dnsInTag     = "ob-dns-in"
	dnsDirectTag = "ob-dns-direct"

	// DefaultDNSTimeout 是一次 DNS 测试等答复的上限(不含 sing-box 的启动)。
	DefaultDNSTimeout = 8 * time.Second
	maxDNSName        = 253
)

// DNSJob 是一次「内核按指定上游真查一次」的任务。
type DNSJob struct {
	// Server 是被测上游,sing-box 1.12+ 的 DNS server 对象(type / server / server_port)。
	// tag 和 detour 由本包设置,传进来的会被覆盖。
	Server map[string]any
	// Detour 非空时,被测上游经这个出站去连;它和它依赖的东西必须在 Material 里。
	Detour   string
	Material *Material
	// Name 是要查的域名(A 记录)。
	Name    string
	Timeout time.Duration
}

// DNSResult 是一次 DNS 测试的结果。Reason:invalid(配置 / 参数不合法)、timeout、failed。
type DNSResult struct {
	OK     bool
	Ms     int
	Error  string
	Reason string
}

// buildDNSConfig 生成临时配置。
func buildDNSConfig(j DNSJob, port int) ([]byte, error) {
	server := map[string]any{}
	for k, v := range j.Server {
		server[k] = v
	}
	server["tag"] = dnsTestTag
	delete(server, "detour")
	if j.Detour != "" {
		server["detour"] = j.Detour
	}
	servers := []any{server, map[string]any{"type": "local", "tag": dnsTag}}
	outbounds := []any{map[string]any{"type": "direct", "tag": dnsDirectTag}}
	var endpoints []any
	if j.Material != nil {
		servers = append(servers, j.Material.DNS...)
		outbounds = append(outbounds, j.Material.Outbounds...)
		endpoints = j.Material.Endpoints
	}
	cfg := map[string]any{
		"log": map[string]any{"level": "error"},
		"dns": map[string]any{"servers": servers, "final": dnsTestTag},
		"inbounds": []any{map[string]any{
			"type": "direct", "tag": dnsInTag, "listen": "127.0.0.1", "listen_port": port,
		}},
		"outbounds": outbounds,
		"route": map[string]any{
			"rules": []any{
				map[string]any{"inbound": []any{dnsInTag}, "action": "sniff"},
				map[string]any{"protocol": "dns", "action": "hijack-dns"},
			},
			"final":                   dnsDirectTag,
			"default_domain_resolver": dnsTag,
		},
	}
	if len(endpoints) > 0 {
		cfg["endpoints"] = endpoints
	}
	return json.Marshal(cfg)
}

// validDNSName 只收普通的主机名:总长 253、每段 1 到 63、字母数字和连字符(不收 IP、不收下划线之类)。
func validDNSName(s string) bool {
	if s == "" || len(s) > maxDNSName || net.ParseIP(s) != nil {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// buildDNSQuery 做一个 A 记录的查询报文(递归位置 1)。
func buildDNSQuery(id uint16, name string) []byte {
	b := make([]byte, 12, 18+len(name))
	binary.BigEndian.PutUint16(b[0:], id)
	binary.BigEndian.PutUint16(b[2:], 0x0100)
	binary.BigEndian.PutUint16(b[4:], 1) // QDCOUNT
	for _, label := range strings.Split(name, ".") {
		b = append(b, byte(len(label)))
		b = append(b, label...)
	}
	b = append(b, 0, 0, 1, 0, 1) // 根、QTYPE=A、QCLASS=IN
	return b
}

// parseDNSReply 取回复的返回码和答案条数;id 对不上、不是回复、太短都报错(调用方继续等)。
func parseDNSReply(id uint16, b []byte) (rcode, answers int, err error) {
	if len(b) < 12 {
		return 0, 0, errors.New("回复太短")
	}
	if binary.BigEndian.Uint16(b[0:]) != id {
		return 0, 0, errors.New("回复的 ID 对不上")
	}
	flags := binary.BigEndian.Uint16(b[2:])
	if flags&0x8000 == 0 {
		return 0, 0, errors.New("不是回复")
	}
	return int(flags & 0x000F), int(binary.BigEndian.Uint16(b[6:])), nil
}

var rcodeNames = map[int]string{1: "FORMERR(格式错误)", 2: "SERVFAIL(服务器出错)", 3: "NXDOMAIN(名字不存在)", 4: "NOTIMP(不支持)", 5: "REFUSED(拒绝回答)"}

func rcodeText(rc int) string {
	if s, ok := rcodeNames[rc]; ok {
		return s
	}
	return fmt.Sprintf("返回码 %d", rc)
}

// QueryDNS 起临时 sing-box,让内核按 j 的上游和线路查 j.Name 一次。
// 找不到 sing-box 返回 ErrNoSingBox;其余结果(超时、被拒绝、配置不合法)都在 DNSResult 里。
func (r *Runner) QueryDNS(ctx context.Context, j DNSJob) (DNSResult, error) {
	if !validDNSName(j.Name) {
		return DNSResult{Error: "测试用的域名不合法", Reason: "invalid"}, nil
	}
	if len(j.Server) == 0 {
		return DNSResult{Error: "没有要测的上游", Reason: "invalid"}, nil
	}
	timeout := j.Timeout
	if timeout <= 0 {
		timeout = DefaultDNSTimeout
	}
	if timeout > MaxTimeout {
		timeout = MaxTimeout
	}
	bin, err := exec.LookPath(r.Bin)
	if err != nil {
		return DNSResult{}, ErrNoSingBox
	}
	port, err := freePort()
	if err != nil {
		return DNSResult{}, fmt.Errorf("找不到空闲端口: %w", err)
	}
	cfg, err := buildDNSConfig(j, port)
	if err != nil {
		return DNSResult{}, err
	}
	f, err := os.CreateTemp(r.Dir, "dnstest-*.json")
	if err != nil {
		return DNSResult{}, fmt.Errorf("创建临时配置失败: %w", err)
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return DNSResult{}, err
	}
	if _, err := f.Write(cfg); err != nil {
		f.Close()
		return DNSResult{}, err
	}
	if err := f.Close(); err != nil {
		return DNSResult{}, err
	}

	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(pctx, bin, "run", "-c", name)
	errOut := &tailBuffer{max: maxStderr}
	cmd.Stdout, cmd.Stderr = errOut, errOut
	if err := cmd.Start(); err != nil {
		return DNSResult{}, fmt.Errorf("启动 sing-box 失败: %w", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	defer func() { cancel(); <-exited }()

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	if err := waitListening(ctx, addr, exited, startupWait); err != nil {
		if ctx.Err() != nil {
			return DNSResult{}, ctx.Err()
		}
		msg := firstLine(strings.ReplaceAll(errOut.String(), name, "config.json"))
		if msg == "" {
			msg = err.Error()
		}
		return DNSResult{Error: msg, Reason: "invalid"}, nil
	}

	res := exchangeDNS(ctx, addr, j.Name, timeout)
	if !res.OK && res.Reason != "invalid" {
		// 内核自己记的错误(比如「连不上上游」)比「超时」更有用,有就附在后面
		if why := kernelReason(errOut.String()); why != "" {
			res.Error += "(内核报:" + why + ")"
		}
	}
	return res, ctx.Err()
}

// exchangeDNS 向临时入站发一个 A 查询,等到对得上 ID 的回复为止。
func exchangeDNS(ctx context.Context, addr, name string, timeout time.Duration) DNSResult {
	conn, err := (&net.Dialer{}).DialContext(ctx, "udp", addr)
	if err != nil {
		return DNSResult{Error: "连不上临时的 DNS 入口: " + err.Error(), Reason: "failed"}
	}
	defer conn.Close()
	var idb [2]byte
	_, _ = rand.Read(idb[:])
	id := binary.BigEndian.Uint16(idb[:])
	deadline := time.Now().Add(timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	start := time.Now()
	if _, err := conn.Write(buildDNSQuery(id, name)); err != nil {
		return DNSResult{Error: "发送查询失败: " + err.Error(), Reason: "failed"}
	}
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			if isTimeout(err) || ctx.Err() != nil {
				return DNSResult{Error: fmt.Sprintf("超时:%d 秒内没有答复", int(timeout.Seconds())), Reason: "timeout"}
			}
			return DNSResult{Error: "读取答复失败: " + err.Error(), Reason: "failed"}
		}
		rcode, answers, perr := parseDNSReply(id, buf[:n])
		if perr != nil {
			continue // 别的报文,接着等
		}
		ms := msOf(time.Since(start))
		switch {
		case rcode != 0:
			return DNSResult{Ms: ms, Error: "上游回了 " + rcodeText(rcode), Reason: "failed"}
		case answers == 0:
			return DNSResult{Ms: ms, Error: "上游答了,但没有这个名字的记录(空答案)", Reason: "failed"}
		}
		return DNSResult{OK: true, Ms: ms}
	}
}

// kernelReason 从内核输出里取最后一条 ERROR / WARN 的正文(去掉等级、时间前缀,最多 160 个字符)。
func kernelReason(out string) string {
	lines := strings.Split(ansi.ReplaceAllString(out, ""), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if !(strings.HasPrefix(l, "ERROR") || strings.HasPrefix(l, "WARN")) {
			continue
		}
		if k := strings.Index(l, "] "); k >= 0 {
			l = l[k+2:]
		}
		if r := []rune(l); len(r) > 160 {
			l = string(r[:160])
		}
		return l
	}
	return ""
}
