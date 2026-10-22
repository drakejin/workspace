# gp-editor

| 문서 | 한 줄 요약 |
| --- | --- |
| [adr/0001-desktop-stack-and-readers.md](./adr/0001-desktop-stack-and-readers.md) | react-native-macos 데스크톱 + TuxGuitar 포팅 리더(GP1–GP8), LGPL, 픽스처 라이선스 처리 |
| [adr/0002-playback-engine.md](./adr/0002-playback-engine.md) | 재생을 AVAudioEngine 하나 + MIDISynth(GPMidiEngine)로: 뱅크 한 번 로드, 재생 중 악기 실시간 변경, 정지 60초 뒤 해제. 메모리 상한은 원인이 아니어서 그대로 |
| [adr/0003-pdf-import-and-export.md](./adr/0003-pdf-import-and-export.md) | PDF 가져오기는 Claude(Anthropic API) 로 페이지마다 전사해 프로젝트 폴더에, 내보내기는 화면과 같은 engraver·painter 로 Score+Tab/Score/Tab PDF |
