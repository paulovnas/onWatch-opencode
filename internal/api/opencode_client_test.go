package api

import "testing"

func TestNormalizeOpenCodeCookie(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "value_only", in: "abc123", want: "auth=abc123"},
		{name: "trimmed_value_only", in: "  abc123  ", want: "auth=abc123"},
		{name: "already_auth_cookie", in: "auth=abc123", want: "auth=abc123"},
		{name: "multiple_cookie_header", in: "session=foo; auth=abc123; theme=dark", want: "session=foo; auth=abc123; theme=dark"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := NormalizeOpenCodeCookie(tc.in)
			if got != tc.want {
				t.Fatalf("NormalizeOpenCodeCookie(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestOpenCodeClient_CookieNormalization(t *testing.T) {
	t.Parallel()

	client := NewOpenCodeClient("raw_cookie_value", nil)
	if got, want := client.GetCookieHeader(), "auth=raw_cookie_value"; got != want {
		t.Fatalf("NewOpenCodeClient cookie = %q, want %q", got, want)
	}

	client.SetCookieHeader("another_cookie_value")
	if got, want := client.GetCookieHeader(), "auth=another_cookie_value"; got != want {
		t.Fatalf("SetCookieHeader cookie = %q, want %q", got, want)
	}
}
