package mihomo

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// 把配置写成 mihomo.yaml(双内核 3e-5)。
//
// 只需要覆盖 Mihomo 配置用到的那几种形状:有序对象(*OrderedMap)、普通 map、切片、字符串、布尔、数字。
// 上游是 JS 对象,`JSON.stringify` 之后由服务端进程写成 YAML,键序就是字面量顺序;这里用 *OrderedMap 保住这个顺序,
// 普通 map 按字典序输出(只出现在不影响行为的边角,例如 hosts)。
//
// 引号规则:只要值不是「一眼能看出是字符串」的,就加双引号。宁可多引几个,也不要让 Mihomo
// 把 `1.1.1.1` / `no-resolve` / `0x2024` 之类解析成别的类型。

// MarshalYAML 把配置写成 YAML 文本(结尾带换行)。顶层必须是对象。
func MarshalYAML(v any) ([]byte, error) {
	m, ok := asOrdered(v)
	if !ok {
		return nil, fmt.Errorf("mihomo: 配置顶层必须是对象,拿到 %T", v)
	}
	var b strings.Builder
	writeYAMLMap(&b, m, 0)
	return []byte(b.String()), nil
}

// asOrdered 把各种「键值对」都归一成有序对象;普通 map 的键按字典序。
func asOrdered(v any) (*OrderedMap, bool) {
	switch t := v.(type) {
	case *OrderedMap:
		return t, true
	case map[string]any:
		return sortedOrdered(t), true
	case map[string]string:
		m := map[string]any{}
		for k, s := range t {
			m[k] = s
		}
		return sortedOrdered(m), true
	}
	return nil, false
}

func sortedOrdered(m map[string]any) *OrderedMap {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := NewOrdered()
	for _, k := range keys {
		out.Set(k, m[k])
	}
	return out
}

func writeYAMLMap(b *strings.Builder, m *OrderedMap, indent int) {
	for _, k := range m.Keys() {
		writeYAMLEntry(b, k, m.vals[k], indent)
	}
}

func writeYAMLEntry(b *strings.Builder, key string, v any, indent int) {
	pad := strings.Repeat(" ", indent)
	name := yamlScalar(key)
	switch t := v.(type) {
	case *OrderedMap:
		if t.Len() == 0 {
			b.WriteString(pad + name + ": {}\n")
			return
		}
		b.WriteString(pad + name + ":\n")
		writeYAMLMap(b, t, indent+2)
	case map[string]any:
		writeYAMLEntry(b, key, sortedOrdered(t), indent)
	case map[string]string:
		o := map[string]any{}
		for kk, vv := range t {
			o[kk] = vv
		}
		writeYAMLEntry(b, key, sortedOrdered(o), indent)
	case []any:
		if len(t) == 0 {
			b.WriteString(pad + name + ": []\n")
			return
		}
		b.WriteString(pad + name + ":\n")
		writeYAMLList(b, t, indent+2)
	case []string:
		list := make([]any, len(t))
		for i, s := range t {
			list[i] = s
		}
		writeYAMLEntry(b, key, list, indent)
	case nil:
		b.WriteString(pad + name + ": null\n")
	default:
		b.WriteString(pad + name + ": " + yamlScalarValue(v) + "\n")
	}
}

func writeYAMLList(b *strings.Builder, list []any, indent int) {
	pad := strings.Repeat(" ", indent)
	for _, item := range list {
		switch t := item.(type) {
		case *OrderedMap:
			writeYAMLListItem(b, t, indent)
		case map[string]any:
			writeYAMLListItem(b, sortedOrdered(t), indent)
		case nil:
			b.WriteString(pad + "- null\n")
		default:
			b.WriteString(pad + "- " + yamlScalarValue(item) + "\n")
		}
	}
}

// writeYAMLListItem 写列表里的一个对象:第一个键跟在 `- ` 后面,其余按普通缩进(读起来最接近上游的写法)。
func writeYAMLListItem(b *strings.Builder, m *OrderedMap, indent int) {
	pad := strings.Repeat(" ", indent)
	keys := m.Keys()
	if len(keys) == 0 {
		b.WriteString(pad + "- {}\n")
		return
	}
	first := keys[0]
	// 第一个值本身是对象或非空数组时没法跟在 `- ` 后面,单独一行写 `-`。
	if !isYAMLScalar(m.vals[first]) {
		b.WriteString(pad + "-\n")
		writeYAMLMap(b, m, indent+2)
		return
	}
	b.WriteString(pad + "- " + yamlScalar(first) + ": " + yamlScalarValue(m.vals[first]) + "\n")
	rest := NewOrdered()
	for _, k := range keys[1:] {
		rest.Set(k, m.vals[k])
	}
	writeYAMLMap(b, rest, indent+2)
}

func isYAMLScalar(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case *OrderedMap, map[string]any, map[string]string:
		return false
	case []any:
		return len(t) == 0
	case []string:
		return len(t) == 0
	}
	return true
}

func yamlScalarValue(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return yamlScalar(t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case json.Number:
		return t.String()
	case *OrderedMap:
		return "{}"
	case []any:
		return "[]"
	case []string:
		return "[]"
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(rv.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(rv.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(rv.Float(), 'f', -1, 64)
	case reflect.String:
		return yamlScalar(rv.String())
	}
	return yamlScalar(fmt.Sprint(v))
}
