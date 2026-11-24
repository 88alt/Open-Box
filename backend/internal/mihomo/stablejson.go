package mihomo

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
)

// StableJSON 对应上游 v0.1.462 Node 的 Ne:对象的键按字典序、值为 nil 的键整个不写(JS 的 undefined)、
// 数组保持顺序、字符串不转义 < > &。给缓存键和「输入指纹」用:同样的内容不管键序都得到同一个串。
//
// 只处理这里会遇到的形状:nil、bool、数字、字符串、[]any、[]string、map[string]any;
// 别的类型(结构体等)交给 encoding/json,那样键序是结构体字段序,调用方要自己保证稳定。
func StableJSON(v any) string {
	var b bytes.Buffer
	writeStableJSON(&b, v)
	return b.String()
}

func writeStableJSON(b *bytes.Buffer, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeStableJSON(b, e)
		}
		b.WriteByte(']')
	case []string:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSONScalar(b, e)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k, val := range x {
			if val != nil {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSONScalar(b, k)
			b.WriteByte(':')
			writeStableJSON(b, x[k])
		}
		b.WriteByte('}')
	case string, bool, int, int64, float64, json.Number:
		writeJSONScalar(b, x)
	default:
		writeJSONScalar(b, v)
	}
}

// writeJSONScalar 写一个标量或兜底值:不转义 < > &(与 JS 的 JSON.stringify 一致)。
func writeJSONScalar(b *bytes.Buffer, v any) {
	if s, ok := v.(string); ok {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(s); err == nil {
			b.Write(bytes.TrimRight(buf.Bytes(), "\n"))
			return
		}
		b.WriteString(`""`)
		return
	}
	if n, ok := v.(int); ok {
		b.WriteString(strconv.Itoa(n))
		return
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err == nil {
		b.Write(bytes.TrimRight(buf.Bytes(), "\n"))
		return
	}
	b.WriteString("null")
}

// nonNilStrings 把 nil 切片换成空切片:JSON 里是 [] 而不是 null(上游的数组一律是 []）。
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
