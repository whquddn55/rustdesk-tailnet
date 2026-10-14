package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"rustdesk-tailnet-api/internal/api"
	"rustdesk-tailnet-api/internal/netguard"
	"rustdesk-tailnet-api/internal/store"
)

const usage = `rustdesk-tailnet-api — RustDesk API server reachable only over Tailscale

Commands:
  serve                                   run the API server
  user add <name> [-group G] [-admin] [-display-name N] [-email E]
  user list
  user del <name>
  user passwd <name>
  user set-group <name> <group|->         "-" removes the group
  user disable <name> | user enable <name>
  group add <name>
  group list
  group del <name>
  device list
  device del <rustdesk-id>

Passwords are read from the terminal (no echo) or, when stdin is not a
terminal, from the first line of stdin.

Environment:
  LISTEN_ADDR     required for serve; Tailscale IP:port, e.g. 100.101.102.103:21114
  DB_PATH         SQLite file (default ./data/api.db)
  TOKEN_TTL       login token lifetime, Go duration (default 720h); sliding
  ALLOW_LOOPBACK  "1" to also accept 127.0.0.1/::1 — local testing only, default off
  SERVER_HOST     ID/relay/API host pushed to logged-in devices (default: LISTEN_ADDR host)
  HBBS_KEY_FILE   hbbs public key file pushed as the client Key, e.g. /rustdesk/id_ed25519.pub
  HBBS_PORT       hbbs ID port the devices use (default 21116; NAT test port is one below)
  HBBR_PORT       hbbr relay port the devices use (default 21117)
  PUSH_SERVER_HOST, PUSH_API_PORT, PUSH_HBBS_PORT, PUSH_HBBR_PORT
                  hand out another server's address instead (moving devices; ports default to 21114/21116/21117)
  GITHUB_REPO     client builds for the update check (default whquddn55/rustdesk-tailnet, "-" disables)
`

func dbPath() string {
	if p := os.Getenv("DB_PATH"); p != "" {
		return p
	}
	return "./data/api.db"
}

func openStore() (*store.Store, error) {
	p := dbPath()
	if dir := dirOf(p); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	return store.Open(p)
}

func dirOf(p string) string {
	i := strings.LastIndexAny(p, `/\`)
	if i <= 0 {
		return ""
	}
	return p[:i]
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("missing command")
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "user":
		return userCmd(args[1:])
	case "group":
		return groupCmd(args[1:])
	case "device":
		return deviceCmd(args[1:])
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// ---------- serve ----------

func envBool(name string) bool {
	v, _ := strconv.ParseBool(os.Getenv(name))
	return v
}

// serverConfig validates the environment. Split out for testing.
func serverConfig() (listen string, policy netguard.Policy, ttl time.Duration, err error) {
	policy = netguard.Policy{AllowLoopback: envBool("ALLOW_LOOPBACK")}
	listen = os.Getenv("LISTEN_ADDR")
	if err = policy.ValidateListenAddr(listen); err != nil {
		return
	}
	ttl = 720 * time.Hour
	if v := os.Getenv("TOKEN_TTL"); v != "" {
		ttl, err = time.ParseDuration(v)
		if err != nil || ttl <= 0 {
			err = fmt.Errorf("invalid TOKEN_TTL %q", v)
			return
		}
	}
	return
}

// releasesURL is where the update check looks for client builds.
func releasesURL() string {
	repo := os.Getenv("GITHUB_REPO")
	switch repo {
	case "":
		repo = "whquddn55/rustdesk-tailnet"
	case "-":
		return ""
	}
	return "https://api.github.com/repos/" + repo + "/releases?per_page=30"
}

// serverSettings is what logged-in devices receive as their ID / relay / API
// server and key (see api.ServerSettings).
func serverSettings(listen string) api.ServerSettings {
	host := os.Getenv("SERVER_HOST")
	if host == "" {
		host, _, _ = net.SplitHostPort(listen)
	}
	_, listenPort, _ := net.SplitHostPort(listen)
	apiPort, _ := strconv.Atoi(listenPort)
	idPort, _ := strconv.Atoi(os.Getenv("HBBS_PORT"))
	relayPort, _ := strconv.Atoi(os.Getenv("HBBR_PORT"))
	// PUSH_* move every logged-in device to another server: this instance keeps
	// serving on its own address but hands out the new one (see README, 서버 이전).
	if v := os.Getenv("PUSH_SERVER_HOST"); v != "" {
		host = v
		apiPort, _ = strconv.Atoi(os.Getenv("PUSH_API_PORT"))
		idPort, _ = strconv.Atoi(os.Getenv("PUSH_HBBS_PORT"))
		relayPort, _ = strconv.Atoi(os.Getenv("PUSH_HBBR_PORT"))
	}
	return api.ServerSettings{
		Host: host, KeyFile: os.Getenv("HBBS_KEY_FILE"),
		APIPort: apiPort, IDPort: idPort, RelayPort: relayPort,
	}
}

func serve(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("serve takes no arguments (configure with environment variables)")
	}
	listen, policy, ttl, err := serverConfig()
	if err != nil {
		return fmt.Errorf("refusing to start: %w", err)
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)
	if policy.AllowLoopback {
		logger.Printf("WARNING: ALLOW_LOOPBACK is on; loopback clients are accepted. Do not use in production.")
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w (is Tailscale up on this machine?)", listen, err)
	}
	settings := serverSettings(listen)
	if settings.KeyFile == "" {
		logger.Printf("WARNING: HBBS_KEY_FILE is not set; devices will not receive the server key.")
	}
	srv := &http.Server{
		Handler:           api.New(api.Config{Store: st, Guard: policy, TokenTTL: ttl, Logger: logger, Settings: settings, ReleasesURL: releasesURL()}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	logger.Printf("listening on http://%s (db %s, token ttl %s)", listen, dbPath(), ttl)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// ---------- password input ----------

var stdinReader = bufio.NewReader(os.Stdin)

func readPassword(confirm bool) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, "Password: ")
		p1, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		if confirm {
			fmt.Fprint(os.Stderr, "Repeat password: ")
			p2, err := term.ReadPassword(fd)
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return "", err
			}
			if string(p1) != string(p2) {
				return "", errors.New("passwords do not match")
			}
		}
		if len(p1) == 0 {
			return "", errors.New("empty password")
		}
		return string(p1), nil
	}
	line, err := stdinReader.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", fmt.Errorf("read password from stdin: %w", err)
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errors.New("empty password")
	}
	return line, nil
}

// parseFlags parses flags that may appear before or after positional args.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func need(pos []string, n int, use string) error {
	if len(pos) != n {
		return fmt.Errorf("usage: %s", use)
	}
	return nil
}

// ---------- user ----------

func userCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: user add|list|del|passwd|set-group|disable|enable")
	}
	ctx := context.Background()
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		fs := flag.NewFlagSet("user add", flag.ContinueOnError)
		group := fs.String("group", "", "group name")
		admin := fs.Bool("admin", false, "mark as admin (shown in the client only)")
		display := fs.String("display-name", "", "display name")
		email := fs.String("email", "", "email")
		pos, err := parseFlags(fs, rest)
		if err != nil {
			return err
		}
		if err := need(pos, 1, "user add <name> [-group G] [-admin] [-display-name N] [-email E]"); err != nil {
			return err
		}
		pw, err := readPassword(true)
		if err != nil {
			return err
		}
		u, err := st.AddUser(ctx, store.NewUser{Name: pos[0], Password: pw, DisplayName: *display, Email: *email, IsAdmin: *admin, Group: *group})
		if err != nil {
			return err
		}
		fmt.Printf("user %q created (group: %s)\n", u.Name, orDash(u.GroupName))
	case "list":
		users, err := st.ListUsers(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tDISPLAY NAME\tGROUP\tADMIN\tSTATUS\tCREATED")
		for _, u := range users {
			status := "enabled"
			if u.Status != 1 {
				status = "disabled"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%v\t%s\t%s\n", u.Name, orDash(u.DisplayName), orDash(u.GroupName), u.IsAdmin, status, u.CreatedAt.Format("2006-01-02"))
		}
		return tw.Flush()
	case "del":
		if err := need(rest, 1, "user del <name>"); err != nil {
			return err
		}
		if err := st.DeleteUser(ctx, rest[0]); err != nil {
			return err
		}
		fmt.Printf("user %q deleted (tokens revoked, devices unbound)\n", rest[0])
	case "passwd":
		if err := need(rest, 1, "user passwd <name>"); err != nil {
			return err
		}
		if _, err := st.UserByName(ctx, rest[0]); err != nil {
			return err
		}
		pw, err := readPassword(true)
		if err != nil {
			return err
		}
		if err := st.SetPassword(ctx, rest[0], pw); err != nil {
			return err
		}
		fmt.Printf("password changed for %q (existing logins revoked)\n", rest[0])
	case "set-group":
		if err := need(rest, 2, "user set-group <name> <group|->"); err != nil {
			return err
		}
		g := rest[1]
		if g == "-" {
			g = ""
		}
		if err := st.SetUserGroup(ctx, rest[0], g); err != nil {
			return err
		}
		fmt.Printf("user %q group set to %s\n", rest[0], orDash(g))
	case "disable", "enable":
		if err := need(rest, 1, "user "+sub+" <name>"); err != nil {
			return err
		}
		status := 1
		if sub == "disable" {
			status = 0
		}
		if err := st.SetUserStatus(ctx, rest[0], status); err != nil {
			return err
		}
		fmt.Printf("user %q %sd\n", rest[0], sub)
	default:
		return fmt.Errorf("unknown user command %q", sub)
	}
	return nil
}

// ---------- group ----------

func groupCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: group add|list|del")
	}
	ctx := context.Background()
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		if err := need(rest, 1, "group add <name>"); err != nil {
			return err
		}
		g, err := st.AddGroup(ctx, rest[0])
		if err != nil {
			return err
		}
		fmt.Printf("group %q created\n", g.Name)
	case "list":
		groups, err := st.ListGroups(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tMEMBERS\tCREATED")
		for _, g := range groups {
			fmt.Fprintf(tw, "%s\t%d\t%s\n", g.Name, g.Members, g.CreatedAt.Format("2006-01-02"))
		}
		return tw.Flush()
	case "del":
		if err := need(rest, 1, "group del <name>"); err != nil {
			return err
		}
		if err := st.DeleteGroup(ctx, rest[0]); err != nil {
			return err
		}
		fmt.Printf("group %q deleted (members now have no group)\n", rest[0])
	default:
		return fmt.Errorf("unknown group command %q", sub)
	}
	return nil
}

// ---------- device ----------

func deviceCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: device list|del")
	}
	ctx := context.Background()
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		devs, err := st.ListDevices(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "RUSTDESK ID\tHOSTNAME\tOS\tUSER\tGROUP\tLAST IP\tLAST SEEN\tUUID")
		for _, d := range devs {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d.RustdeskID, orDash(d.Hostname), orDash(d.OS), orDash(d.UserName),
				orDash(d.GroupName), orDash(d.LastIP), d.LastSeen.Format("2006-01-02 15:04:05"), d.UUID)
		}
		return tw.Flush()
	case "del":
		if err := need(rest, 1, "device del <rustdesk-id>"); err != nil {
			return err
		}
		if err := st.DeleteDevice(ctx, rest[0]); err != nil {
			return err
		}
		fmt.Printf("device %q deleted\n", rest[0])
	default:
		return fmt.Errorf("unknown device command %q", sub)
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
