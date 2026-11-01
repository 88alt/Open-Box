package subs

import (
	"context"
	"regexp"
	"unicode/utf8"

	"github.com/timeflysoon/Open-Box/backend/internal/sub"
)

// 备份导入用的订阅与节点恢复。和 Create 不同:不联网、不重新解析、不套改名规则,
// 节点就是备份文件里那份(改名在导出之前已经做完了),只做校验和整理。

// RestoreMode 是订阅导入的方式。
type RestoreMode string

const (
	// RestoreReplace 清空现有的订阅和节点,按文件里的 id 写入。
	RestoreReplace RestoreMode = "replace"
	// RestoreAppend 保留现有的,把文件里的加在后面;id 冲突的换新 id,节点名冲突的改名。
	RestoreAppend RestoreMode = "append"
)

// 外部输入的上限(备份文件本身还有请求体上限)。
const (
	MaxRestoreSubs  = 500
	MaxRestoreNodes = 100000
	maxTagRunes     = 200
	maxSkippedKept  = 500
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// RestoreNode 是备份文件里的一个节点:所属订阅的 id 加节点本身。
type RestoreNode struct {
	Subscription string
	Node         sub.Node
}

// RestoreInput 是要恢复的订阅和节点(来自备份文件,未经校验)。
type RestoreInput struct {
	Records []Record
	Nodes   []RestoreNode
	Mode    RestoreMode
}

// RestoreResult 是恢复(或预检)的结果。
type RestoreResult struct {
	Subscriptions int // 写入(或将写入)的订阅数
	Nodes         int // 写入(或将写入)的节点数
	Renamed       int // 追加时因与现有节点重名而改了名的节点数
	// EnabledTags 是恢复之后,已启用订阅里所有节点的名字(给节点组的重名校验用)。
	EnabledTags map[string]bool
}

// cleanRestore 校验并整理文件里的订阅和节点,返回按文件顺序的订阅和「订阅 id → 节点」。纯计算,不碰存储。
func (s *Service) cleanRestore(in RestoreInput) ([]Record, map[string][]sub.Node, error) {
	if in.Mode != RestoreReplace && in.Mode != RestoreAppend {
		return nil, nil, bad("订阅导入方式只能是 replace 或 append")
	}
	if len(in.Records) > MaxRestoreSubs {
		return nil, nil, bad("备份里的订阅太多了(最多 %d 个)", MaxRestoreSubs)
	}
	if len(in.Nodes) > MaxRestoreNodes {
		return nil, nil, bad("备份里的节点太多了(最多 %d 个)", MaxRestoreNodes)
	}
	now := s.now().UnixMilli()
	seenID := make(map[string]bool, len(in.Records))
	for i := range in.Records {
		if id := in.Records[i].ID; !idPattern.MatchString(id) {
			return nil, nil, bad("第 %d 个订阅的 id 不合法(只能是 1–64 位字母、数字、_、-)", i+1)
		} else if seenID[id] {
			return nil, nil, bad("订阅 id 重复:%s", id)
		} else {
			seenID[id] = true
		}
	}
	byID := make(map[string][]sub.Node, len(in.Records))
	seenTag := map[string]bool{}
	for i, rn := range in.Nodes {
		n := rn.Node
		if !seenID[rn.Subscription] {
			return nil, nil, bad("第 %d 个节点属于不存在的订阅:%q", i+1, rn.Subscription)
		}
		if n.Tag == "" || utf8.RuneCountInString(n.Tag) > maxTagRunes {
			return nil, nil, bad("第 %d 个节点的名字为空或超过 %d 个字符", i+1, maxTagRunes)
		}
		if n.Type == "" || len(n.Type) > 32 {
			return nil, nil, bad("节点「%s」的类型为空或太长", n.Tag)
		}
		if n.ServerPort < 0 || n.ServerPort > 65535 {
			return nil, nil, bad("节点「%s」的端口不合法", n.Tag)
		}
		if seenTag[n.Tag] {
			return nil, nil, bad("备份里的节点名重复:%s", n.Tag)
		}
		seenTag[n.Tag] = true
		byID[rn.Subscription] = append(byID[rn.Subscription], n)
	}
	recs := make([]Record, 0, len(in.Records))
	for _, r := range in.Records {
		rec, err := cleanRecord(r, len(byID[r.ID]), now)
		if err != nil {
			return nil, nil, err
		}
		recs = append(recs, rec)
	}
	return recs, byID, nil
}

// cleanRecord 校验一条订阅元数据并整理成可保存的样子(来源规整、节点数按实际节点重算)。
func cleanRecord(r Record, nodeCount int, now int64) (Record, error) {
	name, err := validateName(r.Name)
	if err != nil {
		return Record{}, bad("订阅 %s:%s", r.ID, err.(*InputError).Msg)
	}
	fail := func(err error) (Record, error) {
		if ie, ok := err.(*InputError); ok {
			return Record{}, bad("订阅「%s」:%s", name, ie.Msg)
		}
		return Record{}, bad("订阅「%s」:%v", name, err)
	}
	src, err := normalizeSource(Source{URL: r.URL, URLs: r.URLs, Content: r.Content, UserAgent: r.UserAgent})
	if err != nil {
		return fail(err)
	}
	if err := validateRename(r.Rename); err != nil {
		return fail(err)
	}
	if err := validateAutoUpdate(r.AutoUpdate); err != nil {
		return fail(err)
	}
	if err := validateNodeDNS(r.NodeDNS); err != nil {
		return fail(err)
	}
	if utf8.RuneCountInString(r.Format) > 32 {
		return fail(bad("格式名太长"))
	}
	rec := r
	rec.Name = name
	rec.setSource(src)
	rec.UserAgent = src.ua
	rec.NodeCount = nodeCount
	rec.Account = nil // 账号登录来源不支持,备份里即使带了也不保存
	if rec.CreatedAt <= 0 {
		rec.CreatedAt = now
	}
	if rec.UpdatedAt <= 0 {
		rec.UpdatedAt = now
	}
	if len(rec.Skipped) > maxSkippedKept {
		rec.Skipped = rec.Skipped[:maxSkippedKept]
	}
	return rec, nil
}

// planRestore 把整理好的文件内容和现有数据合成恢复之后的订阅和节点(纯计算)。
func planRestore(mode RestoreMode, curRecs []Record, curNodes map[string][]sub.Node, recs []Record, byID map[string][]sub.Node) ([]Record, map[string][]sub.Node, int) {
	if mode == RestoreReplace {
		return recs, byID, 0
	}
	// 追加:id 冲突换新 id;节点名与现有节点(或文件里别的节点)冲突的改名。
	usedID := make(map[string]bool, len(curRecs))
	for _, r := range curRecs {
		usedID[r.ID] = true
	}
	existing := takenTags(curRecs, curNodes, "")
	blocked := make(map[string]bool, len(existing))
	for k := range existing {
		blocked[k] = true
	}
	for _, ns := range byID { // 文件里的名字也先占住,改名时不会撞上它们
		for _, n := range ns {
			blocked[n.Tag] = true
		}
	}
	outRecs := append([]Record{}, curRecs...)
	outNodes := make(map[string][]sub.Node, len(curNodes)+len(recs))
	for _, r := range curRecs {
		outNodes[r.ID] = curNodes[r.ID]
	}
	renamed := 0
	for _, r := range recs {
		oldID := r.ID
		if usedID[oldID] {
			for {
				if id := newID(); !usedID[id] {
					r.ID = id
					break
				}
			}
		}
		usedID[r.ID] = true
		ns := make([]sub.Node, 0, len(byID[oldID]))
		for _, n := range byID[oldID] {
			if existing[n.Tag] {
				n.Tag = unique(n.Tag, nil, blocked)
				blocked[n.Tag] = true
				renamed++
			}
			ns = append(ns, n)
		}
		outRecs = append(outRecs, r)
		outNodes[r.ID] = ns
	}
	return outRecs, outNodes, renamed
}

func enabledTags(recs []Record, nodes map[string][]sub.Node) map[string]bool {
	t := map[string]bool{}
	for _, r := range recs {
		if r.Enabled {
			for _, n := range nodes[r.ID] {
				t[n.Tag] = true
			}
		}
	}
	return t
}

// CheckRestore 只校验、只推算,不写盘:恢复之后会是什么样。给「先整份校验、再动手」的导入用。
func (s *Service) CheckRestore(_ context.Context, in RestoreInput) (*RestoreResult, error) {
	recs, byID, err := s.cleanRestore(in)
	if err != nil {
		return nil, err
	}
	curRecs, curNodes, err := s.loadAll()
	if err != nil {
		return nil, err
	}
	outRecs, outNodes, renamed := planRestore(in.Mode, curRecs, curNodes, recs, byID)
	return s.summary(outRecs, outNodes, len(recs), renamed), nil
}

// Restore 把 in 写进存储:replace 先清空再写,append 加在现有的后面。不联网、不重启内核。
// 先写节点再写元数据(与 persist 一致):中途失败最多留下没人引用的节点。
func (s *Service) Restore(_ context.Context, in RestoreInput) (*RestoreResult, error) {
	recs, byID, err := s.cleanRestore(in)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	curRecs, curNodes, err := s.loadAll()
	if err != nil {
		return nil, err
	}
	outRecs, outNodes, renamed := planRestore(in.Mode, curRecs, curNodes, recs, byID)
	if err := s.persist(outRecs, outNodes); err != nil {
		return nil, err
	}
	return s.summary(outRecs, outNodes, len(recs), renamed), nil
}

// summary 的数量只算「这次导入带来的」:replace 时结果里就是文件里的全部,append 时是末尾新加的 fileRecs 条。
func (s *Service) summary(recs []Record, nodes map[string][]sub.Node, fileRecs, renamed int) *RestoreResult {
	added := recs[len(recs)-fileRecs:]
	res := &RestoreResult{Subscriptions: fileRecs, Renamed: renamed, EnabledTags: enabledTags(recs, nodes)}
	for _, r := range added {
		res.Nodes += len(nodes[r.ID])
	}
	return res
}
