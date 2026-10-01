package browse

import "testing"

func TestParseDroppedPath(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{`/tmp/My File.txt`, `/tmp/My File.txt`},
		{`/tmp/My\ File.txt `, `/tmp/My File.txt`},
		{`'/tmp/O'\''Brien.txt'`, `/tmp/O'Brien.txt`},
		{`'/tmp/a\ b'`, `/tmp/a\ b`},
		{`"/tmp/a\ b"`, `/tmp/a\ b`},
		{`"/tmp/a\"b"`, `/tmp/a"b`},
		{`/tmp/\(雪\).txt`, `/tmp/(雪).txt`},
		{`"file://localhost/tmp/My%20File.txt"`, `/tmp/My File.txt`},
		{`file:///tmp/%E9%9B%AA.txt`, `/tmp/雪.txt`},
		{`'/tmp/$(touch nope);$HOME'`, `/tmp/$(touch nope);$HOME`},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParsePath(tc.input)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	for _, input := range []string{"", "\n", "/tmp/a\n/tmp/b", "/tmp/a\x00b", `'/tmp/a' '/tmp/b'`, `/tmp/a\ b /tmp/c`, `'/tmp/a`, `/tmp/a\`, `file://elsewhere/tmp/a`, `file:///tmp/%GG`, `file:///tmp/%00`, `file:///tmp/a?query`, `''`} {
		t.Run("invalid "+input, func(t *testing.T) {
			if p, err := ParsePath(input); err == nil {
				t.Fatalf("accepted %q as %q", input, p)
			}
		})
	}
}
