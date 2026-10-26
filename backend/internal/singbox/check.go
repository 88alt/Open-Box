package singbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	checkTimeout = 30 * time.Second
	maxCheckOut  = 8 << 10
)

// ErrNoSingBox:找不到 sing-box 可执行文件。
var ErrNoSingBox = errors.New("找不到 sing-box 可执行文件")

// CheckError 是 `sing-box check` 判定配置不合法(内容是它的输出,已截断)。
type CheckError struct{ Output string }

func (e *CheckError) Error() string { return "sing-box 校验未通过:" + e.Output }

// limitedWriter 只留前 max 字节,多的丢弃(校验输出可能很长)。
type limitedWriter struct {
	buf bytes.Buffer
	max int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}

// Check 把配置写到 dir 下的临时文件(0600),运行 `<bin> check -c <文件>`,结束后删除临时文件。
// 配置合法返回 nil;不合法返回 *CheckError;找不到可执行文件返回 ErrNoSingBox。
// 它不会碰正式配置,也不启动 sing-box。
func Check(ctx context.Context, bin, dir string, cfg []byte) error {
	path, err := exec.LookPath(bin)
	if err != nil {
		return ErrNoSingBox
	}
	f, err := os.CreateTemp(dir, "check-*.json")
	if err != nil {
		return fmt.Errorf("创建临时配置失败: %w", err)
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(cfg); err != nil {
		f.Close()
		return fmt.Errorf("写临时配置失败: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "check", "-c", name)
	out := &limitedWriter{max: maxCheckOut}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("sing-box check 超时(%s)", checkTimeout)
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			msg := strings.TrimSpace(out.buf.String())
			if msg == "" {
				msg = ee.Error()
			}
			// 输出里带着临时文件名,没有信息量
			return &CheckError{Output: strings.ReplaceAll(msg, name, "config.json")}
		}
		return fmt.Errorf("运行 sing-box 失败: %w", err)
	}
	return nil
}
