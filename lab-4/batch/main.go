package main

import (
	"context"
	"crypto/sha256"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"
)

func setting(name string, fallback, minimum int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < minimum {
		log.Fatalf("%s must be an integer >= %d", name, minimum)
	}
	return n
}

func main() {
	workers := setting("CPU_WORKERS", 1, 0)
	memoryMiB := setting("MEMORY_MIB", 128, 0)
	stepMiB := setting("MEMORY_STEP_MIB", 16, 1)
	port := setting("PORT", 8080, 1)

	listener, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	for i := 0; i < workers; i++ {
		go func() {
			var digest [32]byte
			for ctx.Err() == nil {
				for j := 0; j < 16384; j++ {
					digest = sha256.Sum256(digest[:])
				}
			}
			runtime.KeepAlive(digest)
		}()
	}

	log.Printf("batch started cpu_workers=%d target_mib=%d step_mib=%d port=%d", workers, memoryMiB, stepMiB, port)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var chunks [][]byte
	allocatedMiB := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if allocatedMiB < memoryMiB {
				sizeMiB := min(stepMiB, memoryMiB-allocatedMiB)
				chunk := make([]byte, sizeMiB*1024*1024)
				// Touch every page so the allocation consumes physical memory.
				for offset := 0; offset < len(chunk); offset += os.Getpagesize() {
					chunk[offset] = 1
				}
				chunks = append(chunks, chunk)
				allocatedMiB += sizeMiB
			}
			log.Printf("batch allocated_mib=%d target_mib=%d cpu_workers=%d", allocatedMiB, memoryMiB, workers)
			runtime.KeepAlive(chunks)
		}
	}
}
