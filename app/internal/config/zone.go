package config

import (
	"path/filepath"
	"strings"
	"time"
)

// localtimeLink 는 이 머신의 시간대 파일을 가리키는 링크다 (macOS 는 /var/db/timezone/zoneinfo/…, Linux 는 /usr/share/zoneinfo/…).
const localtimeLink = "/etc/localtime"

// zoneinfoDir 는 시간대 파일 경로에서 IANA 이름 앞에 오는 디렉토리다.
const zoneinfoDir = "zoneinfo/"

// QueryTimeZone 은 OpenSearch 질의의 time_zone 에 넘길 시간대다. 이름(IANA, Asia/Seoul)이 있으면 그 이름이다 — 고정 오프셋을
// 넘기면 DST 를 넘는 date math(now-90d/d)·오프셋 없는 시각 경계가 한 시간 어긋난다. 이름이 없는 시간대(이름을 못 찾은
// time.Local)만 at 시각의 오프셋(+09:00)으로 물러난다. time.Local 의 이름은 "Local" 이거나 TZ 값 그대로(zoneinfo 경로일 수
// 있다)라 넘기지 않는다 — Load 가 이름을 찾았으면 이미 이름이 붙은 Location 이다.
func (c Config) QueryTimeZone(at time.Time) string {
	loc := c.Location
	if loc == nil {
		loc = time.UTC
	}
	if name := loc.String(); loc != time.Local && name != "" {
		return name
	}
	return at.In(loc).Format("-07:00")
}

// machineLocation 은 이 머신의 시간대다. IANA 이름을 찾았고 그 시간대가 local(이 프로세스의 time.Local)과 같으면 이름이 붙은
// Location 을, 아니면 local 그대로 돌려준다 — TZ="" 처럼 Go 가 링크와 다르게 읽은 경우에 날짜 계산이 바뀌지 않게.
func machineLocation(getenv func(string) string, readlink func(string) (string, error), local *time.Location) *time.Location {
	name := machineZoneName(getenv, readlink)
	if name == "" {
		return local
	}
	loc, err := time.LoadLocation(name)
	if err != nil || !sameOffsets(loc, local) {
		return local
	}
	return loc
}

// machineZoneName 은 TZ(이름, ":이름", zoneinfo 경로), 없으면 /etc/localtime 링크에서 읽은 IANA 이름이다. 못 찾으면 "".
func machineZoneName(getenv func(string) string, readlink func(string) (string, error)) string {
	if tz := strings.TrimPrefix(strings.TrimSpace(getenv("TZ")), ":"); tz != "" {
		if !filepath.IsAbs(tz) {
			return tz
		}
		return zoneUnderZoneinfo(tz)
	}
	target, err := readlink(localtimeLink)
	if err != nil {
		return ""
	}
	return zoneUnderZoneinfo(target)
}

// zoneUnderZoneinfo 는 …/zoneinfo/Asia/Seoul 꼴 경로의 IANA 이름이다. zoneinfo 아래가 아니면 "".
func zoneUnderZoneinfo(path string) string {
	_, name, ok := strings.Cut(filepath.ToSlash(path), zoneinfoDir)
	if !ok {
		return ""
	}
	return name
}

// sameOffsets 는 두 시간대가 올해 겨울·여름에 같은 오프셋인가다 (DST 규칙까지 같은지 보는 근사).
func sameOffsets(a, b *time.Location) bool {
	year := time.Now().Year()
	for _, month := range []time.Month{time.January, time.July} {
		at := time.Date(year, month, 15, 12, 0, 0, 0, time.UTC)
		_, offA := at.In(a).Zone()
		_, offB := at.In(b).Zone()
		if offA != offB {
			return false
		}
	}
	return true
}
