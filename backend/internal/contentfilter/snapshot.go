package contentfilter

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// 同步给 App 的内容过滤快照:合并后的结构化文档(对齐上游文档口径)。
// sha256 / size 是文档 JSON 正文的摘要与字节数;上限 MaxDocBytes(2 MiB)。

// MaxSnapshotSize 保留旧名,取值与 MaxDocBytes 相同(5b 起同步上限 2 MiB)。
const MaxSnapshotSize = MaxDocBytes

// Snapshot 是启用集合合并后的文档快照(给 client-config 明文和后续 token 接口共用)。
type Snapshot struct {
	SHA256    string            `json:"sha256"`
	Size      int               `json:"size"`
	Version   int               `json:"version"`
	Hostnames []string          `json:"hostnames"`
	Rules     []Rule            `json:"rules"`
	Scripts   map[string]string `json:"scripts"`
	Sets      []SetMeta         `json:"sets"`
}

// TooLargeError:启用集合的总大小超过上限,快照不给。
type TooLargeError struct{ Size, Max int }

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("启用的内容过滤规则共 %s,超过同步上限 %s", humanSize(e.Size), humanSize(e.Max))
}

func humanSize(n int) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KiB", (n+1023)>>10)
}

// BuiltSnapshot 返回合并文档(元数据 + 正文字节)。limit <= 0 用 MaxSnapshotSize。
func (s *Service) BuiltSnapshot(ctx context.Context, limit int) (*Built, error) {
	if limit <= 0 {
		limit = MaxSnapshotSize
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	on := make([]Set, 0)
	for _, st := range f.Sets {
		if st.Enabled {
			on = append(on, st)
		}
	}
	slices.SortFunc(on, func(a, b Set) int { return strings.Compare(a.ID, b.ID) })
	data := make([]SetData, 0, len(on))
	for _, st := range on {
		sd, err := s.setDataLocked(ctx, st)
		if err != nil {
			return nil, err
		}
		data = append(data, sd)
	}
	built, err := BuildDocument(data)
	if err != nil {
		if err == ErrDocTooLarge {
			return nil, &TooLargeError{Size: MaxDocBytes + 1, Max: limit}
		}
		return nil, err
	}
	if built.Metadata.Size > limit {
		return nil, &TooLargeError{Size: built.Metadata.Size, Max: limit}
	}
	return built, nil
}

// Snapshot 返回启用集合合并后的文档。limit <= 0 用 MaxSnapshotSize。
func (s *Service) Snapshot(ctx context.Context, limit int) (Snapshot, error) {
	built, err := s.BuiltSnapshot(ctx, limit)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		SHA256: built.Metadata.SHA256, Size: built.Metadata.Size,
		Version: built.Document.Version, Hostnames: built.Document.Hostnames,
		Rules: built.Document.Rules, Scripts: built.Document.Scripts, Sets: built.Metadata.Sets,
	}, nil
}

func (s *Service) setDataLocked(ctx context.Context, st Set) (SetData, error) {
	stored, ok, err := s.readStruct(st.ID)
	if err != nil {
		return SetData{}, err
	}
	if ok {
		return stored.toSetData(), nil
	}
	// 旧数据:按文本重建,不联网抓脚本
	text, err := s.readText(st.ID)
	if err != nil {
		return SetData{}, err
	}
	vals := map[string]string{}
	if st.Arguments != nil {
		for k, v := range st.Arguments.Values {
			vals[k] = fmt.Sprint(v)
		}
	}
	a, err := Analyze(st.Dialect, text, vals, st.SourceURL)
	if err != nil {
		return SetData{ID: st.ID, Name: st.Name, Dialect: st.Dialect}, nil
	}
	return SetData{
		ID: st.ID, Name: st.Name, Dialect: st.Dialect,
		Hostnames: a.Hostnames, Rules: a.Rules, Scripts: map[string]string{},
	}, nil
}
