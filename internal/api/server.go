// Package api implements the subset of the RustDesk Server Pro HTTP API that
// the official RustDesk client calls, backed by the local store.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rustdesk-tailnet-api/internal/netguard"
	"rustdesk-tailnet-api/internal/store"
)

const (
	maxJSONBody   = 1 << 20  // 1 MiB for ordinary requests
	maxABBody     = 8 << 20  // address book blob
	maxRecordBody = 64 << 20 // discarded screen-recording chunks
	maxPageSize   = 1000
)

// Config controls the API handler.
type Config struct {
	Store    *store.Store
	Guard    netguard.Policy
	TokenTTL time.Duration
	Logger   *log.Logger
}

// Server is the HTTP handler.
type Server struct {
	st     *store.Store
	guard  netguard.Policy
	ttl    time.Duration
	log    *log.Logger
	mux    *http.ServeMux
	handle http.Handler
}

// New builds the handler. Every route sits behind the Tailscale guard.
func New(cfg Config) *Server {
	if cfg.TokenTTL <= 0 {
		cfg.TokenTTL = 30 * 24 * time.Hour
	}
	if cfg.Logger == nil {
		cfg.Logger = log.New(io.Discard, "", 0)
	}
	s := &Server{st: cfg.Store, guard: cfg.Guard, ttl: cfg.TokenTTL, log: cfg.Logger, mux: http.NewServeMux()}
	s.routes()
	s.handle = cfg.Guard.Middleware(s.mux)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handle.ServeHTTP(w, r) }

func (s *Server) routes() {
	m := s.mux
	// Account
	m.HandleFunc("GET /api/login-options", s.loginOptions)
	m.HandleFunc("POST /api/login", s.login)
	m.HandleFunc("POST /api/logout", s.logout)
	m.HandleFunc("POST /api/currentUser", s.currentUser)

	// Device registration (unauthenticated in the client: sync.rs:211,245 pass "" as header)
	m.HandleFunc("POST /api/heartbeat", s.heartbeat)
	m.HandleFunc("POST /api/sysinfo", s.sysinfo)
	m.HandleFunc("POST /api/sysinfo_ver", s.sysinfoVer)

	// Group ("Accessible devices") tab
	m.HandleFunc("GET /api/users", s.auth(s.users))
	m.HandleFunc("GET /api/peers", s.auth(s.peers))
	m.HandleFunc("GET /api/device-group/accessible", s.auth(s.deviceGroups))

	// Address book: legacy single-blob mode (see README / abPersonal comment)
	m.HandleFunc("POST /api/ab/personal", s.abPersonal)
	m.HandleFunc("GET /api/ab", s.auth(s.abGet))
	m.HandleFunc("POST /api/ab/get", s.auth(s.abGet)) // old Sciter UI (src/ui/ab.tis:650)
	m.HandleFunc("POST /api/ab", s.auth(s.abSet))

	// Accepted and ignored
	m.HandleFunc("/api/audit/{typ}", s.ignoreJSON)
	m.HandleFunc("POST /api/record", s.record)

	m.HandleFunc("/", s.notFound)
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func remoteIP(r *http.Request) string {
	if ip, ok := netguard.RemoteIP(r); ok {
		return ip.String()
	}
	return ""
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

type ctxKey struct{}

func userFrom(r *http.Request) *store.User { return r.Context().Value(ctxKey{}).(*store.User) }

// auth requires a valid bearer token. 401 makes the client drop its token
// (user_model.dart:85-87, group_model.dart:57-58, ab_model.dart:225-227).
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := s.st.UserByToken(r.Context(), bearer(r), s.ttl)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				s.log.Printf("token lookup: %v", err)
			}
			writeError(w, http.StatusUnauthorized, "Invalid token")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	}
}

func toUserPayload(u *store.User) userPayload {
	return userPayload{
		Name:        u.Name,
		DisplayName: u.DisplayName,
		Email:       u.Email,
		Note:        u.Note,
		Status:      u.Status,
		IsAdmin:     u.IsAdmin,
		Info:        userInfo{LoginDeviceWhitelist: []any{}, Other: map[string]string{}},
	}
}

// page applies the client's current/pageSize query (1-based, group_model.dart:118-121).
func page[T any](r *http.Request, all []T) paged[T] {
	current, _ := strconv.Atoi(r.URL.Query().Get("current"))
	size, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	if current < 1 {
		current = 1
	}
	if size < 1 {
		size = 100
	}
	if size > maxPageSize {
		size = maxPageSize
	}
	out := paged[T]{Total: len(all), Data: []T{}}
	start := (current - 1) * size
	if start < len(all) {
		end := min(start+size, len(all))
		out.Data = all[start:end]
	}
	return out
}

// ---------- account ----------

// loginOptions: client expects a JSON array of strings (user_model.dart:251-254).
// No OIDC providers, so an empty list.
func (s *Server) loginOptions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []string{})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(w, r, maxJSONBody, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if req.Type != "" && req.Type != "account" {
		writeError(w, http.StatusBadRequest, "Unsupported login type")
		return
	}
	u, err := s.st.Authenticate(r.Context(), req.Username, req.Password)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Printf("login %q: %v", req.Username, err)
			writeError(w, http.StatusInternalServerError, "Internal error")
			return
		}
		s.log.Printf("login failed for %q from %s", req.Username, remoteIP(r))
		writeError(w, http.StatusUnauthorized, "Wrong username or password")
		return
	}
	tok, err := s.st.IssueToken(r.Context(), u.ID, req.ID, req.UUID, s.ttl)
	if err != nil {
		s.log.Printf("issue token: %v", err)
		writeError(w, http.StatusInternalServerError, "Internal error")
		return
	}
	if err := s.st.BindDevice(r.Context(), req.ID, req.UUID, u.ID, remoteIP(r)); err != nil {
		s.log.Printf("bind device %s: %v", req.ID, err)
	}
	s.log.Printf("login %q device=%s from %s", u.Name, req.ID, remoteIP(r))
	writeJSON(w, http.StatusOK, loginResponse{AccessToken: tok, Type: "access_token", User: toUserPayload(u)})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	var req idUUID
	_ = decodeJSON(w, r, maxJSONBody, &req)
	tok := bearer(r)
	if u, err := s.st.UserByToken(r.Context(), tok, s.ttl); err == nil {
		if req.UUID != "" {
			_ = s.st.UnbindDevice(r.Context(), req.UUID, u.ID)
		}
		_ = s.st.RevokeToken(r.Context(), tok)
		s.log.Printf("logout %q device=%s", u.Name, req.ID)
	}
	// The client ignores the response (user_model.dart:176-189).
	writeJSON(w, http.StatusOK, map[string]any{})
}

// currentUser returns a flat UserPayload (user_model.dart:89-100); 401 resets
// the client's login. The authenticated id/uuid also (re)binds the device.
func (s *Server) currentUser(w http.ResponseWriter, r *http.Request) {
	u, err := s.st.UserByToken(r.Context(), bearer(r), s.ttl)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "Invalid token")
		return
	}
	var req idUUID
	_ = decodeJSON(w, r, maxJSONBody, &req)
	if err := s.st.BindDevice(r.Context(), req.ID, req.UUID, u.ID, remoteIP(r)); err != nil {
		s.log.Printf("bind device %s: %v", req.ID, err)
	}
	writeJSON(w, http.StatusOK, toUserPayload(u))
}

// ---------- device registration ----------

// heartbeat: the client parses the reply as a JSON object and looks for the
// optional keys "sysinfo" (force re-upload), "disconnect", "modified_at",
// "strategy" (sync.rs:249-273). We only ever send "sysinfo".
func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	var req heartbeatRequest
	if err := decodeJSON(w, r, maxJSONBody, &req); err != nil || req.ID == "" || req.UUID == "" {
		writeError(w, http.StatusBadRequest, "id and uuid required")
		return
	}
	need, err := s.st.Heartbeat(r.Context(), req.ID, req.UUID, remoteIP(r))
	if err != nil {
		s.log.Printf("heartbeat %s: %v", req.ID, err)
		writeError(w, http.StatusInternalServerError, "Internal error")
		return
	}
	resp := map[string]any{}
	if need {
		resp["sysinfo"] = true
	}
	writeJSON(w, http.StatusOK, resp)
}

// sysinfo: the client compares the plain-text body with "SYSINFO_UPDATED"
// (mark uploaded) or "ID_NOT_FOUND" (retry next heartbeat) (sync.rs:211-226).
func (s *Server) sysinfo(w http.ResponseWriter, r *http.Request) {
	var req sysinfoRequest
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if err := decodeJSON(w, r, maxJSONBody, &req); err != nil || req.ID == "" || req.UUID == "" {
		_, _ = io.WriteString(w, "ID_NOT_FOUND")
		return
	}
	err := s.st.UpdateSysinfo(r.Context(), store.Sysinfo{
		RustdeskID: req.ID, UUID: req.UUID, Hostname: req.Hostname, OS: req.OS,
		OSUsername: req.Username, CPU: req.CPU, Memory: req.Memory, Version: req.Version,
	}, remoteIP(r))
	if err != nil {
		s.log.Printf("sysinfo %s: %v", req.ID, err)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "ERROR")
		return
	}
	_, _ = io.WriteString(w, "SYSINFO_UPDATED")
}

// sysinfoVer is only queried for *.rustdesk.com servers (sync.rs:182-193);
// answered with an empty version for completeness.
func (s *Server) sysinfoVer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "")
}

// ---------- group tab ----------

func (s *Server) users(w http.ResponseWriter, r *http.Request) {
	members, err := s.st.GroupMembers(r.Context(), userFrom(r))
	if err != nil {
		s.log.Printf("users: %v", err)
		writeError(w, http.StatusInternalServerError, "Internal error")
		return
	}
	all := make([]userPayload, 0, len(members))
	for i := range members {
		all = append(all, toUserPayload(&members[i]))
	}
	writeJSON(w, http.StatusOK, page(r, all))
}

func (s *Server) peers(w http.ResponseWriter, r *http.Request) {
	devs, err := s.st.AccessibleDevices(r.Context(), userFrom(r))
	if err != nil {
		s.log.Printf("peers: %v", err)
		writeError(w, http.StatusInternalServerError, "Internal error")
		return
	}
	all := make([]peerPayload, 0, len(devs))
	for _, d := range devs {
		all = append(all, peerPayload{
			ID:              d.RustdeskID,
			Info:            peerInfo{Username: d.OSUsername, OS: d.OS, DeviceName: d.Hostname},
			Status:          1,
			User:            d.UserName,
			UserName:        d.UserName,
			DeviceGroupName: d.GroupName,
			Note:            "",
		})
	}
	writeJSON(w, http.StatusOK, page(r, all))
}

// deviceGroups exposes the caller's group as one device group so the tab can
// show "all devices of my group"; peers carry the same device_group_name.
func (s *Server) deviceGroups(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	all := []deviceGroupPayload{}
	if u.GroupName != "" {
		all = append(all, deviceGroupPayload{Name: u.GroupName})
	}
	writeJSON(w, http.StatusOK, page(r, all))
}

// ---------- address book ----------

// abPersonal deliberately answers 404. The client then runs in "legacy" address
// book mode (ab_model.dart:271-275 -> 145, 182-186) and only uses
// GET/POST /api/ab with a single JSON blob per user (LegacyAb,
// ab_model.dart:1008-1088). That gives each user a working personal address
// book without implementing the shared-address-book API (/api/ab/settings,
// /shared/profiles, /peers, /peer/add/..., /tags/...), which the client only
// calls when /api/ab/personal returns a guid.
func (s *Server) abPersonal(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "shared address book not supported")
}

func (s *Server) abGet(w http.ResponseWriter, r *http.Request) {
	data, updated, err := s.st.AddressBook(r.Context(), userFrom(r).ID)
	if err != nil {
		s.log.Printf("ab get: %v", err)
		writeError(w, http.StatusInternalServerError, "Internal error")
		return
	}
	resp := legacyAbResponse{Data: data, LicensedDevices: 0}
	if data == "" {
		// Must still be a JSON string the client can jsonDecode (ab_model.dart:1033).
		resp.Data = `{"tags":[],"peers":[]}`
	} else {
		resp.UpdatedAt = updated.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) abSet(w http.ResponseWriter, r *http.Request) {
	var req legacyAbRequest
	if err := decodeJSON(w, r, maxABBody, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if !json.Valid([]byte(req.Data)) {
		writeError(w, http.StatusBadRequest, "data must be a JSON string")
		return
	}
	if err := s.st.SetAddressBook(r.Context(), userFrom(r).ID, req.Data); err != nil {
		s.log.Printf("ab set: %v", err)
		writeError(w, http.StatusInternalServerError, "Internal error")
		return
	}
	// 200 with empty body counts as success (ab_model.dart:1070-1072).
	w.WriteHeader(http.StatusOK)
}

// ---------- ignored ----------

// ignoreJSON accepts audit posts (/api/audit/conn|file|alarm, common.rs:1217-1223,
// connection.rs:1550-1640) whose responses the client does not use.
func (s *Server) ignoreJSON(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, maxJSONBody))
	writeJSON(w, http.StatusOK, map[string]any{})
}

// record accepts screen-recording upload chunks and drops them. The client only
// fails if the JSON reply has an "error" key (record_upload.rs:96-110).
func (s *Server) record(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, maxRecordBody))
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "not found")
}
