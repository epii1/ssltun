package ssltun

import (
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

type Handler struct {
	Handler http.Handler
	Root    string
}

func (h *Handler) Rewritten(w http.ResponseWriter, req *http.Request) bool {
	b, err := os.ReadFile(path.Join(h.Root, "rewrite.txt"))

	if os.IsNotExist(err) {
		return false
	}

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return false
	}

	lines := strings.TrimSpace(string(b))
	for _, line := range strings.Split(lines, "\n") {
		parts := strings.Split(line, " -> ")

		if len(parts) < 2 {
			continue
		}

		oldURL := parts[0]
		newURL := parts[1]

		if req.URL.Path == oldURL {
			http.Redirect(w, req, newURL, http.StatusMovedPermanently)
			return true
		}
	}

	return false
}

// Proxy http proxy handler
type Proxy struct {
	// DomainNames proxy server domain name
	DomainNames []string
	// Auth is function to check if username and password is match.
	Auth func(username, password string) bool

	FileHandlers map[string]Handler

	AltSvc []string
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	for _, name := range p.DomainNames {
		host := req.Host
		if i := strings.Index(host, ":"); i > 0 {
			host = host[:i]
		}
		if host == name {
			if h, ok := p.FileHandlers[host]; ok {
				if h.Rewritten(w, req) {
					return
				}

				for _, a := range p.AltSvc {
					w.Header().Add("Alt-Svc", a)
				}
				h.Handler.ServeHTTP(w, req)
				return
			}

			// send default slogan
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("Across the Great Wall we can reach every corner in the world.\n"))
			return
		}
	}

	auth := req.Header.Get("Proxy-Authorization")
	username, password, _ := parseBasicAuth(auth)
	if !p.Auth(username, password) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="word wide web"`)
		w.WriteHeader(http.StatusProxyAuthRequired)
		return
	}

	if req.Method == http.MethodConnect {
		proxyHTTPS(w, req)
	} else {
		proxyHTTP(w, req)
	}
}

func proxyHTTPS(w http.ResponseWriter, req *http.Request) {
	address := req.RequestURI
	upConn, err := net.DialTimeout("tcp", address, 500*time.Millisecond)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer upConn.Close()

	var downConn io.ReadWriter
	if req.ProtoMajor == 2 {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		downConn = flushWriter{w: w, r: req.Body}
	} else {
		downConn, _, err = w.(http.Hijacker).Hijack()
		downConn.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
	}

	go func() {
		io.Copy(upConn, downConn)
	}()

	io.Copy(downConn, upConn)
}

func proxyHTTP(w http.ResponseWriter, req *http.Request) {
	var url string
	if strings.HasPrefix(req.RequestURI, "http") {
		url = req.RequestURI
	} else {
		url = "http://" + req.Host + req.RequestURI
	}
	r, err := http.NewRequest(req.Method, url, req.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}

	h := req.Header.Clone()
	h.Del("Proxy-Authorization")
	h.Del("Te")
	h.Del("TransferEncoding")
	h.Del("Host")
	h.Set("Connection", "close")
	req.Header = h

	c := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// do not follow redirect response
			return http.ErrUseLastResponse
		},
	}

	resp, err := c.Do(r)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(err.Error()))
		return
	}

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}

	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
	return
}

type flushWriter struct {
	w io.Writer
	r io.Reader
}

func (fw flushWriter) Write(p []byte) (n int, err error) {
	n, err = fw.w.Write(p)
	fw.w.(http.Flusher).Flush()
	return
}

func (fw flushWriter) Read(p []byte) (n int, err error) {
	return fw.r.Read(p)
}

func (fw flushWriter) Close() error {
	return nil
}

// parseBasicAuth parses an HTTP Basic Authentication string.
// "Basic QWxhZGRpbjpvcGVuIHNlc2FtZQ==" returns ("Aladdin", "open sesame", true).
func parseBasicAuth(auth string) (username, password string, ok bool) {
	const prefix = "Basic "
	// Case insensitive prefix match. See Issue 22736.
	if len(auth) < len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return
	}
	c, err := base64.StdEncoding.DecodeString(auth[len(prefix):])
	if err != nil {
		return
	}
	cs := string(c)
	s := strings.IndexByte(cs, ':')
	if s < 0 {
		return
	}
	return cs[:s], cs[s+1:], true
}
