package groups

import "testing"

// 自写测试向量:match.go(normalizeForMatch / keywordMatches / parseDuration)与 expected.go(NormalizeExpectedStatus)。
// 期望值按下面的规则人工推算:
//   - normalizeForMatch:连续两个区域指示符(国旗 emoji,U+1F1E6..U+1F1FF)还原成两个小写 ASCII 字母并在两侧各补一个空格,
//     再整体转小写;单个区域指示符、三个连着时多出来的那个原样保留;
//   - keywordMatches(lower, kw):关键词先过 normalizeForMatch 再去首尾空白,空了就不匹配;纯 ASCII 字母的 2~3 位短码
//     (us / hk / jp)要求前面和后面都不是字母(数字、符号、中日文、开头结尾都可以),大小写不分;cn 例外:cn 后面紧跟 2 且
//     2 的后面不是数字时不算(「CN2」线路),cn22 / cn1 照常算;出现多次时任何一次满足就算;其余关键词按子串匹配;
//   - parseDuration(毫秒):trim 后为空得 0;纯数字当秒;否则取出所有「数字加 ms / h / m / s」(小写、可带小数、不要求连续
//     也不要求整串匹配,ms 优先于 m)累加,一个也没有得 0;
//   - NormalizeExpectedStatus:没写 / null / 空白 / "*" 返回 ("", true);不是字符串返回 ("", false);按 / 或 , 切段,空段跳过,
//     全空 → 失败;每段 trim 后是三位数字,或「三位-三位」(横线两侧可有 \t \n \f \r 空格,不含 \v),起点不小于 100、终点
//     不大于 599、终点不小于起点,起止相同写成单个;合法段按原顺序用 / 连接,不排序不去重;一段写错整体失败。

func TestNormalizeForMatchVectors(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"Hong Kong 01", "hong kong 01"},
		{"香港 HK-01", "香港 hk-01"},
		{"\U0001F1ED\U0001F1F0", " hk "},                       // 🇭🇰
		{"\U0001F1FA\U0001F1F8 US-1", " us  us-1"},             // 🇺🇸 后面的空格保留
		{"\U0001F1ED\U0001F1F0香港", " hk 香港"},                   // 国旗后直接接中文
		{"A\U0001F1EF\U0001F1F5B", "a jp b"},                   // 🇯🇵 夹在字母中间,两侧补的空格隔开了 token
		{"\U0001F1E6\U0001F1E7\U0001F1E8", " ab \U0001F1E8"},   // 三个连着:前两个成对,第三个单独留下
		{"\U0001F1ED", "\U0001F1ED"},                           // 单个不动
		{"\U0001F1ED x \U0001F1F0", "\U0001F1ED x \U0001F1F0"}, // 不相邻不成对
		{"\U0001F1E6\U0001F1FF", " az "},                       // 区间两端
		{"\U0001F1E5\U0001F1E6", "\U0001F1E5\U0001F1E6"},       // U+1F1E5 不是区域指示符
		{"\U0001F1FF\U0001F200", "\U0001F1FF\U0001F200"},       // U+1F200 不是
		{"ÄÖ", "äö"},
	} {
		if got := normalizeForMatch(c.in); got != c.want {
			t.Errorf("normalizeForMatch(%q)=%q 要 %q", c.in, got, c.want)
		}
	}
}

func TestKeywordMatchesVectors(t *testing.T) {
	for _, c := range []struct {
		name, text, kw string
		want           bool
	}{
		// 空关键词
		{"空", "hk", "", false},
		{"空白", "hk", "  \t", false},
		{"U+3000", "hk", "\u3000", false},
		// 短码
		{"短码单独", "hk", "hk", true},
		{"短码后跟数字", "hk01 node", "hk", true},
		{"短码前后符号", "[hk]-1", "hk", true},
		{"短码下划线", "_us_", "us", true},
		{"短码前是中文", "香港hk01", "hk", true},
		{"短码后是中文", "hk香港", "hk", true},
		{"短码前是字母", "bus", "us", false},
		{"短码后是字母", "usa", "us", false},
		{"短码前后都是字母", "abusb", "us", false},
		{"短码第一处不行第二处行", "bus us", "us", true},
		{"短码第一处后跟字母第二处行", "uso us", "us", true},
		{"短码在结尾", "node us", "us", true},
		{"文本大写也认", "US 01", "us", true},
		{"文本大写前后字母", "BUS", "us", false},
		{"关键词大写", "us 01", "US", true},
		{"三位短码", "hkg-1", "hkg", true},
		{"三位短码后跟字母", "hkgx", "hkg", false},
		{"两位短码不能是三位的前缀", "hkg", "hk", false},
		{"短码不跨词", "u s", "us", false},
		{"短码前是 z", "zus", "us", false},
		{"短码后是 z", "usz", "us", false},
		{"短码前是 a", "aus", "us", false},
		// 国旗关键词折成短码后同样按 token 判
		{"国旗关键词匹配国旗节点", " hk  香港", "\U0001F1ED\U0001F1F0", true},
		{"国旗关键词不匹配更长的字母串", "hkt", "\U0001F1ED\U0001F1F0", false},
		// cn 例外
		{"cn 单独", "cn", "cn", true},
		{"cn 后跟 1", "cn1", "cn", true},
		{"cn2 不算", "cn2", "cn", false},
		{"cn2 后跟空格不算", "cn2 线路", "cn", false},
		{"cn2 后跟字母不算", "cn2a", "cn", false},
		{"cn22 照常算", "cn22", "cn", true},
		{"cn29 照常算", "cn29", "cn", true},
		{"cn20 之后的数字上下界", "cn2" + "0", "cn", true},
		{"cn20 照常算", "cn20", "cn", true},
		{"cn2 之后还有 cn", "cn2 cn", "cn", true},
		{"cn2 在结尾", "node cn2", "cn", false},
		{"cn 前面有中文", "中国cn", "cn", true},
		{"cn 大写关键词", "cn", "CN", true},
		{"cn 例外只给 cn", "us2", "us", true},
		{"cn 前是字母", "scn", "cn", false},
		{"cn 后是字母", "cnx", "cn", false},
		// 子串
		{"一位当子串", "abc", "b", true},
		{"四位当子串", "xjapanx", "japa", true},
		{"含数字当子串", "xus1x", "us1", true},
		{"含符号当子串", "a-us-b", "-us-", true},
		{"中文子串", "香港 01", "香港", true},
		{"子串大小写", "hong kong", "HONG", true},
		{"子串含空格", "hong kong 01", "hong kong", true},
		{"子串前后空白被去掉", "hong kong", "  kong  ", true},
		{"子串不存在", "hong kong", "japan", false},
		{"关键词比文本长", "hk", "hkkk", false},
		{"非 ASCII 字母不算短码", "éa", "éa", true},
	} {
		if got := keywordMatches(normalizeForMatch(c.text), c.kw); got != c.want {
			t.Errorf("%s:keywordMatches(%q, %q)=%v 要 %v", c.name, c.text, c.kw, got, c.want)
		}
	}
	// 直接传大写的 lower 参数:短码按不分大小写比较
	if keywordMatches("AUS", "us") || keywordMatches("USA", "us") || keywordMatches("USZ", "us") || keywordMatches("ZUS", "us") {
		t.Error("相邻的大写字母 A / Z 也算字母")
	}
	if !keywordMatches("US 01", "us") || keywordMatches("BUS", "us") {
		t.Error("lower 参数里的大写也该按不分大小写判")
	}
}

func TestParseDurationVectors(t *testing.T) {
	for _, c := range []struct {
		in   string
		want float64
	}{
		{"", 0},
		{"   ", 0},
		{"\u3000\ufeff", 0},
		{"0", 0},
		{"90", 90000},
		{"00090", 90000},
		{" 90 ", 90000},
		{"5m", 300000},
		{"1h", 3600000},
		{"90s", 90000},
		{"250ms", 250},
		{"1h30m", 5400000},
		{"1h30m15s", 5415000},
		{"1h30m15s250ms", 5415250},
		{"1.5h", 5400000},
		{"0.5s", 500},
		{"1h 30m", 5400000},
		{"1ms1s", 1001},
		{"5ms", 5},
		{" 5m ", 300000},
		{"5m extra 3s", 303000},
		{"x5m", 300000},
		{"5m5m", 600000},
		{"abc", 0},
		{"5x", 0},
		{"5M", 0},
		{"5S", 0},
		{"1e3", 0},
		{"m", 0},
		{"5 m", 0},
		{"٣", 0},
		{"1.", 0},
		{".5s", 5000},
	} {
		if got := parseDuration(c.in); got != c.want {
			t.Errorf("parseDuration(%q)=%v 要 %v", c.in, got, c.want)
		}
	}
}

func TestNormalizeExpectedStatusVectors(t *testing.T) {
	for _, c := range []struct {
		name string
		in   any
		want string
		ok   bool
	}{
		{"nil", nil, "", true},
		{"空串", "", "", true},
		{"空白", "  \t", "", true},
		{"星号", "*", "", true},
		{"星号带空白", " * ", "", true},
		{"数字", 200.0, "", false},
		{"布尔", true, "", false},
		{"数组", []any{"200"}, "", false},
		{"对象", map[string]any{"a": "200"}, "", false},
		{"单个", "200", "200", true},
		{"边界 100", "100", "100", true},
		{"边界 599", "599", "599", true},
		{"区间", "200-299", "200-299", true},
		{"区间横线两侧空白", "200 - 299", "200-299", true},
		{"区间横线两侧 tab 与换行", "200\t-\n299", "200-299", true},
		{"区间横线两侧换页回车", "200\f-\r299", "200-299", true},
		{"起止相同写成单个", "200-200", "200", true},
		{"逗号", "200,302", "200/302", true},
		{"斜杠", "200/302", "200/302", true},
		{"斜杠逗号混用", "200,302/404", "200/302/404", true},
		{"段两侧空白", " 200 / 302 ", "200/302", true},
		{"区间加单个", "200-299/302", "200-299/302", true},
		{"空段跳过", "200,,302", "200/302", true},
		{"首尾分隔符", ",200/", "200", true},
		{"只有空白的段跳过", "200, ,302", "200/302", true},
		{"不排序", "302,200", "302/200", true},
		{"不去重", "200,200", "200/200", true},
		{"NBSP 被 TrimSpace 去掉", "\u00a0200\u00a0", "200", true},
		{"U+0085 被去掉", "\u0085200", "200", true},
		{"一段整体 trim 后再比对", "200,\u3000302\u3000", "200/302", true},
		// 写错
		{"只有分隔符", "/", "", false},
		{"只有逗号", ",,", "", false},
		{"低于 100", "99", "", false},
		{"三位但低于 100", "099", "", false},
		{"区间起点低于 100", "099-200", "", false},
		{"高于 599", "600", "", false},
		{"两位", "20", "", false},
		{"四位", "2000", "", false},
		{"字母", "abc", "", false},
		{"2xx", "2xx", "", false},
		{"缺终点", "200-", "", false},
		{"缺起点", "-200", "", false},
		{"双横线", "200--299", "", false},
		{"三段横线", "200-299-399", "", false},
		{"倒置", "299-200", "", false},
		{"终点超界", "200-600", "", false},
		{"起点超界", "99-200", "", false},
		{"段内空格", "200 301", "", false},
		{"垂直制表不算横线两侧空白", "200\v-299", "", false},
		{"全角数字", "２００", "", false},
		{"一段错整体错", "200,abc,302", "", false},
		{"U+FEFF 不 trim", "\ufeff200", "", false},
		{"负号", "-200--100", "", false},
		{"小数", "200.5", "", false},
		{"加号", "+200", "", false},
	} {
		got, ok := NormalizeExpectedStatus(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("%s:NormalizeExpectedStatus(%#v)=(%q,%v) 要 (%q,%v)", c.name, c.in, got, ok, c.want, c.ok)
		}
	}
}
