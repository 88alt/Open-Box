package mihomo

import (
	"bytes"
	"encoding/json"
)

// OrderedMap 是「键按插入顺序」的对象,给 mihomo.yaml 里需要跟上游同序的段用
// (DNS 段的键顺序、nameserver-policy / openbox-dns-policy 里的条目顺序)。
//
// 上游是 JS 对象,写 YAML 时按字面量的定义顺序输出;Go 的 map 没有顺序,
// 所以这里单独存一份 keys。值为 nil 的键不写(与上游的展开语法 `...cond?{k:v}:{}` 一致)。
type OrderedMap struct {
	keys []string
	vals map[string]any
}

// NewOrdered 创建空的有序对象。
func NewOrdered() *OrderedMap {
	return &OrderedMap{vals: map[string]any{}}
}

// Set 写入一个键。已经存在的键保持原位置(与 JS 对象一致:重复赋值不改顺序),值为 nil 时整个删掉。
func (m *OrderedMap) Set(key string, v any) *OrderedMap {
	if m.vals == nil {
		m.vals = map[string]any{}
	}
	if v == nil {
		if _, ok := m.vals[key]; ok {
			delete(m.vals, key)
			for i, k := range m.keys {
				if k == key {
					m.keys = append(m.keys[:i], m.keys[i+1:]...)
					break
				}
			}
		}
		return m
	}
	if _, ok := m.vals[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.vals[key] = v
	return m
}

// SetIf 只在 cond 为真时写入,给上游的 `...cond?{...}:{}` 用。
func (m *OrderedMap) SetIf(cond bool, key string, v any) *OrderedMap {
	if cond {
		m.Set(key, v)
	}
	return m
}

// Get 读一个键。
func (m *OrderedMap) Get(key string) (any, bool) {
	v, ok := m.vals[key]
	return v, ok
}

// Has 判断键在不在。
func (m *OrderedMap) Has(key string) bool {
	_, ok := m.vals[key]
	return ok
}

// Keys 返回键的顺序(副本)。
func (m *OrderedMap) Keys() []string {
	return append([]string(nil), m.keys...)
}

// Len 是键的个数。
func (m *OrderedMap) Len() int { return len(m.keys) }

// MarshalJSON 按插入顺序输出,便于测试与日志里直接看。
func (m *OrderedMap) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range m.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		val, err := json.Marshal(m.vals[k])
		if err != nil {
			return nil, err
		}
		b.Write(val)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}
