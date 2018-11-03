// vim:set sw=2 sts=2:
package main

import (
  "io"
  "log"
  "net"
  "time"
  "strings"

  "github.com/felixge/tcpkeepalive"
  "github.com/daemonwhisper0101/openproxy/proxydb"
)

type Browser struct {
  useragent string
  proxy *proxydb.Proxy
}

var db *proxydb.DB
var browsers map[string]*Browser

func iocopy(lconn, rconn net.Conn) {
  d1 := make(chan struct{})
  d2 := make(chan struct{})
  ldone, rdone := false, false
  go func() {
    io.Copy(lconn, rconn)
    d1 <- struct{}{}
    rdone = true
  }()
  go func() {
    io.Copy(rconn, lconn)
    d2 <- struct{}{}
    ldone = true
  }()
  go func() {
    for {
      if rdone && ldone {
	return
      }
      if rdone && !ldone {
	log.Printf("alive -> %s\n", rconn.RemoteAddr().String())
      }
      if !rdone && ldone {
	log.Printf("alive <- %s\n", rconn.RemoteAddr().String())
      }
      time.Sleep(time.Minute)
    }
  }()
  select {
  case <-d1: go func() { <-d2 }()
  case <-d2: go func() { <-d1 }()
  }
  time.Sleep(time.Second)
}

func isDirect(r []string) bool {
  if r[0] == "CONNECT" {
    if strings.Index(r[1], "www.google.com:443") > 0 {
      return true
    }
    if strings.Index(r[1], "www.google.co.jp:443") > 0 {
      return true
    }
  }
  return false
}

func handle(conn net.Conn) {
  defer conn.Close()

  rawbuf := make([]byte, 4096) // for request header
  n, _ := conn.Read(rawbuf)
  if n <= 0 {
    return
  }
  buf := rawbuf[:n]
  req := string(buf)
  // find User-Agent
  ua := "default"
  lines := strings.Split(req, "\r\n")
  r := strings.Split(lines[0], " ")
  if isDirect(r) {
    // no proxy
    log.Printf("direct connect to %s\n", r[1])
    rconn, err := net.DialTimeout("tcp", r[1], time.Second * 10)
    if err != nil {
      log.Println(err)
      conn.Write([]byte("HTTP/1.1 500 Internal Server Error\r\n\r\n"))
      return
    }
    defer rconn.Close()
    conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
    iocopy(conn, rconn)
    return
  }
  for _, line := range lines {
    a := strings.SplitN(line, ":", 2)
    if a[0] == "User-Agent" {
      ua = strings.TrimSpace(a[1])
    }
  }
  b, ok := browsers[ua]
  if !ok {
    // add
    b = &Browser{ useragent: ua, proxy: nil}
    browsers[ua] = b
    b.proxy = db.GetProxy()
  }

  if b.proxy == nil || b.proxy.IsAlive() == false {
    b.proxy = db.GetProxy()
    if b.proxy == nil {
      // still nil, no proxy found
      log.Printf("no proxy for %s\n", ua)
      return
    }
  }

  proxy := b.proxy.OpenProxy()

  log.Printf("use %s for %s\n", proxy.HostPort(), ua)

  rconn, err := net.DialTimeout("tcp", proxy.HostPort(), time.Second * 10)
  if err != nil {
    log.Println(err)
    b.proxy.Bad() // mark bad
    return
  }
  defer rconn.Close()

  // TCP keep alive
  ka, err := tcpkeepalive.EnableKeepAlive(rconn)
  ka.SetKeepAliveIdle(time.Second * 30)
  ka.SetKeepAliveCount(4)
  ka.SetKeepAliveInterval(time.Second * 5)

  rconn.Write(buf)

  iocopy(conn, rconn)
}

func main() {
  // listen on localhost
  ln, err := net.Listen("tcp", "127.0.0.1:8080")
  if err != nil {
    log.Fatal(err)
    return
  }

  browsers = map[string]*Browser{}
  db = proxydb.New(proxydb.SSL)
  db.Update()
  running := true

  // currently we don't need graceful shutdown
  go func() {
    lasttime := time.Now()
    db.Start()
    for running {
      log.Printf("proxydb %d candidates\n", len(db.Proxies))
      now := time.Now()
      // update every 5min.
      if now.After(lasttime.Add(time.Minute * 5)) {
	db.Update()
	lasttime = now
      }
      time.Sleep(time.Minute)
    }
    db.Stop()
  }()

  log.Println("start")

  for {
    conn, err := ln.Accept()
    if err != nil {
      log.Println(err)
      continue
    }
    go handle(conn)
  }

  log.Println("done")
}
