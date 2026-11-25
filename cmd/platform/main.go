package main

import (
	"context"
	"crypto/tls"
	"embed"
	"flag"
	"fmt"
	"github.com/william-lbn/ops-observability-platform/internal/api"
	"github.com/william-lbn/ops-observability-platform/internal/config"
	"github.com/william-lbn/ops-observability-platform/internal/store"
	"github.com/william-lbn/ops-observability-platform/internal/worker"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

//go:embed web/*
var webFiles embed.FS
var Version = "development"

func main() {
	mode := flag.String("mode", "api", "api, worker, migrate, validate")
	path := flag.String("config", "/etc/ops/config.json", "configuration file")
	metricsListen := flag.String("metrics-listen", ":9090", "internal metrics listener")
	flag.Parse()
	if *mode == "prestop" {
		_, port, e := net.SplitHostPort(*metricsListen)
		if e != nil {
			log.Fatal("invalid local metrics listener")
		}
		client := &http.Client{Timeout: 12 * time.Second, Transport: &http.Transport{Proxy: nil}}
		req, _ := http.NewRequest("POST", "http://127.0.0.1:"+port+"/internal/drain", nil)
		response, e := client.Do(req)
		if e != nil {
			log.Fatal("local drain hook failed")
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			log.Fatal("local drain hook refused")
		}
		return
	}
	c, e := config.Load(*path)
	if e != nil {
		log.Fatal("configuration rejected: ", e)
	}
	if *mode == "validate" {
		log.Print("configuration accepted; no backend connection attempted")
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	s, e := store.Open(startup, c.DatabaseURL)
	cancel()
	if e != nil {
		log.Fatal("database connection unavailable")
	}
	defer s.DB.Close()
	if *mode == "migrate" {
		if e = s.Migrate(ctx); e != nil {
			log.Fatal("database migration failed: ", e)
		}
		return
	}
	if *mode == "worker" {
		w := worker.Worker{Store: s, ConfigPath: *path}
		mux := http.NewServeMux()
		mux.HandleFunc("/metrics", w.Metrics)
		mux.HandleFunc("/healthz", func(out http.ResponseWriter, r *http.Request) {
			if last := w.LastAttempt.Load(); last > 0 && time.Now().Unix()-last > 90 {
				out.WriteHeader(503)
				return
			}
			out.Write([]byte("alive"))
		})
		server := &http.Server{Addr: *metricsListen, Handler: mux, ReadHeaderTimeout: 3 * time.Second}
		go func() {
			if e := server.ListenAndServe(); e != nil && e != http.ErrServerClosed {
				log.Fatal("internal metrics listener unavailable")
			}
		}()
		defer server.Close()
		if e = w.Run(ctx); e != nil {
			log.Fatal("worker failed")
		}
		return
	}
	static, _ := fs.Sub(webFiles, "web")
	handler, e := api.New(s, *path, http.FileServer(http.FS(static)))
	if e != nil {
		log.Fatal("contract compilation failed: ", e)
	}
	metrics := http.NewServeMux()
	metrics.HandleFunc("/internal/drain", func(w http.ResponseWriter, r *http.Request) {
		host, _, e := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		if e != nil || ip == nil || !ip.IsLoopback() || r.Method != "POST" {
			w.WriteHeader(403)
			return
		}
		handler.Draining.Store(true)
		time.Sleep(8 * time.Second)
		w.Write([]byte("draining"))
	})
	metrics.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "ops_http_requests_total %d\nops_build_info{version=%q} 1\n", handler.Requests.Load(), Version)
	})
	metricServer := &http.Server{Addr: *metricsListen, Handler: metrics, ReadHeaderTimeout: 3 * time.Second}
	go func() {
		if e := metricServer.ListenAndServe(); e != nil && e != http.ErrServerClosed {
			log.Fatal("internal metrics listener unavailable")
		}
	}()
	defer metricServer.Close()
	server := &http.Server{Addr: c.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	log.Printf("platform version=%s mode=api", Version)
	if c.TLSCert != "" && c.TLSKey != "" {
		e = server.ListenAndServeTLS(c.TLSCert, c.TLSKey)
	} else {
		e = server.ListenAndServe()
	}
	if e != nil && e != http.ErrServerClosed {
		log.Fatal("HTTP server failed")
	}
}
