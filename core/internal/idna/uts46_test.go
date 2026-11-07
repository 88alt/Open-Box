package idna

import "testing"

func TestMapAndToASCII(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"ﬁ.com", "fi.com", true},
		{"ǆ.com", "xn--d-toa.com", true}, // dž
		{"ẞ.com", "ss.com", true},
		{"².com", "2.com", true},
		{"a\u00ADb.com", "ab.com", true}, // soft hyphen
		{"a\u200Bb.com", "ab.com", true}, // ZWSP
		{"a\uFEFFb.com", "ab.com", true}, // FEFF
		{"a。b.com", "a.b.com", true},     // ideographic full stop
		{"ａｂｃ.com", "abc.com", true},     // fullwidth
		{"Ⅰ.com", "i.com", true},
		{"ה.com", "xn--8db.com", true}, // pure Hebrew OK
		{"הa.com", "", false},          // mixed bidi reject
		{"aה.com", "", false},
		{"⒈.com", "", false}, // circled number reject
		{"example.com", "example.com", true},
		{"中文.example", "xn--fiq228c.example", true},
	}
	for _, c := range cases {
		got, ok := ToASCII(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("ToASCII(%q) = %q,%v; want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}
