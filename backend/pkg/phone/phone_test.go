package phone

import "testing"

func TestNormalizeCNMobile(t *testing.T) {
	ok := map[string]string{
		"13800000000":      "13800000000",
		" 13800000000 ":    "13800000000",
		"+8613800000000":   "13800000000",
		"+86 19912345678":  "19912345678",
		" +8615000000000 ": "15000000000",
	}
	for in, want := range ok {
		got, valid := NormalizeCNMobile(in)
		if !valid || got != want {
			t.Errorf("NormalizeCNMobile(%q) = %q, %v; want %q", in, got, valid, want)
		}
	}
	for _, in := range []string{"", "1380000000", "138000000000", "12800000000", "8613800000000", "+85212345678",
		"138 0000 0000", "138-0000-0000", "1380000000a", "138****0000"} {
		if got, valid := NormalizeCNMobile(in); valid {
			t.Errorf("NormalizeCNMobile(%q) = %q, should be rejected", in, got)
		}
	}
}

func TestMask(t *testing.T) {
	if got := Mask("13800000000"); got != "138****0000" {
		t.Errorf("Mask = %q", got)
	}
	if got := Mask("12345"); got != "*****" {
		t.Errorf("Mask short = %q", got)
	}
	if got := MaskEmail("alice@example.com"); got != "a***@example.com" {
		t.Errorf("MaskEmail = %q", got)
	}
	if got := MaskEmail("not-an-email"); got != "************" {
		t.Errorf("MaskEmail invalid = %q", got)
	}
}
