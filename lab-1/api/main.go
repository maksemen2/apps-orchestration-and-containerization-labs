package main

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const megabyte = 1024 * 1024

type api struct {
	mu       sync.Mutex
	memory   [][]byte
	burnOnce sync.Once
}

func (a *api) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("GET /eat", a.eat)
	mux.HandleFunc("GET /burn", a.burn)
	return mux
}

func (a *api) eat(w http.ResponseWriter, r *http.Request) {
	mb, err := strconv.Atoi(r.URL.Query().Get("mb"))
	if err != nil || mb < 0 || mb > int(^uint(0)>>1)/megabyte {
		http.Error(w, "mb must be a non-negative integer that fits in memory address space", http.StatusBadRequest)
		return
	}

	if mb > 0 {
		block := make([]byte, mb*megabyte)
		// Touch the allocation so the OS commits physical memory.
		for i := range block {
			block[i] = 1
		}
		a.mu.Lock()
		a.memory = append(a.memory, block)
		a.mu.Unlock()
	}
	fmt.Fprint(w, "ok")
}

func (a *api) burn(w http.ResponseWriter, r *http.Request) {
	a.burnOnce.Do(func() {
		// One runnable goroutine consumes approximately one logical CPU.
		go func() {
			for {
			}
		}()
	})
	fmt.Fprint(w, "ok")
}

func main() {
	a := &api{}
	server := &http.Server{
		Addr:              ":8080",
		Handler:           a.handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("api listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
}
