# rustdesk-tailnet

RustDesk 를 Tailscale 전용으로 고친 포크다. 업스트림 `rustdesk/rustdesk` 의 `0bc2473` (2026-10-09) 위에 `tailnet` 브랜치로 올렸다.

## 바꾼 것

| 무엇 | 어디 | 내용 |
|---|---|---|
| Tailscale 강제 (소켓) | `tailnet/hbb_common.patch` → `libs/hbb_common` | TCP 연결 · UDP 송수신 · 서버 주소 해석에서 `100.64.0.0/10` · `fd7a:115c:a1e0::/48` · loopback 밖 주소를 거부한다. websocket · webrtc 엔드포인트와 프록시(SOCKS/HTTP)를 막는다. |
| Tailscale 강제 (앱) | `src/tailnet.rs` + 얇은 훅 | UDP punch · IPv6 punch · KCP · WebRTC(보내는 쪽 · 받는 쪽) · LAN 탐색을 끄고, 직접 IP 접속(21118)을 켠다. 들어오는 연결도 Tailscale 주소만 받는다 (`src/server.rs`). HTTP(API 서버 · 업데이트 확인)는 Tailscale 주소로 풀리는 이름과 Tailscale IP 만 간다. |
| 한/영 전환 | `src/keyboard.rs` `ime_toggle_key_events`, `src/platform/macos_ime.rs` | 키를 키로 바꾸지 않고 "입력 소스 전환" 동작을 보낸다 (아래 표). 수동 설정은 없다. |
| 폰 한글 | `flutter/lib/mobile/hangul_keys.dart`, `remote_page.dart`, `src/tailnet.rs` `input_ime_keys` | Windows · macOS 로 접속했을 때 Android 키보드 입력을 두벌식 키 입력(USB HID, 맵 모드)으로 바꿔 보낸다. 받는 쪽 한글 입력기가 직접 조합하므로 글자가 지워졌다 다시 써지지 않는다. 받는 쪽이 조합 중인 음절을 폰에서 따라 계산해 백스페이스는 자모 단위로 필요한 만큼만 보낸다. 한글을 치면 받는 쪽 입력 소스를 한국어로, 영문자를 치면 영문으로 맞춘다 (전환이 아니라 지정이라 어긋나지 않는다). 입력 호출은 하나씩 차례로 끝낸다. 다른 OS 로 접속하면 글자 단위 비교로 보낸다. |
| 코덱 기본값 | `src/tailnet.rs` | 새로 붙는 기기의 코덱 기본값을 H.264 로 둔다 (사용자가 바꾸면 그 값을 쓴다). |
| API 서버 기본값 | `src/tailnet.rs`, `tailnet-build.yml` | 빌드 때 `TAILNET_API_SERVER` 를 API 서버 기본값으로 넣는다. 나머지 서버 설정은 계정에서 받는다. |
| CI | `.github/workflows/flutter-build.yml` | checkout 뒤마다 `tailnet/apply-patches.sh` 로 `hbb_common` 패치를 적용한다. |

`hbb_common` 은 서버와 같이 쓰는 git submodule 이다. submodule 커밋은 건드리지 않고 패치 파일로 들고 다닌다.

```bash
bash tailnet/apply-patches.sh   # submodule 에 패치 적용 (이미 적용돼 있으면 넘어감)
bash tailnet/make-patch.sh      # libs/hbb_common 을 고친 뒤 패치 파일 다시 만들기
```

## API 서버

`tailnet/api-server` 에 있다 (Go, Docker Compose 로 API · hbbs · hbbr 를 같이 띄운다). 로그인 · 그룹별 기기 목록과, 로그인한 기기에 ID 서버 · 릴레이 · Key 를 내려 주는 일을 한다. 설치는 [tailnet/api-server/README.md](api-server/README.md).

## 한/영 전환

기기마다 전환 키가 달라도 맞출 필요가 없다. 보내는 쪽이 자기 전환 키를 알아보고, 받는 쪽은 자기 방식으로 전환한다.

| 보내는 쪽 | 전환 키를 알아보는 법 |
|---|---|
| 맥 | 그 맥의 시스템 설정을 5초마다 읽는다. 키보드 단축키의 "이전 입력 소스 선택"(60) · "입력 메뉴에서 다음 소스 선택"(61) (`com.apple.symbolichotkeys`, 바꾼 적 없으면 기본값 Ctrl+Space · Ctrl+Option+Space), "Caps Lock 으로 ABC 전환"(`TISRomanSwitchState`), "🌐 키를 눌러 입력 소스 변경"(`AppleFnUsageType`). F13 · Ctrl+Space · Caps Lock · 🌐 무엇을 쓰든 따라간다. |
| 윈도우 | `VK_HANGUL`. 오른쪽 Alt 가 한/영인 배열도 키보드 드라이버가 `VK_HANGUL` 로 바꿔 주는 것으로 본다 (실기기 확인 필요). |

| 받는 쪽 | 전환하는 법 |
|---|---|
| 윈도우 | `VK_HANGUL` 을 주입한다. 한국어 IME 는 배열 종류와 상관없이 이 키로 전환한다. 단축키 조합(Ctrl+Space 등)으로 보낸 경우, 받는 쪽에 눌려 있던 수식키는 먼저 뗀다. |
| 맥 | 키를 주입하지 않고 TIS API 로 한국어 ↔ 직전에 쓰던 영문 입력 소스를 바로 고른다. 받는 맥의 단축키가 무엇이든 상관없다. |

윈도우 → 윈도우는 원래 동작을 그대로 쓴다. 키를 누르고 있어 반복 입력이 들어와도 한 번만 전환한다.

## 기기 설정

설치하고 로그인만 한다.

- API 서버 주소는 빌드 기본값이다. GitHub 저장소 변수 `TAILNET_API_SERVER` (지금 `http://rustdesk-server.example.ts.net:21114`) 가 빌드에 들어간다. 설정 → 네트워크의 API 서버 칸에서 바꿀 수 있다.
- 로그인하면 API 서버가 heartbeat 응답(`strategy`)으로 ID 서버 · 릴레이 · Key 를 내려 주고, 클라이언트가 저장한다. 같은 계정의 기기는 모두 같은 값을 쓴다. 서버를 옮기면 API 서버 쪽 설정만 바꾼다.
- 접속할 때는 상대 기기의 비밀번호를 넣는다 (기억하기를 켜면 다음부터 생략).
- 공인 도메인(`rust.example.com` 처럼 공인 IP 로 풀리는 이름)이 남아 있으면 이 포크는 연결을 거부하고 로그에 `tailnet-only:` 줄을 남긴다. 로그인 뒤 계정 설정이 내려오면 덮어써진다.
- 공유기의 RustDesk 포트 포워딩(21114~21119)은 닫는다.

## 빌드

로컬 빌드는 vcpkg · Flutter 가 필요해서 GitHub Actions 로 빌드한다. `tailnet-build` 워크플로(수동 실행)가 Windows x64 · macOS arm64 · Android arm64 · Linux x64 를 만들어 Release `tailnet-b<실행 번호>` 와 Actions 산출물로 올린다. 원본의 다른 워크플로는 저장소에서 꺼 두었다.

- 버전: RustDesk 버전(`1.5.0`)은 원본 그대로 둔다. 상대 기기가 이 값으로 기능 지원 여부를 판단하기 때문이다. 대신 빌드 번호 `1000 + 실행 번호` 를 Flutter 빌드 번호로 넣는다 (Android `versionCode`, macOS `CFBundleVersion`, Windows 파일 버전 넷째 자리). `tailnet/apply-patches.sh` 가 `TAILNET_BUILD_NUMBER` 를 보고 `flutter/pubspec.yaml` 을 고친다.
- Android 서명: 저장소 비밀 값 `ANDROID_SIGNING_KEY` · `ANDROID_ALIAS` · `ANDROID_KEY_STORE_PASSWORD` · `ANDROID_KEY_PASSWORD` 의 고정 키로 서명해, 새 APK 를 지우지 않고 덮어써 업데이트한다. 키 파일을 잃으면 다시 지우고 설치해야 하므로 따로 백업한다.
- 서명하지 않은 데스크톱 빌드라 맥은 처음 열 때 우클릭 → 열기, 윈도우는 SmartScreen 의 "추가 정보 → 실행" 이 필요하다. iOS 는 Apple 개발자 계정이 있어야 설치할 수 있다.

## 검증한 것

- `libs/hbb_common`: `cargo test --lib` 100개 통과 (Tailscale 주소 판별 테스트 포함).
- Android 한글 입력: 수정 전후 로직을 Dart 로 흉내 내 비교했다. "안녕" 입력이 수정 전 `ㅇㄴ`, 수정 후 `안녕` 이다. 받침 넘어감 · 조합 중 지우기 · 괄호 안 입력 · 이모지도 맞다.

## 알려진 한계

- 주소 대역으로만 판단한다. `100.64.0.0/10` 은 통신사 CGNAT 대역이기도 해서, 테일넷 피어가 아닌 100.x 주소로 가는 것까지는 구분하지 않는다.
- 포트 포워딩(RDP 터널 등)에서 받는 쪽 PC 가 터널 목적지(예: `127.0.0.1:3389`)로 붙는 연결은 제한하지 않는다. 터널 자체는 Tailscale 로 맺은 세션 안에 있다.
- 맥에서 Caps Lock 을 전환 키로 쓰면 원격으로는 Caps Lock 잠금을 보낼 수 없다 (누를 때마다 전환으로 보낸다). 맥이 Caps Lock · 🌐 키 이벤트를 RustDesk 에 어떻게 넘기는지는 실기기에서 확인해야 한다.
- 받는 맥의 입력 소스 전환(TIS)은 이 Mac 에서 별도 실행으로 확인했다 (`2SetKorean → ABC → 2SetKorean`). RustDesk 서버 프로세스 안에서 같은 호출이 되는지는 실기기에서 확인해야 한다.
- 받는 맥에 한국어 입력 소스가 없으면 전환하지 않고 로그만 남긴다.
- 폰 한글 키 입력은 받는 쪽이 두벌식 자판, US 배열이라고 가정한다. 받는 쪽에서 마우스로 커서를 옮기는 등 조합이 끊기면 폰 쪽 계산과 어긋날 수 있다 (다음 글자부터 다시 맞는다).
- 윈도우가 받는 쪽일 때 한/영 지정은 앞 창의 IME 상태(`WM_IME_CONTROL`)를 읽어 다를 때만 한/영 키를 넣는다. 실기기에서 확인해야 한다.
- HTTP 클라이언트를 만들다 실패해 기본 클라이언트로 떨어지는 경로(`<Client>::new()`)에는 이름 해석 필터가 붙지 않는다. 이름 해석 필터를 붙인 빌더가 실패할 때만 타는 경로다.
