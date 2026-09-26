//go:build pprofdiag

// Build de DIAGNÓSTICO de memoria, nunca la de producción: sólo entra con
// `-tags pprofdiag` (el Dockerfile no la pone). Abre pprof y un volcado de
// runtime.MemStats en 127.0.0.1:$PPROF_PORT (6061 por defecto), en loopback,
// para medir el reposo sin tocar el binario que se despliega.
//
//	go build -tags "noswagger pprofdiag" ./cmd/evolution-go
//	curl -s localhost:6061/debug/memstats
//	go tool pprof -sample_index=inuse_space http://localhost:6061/debug/pprof/heap
package main

import (
	"encoding/json"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"
	"runtime/debug"
)

func init() {
	port := os.Getenv("PPROF_PORT")
	if port == "" {
		port = "6061"
	}
	http.HandleFunc("/debug/memstats", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("gc") == "1" {
			runtime.GC()
		}
		if r.URL.Query().Get("free") == "1" {
			debug.FreeOSMemory()
		}
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Goroutines":   runtime.NumGoroutine(),
			"HeapAlloc":    m.HeapAlloc,
			"HeapInuse":    m.HeapInuse,
			"HeapIdle":     m.HeapIdle,
			"HeapReleased": m.HeapReleased,
			"HeapSys":      m.HeapSys,
			"StackInuse":   m.StackInuse,
			"StackSys":     m.StackSys,
			"MSpanSys":     m.MSpanSys,
			"MCacheSys":    m.MCacheSys,
			"BuckHashSys":  m.BuckHashSys,
			"GCSys":        m.GCSys,
			"OtherSys":     m.OtherSys,
			"Sys":          m.Sys,
			"NumGC":        m.NumGC,
			"Mallocs":      m.Mallocs,
			"Frees":        m.Frees,
		})
	})
	go func() { _ = http.ListenAndServe("127.0.0.1:"+port, nil) }()
}
