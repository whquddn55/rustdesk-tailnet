package api

// Wire types. Field names follow the RustDesk client parsers at commit 0bc2473:
//   flutter/lib/common/hbbs/hbbs.dart  (UserPayload, PeerPayload, LoginRequest,
//                                       LoginResponse, DeviceGroupPayload)
//   src/hbbs_http/account.rs           (Rust UserPayload / AuthBody)
//   src/hbbs_http/sync.rs              (heartbeat / sysinfo)
//   flutter/lib/models/group_model.dart, ab_model.dart (paged {total, data})

// loginRequest mirrors LoginRequest.toJson (hbbs.dart:155-177).
type loginRequest struct {
	Username   string         `json:"username"`
	Password   string         `json:"password"`
	ID         string         `json:"id"`
	UUID       string         `json:"uuid"`
	AutoLogin  bool           `json:"autoLogin"`
	Type       string         `json:"type"`
	DeviceInfo map[string]any `json:"deviceInfo"`
}

// idUUID is the body of /api/currentUser and /api/logout
// (user_model.dart:64-67, 178-181).
type idUUID struct {
	ID   string `json:"id"`
	UUID string `json:"uuid"`
}

// userPayload mirrors UserPayload.fromJson (hbbs.dart:36-48) and the Rust
// UserPayload (account.rs:79-97). Rust requires "info" (no serde default),
// so it is always sent.
type userPayload struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Avatar      string   `json:"avatar"`
	Email       string   `json:"email"`
	Note        string   `json:"note"`
	Status      int      `json:"status"` // 1 normal, 0 disabled, -1 unverified
	IsAdmin     bool     `json:"is_admin"`
	Info        userInfo `json:"info"`
}

// userInfo mirrors Rust UserInfo (account.rs:53-61); all fields default.
type userInfo struct {
	EmailVerification      bool              `json:"email_verification"`
	EmailAlarmNotification bool              `json:"email_alarm_notification"`
	LoginDeviceWhitelist   []any             `json:"login_device_whitelist"`
	Other                  map[string]string `json:"other"`
}

// loginResponse mirrors LoginResponse.fromJson (hbbs.dart:190-196) and Rust
// AuthBody (account.rs:99-108). type must be "access_token"
// (HttpType.kAuthResTypeToken, hbbs.dart:17) for the client to store the token.
type loginResponse struct {
	AccessToken string      `json:"access_token"`
	Type        string      `json:"type"`
	User        userPayload `json:"user"`
}

// peerInfo is the "info" map read by PeerPayload.toPeer (hbbs.dart:95-104):
// info['username'], info['os'] (split on " / "), info['device_name'].
type peerInfo struct {
	Username   string `json:"username"`
	OS         string `json:"os"`
	DeviceName string `json:"device_name"`
}

// peerPayload mirrors PeerPayload.fromJson (hbbs.dart:86-93). user_name must
// equal the owning user's "name": the group tab filters peers by
// peer.loginName == selected user name (peers_view.dart:575,591).
type peerPayload struct {
	ID              string   `json:"id"`
	Info            peerInfo `json:"info"`
	Status          int      `json:"status"`
	User            string   `json:"user"`
	UserName        string   `json:"user_name"`
	DeviceGroupName string   `json:"device_group_name"`
	Note            string   `json:"note"`
}

// deviceGroupPayload mirrors DeviceGroupPayload.fromJson (hbbs.dart:293-294).
type deviceGroupPayload struct {
	Name string `json:"name"`
}

// paged is the {total, data} envelope read by group_model.dart:132-148,
// 197-213, 256-273 and ab_model.dart:333-347.
type paged[T any] struct {
	Total int `json:"total"`
	Data  []T `json:"data"`
}

// heartbeatRequest mirrors the body built in sync.rs:236-245.
type heartbeatRequest struct {
	ID         string `json:"id"`
	UUID       string `json:"uuid"`
	Ver        int64  `json:"ver"`
	Conns      []int  `json:"conns"`
	ModifiedAt int64  `json:"modified_at"`
}

// sysinfoRequest mirrors crate::get_sysinfo() (common.rs:959-971) plus the
// fields added in sync.rs:133-179.
type sysinfoRequest struct {
	ID       string `json:"id"`
	UUID     string `json:"uuid"`
	Version  string `json:"version"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Username string `json:"username"`
	CPU      string `json:"cpu"`
	Memory   string `json:"memory"`
}

// legacyAbResponse is what LegacyAb.pullAbImpl reads (ab_model.dart:1028-1036):
// "data" is a JSON *string* holding {tags, peers, tag_colors}.
type legacyAbResponse struct {
	Data            string `json:"data"`
	LicensedDevices int    `json:"licensed_devices"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

// legacyAbRequest is what LegacyAb.pushAb sends (ab_model.dart:1067).
type legacyAbRequest struct {
	Data string `json:"data"`
}
