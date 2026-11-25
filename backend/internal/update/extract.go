package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/release"
)

// spaceMargin 是解压所需空间的余量:除了清单里的文件,还要放临时文件与日志(M6 第七节第 3 步)。
const spaceMargin = 10 << 20

// ExtractHooks 是解压过程中的回调,都可以为空。
type ExtractHooks struct {
	// OnProgress 报告已读的压缩字节数(边下边解,所以这就是下载进度)。total 是 Content-Length,未知时为 0。
	OnProgress func(done, total int64)
	// OnManifest 在读到包里的 manifest.json 时调用。它排在压缩包最前面,所以在写任何文件之前就能知道
	// 解压后有多大、是哪个架构、哪一版,空间不够当场失败,不用先占一堆磁盘再删。
	OnManifest func(m release.Manifest) error
	// FreeSpace 报某个目录所在文件系统还剩多少字节;为空时用 FreeSpace(只有 linux 有实现)。
	FreeSpace func(dir string) (int64, error)
}

// Extracted 是一次解压的结果。
type Extracted struct {
	// Root 是解压出来的顶层目录(<dir>/openbox-<版本>-linux-<架构>)。
	Root     string
	Manifest release.Manifest
	// Sum 是压缩包整体的 sha256(十六进制),用来对 SHA256SUMS。
	Sum string
	// Size 是压缩包的实际字节数(与 SHA256SUMS 里的长度不一定相同,以实际下载为准)。
	Size int64
}

// Extract 把 tar.gz 流解到 dir 下,边读边写、边算 sha256,不把包放进内存(路由器内存小)。
//
// 安全:只接受普通文件与目录;拒绝绝对路径、..、反斜杠、符号链接 / 硬链接 / 设备文件;
// 所有条目必须在同一个顶层目录下,且这个目录名必须与 manifest 里的版本 / 架构对得上。
// 内容是事后用 release.Verify 逐文件核的——这里只保证「写出去的东西不会跑到 dir 外面」。
func Extract(ctx context.Context, r io.Reader, total int64, dir string, h ExtractHooks) (*Extracted, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("建暂存目录失败:%w", err)
	}
	hasher := sha256.New()
	var n int64
	tee := io.TeeReader(r, io.MultiWriter(hasher, writerFunc(func(p []byte) (int, error) {
		n += int64(len(p))
		if h.OnProgress != nil {
			h.OnProgress(n, total)
		}
		return len(p), nil
	})))
	gz, err := gzip.NewReader(tee)
	if err != nil {
		return nil, errors.New("下载到的不是 gzip 压缩包")
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	top := ""
	var mf *release.Manifest
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("压缩包损坏,读不下去了")
		}
		name, err := entryPath(hdr.Name)
		if err != nil {
			return nil, err
		}
		first, rest, _ := strings.Cut(name, "/")
		if top == "" {
			top = first
		} else if first != top {
			return nil, fmt.Errorf("压缩包里不止一个顶层目录(%q 与 %q)", top, first)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(name)), 0o755); err != nil {
				return nil, fmt.Errorf("建目录失败:%w", err)
			}
		case tar.TypeReg:
			if rest == "" {
				return nil, fmt.Errorf("压缩包里的 %q 是个文件,但它在顶层(顶层必须是目录)", name)
			}
			dst := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return nil, fmt.Errorf("建目录失败:%w", err)
			}
			// 权限按压缩包头里的写(清单里也记了同一份):暂存的 bin/* 要有执行位,否则冒烟跑不起来。
			// Windows 上 chmod 是空操作,不影响。
			mode := os.FileMode(hdr.Mode).Perm()
			if mode == 0 {
				mode = 0o644
			}
			f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return nil, fmt.Errorf("写文件失败:%w", err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return nil, fmt.Errorf("写文件 %s 失败:%w", name, err)
			}
			if err := f.Close(); err != nil {
				return nil, fmt.Errorf("写文件 %s 失败:%w", name, err)
			}
			// OpenFile 的 mode 受 umask 影响,再显式 chmod 一次才是头里写的权限
			if err := os.Chmod(dst, mode); err != nil && !os.IsPermission(err) {
				return nil, fmt.Errorf("设权限失败:%w", err)
			}
			// manifest.json 排在包的最前面:读到它就先做检查,后面的文件还没落地
			if mf == nil && rest == release.ManifestName {
				m, err := readManifest(dst)
				if err != nil {
					return nil, err
				}
				mf = &m
				if h.OnManifest != nil {
					if err := h.OnManifest(m); err != nil {
						return nil, err
					}
				}
			}
		default:
			return nil, fmt.Errorf("压缩包里有不支持的条目类型(%q):只接受普通文件和目录", name)
		}
	}
	if mf == nil {
		return nil, fmt.Errorf("压缩包里没有 %s,不像是本仓库的安装包", release.ManifestName)
	}
	// tar 读完不代表流已经到结尾(尾部有填充),剩下的也要读进 sha256,否则算出来的不是整个文件的摘要
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return nil, errors.New("读取下载内容失败")
	}
	want := release.TopDir(mf.Version, mf.Arch)
	if top != want {
		return nil, fmt.Errorf("压缩包的顶层目录是 %q,按 manifest 应该是 %q", top, want)
	}
	return &Extracted{
		Root:     filepath.Join(dir, filepath.FromSlash(top)),
		Manifest: *mf,
		Sum:      hex.EncodeToString(hasher.Sum(nil)),
		Size:     n,
	}, nil
}

// entryPath 把 tar 里的条目名规范成相对路径,并拒掉会写到 dir 外面的写法。
func entryPath(name string) (string, error) {
	p := strings.TrimSuffix(name, "/")
	p = strings.TrimPrefix(p, "./")
	if p == "" || p == "." {
		return "", errors.New("压缩包里有空路径")
	}
	if err := release.CheckPath(p); err != nil {
		return "", fmt.Errorf("压缩包里的路径 %q 不安全:%w", name, err)
	}
	if path.Clean(p) != p {
		return "", fmt.Errorf("压缩包里的路径 %q 不规范", name)
	}
	return p, nil
}

// readManifest 读一个已经落地到磁盘的 manifest.json。
func readManifest(name string) (release.Manifest, error) {
	f, err := os.Open(name)
	if err != nil {
		return release.Manifest{}, fmt.Errorf("读 %s 失败:%w", release.ManifestName, err)
	}
	defer f.Close()
	m, err := release.Parse(f)
	if err != nil {
		return release.Manifest{}, fmt.Errorf("%s 有问题:%w", release.ManifestName, err)
	}
	return *m, nil
}

// checkSpace 在开始写文件之前核对磁盘空间:可用空间要大于解压后大小加余量(M6 第七节第 3 步)。
func checkSpace(free func(string) (int64, error), dir string, need int64) error {
	if free == nil {
		free = FreeSpace
	}
	got, err := free(dir)
	if err != nil {
		// 量不出来就不拦:拦错了会让人明明有空间却升不了级
		return nil
	}
	if got < need+spaceMargin {
		return fmt.Errorf("磁盘空间不够:解压需要 %d MB,加 %d MB 余量,只剩 %d MB", need>>20, spaceMargin>>20, got>>20)
	}
	return nil
}

// writerFunc 把函数当 io.Writer 用。
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
