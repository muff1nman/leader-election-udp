package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/oklog/run"
)

var ready atomic.Bool

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readyFile := os.Getenv("READY_FILE")
	if readyFile == "" {
		readyFile = "/var/run/udplisten/ready"
	}
	file, err := os.OpenFile(readyFile, os.O_RDONLY|os.O_CREATE, 0664)
	if err != nil {
		slog.Error("readyFile is not readable or could not be created", "err", err, "file", readyFile)
		os.Exit(1)
	}
	if err := file.Close(); err != nil {
		slog.Error("failed to close readyFile", "err", err)
		os.Exit(1)
	}
	udpPort := os.Getenv("UDP_LISTEN_PORT")
	if udpPort == "" {
		udpPort = "1100"
	}
	healthPort := os.Getenv("HEALTH_PORT")
	if healthPort == "" {
		healthPort = "8080"
	}
	pc, err := net.ListenPacket("udp", fmt.Sprintf(":%s", udpPort))
	if err != nil {
		slog.Error("failed to listen", "err", err)
		os.Exit(1)
	}
	defer pc.Close()
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Error("failed to create file watcher", "err", err)
		os.Exit(1)
	}
	defer fw.Close()
	if err := fw.Add(readyFile); err != nil {
		slog.Error("failed to watch", "err", err, "file", readyFile)
		os.Exit(1)
	}
	if os.Getenv("START_READY") == "true" {
		ready.Store(true)
	}
	slog.Info("Initial state", "ready", ready.Load())

	var g run.Group

	mux := http.NewServeMux()
	mux.Handle("GET /readyz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ready.Load() {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusLocked)
			w.Write([]byte("not ready\n"))
		}
	}))
	mux.Handle("PUT /off", http.HandlerFunc(toggleFunc(false)))
	mux.Handle("PUT /on", http.HandlerFunc(toggleFunc(true)))

	s := &http.Server{
		Addr:         fmt.Sprintf(":%s", healthPort),
		Handler:      mux,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
	}

	g.Add(run.SignalHandler(ctx, syscall.SIGINT, syscall.SIGTERM))
	g.Add(s.ListenAndServe, func(err error) {
		if err := s.Close(); err != nil {
			slog.Error("failed to shutdown http listener", "err", err)
		}
	})
	g.Add(func() error {
		for {
			select {
			case event, ok := <-fw.Events:
				if !ok {
					slog.Info("watcher closed")
					return nil
				}
				if event.Has(fsnotify.Write) {
					contents, err := os.ReadFile(readyFile)
					if err != nil {
						return fmt.Errorf("could not read ready file: %w", err)
					}
					if bytes.HasPrefix(contents, []byte("yes")) {
						slog.Info("toggled ready on from ready file")
						ready.Store(true)
					} else if bytes.HasPrefix(contents, []byte("no")) {
						slog.Info("toggled ready off from ready file")
						ready.Store(false)
					}
				}
			case err, ok := <-fw.Errors:
				if !ok {
					return nil
				}
				slog.Warn("watcher closed with errors", "err:", err)
			}
		}
	}, func(err error) {
		fw.Close()
	})
	g.Add(func() error {
		for {
			buf := make([]byte, 2048)
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return nil
				}
				slog.Error("failed to read", "err", err)
				continue
			}
			slog.Info("read message", "addr", addr, "buf", string(buf[:n]))
			go func() {
				var b bytes.Buffer
				_, err := fmt.Fprintf(&b, "ack from %s\n", os.Getenv("RECEIVER_IDENTIFIER"))
				if err != nil {
					slog.Info("failed to create message", "err", err)
					return
				}
				_, err = pc.WriteTo(b.Bytes(), addr)
				if err != nil {
					slog.Info("failed to respond to udp", "err", err)
				}
			}()
		}
	}, func(err error) {
		pc.Close()
	})
	slog.Info("Starting")
	if err := g.Run(); err != nil && !errors.As(err, &run.SignalError{}) {
		slog.Error("exit on err", "err", "err")
		os.Exit(1)
	}
}

func toggleFunc(on bool) func(w http.ResponseWriter, r *http.Request) {
	desired := "off"
	if on {
		desired = "on"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		swapped := ready.CompareAndSwap(!on, on)
		if swapped {
			_, err := fmt.Fprintf(w, "turned %s\n", desired)
			if err != nil {
				slog.Error("failed to write", "err", err)
			}
		} else {
			w.WriteHeader(http.StatusNotModified)
		}
	}
}
