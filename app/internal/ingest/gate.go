package ingest

import "fmt"

// 게이트는 색인하지 않을 파일을 거른다: MEMORY_MAX_FILE_BYTES 초과, 바이너리(앞부분의 NUL 바이트). 데몬(ScanHot·Prepare)·
// ingest_path·audit 가 모두 이 규칙 하나를 거친다 — ingest_path 만 규칙이 다르면 큰 파일은 "적재했다" 고 한 뒤 다음 사이클에
// 데몬이 지우고, 바이너리는 데몬이 다시 보지 않아(stat 이 그대로) 영영 색인에 남는다.

// reasonBinary 는 바이너리로 본 파일의 사유다 (데몬 로그 [reconcile] skip <path> (binary content)).
const reasonBinary = "binary content"

// SkipError 는 게이트에 걸려 색인하지 않는 파일이다. 데몬은 skip 으로 세고, ingest_path 는 건너뛴 파일로 알린다.
type SkipError struct {
	Rel    string
	Reason string
}

func (e *SkipError) Error() string {
	return fmt.Sprintf("%s: 색인하지 않습니다 (%s)", e.Rel, e.Reason)
}

// Gate 는 색인 게이트다. 걸리면 사유, 아니면 "". 크기는 stat 으로 먼저 보고(내용을 읽지 않는다), 그다음 앞부분을 읽어 바이너리를 본다.
func Gate(abs string, size, maxFileBytes int64) (string, error) {
	if reason := sizeGate(size, maxFileBytes); reason != "" {
		return reason, nil
	}
	binary, err := IsProbablyBinary(abs)
	if err != nil {
		return "", fmt.Errorf("binary sniff %s: %w", abs, err)
	}
	if binary {
		return reasonBinary, nil
	}
	return "", nil
}

// sizeGate 는 크기 게이트다. 스캔은 내용을 읽지 않으므로 이것만 본다.
func sizeGate(size, maxFileBytes int64) string {
	if size > maxFileBytes {
		return fmt.Sprintf("size %d > %d", size, maxFileBytes)
	}
	return ""
}
