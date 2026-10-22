---
team: default
repo: gp-editor
topic: playback
doc_type: adr
date: 2026-10-22
relates:
  - path: docs/repository/gp-editor/adr/0001-desktop-stack-and-readers.md
    why: 같은 앱의 스택 결정. 재생은 그 위에 얹은 네이티브 모듈이다
---

# 0002. 재생: AVMIDIPlayer 대신 AVAudioEngine 하나 + MIDISynth (GPMidiEngine)

## 결정

- 재생은 `apps/desktop/macos/GPEditor-macOS/GPMidiEngine.h/.m` 이 맡는다. AVAudioEngine 하나에
  MIDISynth(`kAudioUnitSubType_MIDISynth`, 사운드 뱅크는 처음 한 번만, 다른 뱅크를 고를 때만 다시 로드) →
  PeakLimiter → main mixer(출력 0.8) 를 잇고, 곡마다 AVAudioSequencer 를 만든다. 곡이 시작할 때 쓰는 프로그램은
  첫 음 전에 미리 올린다(`kAUMIDISynthProperty_EnablePreload`).
- 재생 중 트랙 악기를 바꾸면 곡을 다시 MIDI 로 만들지 않고 그 MIDI 채널에 프로그램 체인지만 보낸다
  (`apps/desktop/src/lib/live-programs.ts`, gp-core `buildChannelRoutes`). 채널 구성이 바뀌는 경우(트랙이 채널을
  새로 받는 경우)만 다시 시작하고, 여러 번 바뀌면 마지막 변경 300 ms 뒤 한 번으로 묶는다
  (`apps/desktop/src/hooks/use-playback.ts`).
- 정지 60초 뒤 엔진과 뱅크를 놓는다. 다음 재생은 뱅크를 다시 로드한다.
- `GPPlayer` 의 메서드는 자기 직렬 큐에서 돈다. 뱅크 로드가 창을 멈추지 않는다. 사운드 뱅크 추가의 열기 패널만
  메인 큐로 간다.
- 메모리 상한은 바꾸지 않았다. 사용자가 "프로세스 메모리 제한이 걸려 있다면 두 배로" 를 요청했지만, 걸려 있는
  상한은 Hermes JS 힙 3 GB 뿐이고(`react-native-macos` 의 `HermesInstance.cpp`, `withMaxHeapSize(3072 << 20)`)
  macOS 는 앱별 메모리 상한이 없다. 앱 사용량(아래 측정)은 그 상한과 거리가 멀다.

## 근거 (확인된 것만)

- 요청 (2026-10-22): "악기 선택할때마다 랙이 너무 걸리고 메모리가 갑자기 터지는 현상이 있어", "메모리와 cpu 가
  왜 클릭되는지 확인하고, 최적화 작업을 진행해줘".
- 원인 측정 (이전 구조, 8ac31ac): 악기를 고를 때마다 곡 전체를 `renderMidi` 로 다시 만들고(Hermes 에서
  nightwish.gp5 83.6 ms, 224마디 곡 218 ms) 새 AVMIDIPlayer 가 뱅크를 다시 읽었다. 클릭마다 CPU 25–45 %.
- 바꾼 뒤 측정 (Release 앱, QA 드라이버, nightwish.gp5): 뱅크 로드 245 ms 한 번, 재생 시작 3 ms, 프로그램 체인지
  0 ms. 재생 중 악기 16번 클릭 동안 CPU 5–30 %, RSS 395–430 MB 에서 평평. 재생 시작 때 RSS +227 MB 는
  GeneralUser GS 뱅크(MIDISynth 가 뱅크 전체를 메모리에 둔다, macOS 뱅크는 약 30 MB).
- 정지 60초 뒤 해제 확인 (vmmap): physical footprint 282 MB → 147 MB. RSS 는 비운 malloc 페이지
  ("MALLOC_SMALL (empty)") 를 계속 세어서 거의 그대로 보인다.
- 클리핑: GeneralUser GS 로 nightwish.gp5 를 재생할 때 최대 1.73 (30초에 1721 샘플 클리핑) → 리미터 + 출력 0.8 로
  최대 0.80, 클리핑 0.
- 악보 클릭: 행 하나를 다시 그리는 JS 비용은 Hermes 에서 약 3 ms (score+tab, 행당 명령 413개), 넥 0.2 ms.
  커서가 움직일 때 트랙 목록·곡 정보·넥 설정이 다시 그려지지 않게 memo 로 막았다.
- gp-editor 커밋: 6e8c654 (GPMidiEngine, 실시간 프로그램 체인지), f7bf4f7 (패널 memo), a025abb (플레이어 큐).

## 미결

- GeneralUser GS 를 쓰면 재생 중 메모리가 약 220 MB 늘어난다. 줄이려면 더 작은 뱅크(macOS 뱅크)를 고르는 수밖에
  없다.

## 에피소드
- 배경: (에이전트) 2026-10-22 사용자 보고 — 악기 선택 때 랙과 메모리 급증, 악기 패널 역할 구분, 메모리 상한 두 배
  요청. 측정 후 재생 엔진을 바꿨다 (6e8c654, a025abb). 상한은 원인이 아니어서 바꾸지 않았다.
- 선택지와 결정: (사람)
- 이유: (사람)
- 그때의 생각: (사람)
- 결과:
