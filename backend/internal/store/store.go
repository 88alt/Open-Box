// Package store 是 JSON 文件持久化:写临时文件 → fsync → rename,
// 进程内对同一个文件的读改写用互斥锁串行化。配置数据量小,先不引入数据库(D6)。
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// 文件名只允许字母数字、下划线、连字符,防止路径穿越。
var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ErrInvalidName 表示文件名不合法。
var ErrInvalidName = errors.New("store: 文件名不合法")

// Store 管理一个目录下的若干 <name>.json 文件。
type Store struct {
	dir   string
	mu    sync.Mutex // 保护 locks
	locks map[string]*sync.Mutex
}

// Open 打开(必要时创建)目录。目录权限 0700,文件权限 0600。
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: 创建目录失败: %w", err)
	}
	return &Store{dir: dir, locks: map[string]*sync.Mutex{}}, nil
}

func (s *Store) path(name string) (string, error) {
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	return filepath.Join(s.dir, name+".json"), nil
}

func (s *Store) lockFor(name string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[name]
	if !ok {
		l = &sync.Mutex{}
		s.locks[name] = l
	}
	return l
}

// Load 读取 name 并解析进 v。文件不存在返回 (false, nil)。
// 文件存在但内容损坏会返回错误——不要「猜一个」默认值去覆盖用户数据。
func (s *Store) Load(name string, v any) (bool, error) {
	l, err := s.lock(name)
	if err != nil {
		return false, err
	}
	defer l.Unlock()
	return s.load(name, v)
}

// Save 用 v 整体替换 name。
func (s *Store) Save(name string, v any) error {
	l, err := s.lock(name)
	if err != nil {
		return err
	}
	defer l.Unlock()
	return s.save(name, v)
}

// Update 在持有锁的情况下完成「读 → 改 → 写」:
// 先把现有内容读进 v(不存在时 v 保持原样),再调用 fn 修改 v,fn 返回 nil 才写回。
func (s *Store) Update(name string, v any, fn func() error) error {
	l, err := s.lock(name)
	if err != nil {
		return err
	}
	defer l.Unlock()
	if _, err := s.load(name, v); err != nil {
		return err
	}
	if err := fn(); err != nil {
		return err
	}
	return s.save(name, v)
}

// Delete 删除 name;不存在不算错误。
func (s *Store) Delete(name string) error {
	l, err := s.lock(name)
	if err != nil {
		return err
	}
	defer l.Unlock()
	p, _ := s.path(name)
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("store: 删除 %s 失败: %w", name, err)
	}
	return syncDir(s.dir)
}

func (s *Store) lock(name string) (*sync.Mutex, error) {
	if _, err := s.path(name); err != nil {
		return nil, err
	}
	l := s.lockFor(name)
	l.Lock()
	return l, nil
}

func (s *Store) load(name string, v any) (bool, error) {
	p, _ := s.path(name)
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: 读取 %s 失败: %w", name, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("store: %s 内容损坏: %w", name, err)
	}
	return true, nil
}

func (s *Store) save(name string, v any) error {
	p, _ := s.path(name)
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("store: 编码 %s 失败: %w", name, err)
	}
	return WriteFileAtomic(p, data, 0o600)
}

// WriteFileAtomic 把 data 写到 path:同目录临时文件 → 写入 → fsync → 改名 → fsync 目录。
// 任何时刻读到的都是完整的旧内容或完整的新内容,断电也不会留下半个文件。
func WriteFileAtomic(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("store: 创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if err = tmp.Chmod(perm); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		return fmt.Errorf("store: 设置权限失败: %w", err)
	}
	err = nil
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("store: 写入失败: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("store: fsync 失败: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("store: 关闭临时文件失败: %w", err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("store: 替换文件失败: %w", err)
	}
	return syncDir(dir)
}

// syncDir 让改名 / 删除落盘。有些系统(Windows)不支持目录 fsync,忽略其错误。
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer d.Close()
	_ = d.Sync()
	return nil
}
