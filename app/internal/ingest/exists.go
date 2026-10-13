package ingest

import "os"

// ExistsAsSpelled 는 rel(meta-repo 기준 posix 경로)의 파일이 디스크에 그 철자 그대로 있는가다 — 색인 범위 밖이어도 된다.
// 링크가 살아 있는지 볼 때 쓴다. APFS 는 대소문자를 구분하지 않아 os.Stat 만으로는 철자가 틀린 링크도 있다고 나오는데,
// GitHub(대소문자 구분)에서는 깨진 링크라 디스크의 철자(NFC)가 rel 과 같을 때만 있다고 본다. meta-repo 밖의 경로는 없다고 본다.
func ExistsAsSpelled(root, rel string) bool {
	abs, inside, err := ResolveInsideRoot(root, rel)
	if err != nil {
		return false
	}
	if _, err := os.Stat(abs); err != nil {
		return false
	}
	onDisk, found := onDiskSpelling(root, inside)
	return found && CanonicalRel(onDisk) == CanonicalRel(inside)
}
