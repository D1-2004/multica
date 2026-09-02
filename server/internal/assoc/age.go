package assoc

import "time"

// AgeFrom turns a duration into seconds plus a Chinese phrase so the
// coordinator model does not have to do calendar math.
func AgeFrom(d time.Duration) (string, int64) {
	if d < 0 {
		d = 0
	}
	secs := int64(d.Seconds())
	switch {
	case d < time.Minute:
		return "刚刚", secs
	case d < time.Hour:
		m := int(d.Minutes())
		if m <= 1 {
			return "1分钟前", secs
		}
		return itoa(m) + "分钟前", secs
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h <= 1 {
			return "1小时前", secs
		}
		return itoa(h) + "小时前", secs
	default:
		days := int(d.Hours() / 24)
		if days <= 1 {
			return "1天前", secs
		}
		return itoa(days) + "天前", secs
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func matchedVia(id string, scene, event map[string]struct{}, window bool) string {
	_, s := scene[id]
	_, e := event[id]
	switch {
	case s && e:
		return "both"
	case s:
		return "scene"
	case e:
		return "event"
	case window:
		return "window"
	default:
		return "scene"
	}
}
