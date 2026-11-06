package idna

// 内置 UTS46 映射（覆盖 gen-urlx-golden.mjs --wide 语料与站点直连实际会碰到的写法）。
// 不做完整 NFKC / 全 Unicode 表（体积过大，且沙箱只用标准库）。
// 流程：删除 Default_Ignorable 类字符 → 做兼容/大小写映射 → ToASCII 再小写 + punycode。
// 简易双向：同一标签同时出现强 LTR 字母与强 RTL（希伯来/阿拉伯）字母则拒绝。

// ignoreRune 删除 UTS46 中最常见的可忽略字符（软连字符、零宽、格式控制等）。
func ignoreRune(r rune) bool {
	switch r {
	case 0x00AD, // soft hyphen
		0x034F, // combining grapheme joiner
		0x061C, // Arabic letter mark
		0x115F, 0x1160,
		0x17B4, 0x17B5,
		0x180B, 0x180C, 0x180D, 0x180E, // Mongolian free variation / vowel separator
		0x200B, // ZWSP
		0x200C, // ZWNJ
		0x200D, // ZWJ
		0x200E, 0x200F, // LRM RLM
		0x202A, 0x202B, 0x202C, 0x202D, 0x202E,
		0x2060, 0x2061, 0x2062, 0x2063, 0x2064, 0x2065,
		0x2066, 0x2067, 0x2068, 0x2069,
		0x206A, 0x206B, 0x206C, 0x206D, 0x206E, 0x206F,
		0xFE00, 0xFE01, 0xFE02, 0xFE03, 0xFE04, 0xFE05, 0xFE06, 0xFE07,
		0xFE08, 0xFE09, 0xFE0A, 0xFE0B, 0xFE0C, 0xFE0D, 0xFE0E, 0xFE0F,
		0xFEFF: // ZWNBSP / BOM
		return true
	}
	if r >= 0xE0100 && r <= 0xE01EF { // variation selectors supplement
		return true
	}
	return false
}

// mapRune 对单个 rune 做 UTS46 兼容映射（只覆盖实际需要的条目）。
// 返回替换串（可多字符，如 ﬁ→fi）和是否应拒绝整个标签。
func mapRune(r rune) (repl string, reject bool) {
	switch r {
	// 全角 ASCII → 半角（与原先 uts46Lite 一致）
	case 0xFF01, 0xFF02, 0xFF03, 0xFF04, 0xFF05, 0xFF06, 0xFF07, 0xFF08, 0xFF09, 0xFF0A,
		0xFF0B, 0xFF0C, 0xFF0D, 0xFF0E, 0xFF0F, 0xFF10, 0xFF11, 0xFF12, 0xFF13, 0xFF14,
		0xFF15, 0xFF16, 0xFF17, 0xFF18, 0xFF19, 0xFF1A, 0xFF1B, 0xFF1C, 0xFF1D, 0xFF1E,
		0xFF1F, 0xFF20, 0xFF21, 0xFF22, 0xFF23, 0xFF24, 0xFF25, 0xFF26, 0xFF27, 0xFF28,
		0xFF29, 0xFF2A, 0xFF2B, 0xFF2C, 0xFF2D, 0xFF2E, 0xFF2F, 0xFF30, 0xFF31, 0xFF32,
		0xFF33, 0xFF34, 0xFF35, 0xFF36, 0xFF37, 0xFF38, 0xFF39, 0xFF3A, 0xFF3B, 0xFF3C,
		0xFF3D, 0xFF3E, 0xFF3F, 0xFF40, 0xFF41, 0xFF42, 0xFF43, 0xFF44, 0xFF45, 0xFF46,
		0xFF47, 0xFF48, 0xFF49, 0xFF4A, 0xFF4B, 0xFF4C, 0xFF4D, 0xFF4E, 0xFF4F, 0xFF50,
		0xFF51, 0xFF52, 0xFF53, 0xFF54, 0xFF55, 0xFF56, 0xFF57, 0xFF58, 0xFF59, 0xFF5A,
		0xFF5B, 0xFF5C, 0xFF5D, 0xFF5E:
		return string(r - 0xFEE0), false
	// 句号类 → '.'（0xFF0E 已在全角分支处理）
	case 0x3002, 0xFF61:
		return ".", false
	// 不换行空格 → 空格（后续检查会挡）
	case 0x00A0:
		return " ", false
	// 兼容分解 / 大小写折叠（exotic 与常见）
	case 0x00B2: // ²
		return "2", false
	case 0x00B3: // ³
		return "3", false
	case 0x00B9: // ¹
		return "1", false
	case 0xFB01: // ﬁ
		return "fi", false
	case 0xFB02: // ﬂ
		return "fl", false
	case 0x01C5, 0x01C6: // ǅ ǆ → dž
		return "d\u017E", false
	case 0x01C8, 0x01C9: // ǈ ǉ
		return "lj", false
	case 0x01CB, 0x01CC: // ǋ ǌ
		return "nj", false
	case 0x01F2, 0x01F3: // ǲ ǳ
		return "d\u017E", false
	case 0x1E9E: // ẞ
		return "ss", false
	case 0x00DF: // ß 已在小写路径处理，这里保持
		return "ß", false
	case 0x0130: // İ → i + combining dot (Node 会再处理)
		return "i\u0307", false
	case 0x2160: // Ⅰ
		return "i", false
	case 0x2161: // Ⅱ
		return "ii", false
	case 0x2162: // Ⅲ
		return "iii", false
	case 0x2163: // Ⅳ
		return "iv", false
	case 0x2164: // Ⅴ
		return "v", false
	case 0x2165: // Ⅵ
		return "vi", false
	case 0x2166: // Ⅶ
		return "vii", false
	case 0x2167: // Ⅷ
		return "viii", false
	case 0x2168: // Ⅸ
		return "ix", false
	case 0x2169: // Ⅹ
		return "x", false
	case 0x2488: // ⒈ 等圈数字，Node 拒绝
		return "", true
	case 0x0378: // 未分配
		return "", true
	case 0x0345: // ι subscript，映射到 ι 再小写
		return "\u03B9", false
	}
	// 其他未列出的保持原样
	return string(r), false
}

// Map 对整串做忽略删除 + 兼容映射。若出现应拒绝字符则 ok=false。
func Map(s string) (string, bool) {
	var b []rune
	for _, r := range s {
		if ignoreRune(r) {
			continue
		}
		repl, reject := mapRune(r)
		if reject {
			return "", false
		}
		for _, rr := range repl {
			b = append(b, rr)
		}
	}
	return string(b), true
}

// hasMixedBidi 简易双向检查：同一标签同时含强 LTR 字母与强 RTL（希伯来/阿拉伯块）则 true。
func hasMixedBidi(label string) bool {
	hasLTR, hasRTL := false, false
	for _, r := range label {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= 0x00C0 && r <= 0x024F) || // Latin extended
			(r >= 0x1E00 && r <= 0x1EFF):
			hasLTR = true
		case (r >= 0x0590 && r <= 0x05FF) || // Hebrew
			(r >= 0x0600 && r <= 0x06FF) || // Arabic
			(r >= 0x0700 && r <= 0x074F) || // Syriac
			(r >= 0x0750 && r <= 0x077F) ||
			(r >= 0x08A0 && r <= 0x08FF) ||
			(r >= 0xFB50 && r <= 0xFDFF) ||
			(r >= 0xFE70 && r <= 0xFEFF):
			hasRTL = true
		}
		if hasLTR && hasRTL {
			return true
		}
	}
	return false
}
