---
team: default
repo: gp-editor
topic: pdf
doc_type: adr
date: 2026-10-22
relates:
  - path: docs/repository/gp-editor/adr/0001-desktop-stack-and-readers.md
    why: 같은 앱. 가져온 곡은 0001 의 GP5 writer 로 저장한다
---

# 0003. PDF: Claude 로 가져오기(PDF → 탭 프로젝트), 화면과 같은 그리기로 내보내기

## 결정

- 가져오기: PDF 를 페이지마다 Anthropic Messages API 로 보내 탭으로 옮긴다. 모델은 Claude Opus 5.5(기본) 또는
  Claude Sonnet 5 (`apps/desktop/src/pdf-import/request.ts`). 답은 도구 호출 하나로 강제하고(`tool_choice`),
  gp-core 가 그 답을 믿지 않고 검사한 뒤 곡을 만든다 (`packages/gp-core/src/transcription/`).
- 가져올 때마다 `~/Documents/GP Editor Projects/<이름>/` 에 프로젝트 폴더를 만든다: 원본 PDF, `project.json`,
  `pages/page-NNN.json`, `<이름>.gp5`. 샌드박스 예외는 이 폴더 하나 (`GPEditor.entitlements`).
- API 키는 앱의 Application Support 에 소유자 전용 파일로 두고 JS 로 넘기지 않는다 (`GPAnthropic.m`).
- 내보내기: 보이는 트랙을 Score + Tab / Score / Tab 중 하나로, A4 또는 US Letter PDF 로. JS 가 화면과 같은
  engraver 로 페이지마다 그리기 명령을 만들고(`apps/desktop/src/export/`), 네이티브가 화면과 같은 painter
  (`GPCommandPainter`) 로 PDF 에 그린다 (`GPPdfExport.m`). ⌘P 는 시스템 인쇄 대신 내보내기 패널을 연다.

## 근거 (확인된 것만)

- 요청 (2026-10-22): "import PDF 를 하면 tab 로 만들어주는 기능 … import 할때마다 project를 만들어주고 … llm서비스를
  이용해도 좋아". 선택지 질문에 대한 답: LLM 은 Anthropic Claude, 프로젝트 위치는 문서 폴더, 키는 "내가 지금
  입력해줄게, 네가 알아서 키 파일을 앱 캐시파일에 등록해놔".
- 요청 (2026-10-22): "export 에는 pdf 출력기능을 만들어놓고 score + tab. / score / tab 모드로 pdf를 출력",
  "command + p 를 누르면 export 버튼을 누른것같은 단축키를 실행".
- gp-editor 커밋: 87b5359·2342bfa (전사 계약과 곡 만들기), fc48224 (답이 할당 크기를 키우지 못하게 상한),
  aad38a5 (가져오기 앱 쪽, v0.6.0), a490ffc (GPCommandPainter 분리), 4830c58·fd9adff·93a34a8 (내보내기),
  8afe1b1 (스키마 범위를 파서와 한 곳에), 658d52d (가져오기는 한 번에 하나).
- 확인: 2026-10-22 기준 키의 계정에 크레딧이 없어 API 가 "credit balance is too low" 를 돌려준다. 실제 PDF 로
  Claude 전사를 끝까지 돌린 검증은 아직 없다. 가져오기 흐름은 mock 서버(QA 사본만 `GPEDITOR_ANTHROPIC_URL` 허용)
  로 확인했다.
- 내보내기 확인 (Release 앱, QA 드라이버, nightwish.gp5 트랙 1): Score + Tab 7쪽, Score 5쪽, Tab 5쪽.

## 미결

- 실제 Claude 전사 품질은 크레딧이 생긴 뒤 실제 PDF 로 확인해야 한다. 결과에 따라 프롬프트나 스키마를 고칠 수 있다.

## 에피소드
- 배경: (에이전트) 2026-10-22 PDF → 탭 가져오기 요청 (aad38a5, v0.6.0), 이어서 PDF 내보내기와 ⌘P 요청
  (93a34a8). 계정 크레딧이 없어 실제 전사는 검증 전.
- 선택지와 결정: (사람)
- 이유: (사람)
- 그때의 생각: (사람)
- 결과:
