package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/core"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

// smokeOutput 是冒烟检查里每个命令留的输出上限(报错时够看原因)。
const smokeOutput = 4 << 10

// SmokeTest 用暂存目录里的新二进制做冒烟检查(M6 第七节第 5 步):
//
//	openbox-backend -version     起得来、版本串打得出来
//	openbox-core hello           协议对得上(implementation / protocol)
//	sing-box version             内核起得来
//	sing-box check -c <当前配置>  新内核认现有的配置(配置不在就跳过)
//
// 这一步跑完系统还一个字节没动:全部用 <root>/bin 下的新文件,失败就把暂存目录删掉。
func SmokeTest(ctx context.Context, root, configFile string) error {
	bin := func(name string) string { return filepath.Join(root, "bin", name) }

	out, err := runOutput(ctx, bin("openbox-backend"), "-version")
	if err != nil {
		return fmt.Errorf("新的 openbox-backend 起不来:%w", err)
	}
	if !strings.Contains(out, "openbox-backend") {
		return fmt.Errorf("新的 openbox-backend -version 输出不对:%q", firstLine(out))
	}

	info, err := core.New(bin("openbox-core")).Hello(ctx)
	if err != nil {
		return fmt.Errorf("新的 openbox-core 握手失败:%w", err)
	}
	if info.Protocol != core.ProtocolVersion {
		return fmt.Errorf("新的 openbox-core 协议版本是 %d,后端要 %d", info.Protocol, core.ProtocolVersion)
	}

	if out, err := runOutput(ctx, bin("sing-box"), "version"); err != nil {
		return fmt.Errorf("新的 sing-box 起不来:%w", err)
	} else if strings.TrimSpace(out) == "" {
		return errors.New("新的 sing-box version 什么都没输出")
	}

	if configFile != "" {
		cfg, err := os.ReadFile(configFile)
		if err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("读不到当前配置 %s:%w", configFile, err)
			}
			// 还没部署过配置(全新安装),没有可 check 的东西
			return nil
		}
		if len(bytes.TrimSpace(cfg)) == 0 {
			return nil
		}
		if err := singbox.Check(ctx, bin("sing-box"), root, cfg); err != nil {
			return fmt.Errorf("新的 sing-box 不认当前配置:%w", err)
		}
	}
	return nil
}

// runOutput 跑一个命令并取标准输出(带 stderr,方便看失败原因)。
func runOutput(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &buf, n: smokeOutput}
	cmd.Stderr = &limitedWriter{w: &buf, n: smokeOutput}
	if err := cmd.Run(); err != nil {
		if s := strings.TrimSpace(buf.String()); s != "" {
			return s, fmt.Errorf("%v:%s", err, firstLine(s))
		}
		return "", err
	}
	return buf.String(), nil
}

// limitedWriter 只留前 n 个字节,后面的丢掉(命令抽风时不要把它的话全塞进内存)。
type limitedWriter struct {
	w interface{ Write([]byte) (int, error) }
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	if len(p) > l.n {
		if _, err := l.w.Write(p[:l.n]); err != nil {
			return 0, err
		}
		l.n = 0
		return len(p), nil
	}
	n, err := l.w.Write(p)
	l.n -= n
	return len(p), err
}

// firstLine 取第一行,给错误文案用。
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
