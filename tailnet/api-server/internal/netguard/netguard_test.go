package netguard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateListenAddr(t *testing.T) {
	strict := Policy{}
	loop := Policy{AllowLoopback: true}
	cases := []struct {
		addr   string
		p      Policy
		wantOK bool
	}{
		{"100.101.102.103:21114", strict, true},
		{"100.64.0.1:21114", strict, true},
		{"100.127.255.254:21114", strict, true},
		{"[fd7a:115c:a1e0::1]:21114", strict, true},
		{"", strict, false},
		{"0.0.0.0:21114", strict, false},
		{"[::]:21114", strict, false},
		{":21114", strict, false},
		{"192.168.0.10:21114", strict, false},
		{"10.0.0.5:21114", strict, false},
		{"100.128.0.1:21114", strict, false}, // just outside 100.64/10
		{"100.63.255.255:21114", strict, false},
		{"8.8.8.8:21114", strict, false},
		{"myhost.tailnet.ts.net:21114", strict, false},
		{"100.101.102.103", strict, false}, // no port
		{"127.0.0.1:21114", strict, false},
		{"127.0.0.1:21114", loop, true},
		{"0.0.0.0:21114", loop, false},
	}
	for _, c := range cases {
		err := c.p.ValidateListenAddr(c.addr)
		if (err == nil) != c.wantOK {
			t.Errorf("ValidateListenAddr(%q, loopback=%v) err=%v, wantOK=%v", c.addr, c.p.AllowLoopback, err, c.wantOK)
		}
	}
}

func TestMiddleware(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	cases := []struct {
		remote string
		xff    string
		p      Policy
		want   int
	}{
		{"100.100.1.2:5555", "", Policy{}, 204},
		{"[fd7a:115c:a1e0:ab12::1]:5555", "", Policy{}, 204},
		{"[::ffff:100.100.1.2]:5555", "", Policy{}, 204},
		{"192.168.1.10:5555", "", Policy{}, 403},
		{"203.0.113.9:5555", "100.100.1.2", Policy{}, 403}, // XFF is ignored
		{"172.17.0.1:5555", "", Policy{}, 403},
		{"127.0.0.1:5555", "", Policy{}, 403},
		{"127.0.0.1:5555", "", Policy{AllowLoopback: true}, 204},
		{"[fd00::1]:5555", "", Policy{}, 403},
		{"garbage", "", Policy{}, 403},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", "/api/login-options", nil)
		req.RemoteAddr = c.remote
		if c.xff != "" {
			req.Header.Set("X-Forwarded-For", c.xff)
		}
		rec := httptest.NewRecorder()
		c.p.Middleware(ok).ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("remote %s xff %q: got %d want %d", c.remote, c.xff, rec.Code, c.want)
		}
	}
}
