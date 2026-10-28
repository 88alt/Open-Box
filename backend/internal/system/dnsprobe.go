package system

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"strings"
	"time"
)

// ProbeName 是探测用的域名:.invalid 保证不是真实域名,任何递归服务器都会很快答 NXDOMAIN;
// 探测只看「有没有人应答」,不看答案是什么。
const ProbeName = "openbox-probe.invalid"

// ProbeDNS 向 addr(host:port)发一条 UDP 的 A 查询,收到同一个事务号的应答(任何 RCODE)就返回 nil,
// 说明那个地址上有一个会答 DNS 的服务;超时、拒绝连接、应答对不上都返回 error。
// 只读探测:不改任何设置。
func ProbeDNS(ctx context.Context, addr string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	q, id, err := buildProbeQuery(ProbeName)
	if err != nil {
		return err
	}
	if _, err := conn.Write(q); err != nil {
		return err
	}
	buf := make([]byte, 1500)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return errors.New("没有应答")
		}
		// 头部 12 字节:事务号一致且 QR=1 才算应答;对不上的丢掉继续等
		if n >= 12 && buf[0] == id[0] && buf[1] == id[1] && buf[2]&0x80 != 0 {
			return nil
		}
	}
}

func buildProbeQuery(name string) (msg []byte, id [2]byte, err error) {
	if _, err = rand.Read(id[:]); err != nil {
		return nil, id, err
	}
	msg = []byte{id[0], id[1], 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0} // 标准查询、期望递归、1 个问题
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 {
			return nil, id, errors.New("探测域名不合法")
		}
		msg = append(msg, byte(len(label)))
		msg = append(msg, label...)
	}
	msg = append(msg, 0, 0, 1, 0, 1) // 结尾 + 类型 A + 类 IN
	return msg, id, nil
}
