# ars 업데이트 선택 메뉴 설계

- 날짜: 2026-07-25
- 상태: 사용자 요청으로 확정

## 목표

새 릴리스가 있을 때 TUI 진입 전의 단일 키 프롬프트를 번호가 붙은 2행
선택 메뉴로 바꾼다. 사용자는 위/아래 방향키로 커서를 움직이고, 업데이트할지
현재 버전으로 계속할지 명확히 선택할 수 있어야 한다.

## 선택한 접근

`internal/tui`에 작은 Bubble Tea 모델을 추가하고 `internal/update`에는 선택
콜백만 주입한다.

- 숫자만 입력받는 프롬프트는 방향키 요구를 충족하지 못한다.
- ANSI escape sequence를 직접 파싱하면 단독 Escape와 방향키를 구분하기
  위한 타임아웃·버퍼 처리가 새로 필요하다.
- 메인 세션 목록 모델에 업데이트 상태를 합치면 릴리스 적용과 process
  re-exec 경로까지 넓게 변경해야 한다.

기존 Bubble Tea 입력 처리와 스타일을 재사용하는 별도 2행 모델이 가장 작고
현재의 Charm import 경계(`internal/tui`만 Charm을 import)를 그대로 지킨다.

## 화면과 입력

초기 화면은 다음 의미를 전달한다.

```text
ars v2.25.0 available (current v2.24.0)

> 1. Update to v2.25.0
  2. Continue with v2.24.0

↑/↓ move · 1/2 choose · enter confirm
```

- 기본 커서는 `1. Update`다. 기존의 `Enter = update` 동작을 보존한다.
- `Up`/`Down`은 두 행 사이에서 순환한다.
- `Enter`는 현재 커서의 선택을 확정한다.
- `1`/`2`는 해당 번호를 바로 확정한다.
- `q`, `Esc`, `Ctrl+C`는 현재 버전으로 계속한다. 업데이트 선택 화면이 ars
  시작을 가로막지 않는 기존 원칙을 유지한다.
- 그 외 키는 무시한다.

## 경계와 데이터 흐름

1. `internal/update.Maybe`가 기존처럼 최신 버전을 조회하고 새 버전일 때만
   `Dependencies.Choose(current, latest)`를 호출한다.
2. `cmd/ars`가 이 콜백을 `internal/tui.ChooseUpdate`에 연결한다.
3. `ChooseUpdate`는 inline Bubble Tea 프로그램을 실행하고 최종 선택을 bool로
   반환한다.
4. 업데이트 선택이면 기존 npm/standalone 적용과 re-exec 경로를 그대로
   사용한다. 현재 버전 선택 또는 메뉴 실행 오류면 적용 없이 메인 TUI로
   진행한다.

`internal/update`의 릴리스 조회·설치 로직과 `internal/tui`의 키/렌더링 책임은
서로 import하지 않는다.

## 오류 처리

- 메뉴 시작이나 입력 처리가 실패하면 업데이트를 건너뛰고 현재 버전으로
  계속한다.
- 터미널 상태 복원은 Bubble Tea 프로그램 수명주기에 맡긴다.
- 실제 업데이트 적용 후의 오류만 기존처럼 `ars: update: <error>`로 보여준다.

## 검증

- 모델 단위 테스트: 번호 표시, 기본 커서, 위/아래 이동, Enter 확정, 숫자
  직접 선택, q/Esc/Ctrl+C의 현재 버전 선택.
- orchestrator 단위 테스트: 선택 콜백이 업데이트/건너뛰기 경로를 정확히
  제어하는지 검증.
- 전체 빌드·vet·test·format 및 Charm import 경계 테스트.
- 새로 빌드한 가짜 구버전 바이너리를 격리된 tmux에서 실제 방향키와 Enter로
  조작한다. 현재 버전 계속 경로와 임시 바이너리 업데이트/re-exec 경로를
  각각 관찰하고, 사용자가 만든 상태는 건드리지 않는다.
