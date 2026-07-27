# `ars update` 명령 설계

- 날짜: 2026-07-27
- 상태: 사용자 승인

## 목표

사용자가 `ars update` 한 번으로 현재 ARS 설치를 최신 릴리스로 갱신할 수
있게 한다. 명령 실행 자체를 업데이트 동의로 간주하며 별도 확인은 받지 않는다.

## 범위

- 공개 명령은 `ars update` 하나만 추가한다. `ars upgrade` 별칭은 만들지 않는다.
- npm 글로벌 설치와 GitHub Releases 단독 바이너리를 지원한다.
- `--check`, 버전 지정, downgrade, prerelease 선택은 추가하지 않는다.
- 기존 인터랙티브 시작 시 자동 업데이트 확인과 선택 메뉴는 그대로 유지한다.

## 명령과 출력

`ars update`는 TTY, 호스트 inventory, 세션 수집에 의존하지 않는 독립 명령이다.
새 릴리스가 있으면 즉시 적용하고 성공 시 다음 메시지를 stdout에 출력한다.

```text
Updated ars from v2.42.1 to v2.43.0
```

현재 버전이 최신 버전과 같거나 더 높으면 변경 없이 성공 종료한다.

```text
ars v2.42.1 is already up to date
```

성공은 exit code 0, 조회·검증·설치 실패와 개발 빌드는 exit code 1, 잘못된
인자 형태는 exit code 2를 사용한다. 오류는 기존 형식대로 stderr에
`ars: update: <error>`로 출력한다.

## 구조와 데이터 흐름

1. `internal/app.Run`이 정확히 `ars update`인 인자 형태를 도움말·설정 명령과
   같은 상위 명령으로 먼저 인식한다.
2. 주입된 업데이트 콜백을 실행하므로 topology 로드, 세션 수집, TUI 시작은
   일어나지 않는다.
3. `cmd/ars`가 현재 임베드 버전과 `internal/update` 의존성을 연결한다.
   릴리스 조회에는 기존과 같은 1.5초 제한을 둔다.
4. `internal/update`의 명시적 흐름이 GitHub 최신 릴리스를 조회하고 현재
   버전과 비교한다.
5. 새 버전이면 기존 설치 채널 감지와 적용 구현을 재사용한다.
   - npm 설치는 `npm install -g @baleen37/ars@<version>`을 실행한다.
   - 단독 바이너리는 `SHA256SUMS` 검증 후 실행 파일을 원자적으로 교체한다.
6. 명시적 명령은 업데이트한 바이너리를 재실행하지 않고 결과를 출력한 뒤
   종료한다. 다음 `ars` 실행부터 새 바이너리가 사용된다.

시작 시 자동 확인과 명시적 명령은 조회·버전 비교·적용 로직을 공유한다.
자동 확인만 실패를 조용히 무시하고 선택 메뉴와 성공 후 process re-exec를
수행한다. 명시적 명령은 모든 실패를 보고하고 re-exec하지 않는다.

## 오류 처리

- 임베드 버전이 비어 있는 개발 빌드는 설치 출처를 추측하지 않고
  `updates are unavailable for development builds` 오류로 종료한다.
- 최신 릴리스 조회의 네트워크 오류, 시간 초과, HTTP 오류, 잘못된 태그는
  명시적 오류로 반환한다.
- npm 설치 실패, 지원하지 않는 플랫폼, checksum 불일치, 파일 교체 권한
  오류는 기존 적용 오류를 보존한다.
- 적용 실패 시 현재 바이너리로 TUI를 이어서 실행하지 않는다. 명시 명령은
  실패 상태로 종료한다.

## 테스트와 성공 기준

- CLI 라우팅: `ars update`가 업데이트 콜백만 한 번 호출하고 topology,
  collector, TUI는 호출하지 않는다.
- CLI 계약: 도움말과 usage에 `ars update`가 포함되고 `ars upgrade` 및
  추가 인자는 exit code 2로 거절된다.
- 명시적 업데이트: 개발 빌드, 최신 상태, 새 버전 적용, 조회 실패, 적용
  실패의 결과와 exit code를 검증한다.
- 채널 적용: 기존 npm 명령, release archive, checksum, 원자적 교체 테스트를
  재사용한다.
- 회귀 검증: 기존 시작 시 자동 확인의 선택·건너뛰기·re-exec 테스트와 전체
  Go 테스트 및 `go vet ./...`가 통과해야 한다.

완료 기준은 두 설치 채널 모두 기존 안전장치를 유지하면서 `ars update`가
독립적으로 동작하고, 이미 최신인 경우 불필요한 설치를 실행하지 않는 것이다.
