package assoc

import (
	"testing"
	"time"
)

func TestAgeFrom(t *testing.T) {
	t.Parallel()
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "刚刚"},
		{time.Minute, "1分钟前"},
		{3 * time.Minute, "3分钟前"},
		{time.Hour, "1小时前"},
		{5 * time.Hour, "5小时前"},
		{24 * time.Hour, "1天前"},
		{72 * time.Hour, "3天前"},
	}
	for _, tc := range cases {
		got, secs := AgeFrom(tc.d)
		if got != tc.want {
			t.Fatalf("age(%v)=%q want %q", tc.d, got, tc.want)
		}
		if secs != int64(tc.d.Seconds()) {
			t.Fatalf("seconds(%v)=%d", tc.d, secs)
		}
	}
}
