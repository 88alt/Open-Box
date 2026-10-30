package failover

import "encoding/json"

// jsonKey 把组序列化成字符串,用来判断两份计划里的同一个组有没有变。
func jsonKey(g Group) (string, error) {
	b, err := json.Marshal(g)
	return string(b), err
}
