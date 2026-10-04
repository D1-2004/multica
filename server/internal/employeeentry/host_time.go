package employeeentry

import "time"

// HostZone is the zone of every Host timestamp an employee model reads:
// Asia/Shanghai, a fixed +08:00 offset (no daylight saving), so rendering
// never depends on the tzdata installed on a replica.
var HostZone = time.FixedZone("Asia/Shanghai", 8*60*60)

// HostTime returns t in HostZone. JSON encodes it as RFC 3339 with an
// explicit +08:00 offset, so a model never reads a bare UTC clock as local.
// The instant is unchanged, so ordering and comparisons are unaffected.
func HostTime(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.In(HostZone)
}

// HostClock is the short local rendering for Host fact text, for example
// "10-03 19:34". Callers that state the zone once per block use it per line.
func HostClock(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return HostTime(t).Format("01-02 15:04")
}

// HostStamp renders t with its date, minute and explicit offset, for example
// "2026-10-03 19:34 +08:00".
func HostStamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return HostTime(t).Format("2006-01-02 15:04 -07:00")
}
