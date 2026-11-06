// Package idna 提供 UTS46 轻量映射 + 逐标签 punycode 的域名转 ASCII。
//
// 映射表内置（uts46.go）：覆盖软连字符 / 零宽 / 全角 / ﬁ ǆ ẞ ² 等常见写法，以及简易双向检查。
// 不做完整 NFKC 全表。xn-- 标签校验与原先一致（合法 punycode、解出非空、无常见禁用字符；
// 解出来只有 ASCII 的如 xn--a- 照收；含非 ASCII 的 xn-- 直接拒绝）。
// 中文 / 日文 / 西里尔 / emoji / 带音标拉丁 / 全角字母与 Node 一致。
package idna

import (
	"strings"
	"unicode/utf8"
)

// ToASCII 先做 Map（忽略 + 兼容映射），再逐标签小写 + punycode。
// 含空白 / 控制字符的标签原样保留（后面的标签检查会挡掉）。
func ToASCII(d string) (string, bool) {
	mapped, ok := Map(d)
	if !ok {
		return "", false
	}
	labels := strings.Split(mapped, ".")
	for i, l := range labels {
		if hasMixedBidi(l) {
			return "", false
		}
		// 标签级小写（ASCII 范围）
		l = strings.ToLower(l)
		labels[i] = l
		ascii := true
		for _, r := range l {
			if r > 0x7e {
				ascii = false
				break
			}
		}
		if ascii {
			if len(l) >= 4 && strings.EqualFold(l[:4], "xn--") && !validACELabel(l) {
				return "", false // UTS46:xn-- 标签必须能解码,且解出的字符里没有禁用字符
			}
			continue
		}
		if len(l) >= 4 && strings.EqualFold(l[:4], "xn--") {
			return "", false // UTS46:xn-- 开头的标签必须是纯 ASCII 的合法 punycode
		}
		enc, ok := punycodeEncode(l)
		if !ok {
			return "", false
		}
		labels[i] = "xn--" + enc
	}
	return strings.Join(labels, "."), true
}

// punycodeEncode 是 RFC 3492 的编码(不含 xn-- 前缀)。
func punycodeEncode(s string) (string, bool) {
	const (
		base, tMin, tMax, skew, damp = 36, 1, 26, 38, 700
		initialBias, initialN        = 72, 128
	)
	if !utf8.ValidString(s) {
		return "", false
	}
	runes := []rune(s)
	var out []byte
	for _, r := range runes {
		if r < 0x80 {
			out = append(out, byte(r))
		}
	}
	b := len(out)
	h := b
	if b > 0 {
		out = append(out, '-')
	}
	digit := func(d int) byte {
		if d < 26 {
			return byte('a' + d)
		}
		return byte('0' + d - 26)
	}
	adapt := func(delta, numPoints int, first bool) int {
		if first {
			delta /= damp
		} else {
			delta /= 2
		}
		delta += delta / numPoints
		k := 0
		for delta > ((base-tMin)*tMax)/2 {
			delta /= base - tMin
			k += base
		}
		return k + (base-tMin+1)*delta/(delta+skew)
	}
	n, delta, bias := initialN, 0, initialBias
	for h < len(runes) {
		m := int(^uint32(0) >> 1)
		for _, r := range runes {
			if int(r) >= n && int(r) < m {
				m = int(r)
			}
		}
		if m-n > (int(^uint32(0)>>1)-delta)/(h+1) {
			return "", false
		}
		delta += (m - n) * (h + 1)
		n = m
		for _, r := range runes {
			c := int(r)
			if c < n {
				delta++
			}
			if c == n {
				q := delta
				for k := base; ; k += base {
					t := k - bias
					if t < tMin {
						t = tMin
					} else if t > tMax {
						t = tMax
					}
					if q < t {
						break
					}
					out = append(out, digit(t+(q-t)%(base-t)))
					q = (q - t) / (base - t)
				}
				out = append(out, digit(q))
				bias = adapt(delta, h+1, h == b)
				delta = 0
				h++
			}
		}
		delta++
		n++
	}
	return string(out), true
}
