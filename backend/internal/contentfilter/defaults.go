package contentfilter

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// Default 是随包的一份默认规则。
type Default struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Dialect   Dialect `json:"dialect"`
	SourceURL string  `json:"sourceUrl"`
	Enabled   bool    `json:"enabled"`
	Text      string  `json:"text"`
}

// 默认规则做成内嵌数据:以后只改 defaults.json,不改代码。
// 内容对齐上游 v0.1.402 随包 panel/server/defaults/content-filter.json(三份:综合省流 / Youtube-Surge / Youtube-补丁)。
//
//go:embed defaults.json
var defaultsJSON []byte

// BuiltinDefaults 解析内嵌的默认规则。
func BuiltinDefaults() ([]Default, error) {
	var f struct {
		Sets []Default `json:"sets"`
	}
	if err := json.Unmarshal(defaultsJSON, &f); err != nil {
		return nil, fmt.Errorf("内嵌的默认规则损坏: %w", err)
	}
	return f.Sets, nil
}
