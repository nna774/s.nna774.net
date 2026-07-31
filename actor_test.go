package main

import "testing"

// ブーストされた投稿のように、リモートが指定した URI をこちらから引く経路が
// ある。内部アドレスを引かせられないこと。
func TestIsFetchableURI(t *testing.T) {
	// 開発時は制限を掛けないので、本番想定に揃える。
	t.Setenv("ENV", "")

	for _, tt := range []struct {
		in   string
		want bool
	}{
		{"https://pawoo.net/users/x/statuses/1", true},
		{"http://example.com/notes/1", true},
		{"https://sub.example.co.jp/notes/1", true},

		{"", false},
		{"/relative/path", false},
		{"example.com/notes/1", false},
		{"file:///etc/passwd", false},
		{"ftp://example.com/x", false},

		// IP 直打ち。まともな実装はホスト名で連合する。
		{"http://127.0.0.1:8080/x", false},
		{"http://10.0.0.1/x", false},
		{"http://192.168.1.1/x", false},
		{"https://[::1]/x", false},
		// EC2 のインスタンスメタデータ。
		{"http://169.254.169.254/latest/meta-data/", false},
		// Lambda の Runtime API。
		{"http://localhost:9001/2018-06-01/runtime/invocation/next", false},
		{"http://LOCALHOST/x", false},
	} {
		if got := isFetchableURI(tt.in); got != tt.want {
			t.Errorf("isFetchableURI(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// ローカル検証は localhost 相手に行うため、開発時は引けなければならない。
func TestIsFetchableURIAllowsLocalhostInDevelopment(t *testing.T) {
	t.Setenv("ENV", "development")
	if !isFetchableURI("http://localhost:8080/u/nana/status/1") {
		t.Error("開発時は localhost を引けなければ検証ができない")
	}
}
