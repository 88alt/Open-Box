// Package core 是 openbox-core 的子进程客户端(D4)。
//
// openbox-core 是无状态的 stdin/stdout JSON 程序,不是服务,每次调用都拉起一个新进程(协议见 core/README.md):
//
//	stdin : {"protocol":1,"method":"...","args":[...],"geoTags":[...]}
//	stdout: {"protocol":1,"ok":true,"value":...} 或 {"protocol":1,"ok":false,"error":"..."}
//
// 约束:退出码恒为 0;输入上限 32 MiB,输出上限 64 MiB。
// 本客户端在这些约束之上再加:并发上限(路由器内存小)、超时(超时会杀进程)、stderr 只保留末尾一小段。
// 按 D5 不校验 hello 返回的 buildID(那是给上游 Node 服务端写死的期望值)。
package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// 协议常量,与 core/internal/protocol 保持一致。
const (
	ProtocolVersion = 1
	Implementation  = "go-native"
	MaxInputBytes   = 32 << 20
	MaxOutputBytes  = 64 << 20

	DefaultTimeout       = 60 * time.Second
	DefaultMaxConcurrent = 2
	stderrKeep           = 4 << 10
)

// Methods 是核心提供的 15 个方法(不含 hello)。后端暂时用不到 authz 三个(没有激活概念),但客户端不做限制。
var Methods = []string{
	"normalizePolicy", "normalizeRouting", "routingFingerprint", "migrateStoredRouting",
	"classes", "forwardPlan", "bypassPlan", "entryPlan",
	"normalizeGroups", "builtinTags", "emitGroups",
	"configDetailed",
	"checkActivationCode", "activationState", "authorizationState",
}

var (
	// ErrInputTooLarge 表示请求序列化后超过 32 MiB,没有启动进程。
	ErrInputTooLarge = errors.New("core: 请求超过 32 MiB")
	// ErrOutputTooLarge 表示核心输出超过 64 MiB。
	ErrOutputTooLarge = errors.New("core: 核心输出超过 64 MiB")
	// ErrIncompatible 表示 hello 返回的实现或协议版本不是预期的。
	ErrIncompatible = errors.New("core: 核心与后端不兼容")
)

// CoreError 是核心自己返回的 ok:false,Message 是核心给的错误文案(多为中文,可直接展示给用户)。
// 与「进程起不来 / 超时 / 输出损坏」这类系统错误区分开:前者是用户输入有问题,后者是后端的问题。
type CoreError struct {
	Method  string
	Message string
}

func (e *CoreError) Error() string { return e.Message }

// runner 执行一次核心调用:把 stdin 喂给进程,返回 stdout。测试里可以替换。
type runner func(ctx context.Context, stdin []byte) ([]byte, error)

// Client 调用 openbox-core。并发安全。
type Client struct {
	path    string
	timeout time.Duration
	sem     chan struct{}
	run     runner
	env     []string
}

// Option 调整 Client。
type Option func(*Client)

// WithTimeout 设置单次调用的超时(含杀进程)。
func WithTimeout(d time.Duration) Option { return func(c *Client) { c.timeout = d } }

// WithMaxConcurrent 设置同时运行的核心进程数上限(至少 1)。
func WithMaxConcurrent(n int) Option {
	return func(c *Client) { c.sem = make(chan struct{}, max(n, 1)) }
}

// New 创建客户端。path 可以只写名字(从 PATH 找)。
func New(path string, opts ...Option) *Client {
	c := &Client{
		path:    path,
		timeout: DefaultTimeout,
		sem:     make(chan struct{}, DefaultMaxConcurrent),
		env:     []string{"LANG=C"}, // 核心不需要环境变量;给空列表会被当成「继承」,所以放一个无害的
	}
	c.run = c.execRun
	for _, o := range opts {
		o(c)
	}
	return c
}

// CallOption 调整单次调用。
type CallOption func(*request)

// WithGeoTags 附带 geodata 清单里的 geoip-xx / geosite-xx 名单(需要校验规则集的方法会用到)。
// 传空切片也算「带了」(与不带不同),所以这里始终序列化为数组。
func WithGeoTags(tags []string) CallOption {
	return func(r *request) {
		if tags == nil {
			tags = []string{}
		}
		r.GeoTags = &tags
	}
}

type request struct {
	Protocol int       `json:"protocol"`
	Method   string    `json:"method"`
	Args     []any     `json:"args"`
	GeoTags  *[]string `json:"geoTags,omitempty"`
}

type response struct {
	Protocol int             `json:"protocol"`
	OK       bool            `json:"ok"`
	Value    json.RawMessage `json:"value"`
	Error    string          `json:"error"`
}

// Call 调用 method,返回 value 的原始 JSON。核心返回 ok:false 时错误类型是 *CoreError。
func (c *Client) Call(ctx context.Context, method string, args []any, opts ...CallOption) (json.RawMessage, error) {
	if args == nil {
		args = []any{} // 核心按数组读 args,不能是 null
	}
	req := request{Protocol: ProtocolVersion, Method: method, Args: args}
	for _, o := range opts {
		o(&req)
	}
	// 不转义 < > &,与 JS 的 JSON.stringify 一致(核心的金标准按这个写的)
	var in bytes.Buffer
	enc := json.NewEncoder(&in)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(req); err != nil {
		return nil, fmt.Errorf("core: 编码请求失败: %w", err)
	}
	if in.Len() > MaxInputBytes {
		return nil, ErrInputTooLarge
	}

	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	out, err := c.run(ctx, in.Bytes())
	if err != nil {
		if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("core: 调用 %s 超时(%s)", method, c.timeout)
		}
		return nil, fmt.Errorf("core: 调用 %s 失败: %w", method, err)
	}
	return parseResponse(method, out)
}

// CallInto 调用 method 并把 value 解码进 out。
func (c *Client) CallInto(ctx context.Context, method string, args []any, out any, opts ...CallOption) error {
	raw, err := c.Call(ctx, method, args, opts...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("core: 解析 %s 的返回值失败: %w", method, err)
	}
	return nil
}

func parseResponse(method string, out []byte) (json.RawMessage, error) {
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return nil, fmt.Errorf("core: 调用 %s 没有任何输出", method)
	}
	var resp response
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("core: 调用 %s 的输出不是有效的 JSON: %w", method, err)
	}
	if resp.Protocol != ProtocolVersion {
		return nil, fmt.Errorf("%w: 协议版本 %d,需要 %d", ErrIncompatible, resp.Protocol, ProtocolVersion)
	}
	if !resp.OK {
		msg := strings.TrimSpace(resp.Error)
		if msg == "" {
			msg = "核心返回了错误但没有说明原因"
		}
		return nil, &CoreError{Method: method, Message: msg}
	}
	if len(resp.Value) == 0 {
		return json.RawMessage("null"), nil
	}
	return resp.Value, nil
}

// HelloInfo 是 hello 的返回值。
type HelloInfo struct {
	Implementation string `json:"implementation"`
	Protocol       int    `json:"protocol"`
	BuildID        string `json:"buildID"`
}

// Hello 握手并检查兼容性:只要求 implementation == "go-native" 且 protocol == 1(D5,不校验 buildID)。
func (c *Client) Hello(ctx context.Context) (HelloInfo, error) {
	var info HelloInfo
	if err := c.CallInto(ctx, "hello", nil, &info); err != nil {
		return HelloInfo{}, err
	}
	if info.Implementation != Implementation || info.Protocol != ProtocolVersion {
		return info, fmt.Errorf("%w: implementation=%q protocol=%d", ErrIncompatible, info.Implementation, info.Protocol)
	}
	return info, nil
}

// execRun 启动一个核心进程。
func (c *Client) execRun(ctx context.Context, stdin []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.path)
	cmd.Env = c.env
	cmd.Stdin = bytes.NewReader(stdin)
	stdout := &limitedBuffer{limit: MaxOutputBytes + 1}
	stderr := &tailBuffer{keep: stderrKeep}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = 2 * time.Second // 被杀后管道没关也不会一直等

	err := cmd.Run()
	if stdout.exceeded {
		return nil, ErrOutputTooLarge
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if tail := strings.TrimSpace(stderr.String()); tail != "" {
			return nil, fmt.Errorf("%w(stderr: %s)", err, tail)
		}
		return nil, err
	}
	return stdout.buf.Bytes(), nil
}

// limitedBuffer 超过 limit 字节就记下并拒绝继续写(进程会因管道错误退出),避免内存被吃光。
type limitedBuffer struct {
	buf      bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.limit {
		b.exceeded = true
		return 0, io.ErrShortWrite
	}
	return b.buf.Write(p)
}

// tailBuffer 只保留最后 keep 字节。
type tailBuffer struct {
	keep int
	data []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	if len(b.data) > b.keep {
		b.data = b.data[len(b.data)-b.keep:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string { return string(b.data) }

// Available 报告核心可执行文件是否存在且可执行(不启动进程)。
func Available(path string) bool {
	p, err := exec.LookPath(path)
	if err != nil {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
