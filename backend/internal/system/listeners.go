package system

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Listeners 是此刻本机上正被占用的端口(只读 /proc/net,不绑定、不连接)。
type Listeners struct {
	TCP map[int]bool // 处于 LISTEN 的 TCP 端口(IPv4 + IPv6)
	UDP map[int]bool // 已绑定、没有连到对端的 UDP 端口(IPv4 + IPv6)
}

// ReadListeners 读 root(通常是 /proc/net)下的 tcp / tcp6 / udp / udp6。
//   - TCP:状态是 0A(LISTEN)的行;
//   - UDP:状态是 07 的行(未连接的 UDP 套接字,即「绑定着、在等包」);已连接的客户端套接字(状态 01)不算占用。
//
// tcp6 / udp6 不存在(系统关了 IPv6)不是错误;tcp 或 udp 读不了,就无法确认端口占用情况,返回错误(不猜)。
func ReadListeners(root string) (*Listeners, error) {
	l := &Listeners{TCP: map[int]bool{}, UDP: map[int]bool{}}
	for _, f := range []struct {
		name     string
		state    string
		into     map[int]bool
		required bool
	}{
		{"tcp", "0A", l.TCP, true},
		{"tcp6", "0A", l.TCP, false},
		{"udp", "07", l.UDP, true},
		{"udp6", "07", l.UDP, false},
	} {
		err := scanNetTable(filepath.Join(root, f.name), f.state, f.into)
		if err != nil && (f.required || !errors.Is(err, fs.ErrNotExist)) {
			return nil, fmt.Errorf("读取 %s 失败: %w", f.name, err)
		}
	}
	return l, nil
}

func scanNetTable(path, wantState string, into map[int]bool) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	first := true
	for sc.Scan() {
		if first { // 表头
			first = false
			continue
		}
		f := strings.Fields(sc.Text())
		if len(f) < 4 || !strings.EqualFold(f[3], wantState) {
			continue
		}
		i := strings.LastIndexByte(f[1], ':')
		if i < 0 {
			continue
		}
		p, err := strconv.ParseUint(f[1][i+1:], 16, 16)
		if err != nil || p == 0 {
			continue
		}
		into[int(p)] = true
	}
	return sc.Err()
}
