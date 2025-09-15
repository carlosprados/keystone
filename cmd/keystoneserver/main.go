package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// keystoneserver: servidor HTTP estático para pruebas locales de artefactos.
// Uso:
//
//	go run ./cmd/keystoneserver --root /ruta/a/servir --addr :9000
//
// Ejemplos de URLs resultantes:
//
//	http://127.0.0.1:9000/telegraf-config-1.0.0.zip
//	http://127.0.0.1:9000/subdir/archivo.bin
func main() {
	var (
		root = flag.String("root", ".", "Directorio raíz a servir (estático)")
		addr = flag.String("addr", ":9000", "Dirección de escucha (host:puerto)")
	)
	flag.Parse()

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		log.Fatalf("error resolviendo ruta: %v", err)
	}
	st, err := os.Stat(absRoot)
	if err != nil {
		log.Fatalf("no puedo acceder a %s: %v", absRoot, err)
	}
	if !st.IsDir() {
		log.Fatalf("%s no es un directorio", absRoot)
	}

	mux := http.NewServeMux()

	// Health/check rápido
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"ok","time_utc":"%s"}`, time.Now().UTC().Format(time.RFC3339))))
	})

	// Servir ficheros estáticos desde root
	fs := http.FileServer(http.Dir(absRoot))
	mux.Handle("/", fs)

	srv := &http.Server{Addr: *addr, Handler: mux}
	log.Printf("keystoneserver: sirviendo %s en http://%s", absRoot, *addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("ListenAndServe: %v", err)
	}
}
