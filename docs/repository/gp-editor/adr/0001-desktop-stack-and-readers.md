---
team: default
repo: gp-editor
topic: desktop-stack
doc_type: adr
date: 2026-10-22
---

# 0001. Guitar Pro 편집기: react-native-macos 데스크톱 + TuxGuitar 포팅 리더

## 결정

- 앱: React Native 0.83.10 + react-native-macos 0.83.0 (`main/gp-editor/apps/desktop`, macOS 타깃 `macos/`).
  요청 "typescript / react / react-native 를 사용한 guitar pro 편집기, desktop 앱" 에 따른 선택.
- 파일 읽기: 순수 TypeScript 패키지 `main/gp-editor/packages/gp-core`. GP1·GP2(.gtp), GP3, GP4, GP5 는 TuxGuitar
  `common/TuxGuitar-gtp`, GP6(.gpx)·GP7/8(.gp) 는 `common/TuxGuitar-gpx` 를 포팅했다 (`main/tuxguitar`).
  그래서 gp-core 라이선스는 LGPL-2.1-or-later (`packages/gp-core/package.json`).
- 라이선스: 레포 전체 LGPL-2.1-or-later (2026-10-22 사용자 결정 "LGPL"). 루트 `LICENSE`, 모든 `package.json`, gp-editor 커밋 ab44c6a.
- 테스트 픽스처: alphaTab test-data(MPL-2.0) GP3–GP8 222개는 커밋, 실제 GP1/GP2 .gtp 6개는 재배포 라이선스가 없어
  커밋하지 않고 `test/fixtures/fetch-gtp.sh` 로 받는다 (`packages/gp-core/test/fixtures/README.md`).

## 근거 (확인된 것만)

- 커밋: 673cc17 (스캐폴드), 307e847 (리더 전체 + 편집 UI), b016dd3 (명명·중복 정리), 5eb8f57 (0.1.0 패키징·샌드박스).
- 원격·릴리스: https://github.com/drakejin/gp-editor (public), v0.1.0 — `GPEditor-0.1.0-macos-arm64.zip`, `…-universal.zip`
  (ad-hoc 서명, 공증 없음).
- 검증 (0.1.0 시점): gp-core 682 테스트, 라인 커버리지 98.3%. GP3–5 는 실제 TuxGuitar 실행 결과 해시와 비교하는 golden 테스트,
  GP1/2 는 TuxGuitar 리더 코드 출력과 대조. 앱에 들어가는 Hermes VM 에서 228 파일 / 70,980 음표 파싱 실패 0
  (`apps/desktop/scripts/hermes-smoke/run.sh`), Release 앱 `open -a` E2E 8/8 (`scripts/e2e-open.sh`).
- TuxGuitar 와 의도적으로 다르게 한 것: 트레몰로 바 단위를 쿼터톤으로(모델 계약), 노트 효과 상호배제 미적용(파일 그대로 보존),
  하모닉 data 의미, GP6 트릴·드럼 노트 처리 등. 목록은 각 리더 파일 주석과 커밋 메시지에 있다.

## 미결

- 해결됨: 저장 — GP5/GP4/GP3 writer (`packages/gp-core/src/write/`, 3b29de4). 앱은 항상 .gp5 로 저장한다
  (4f50bd0, v0.3.0). GP6–8 에만 있는 정보는 저장할 때 일부 빠진다 (`packages/gp-core/test/helpers/write-expected.ts`).
- 해결됨: 표준 악보 — `apps/desktop/src/score/notation/` (48c0324). 보기는 Score + Tab / Score / Tab.
- `main/tuxguitar` 는 helge17/tuxguitar 라 `workspace.seed.json`(단일 org) 에 넣을 수 없다. 새 머신에서는 직접 clone 해야 한다.

## 에피소드
- 배경: (에이전트) 2026-10-21 요청 — tuxguitar 를 workspace 에 받고 REA 설치, TS/React/React Native 로 모든 GP 파일을 읽는 데스크톱 편집기를 빌드까지. 이어서 직접 사용해 보며 사용감 리뷰와 명명·패턴 일관성 점검 요청. 결과는 위 커밋 4개 (673cc17, 307e847, b016dd3, 5eb8f57).
- 선택지와 결정: (사람)
- 이유: (사람)
- 그때의 생각: (사람)
- 결과:
