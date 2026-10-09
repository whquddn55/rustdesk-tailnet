# rustdesk-tailnet-api

RustDesk 공식 클라이언트가 그대로 붙는 **자체 API 서버**(Go). Tailscale 망 안에서만 동작한다.

- 계정 로그인(비밀번호, bcrypt) → 로그인한 기기를 그 사용자에게 묶는다.
- 기기가 보내는 heartbeat · sysinfo 로 RustDesk ID, 호스트명, OS, 마지막 접속 시각, 접속해 온 Tailscale IP 를 기록한다.
- **같은 그룹 사용자들의 기기가 클라이언트의 "그룹(Accessible devices)" 탭에 자동으로 보인다.**
- 사용자마다 개인 주소록 1개(클라이언트의 "Legacy address book" 모드).
- 관리 화면은 없고, 같은 바이너리의 서브커맨드(`user`, `group`, `device`)로 관리한다.

공식 오픈소스 hbbs/hbbr 에는 이 API 가 없으므로, 이 서버를 hbbs/hbbr 옆에 함께 띄운다(`deploy/docker-compose.yml`).

## Tailscale 전용 정책

| 방어선 | 내용 |
|---|---|
| bind 주소 | `LISTEN_ADDR` 환경 변수의 IP 에만 bind. 비어 있거나, `0.0.0.0`/`::`, 호스트명, Tailscale 대역(`100.64.0.0/10`, `fd7a:115c:a1e0::/48`) 밖이면 **시작을 거부**한다. |
| 요청 원본 검사 | TCP 접속 원본(`RemoteAddr`)이 위 대역 밖이면 모든 경로에서 **403**. `X-Forwarded-For` 등 헤더는 보지 않는다. |
| Docker 포트 | hbbs/hbbr 포트는 `${TS_IP}:포트` 로만 publish. API 는 host 네트워크지만 앱이 `${TS_IP}:21114` 하나에만 bind. |
| 테스트 예외 | `ALLOW_LOOPBACK=1` 일 때만 127.0.0.1/::1 허용 (기본 꺼짐, 운영에서 쓰지 말 것). |

코드에 특정 IP 는 없다. 서버 PC 의 Tailscale IP 는 `.env` 의 `TS_IP` 로만 들어간다.

## 서버 PC 설치 (Linux + Docker + Tailscale)

1. 서버 PC 에 Tailscale 을 설치하고 tailnet 에 붙인다. Tailscale 관리 화면에서 이 기기 이름을 `thuthi-scale` 로 바꾼다
   (포크 클라이언트의 기본 API 주소가 `http://rustdesk-server.example.ts.net:21114` 다). IP 확인:
   ```sh
   tailscale ip -4        # 예: 100.101.102.103
   ```
2. 이 저장소를 서버 PC 에 복사한다. **기존 Key 를 그대로 쓰려면** 처음 띄우기 전에 옛 서버의 키 두 개를 복사해 둔다
   (hbbs 는 키가 없을 때만 새로 만든다):
   ```sh
   cd rustdesk-tailnet-api/deploy
   mkdir -p data/rustdesk
   cp <옛 서버>/id_ed25519 <옛 서버>/id_ed25519.pub data/rustdesk/
   ```
3. 설정하고 띄운다:
   ```sh
   cp .env.example .env
   vi .env                # TS_IP=100.101.102.103, SERVER_HOST=rustdesk-server.example.ts.net
   docker compose up -d --build
   docker compose ps
   ```
4. 사용자 · 그룹을 만든다 (아래 "사용자 · 그룹 관리").

로그인한 기기는 heartbeat 응답(`strategy`)으로 ID 서버 · 릴레이 · API 서버(`SERVER_HOST`)와 Key(`data/rustdesk/id_ed25519.pub`)를 받아 저장한다.
기기마다 서버 칸을 채울 필요가 없고, 키를 바꾸면 다음 heartbeat 때 모든 기기에 퍼진다. `data/rustdesk/id_ed25519` 개인 키는 백업하되 외부로 내보내지 말 것.

### 열리는 포트 (모두 `${TS_IP}` 에만)

| 포트 | 서비스 | 용도 |
|---|---|---|
| 21114/tcp | api | 이 API 서버 |
| 21115/tcp | hbbs | NAT 유형 검사 |
| 21116/tcp, 21116/udp | hbbs | ID 등록 · 홀펀칭 |
| 21117/tcp | hbbr | 릴레이 |

21118/21119(웹 클라이언트용 websocket)는 쓰지 않으므로 열지 않는다. hbbs 는 `-r ${TS_IP}:21117` 로 릴레이 주소도 Tailscale 주소를 알려 준다.

### 부팅 순서 주의

Tailscale IP 가 아직 없는 상태에서 Docker 가 먼저 뜨면 `${TS_IP}` bind 가 실패한다
(API 는 종료 후 재시작을 반복하다가 IP 가 생기면 뜬다. hbbs/hbbr 의 포트 publish 는 실패한 채 남을 수 있다).
둘 중 하나로 해결한다.

- 간단한 방법: 아직 없는 주소에도 bind 를 허용
  ```sh
  echo 'net.ipv4.ip_nonlocal_bind=1' | sudo tee /etc/sysctl.d/99-nonlocal-bind.conf && sudo sysctl --system
  ```
- 또는 docker 를 tailscaled 뒤에 띄우기: `sudo systemctl edit docker` 에
  ```ini
  [Unit]
  After=tailscaled.service
  Wants=tailscaled.service
  [Service]
  ExecStartPre=/bin/sh -c 'until tailscale ip -4 >/dev/null 2>&1; do sleep 1; done'
  ```

## 사용자 · 그룹 관리

사용자는 그룹 하나에 속하고, **같은 그룹 사용자끼리 서로의 기기를 본다.** 그룹이 없는 사용자는 자기 기기만 본다.

```sh
cd deploy
alias rdapi='docker compose exec api rustdesk-tailnet-api'

rdapi group add family
rdapi user add alice -group family -display-name "앨리스"   # 비밀번호를 두 번 묻는다
rdapi user add bob -group family
rdapi user list
rdapi group list

rdapi user set-group bob office      # 그룹 변경 ("-" 이면 그룹 해제)
rdapi user passwd alice              # 비밀번호 변경 (기존 로그인 모두 해제)
rdapi user disable bob               # 로그인 차단 (토큰 즉시 무효) / enable 로 복구
rdapi user del bob                   # 삭제 (토큰 무효, 기기는 소유자 없음으로)
rdapi device list                    # 기기 목록: ID, 호스트명, OS, 사용자, 그룹, 마지막 IP, 마지막 접속
rdapi device del 123456789
```

비밀번호는 터미널이면 화면에 안 보이게 묻고, 파이프로 넣으면 첫 줄을 쓴다
(`echo 'pw' | docker compose exec -T api rustdesk-tailnet-api user add alice -group family`).

컨테이너 없이 바이너리만 쓸 때는 `DB_PATH=/경로/api.db ./rustdesk-tailnet-api user list` 처럼 같은 명령을 쓴다.

## 클라이언트 설정

**포크 클라이언트(`rustdesk-tailnet`)**: 설치하고 로그인만 한다. API 서버 주소는 빌드 기본값(`http://rustdesk-server.example.ts.net:21114`)이고,
로그인하면 ID 서버 · 릴레이 · Key 가 계정 설정으로 내려온다. 기본 API 주소가 다르면 설정 → 네트워크의 API 서버 칸 하나만 바꾼다.

**공식 클라이언트**: RustDesk → 설정 → 네트워크 → ID/릴레이 서버에 직접 넣는다. 주소는 모두 서버 PC 의 Tailscale IP 또는 MagicDNS 이름이다.

| 칸 | 값 | 비고 |
|---|---|---|
| ID 서버 | `rustdesk-server.example.ts.net` | 포트 생략 시 21116 |
| 릴레이 서버 | `rustdesk-server.example.ts.net` | 비워도 hbbs 가 `-r` 값을 알려 준다 |
| API 서버 | `http://rustdesk-server.example.ts.net:21114` | **http** 로 쓸 것 |
| Key | `data/rustdesk/id_ed25519.pub` 내용 | |

로그인하면 같은 그룹 사용자의 기기가 "그룹" 탭에 사용자별로, 그리고 그룹 이름(device group) 아래에 모두 나온다.
기기가 목록에 나오려면 그 기기에서 한 번은 로그인해야 한다. 로그아웃하면 그 기기는 목록에서 빠진다. 접속할 때는 상대 기기의 비밀번호를 넣는다.

> 서버의 `TS_IP`/`LISTEN_ADDR` 은 반드시 IP 숫자로 넣는다. MagicDNS 이름은 `SERVER_HOST` 와 클라이언트 쪽에만 쓴다.

## 공유기 포트 포워딩을 닫을 것

모든 접속은 Tailscale 로만 오므로 공유기에 열어 둔 포트 포워딩이 있으면 지운다.

- 21114~21119 TCP/UDP 포워딩 규칙 삭제, DMZ 해제
- 공유기의 UPnP 자동 포트 열기 끄기 (원하면)
- 서버 PC 방화벽에서도 21114~21119 를 Tailscale 인터페이스(`tailscale0`) 외에는 막아 두면 이중으로 안전하다

## 검증

서버 PC 에서:
```sh
sudo ss -ltnup | grep -E ':2111[4-9]'      # 모두 100.x.y.z:포트 로만 떠 있어야 한다 (0.0.0.0, * 이면 안 됨)
docker compose logs api | head              # "listening on http://100.x.y.z:21114"
```

다른 tailnet 기기에서:
```sh
curl http://100.101.102.103:21114/api/login-options      # → []
curl -X POST http://100.101.102.103:21114/api/login \
  -d '{"username":"alice","password":"...","type":"account"}'   # → {"access_token":..., "type":"access_token", "user":{...}}
```

LAN 의 다른 기기에서 서버 PC 의 사설 IP 로:
```sh
curl -m 3 http://192.168.0.10:21114/api/login-options    # → 연결 거부/시간 초과 여야 정상
```

클라이언트 두 대에서 같은 그룹 계정으로 로그인 → 30초 안에 `rdapi device list` 에 호스트명·OS·Tailscale IP 가 채워지고, 서로의 "그룹" 탭에 상대 기기가 보이면 끝.

## 구현한 API 와 근거

RustDesk 소스(커밋 0bc2473) 기준. 응답 필드는 `internal/api/types.go` 에 클라이언트 파싱 위치와 함께 적어 두었고, `internal/api/contract_test.go` 가 클라이언트 소스에서 키를 뽑아 대조한다.

| 경로 | 동작 | 클라이언트 근거 |
|---|---|---|
| `GET /api/login-options` | `[]` (OIDC 없음) | `user_model.dart:243-263` 문자열 배열 |
| `POST /api/login` | `{access_token, type:"access_token", user}`; 실패 시 401 `{"error"}` | `hbbs.dart:133-197`, `user_model.dart:193-235`, `account.rs:79-108` |
| `POST /api/currentUser` | 평평한 UserPayload; 토큰 무효면 401 (클라이언트가 로그아웃 처리) | `user_model.dart:53-114` |
| `POST /api/logout` | 토큰 폐기 + 기기 연결 해제, 응답은 무시됨 | `user_model.dart:170-190` |
| `POST /api/heartbeat` | `{}`, 기기 정보가 없으면 `{"sysinfo":true}` (재전송 요청) | `sync.rs:236-274` (인증 헤더 없음) |
| `POST /api/sysinfo` | 텍스트 `SYSINFO_UPDATED` (id/uuid 없으면 `ID_NOT_FOUND`) | `sync.rs:126-230`, `common.rs:929-973` |
| `GET /api/users` | `{total, data:[UserPayload]}` 같은 그룹 사용자 | `group_model.dart:160-222` |
| `GET /api/peers` | `{total, data:[PeerPayload]}` 같은 그룹 사용자들의 기기 | `group_model.dart:224-282`, `hbbs.dart:77-131`, `peers_view.dart:570-596` |
| `GET /api/device-group/accessible` | `{total, data:[{name: 그룹명}]}` | `group_model.dart:103-158`, `hbbs.dart:288-302` |
| `POST /api/ab/personal` | 의도적 404 → 클라이언트가 Legacy 주소록 모드로 동작 | `ab_model.dart:260-292, 145, 182-186` |
| `GET /api/ab`, `POST /api/ab` | 사용자별 주소록 JSON 문자열 저장/조회 | `ab_model.dart:972-1088` |
| `POST /api/ab/get` | 위와 같음 (옛 Sciter UI) | `src/ui/ab.tis:646-672` |
| `/api/audit/*` | 200 `{}` (버림) | `common.rs:1217-1223`, `connection.rs:1550-1661` |
| `POST /api/record` | 200 `{}` (녹화 업로드 버림) | `record_upload.rs:93-112` |
| 그 밖의 `/api/*` | 404 `{"error"}` | |

### 주소록을 Legacy 모드로 둔 이유

클라이언트는 먼저 `/api/ab/personal` 을 부르고 404 면 "옛 서버"로 보고 `_personalAbGuid = null` 인 채 계속 진행한다(`ab_model.dart:271-275`).
이 경우 `/api/ab/settings`, `/api/ab/shared/profiles`, `/api/ab/peers`, `/api/ab/peer/add/...`, `/api/ab/tags/...` 등 공유 주소록 API 는 **아예 호출하지 않고**,
`GET/POST /api/ab` 한 쌍으로 개인 주소록 전체(JSON 문자열)를 주고받는다. 그래서 오류 없이 개인 주소록이 동작한다. 공유 주소록은 지원하지 않는다.

## 환경 변수

| 이름 | 기본값 | 설명 |
|---|---|---|
| `LISTEN_ADDR` | (없음, 필수) | `100.x.y.z:21114` 형태. Tailscale 대역이 아니면 시작 거부 |
| `DB_PATH` | `./data/api.db` (이미지: `/data/api.db`) | SQLite 파일 |
| `TOKEN_TTL` | `720h` | 로그인 토큰 수명. 사용 중이면 절반 지난 시점에 자동 연장 |
| `ALLOW_LOOPBACK` | 꺼짐 | `1` 이면 127.0.0.1/::1 도 허용. 테스트 전용 |

## 개발

```sh
go vet ./... && go test ./...
# 클라이언트 소스 대조 테스트: RustDesk 소스 경로 지정 (기본 /tmp/rd-src/rustdesk, 없으면 skip)
RUSTDESK_SRC=~/src/rustdesk go test ./internal/api -run Contract -v
# 리눅스 바이너리
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/rustdesk-tailnet-api-linux-amd64 .
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/rustdesk-tailnet-api-linux-arm64 .
```

## 한계

- 온라인/오프라인 표시는 클라이언트가 hbbs 에 직접 묻는다. 이 서버는 관여하지 않는다.
- 2FA, 이메일 인증, OIDC, 공유 주소록, 전략(strategy), 원격 강제 연결 끊기, 감사 로그 저장, 녹화 저장은 없다.
- heartbeat/sysinfo 는 클라이언트가 인증 없이 보내므로(tailnet 안의 누구나 보낼 수 있음) 기기 정보는 덮어쓸 수 있다. 다만 기기를 사용자에게 묶는 것은 로그인한 토큰으로만 된다.
- 로그인 시도 횟수 제한은 없다(bcrypt 비용과 tailnet 접근 제한에 기댄다).
