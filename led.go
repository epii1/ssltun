package led

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/felixge/httpsnoop"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/quicvarint"
	"github.com/taoso/led/ecdsa"
	"github.com/taoso/led/pay"
	"github.com/taoso/led/store"
	"github.com/taoso/led/tiktoken"
	"github.com/taoso/led/ws"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/net/idna"
)

// Proxy http proxy handler
type Proxy struct {
	sites map[string]*FileHandler
	users map[string]string

	BPEs map[string]*tiktoken.BPE

	Alipay *pay.Alipay

	TokenRepo  *store.TokenRepo
	TicketRepo store.TicketRepo

	AltSvc string

	chatLinks sync.Map

	DavEvs chan string
	Root   string
}

func (p *Proxy) bpe(model string) *tiktoken.BPE {
	var name string

	switch model {
	case "gpt-4o":
		name = "o200k_base"
	default:
		name = "cl100k_base"
	}

	return p.BPEs[name]
}

func (p *Proxy) auth(username, password string) bool {
	hash, ok := p.users[username]
	if !ok {
		ts, err := p.TicketRepo.List(username, 1)
		if err != nil {
			log.Println("ticket list error: ", username, err)
			return false
		}
		if len(ts) == 0 || ts[0].Bytes <= 0 || ts[0].Expires.Before(time.Now()) {
			return false
		}
		return true
	}
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

func (p *Proxy) SetUsers(users map[string]string) {
	p.users = users
}

func (p *Proxy) SetSites(sites map[string]string) {
	hs := make(map[string]*FileHandler, len(sites))
	for name := range sites {
		hs[name] = NewHandler(p.Root, name)
	}

	p.sites = hs
}

func (p *Proxy) MySite(name string) bool {
	_, ok := p.sites[name]
	return ok
}

func (p *Proxy) host(host string) string {
	if i := strings.Index(host, ":"); i > 0 {
		host = host[:i]
	}
	host, err := idna.ToUnicode(host)
	if err != nil {
		log.Println("host idna.ToUnicode error", host, err)
	}
	return host
}

func localRedirect(w http.ResponseWriter, r *http.Request, newPath string) {
	if q := r.URL.RawQuery; q != "" {
		newPath += "?" + q
	}
	w.Header().Set("Location", newPath)
	w.WriteHeader(http.StatusMovedPermanently)
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Header.Get("Connection") == "Upgrade" && req.Header.Get("Upgrade") == "websocket" && strings.HasPrefix(req.RequestURI, "/ws/") {
		ws.ProxyWebSocket(w, req)
		return
	}

	auth := req.Header.Get("Proxy-Authorization")
	if auth != "" {
		username, password, ok := parseBasicAuth(auth)
		if username != "" {
			req.URL.User = url.User(username)
		}
		if !ok || !p.auth(username, password) {
			w.Header().Set("Proxy-Authenticate", `Basic realm="Word Wide Web"`)
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		if req.Method == http.MethodConnect {
			if req.Proto == "connect-udp" {
				p.proxyUDP(w, req)
			} else {
				p.proxyHTTPS(w, req)
			}
			return
		} else {
			proxyHTTP(w, req)
			return
		}
	} else if req.Method == http.MethodConnect {
		w.Header().Set("Proxy-Authenticate", `Basic realm="Word Wide Web"`)
		w.WriteHeader(http.StatusProxyAuthRequired)
		return
	}

	w.Header().Set("Alt-Svc", p.AltSvc)
	w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")

	origin := req.Header.Get("Origin")
	if u, err := url.Parse(origin); err == nil {
		if _, ok := p.sites[p.host(u.Host)]; ok {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
	}

	if ip := req.Header.Get("CF-Connecting-IP"); ip != "" {
		_, port, _ := net.SplitHostPort(req.RemoteAddr)
		req.RemoteAddr = ip + ":" + port
	}
	p.serveLocal(w, req)
}

func (p *Proxy) serveLocal(w http.ResponseWriter, req *http.Request) {
	if f := p.sites[p.host(req.Host)]; f != nil {
		if strings.HasSuffix(req.RequestURI, "/index.htm") {
			localRedirect(w, req, "./")
			return
		}

		if strings.HasPrefix(req.RequestURI, "/+/v2/") {
			p.api2(w, req, f)
			return
		}

		if req.URL.Path == "/+/ticket" && req.Method == http.MethodPost {
			p.ServeTicket(w, req)
			return
		}

		if req.RequestURI == "/+/buy-tokens" && req.Method == http.MethodPost {
			p.buyTokens(w, req, f)
			return
		}

		if req.RequestURI == "/+/buy-tokens-notify" {
			p.buyTokensNotify(w, req, f)
			return
		}

		if req.RequestURI == "/+/buy-tokens-log" {
			p.buyTokensLog(w, req, f)
			return
		}

		if req.RequestURI == "/+/buy-tokens-logs" {
			p.buyTokensLogs(w, req, f)
			return
		}

		if req.RequestURI == "/+/buy-tokens-wallet" {
			p.buyTokensWallet(w, req, f)
			return
		}

		if req.RequestURI == "/+/mail" && req.Method == http.MethodPost {
			f.Comment(w, req)
			return
		}

		if req.RequestURI == "/+/push" && req.Method == http.MethodPost {
			f.webPush(w, req)
			return
		}

		if req.RequestURI == "/+/pdf2txt" && req.Method == http.MethodPost {
			f.pdf2txt(w, req)
			return
		}

		if strings.HasPrefix(req.RequestURI, "/+/chat/cancel") && req.Method == http.MethodPost {
			p.chatCancel(w, req, f)
			return
		}

		if strings.HasPrefix(req.RequestURI, "/+/chat") {
			h := w.Header()
			h.Add("Access-Control-Allow-Origin", req.Header.Get("Origin"))
			h.Add("Access-Control-Allow-Methods", "POST, OPTIONS")
			h.Add("Access-Control-Allow-Headers", "*")
			h.Add("Access-Control-Max-Age", "86400")
			if req.Method == http.MethodOptions {
				return
			}
			p.chat(w, req, f)
			return
		}

		if strings.HasPrefix(req.RequestURI, "/+/dav-events") {
			username, password, ok := req.BasicAuth()
			if username != "" {
				req.URL.User = url.User(username)
			}
			if !ok || username != f.Name || !p.auth(username, password) {
				w.Header().Set("WWW-Authenticate", `Basic realm="WebDAV"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			d, err := time.ParseDuration(req.URL.Query().Get("d"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			t := time.NewTimer(d)
			select {
			case e := <-p.DavEvs:
				evs := []string{e}
				// 合并通知短时间产生的文件变更
				time.Sleep(1 * time.Second)
				for {
					select {
					case e := <-p.DavEvs:
						evs = append(evs, e)
					default:
						goto resp
					}
				}
			resp:
				for _, e := range evs {
					w.Write([]byte(e + "\n"))
				}
			case <-t.C:
				w.WriteHeader(http.StatusNoContent)
			}
			return
		}

		if strings.HasPrefix(req.URL.Path, "/+/dav") {
			username, password, ok := req.BasicAuth()
			if username != "" {
				req.URL.User = url.User(username)
			}
			if !ok || username != f.Name || !p.auth(username, password) {
				w.Header().Set("WWW-Authenticate", `Basic realm="WebDAV"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			if strings.HasSuffix(req.URL.Path, ".md") {
				var e string
				switch req.Method {
				case "DELETE", "MOVE":
					e = "-" + f.Name + req.URL.Path[len("/+/dav"):]
				case "COPY", "PUT":
					e = "+" + f.Name
				}

				if e != "" {
					select {
					case p.DavEvs <- e:
					default:
					}
				}
			}
			f.dav.ServeHTTP(w, req)
			return
		}

		if f.Rewritten(w, req) {
			return
		}

		f.fs.ServeHTTP(w, req)
		return
	}
}

func (p *Proxy) api2(w http.ResponseWriter, req *http.Request, f *FileHandler) {
	sign := req.Header.Get("cg-sign")
	pubkey := req.Header.Get("cg-pubk")
	uid, _ := strconv.Atoi(req.Header.Get("cg-uid"))
	sid, _ := strconv.Atoi(req.Header.Get("cg-sid"))
	now, err := time.Parse(utcTime, req.Header.Get("cg-now"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		return
	}
	if time.Now().Sub(now).Abs() > 1*time.Minute {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		return
	}

	b := req.Body
	defer b.Close()

	data, err := io.ReadAll(b)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}

	req.Body = io.NopCloser(bytes.NewReader(data))

	var pk ecdsa.PublicKey
	if sid > 0 {
		s, err := p.TokenRepo.GetSession(sid)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(err.Error()))
			return
		}
		// 会话被删除
		if s.ID == 0 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		pk, _ = s.GetPubkey()
		req.Header.Set("cg-uid", strconv.Itoa(s.UserID))
	} else if uid > 0 {
		u, err := p.TokenRepo.GetWallet(uid)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(err.Error()))
			return
		}
		if u.ID == 0 {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("invalid user_id"))
			return
		}
		pk, _ = u.GetPubkey()
	} else {
		pk, err = ecdsa.ParsePubkey(pubkey)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(err.Error()))
			return
		}
	}

	// 保存压缩版本，供后续使用
	req.Header.Set("cg-pubk", ecdsa.Compress(pk))

	s := req.URL.Path + string(data) + now.UTC().Format(utcTime)
	ok, _, err := ecdsa.VerifyES256(s, sign, pk)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("invalid signature"))
		return
	}

	switch req.URL.Path[len("/+/v2/"):] {
	case "check-name":
		p.checkName(w, req, f)
	case "set-auth":
		p.setAuth(w, req, f)
	case "login":
		p.login(w, req, f)
	case "list-session":
		p.listSession(w, req, f)
	case "del-session":
		p.delSession(w, req, f)
	case "echo":
		w.Header().Set("content-type", req.Header.Get("content-type"))
		w.Write(data)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (p *Proxy) proxyUDP(w http.ResponseWriter, req *http.Request) {
	if req.ProtoMajor < 3 {
		w.WriteHeader(http.StatusNotImplemented)
		w.Write([]byte("does not support connect-udp over http/1.1 and h2"))
		return
	}

	addr, err := parseMasqueTarget(req.URL)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("invalid target"))
		log.Println("invalid target", req.URL)
		return
	}

	log.Println("target:", req.URL)

	up, err := net.Dial("udp", addr)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("dial udp err: " + err.Error()))
		log.Println("dial udp err", err)
		return
	}
	defer up.Close()

	w.Header().Add("capsule-protocol", "?1")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()

	w = w.(httpsnoop.Unwrapper).Unwrap()
	str := w.(http3.HTTPStreamer).HTTPStream()
	defer str.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	user := req.URL.User.Username()

	cost := func(n int) {}

	if _, ok := p.users[user]; !ok {
		cost = func(n int) {
			err := p.TicketRepo.Cost(user, n)
			if err != nil {
				log.Println("ticket cost error: ", user, n, err)
				str.Close()
				up.Close()
			}
		}
	}

	u := &bytesCounter{w: up, d: 1 * time.Second, f: cost}

	go u.Start()
	defer u.Done()

	go func() {
		defer wg.Done()
		b := make([]byte, 1500)
		for {
			n, err := u.Read(b[1:])
			if err != nil {
				log.Println("up.Read err:", err)
				return
			}
			err = str.SendDatagram(b[:n+1])
			if err != nil {
				log.Println("SendDatagram err:", err)
				return
			}
		}
	}()

	go func() {
		defer wg.Done()
		ctx := context.Background()
		for {
			b, err := str.ReceiveDatagram(ctx)
			if err != nil {
				log.Println("ReceiveDatagram err:", err)
				return
			}
			_, n, err := quicvarint.Parse(b)
			if err != nil {
				log.Println("parse cid err:", err)
				return
			}
			_, err = u.Write(b[n:])
			if err != nil {
				log.Println("up.Write err:", err)
				return
			}
		}
	}()

	wg.Wait()
}

func (p *Proxy) proxyHTTPS(w http.ResponseWriter, req *http.Request) {
	address := req.RequestURI
	upConn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer upConn.Close()

	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()

	var downConn io.ReadWriteCloser
	if req.ProtoMajor >= 2 {
		downConn = flushWriter{w: w, r: req.Body}
	} else {
		downConn, _, err = w.(http.Hijacker).Hijack()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("hijack err: " + err.Error()))
			return
		}
		defer downConn.Close()
	}

	var wg sync.WaitGroup
	wg.Add(2)

	user := req.URL.User.Username()

	cost := func(n int) {}

	if _, ok := p.users[user]; !ok {
		cost = func(n int) {
			err := p.TicketRepo.Cost(user, n)
			if err != nil {
				log.Println("ticket cost error: ", user, n, err)
				downConn.Close()
				upConn.Close()
			}
		}
	}

	u := &bytesCounter{w: upConn, d: 1 * time.Second, f: cost}

	go u.Start()
	defer u.Done()

	go func() {
		defer wg.Done()
		io.Copy(u, downConn)
	}()
	go func() {
		defer wg.Done()
		io.Copy(downConn, u)
	}()

	wg.Wait()
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
