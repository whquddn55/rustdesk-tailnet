# rustdesk-tailnet

RustDesk 를 Tailscale 전용으로 고친 포크다. 업스트림 `rustdesk/rustdesk` 의 `0bc2473` (2026-10-09) 위에 `tailnet` 브랜치로 올렸다.

## 바꾼 것

| 무엇 | 어디 | 내용 |
|---|---|---|
| Tailscale 강제 (소켓) | `tailnet/hbb_common.patch` → `libs/hbb_common` | TCP 연결 · UDP 송수신 · 서버 주소 해석에서 `100.64.0.0/10` · `fd7a:115c:a1e0::/48` · loopback 밖 주소를 거부한다. websocket · webrtc 엔드포인트와 프록시(SOCKS/HTTP)를 막는다. |
| Tailscale 강제 (앱) | `src/tailnet.rs` + 얇은 훅 | UDP punch · IPv6 punch · KCP · WebRTC(보내는 쪽 · 받는 쪽) · LAN 탐색을 끄고, 직접 IP 접속(21118)을 켠다. 들어오는 연결도 Tailscale 주소만 받는다 (`src/server.rs`). HTTP(API 서버 · 업데이트 확인)는 Tailscale 주소로 풀리는 이름과 Tailscale IP 만 간다. |
| 한/영 전환 | `src/keyboard.rs` `ime_toggle_key_events`, `src/platform/macos_ime.rs` | 키를 키로 바꾸지 않고 "입력 소스 전환" 동작을 보낸다 (아래 표). 수동 설정은 없다. |
| 폰 한글 | `flutter/lib/mobile/pages/remote_page.dart` `_inputSoftKeyboardReplacement` | Android 키보드가 마지막 글자를 바꿔 쓰는 조합(ㄱ→가→각→가가)을 백스페이스 + 새 글자로 보낸다. 전에는 글자 수만 비교해 "안녕" 이 "ㅇㄴ" 으로 들어갔다. |
| 코덱 기본값 | `src/tailnet.rs` | 새로 붙는 기기의 코덱 기본값을 H.264 로 둔다 (사용자가 바꾸면 그 값을 쓴다). |
| CI | `.github/workflows/flutter-build.yml` | checkout 뒤마다 `tailnet/apply-patches.sh` 로 `hbb_common` 패치를 적용한다. |

`hbb_common` 은 서버와 같이 쓰는 git submodule 이다. submodule 커밋은 건드리지 않고 패치 파일로 들고 다닌다.

```bash
bash tailnet/apply-patches.sh   # submodule 에 패치 적용 (이미 적용돼 있으면 넘어감)
bash tailnet/make-patch.sh      # libs/hbb_common 을 고친 뒤 패치 파일 다시 만들기
```

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

서버 PC 의 Tailscale IP 를 `100.A.B.C` 라고 하면, 모든 기기에서 **네트워크 → ID/릴레이 서버** 에 이렇게 넣는다.

| 칸 | 값 |
|---|---|
| ID 서버 | `100.A.B.C` |
| 릴레이 서버 | `100.A.B.C` |
| API 서버 | `http://100.A.B.C:21114` |
| Key | 서버 PC 의 hbbs 공개키 (`id_ed25519.pub` 내용) |

- 공인 도메인(`rust.example.com` 처럼 공인 IP 로 풀리는 이름)을 넣으면 이 포크는 연결을 거부한다. 로그에 `tailnet-only:` 로 시작하는 줄이 남는다.
- 상대 기기의 Tailscale IP(`100.x.y.z`)를 ID 칸에 넣으면 ID 서버 없이 직접 IP 접속으로 붙는다.
- 공유기의 RustDesk 포트 포워딩(21114~21119)은 닫는다.

## 빌드

로컬 빌드는 vcpkg · Flutter 가 필요해서 GitHub Actions 로 빌드한다. 이 저장소를 GitHub 에 올리고 `flutter-nightly` 나 `flutter-build` 워크플로를 돌리면 윈도우 · 맥 · Android 산출물이 나온다. 서명하지 않은 빌드라 맥은 Gatekeeper, 윈도우는 SmartScreen 경고가 뜬다. iOS 는 Apple 개발자 계정이 있어야 설치할 수 있다.

## 검증한 것

- `libs/hbb_common`: `cargo test --lib` 100개 통과 (Tailscale 주소 판별 테스트 포함).
- Android 한글 입력: 수정 전후 로직을 Dart 로 흉내 내 비교했다. "안녕" 입력이 수정 전 `ㅇㄴ`, 수정 후 `안녕` 이다. 받침 넘어감 · 조합 중 지우기 · 괄호 안 입력 · 이모지도 맞다.

## 알려진 한계

- 주소 대역으로만 판단한다. `100.64.0.0/10` 은 통신사 CGNAT 대역이기도 해서, 테일넷 피어가 아닌 100.x 주소로 가는 것까지는 구분하지 않는다.
- 포트 포워딩(RDP 터널 등)에서 받는 쪽 PC 가 터널 목적지(예: `127.0.0.1:3389`)로 붙는 연결은 제한하지 않는다. 터널 자체는 Tailscale 로 맺은 세션 안에 있다.
- 맥에서 Caps Lock 을 전환 키로 쓰면 원격으로는 Caps Lock 잠금을 보낼 수 없다 (누를 때마다 전환으로 보낸다). 맥이 Caps Lock · 🌐 키 이벤트를 RustDesk 에 어떻게 넘기는지는 실기기에서 확인해야 한다.
- 받는 맥의 입력 소스 전환(TIS)은 이 Mac 에서 별도 실행으로 확인했다 (`2SetKorean → ABC → 2SetKorean`). RustDesk 서버 프로세스 안에서 같은 호출이 되는지는 실기기에서 확인해야 한다.
- 받는 맥에 한국어 입력 소스가 없으면 전환하지 않고 로그만 남긴다.
- HTTP 클라이언트를 만들다 실패해 기본 클라이언트로 떨어지는 경로(`<Client>::new()`)에는 이름 해석 필터가 붙지 않는다. 이름 해석 필터를 붙인 빌더가 실패할 때만 타는 경로다.
