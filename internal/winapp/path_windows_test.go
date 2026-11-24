//go:build windows

package winapp

import "testing"

func TestPathEntries(t *testing.T) {
	t.Setenv("JIKU_TEST_ROOT", `C:\Users\me\AppData\Local`)
	dir := `C:\Users\me\AppData\Local\Programs\jikunode`
	cases := []struct {
		value, with, without string
		has                  bool
	}{
		{"", dir, "", false},
		{`C:\a;C:\b`, `C:\a;C:\b;` + dir, `C:\a;C:\b`, false},
		{`C:\a;`, `C:\a;` + dir, `C:\a`, false},
		{`C:\a;` + dir + `;C:\b`, `C:\a;` + dir + `;C:\b`, `C:\a;C:\b`, true},
		// Case, a trailing slash and %VARIABLES% still name the same folder.
		{`c:\users\ME\appdata\local\programs\JIKUNODE\`, `c:\users\ME\appdata\local\programs\JIKUNODE\`, "", true},
		{`%JIKU_TEST_ROOT%\Programs\jikunode;C:\b`, `%JIKU_TEST_ROOT%\Programs\jikunode;C:\b`, `C:\b`, true},
		// Another folder with a similar name is not ours.
		{dir + `-old`, dir + `-old;` + dir, dir + `-old`, false},
	}
	for _, c := range cases {
		if got := pathHas(c.value, dir); got != c.has {
			t.Errorf("pathHas(%q) = %v", c.value, got)
		}
		if got := pathWith(c.value, dir); got != c.with {
			t.Errorf("pathWith(%q) = %q, want %q", c.value, got, c.with)
		}
		if got := pathWithout(c.value, dir); got != c.without {
			t.Errorf("pathWithout(%q) = %q, want %q", c.value, got, c.without)
		}
	}
}
