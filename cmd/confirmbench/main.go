// Command confirmbench compares the two SYNCHRONOUS transports for the
// SendConfirmation operation — gRPC (HTTP/2 + Protobuf) vs REST (HTTP/1.1 + JSON)
// — on throughput, latency, and bytes on the wire (HW10, ADR-011).
//
// Both servers do NO real work (no SMTP), so we measure pure transport +
// serialization, not the email send. A single harness drives both the same way
// (same machine, connection reuse, no-op backend), so the only variable is the
// transport. The production default stays the async broker; this is the
// REST-vs-gRPC comparison the homework asks for.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	notifierv1 "github-release-notifier/gen/notifier/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

const (
	warmup = 3000  // unmeasured requests to prime connections + JIT-warm the paths
	total  = 30000 // measured requests per (transport, concurrency)
)

var concurrencies = []int{1, 8, 64}

// jsonReq mirrors SendConfirmationRequest for the REST/JSON side.
type jsonReq struct {
	Email      string `json:"email"`
	Repo       string `json:"repo"`
	ConfirmURL string `json:"confirm_url"`
}

func main() {
	grpcAddr, gIn, gOut, stopGRPC := startGRPC()
	defer stopGRPC()
	httpAddr, hIn, hOut, stopHTTP := startHTTP()
	defer stopHTTP()

	req := &notifierv1.SendConfirmationRequest{
		Email:      "user@example.com",
		Repo:       "golang/go",
		ConfirmUrl: "http://localhost:8080/api/confirm/8b1e6a3c-0f4d-4c2a-9b7e-1d2c3f4a5b6c",
	}
	jr := jsonReq{Email: req.GetEmail(), Repo: req.GetRepo(), ConfirmURL: req.GetConfirmUrl()}

	fmt.Printf("confirmbench — %d measured requests per cell, warmup %d\n", total, warmup)
	fmt.Printf("payload: email=%q repo=%q confirm_url=%q\n\n", jr.Email, jr.Repo, jr.ConfirmURL)

	fmt.Printf("%-6s %-6s %12s %10s %10s %10s %10s %10s\n",
		"proto", "conc", "req/s", "p50", "p95", "p99", "req B/w", "resp B/w")
	fmt.Println("-------------------------------------------------------------------------------------")

	for _, c := range concurrencies {
		// gRPC
		atomic.StoreInt64(gIn, 0)
		atomic.StoreInt64(gOut, 0)
		gr := benchGRPC(grpcAddr, c, req)
		printRow("gRPC", c, gr, atomic.LoadInt64(gIn), atomic.LoadInt64(gOut))

		// REST
		atomic.StoreInt64(hIn, 0)
		atomic.StoreInt64(hOut, 0)
		hrr := benchHTTP(httpAddr, c, jr)
		printRow("REST", c, hrr, atomic.LoadInt64(hIn), atomic.LoadInt64(hOut))
	}

	printPayloadSizes()
}

// ---- servers (no-op backends, byte-counting listeners) ----

type noopGRPC struct {
	notifierv1.UnimplementedNotifierServiceServer
}

func (noopGRPC) SendConfirmation(context.Context, *notifierv1.SendConfirmationRequest) (*notifierv1.SendConfirmationResponse, error) {
	return &notifierv1.SendConfirmationResponse{}, nil
}

func startGRPC() (addr string, in, out *int64, stop func()) {
	var ci, co int64
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal("grpc listen", err)
	}
	s := grpc.NewServer()
	notifierv1.RegisterNotifierServiceServer(s, noopGRPC{})
	go func() { _ = s.Serve(&countingListener{Listener: lis, in: &ci, out: &co}) }()
	return lis.Addr().String(), &ci, &co, s.Stop
}

func startHTTP() (addr string, in, out *int64, stop func()) {
	var ci, co int64
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal("http listen", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/confirm", func(w http.ResponseWriter, r *http.Request) {
		var req jsonReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(&countingListener{Listener: lis, in: &ci, out: &co}) }()
	return lis.Addr().String(), &ci, &co, func() { _ = srv.Close() }
}

// ---- load drivers ----

func benchGRPC(addr string, concurrency int, req *notifierv1.SendConfirmationRequest) result {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fatal("grpc dial", err)
	}
	defer func() { _ = conn.Close() }()
	client := notifierv1.NewNotifierServiceClient(conn)
	call := func() time.Duration {
		t := time.Now()
		_, _ = client.SendConfirmation(context.Background(), req)
		return time.Since(t)
	}
	drive(concurrency, warmup, call) // warm up (unmeasured)
	return drive(concurrency, total, call)
}

func benchHTTP(addr string, concurrency int, jr jsonReq) result {
	tr := &http.Transport{MaxIdleConns: 1024, MaxIdleConnsPerHost: 1024, IdleConnTimeout: 30 * time.Second}
	client := &http.Client{Transport: tr}
	url := "http://" + addr + "/confirm"
	body, _ := json.Marshal(jr)
	call := func() time.Duration {
		t := time.Now()
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		return time.Since(t)
	}
	drive(concurrency, warmup, call)
	return drive(concurrency, total, call)
}

// drive runs `count` calls across `concurrency` workers (closed loop) and
// returns the latencies plus wall-clock elapsed.
func drive(concurrency, count int, call func() time.Duration) result {
	lat := make([]time.Duration, count)
	var idx int64 = -1
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := atomic.AddInt64(&idx, 1)
				if int(i) >= count {
					return
				}
				lat[i] = call()
			}
		}()
	}
	wg.Wait()
	return result{reqs: count, elapsed: time.Since(start), latencies: lat}
}

type result struct {
	reqs      int
	elapsed   time.Duration
	latencies []time.Duration
}

func printRow(name string, conc int, r result, bytesIn, bytesOut int64) {
	sort.Slice(r.latencies, func(i, j int) bool { return r.latencies[i] < r.latencies[j] })
	rps := float64(r.reqs) / r.elapsed.Seconds()
	fmt.Printf("%-6s %-6d %12.0f %10s %10s %10s %9.0f %9.0f\n",
		name, conc, rps,
		dur(pct(r.latencies, 0.50)), dur(pct(r.latencies, 0.95)), dur(pct(r.latencies, 0.99)),
		float64(bytesIn)/float64(r.reqs), float64(bytesOut)/float64(r.reqs))
}

func printPayloadSizes() {
	fmt.Println("\npayload size (serialization only, one request):")
	fmt.Printf("%-8s %10s %10s %8s\n", "size", "Protobuf", "JSON", "ratio")
	fmt.Println("--------------------------------------------")
	sizes := []struct {
		label string
		r     *notifierv1.SendConfirmationRequest
	}{
		{"small", &notifierv1.SendConfirmationRequest{
			Email: "user@example.com", Repo: "golang/go",
			ConfirmUrl: "http://localhost:8080/api/confirm/8b1e6a3c-0f4d-4c2a-9b7e-1d2c3f4a5b6c"}},
		{"large", &notifierv1.SendConfirmationRequest{
			Email: "some.longer.name+release-tag@mail.subdomain.example.co.uk", Repo: "kubernetes/kubernetes",
			ConfirmUrl: "https://notifications.example.com/api/v1/confirm/8b1e6a3c-0f4d-4c2a-9b7e-1d2c3f4a5b6c?src=email&campaign=weekly"}},
	}
	for _, s := range sizes {
		pb, _ := proto.Marshal(s.r)
		js, _ := json.Marshal(jsonReq{Email: s.r.GetEmail(), Repo: s.r.GetRepo(), ConfirmURL: s.r.GetConfirmUrl()})
		fmt.Printf("%-8s %9dB %9dB %7.2fx\n", s.label, len(pb), len(js), float64(len(js))/float64(len(pb)))
	}
	fmt.Println("\n(req B/w and resp B/w above are total bytes on the wire per request,")
	fmt.Println(" including HTTP framing + headers: HTTP/2 HPACK vs HTTP/1.1 plain text.)")
}

// ---- helpers ----

func pct(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[int(p*float64(len(sorted)-1))]
}

func dur(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%dµs", d.Microseconds())
	}
	return fmt.Sprintf("%.2fms", float64(d.Microseconds())/1000)
}

func fatal(what string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", what, err)
	os.Exit(1)
}

// countingListener wraps every accepted conn so we can count bytes read (request
// bytes, "in") and written (response bytes, "out") on the wire.
type countingListener struct {
	net.Listener
	in, out *int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: c, in: l.in, out: l.out}, nil
}

type countingConn struct {
	net.Conn
	in, out *int64
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	atomic.AddInt64(c.in, int64(n))
	return n, err
}

func (c *countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	atomic.AddInt64(c.out, int64(n))
	return n, err
}
