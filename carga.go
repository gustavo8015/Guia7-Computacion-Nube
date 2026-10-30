package main

// Prueba de carga sencilla: N solicitudes con C clientes concurrentes.
// Uso: ROL=carga tienda -url http://localhost:8080/api/productos -n 600 -c 30

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

func percentil(v []time.Duration, p float64) time.Duration {
	if len(v) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(float64(len(s)-1) * p)
	return s[i]
}

func ejecutarCarga(args []string) int {
	fs := flag.NewFlagSet("carga", flag.ContinueOnError)
	destino := fs.String("url", "http://localhost:8080/api/productos", "URL a probar")
	n := fs.Int("n", 600, "número de solicitudes")
	c := fs.Int("c", 30, "clientes concurrentes")
	fallar := fs.String("fallar", "", "URL /simular-falla de una réplica a terminar 1 s después de iniciar")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cli := &http.Client{Timeout: 10 * time.Second}
	var mu sync.Mutex
	var lat []time.Duration
	estados := map[int]int{}
	replicas := map[string]int{}
	trabajos := make(chan struct{}, *n)
	for i := 0; i < *n; i++ {
		trabajos <- struct{}{}
	}
	close(trabajos)
	inicio := time.Now()
	if *fallar != "" {
		go func() {
			time.Sleep(time.Second)
			if r, err := cli.Post(*fallar, "application/json", nil); err == nil {
				r.Body.Close()
				fmt.Println(">>> réplica terminada abruptamente (falla simulada)")
			}
		}()
	}
	var wg sync.WaitGroup
	for i := 0; i < *c; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range trabajos {
				t := time.Now()
				resp, err := cli.Get(*destino)
				d := time.Since(t)
				mu.Lock()
				if err != nil {
					estados[0]++
				} else {
					estados[resp.StatusCode]++
					var cuerpo struct {
						Replica string `json:"replica"`
					}
					_ = json.NewDecoder(resp.Body).Decode(&cuerpo)
					resp.Body.Close()
					if cuerpo.Replica != "" {
						replicas[cuerpo.Replica]++
					}
					lat = append(lat, d)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	total := time.Since(inicio)
	fmt.Printf("Solicitudes: %d  Concurrencia: %d  Tiempo total: %.2f s\n", *n, *c, total.Seconds())
	fmt.Printf("Rendimiento: %.1f solicitudes/s\n", float64(*n)/total.Seconds())
	fmt.Printf("Latencia p50: %d ms  p95: %d ms  max: %d ms\n",
		percentil(lat, 0.50).Milliseconds(), percentil(lat, 0.95).Milliseconds(), percentil(lat, 1).Milliseconds())
	fmt.Printf("Códigos HTTP: %v\n", estados)
	nombres := make([]string, 0, len(replicas))
	for k := range replicas {
		nombres = append(nombres, k)
	}
	sort.Strings(nombres)
	for _, k := range nombres {
		fmt.Printf("  réplica %s atendió %d\n", k, replicas[k])
	}
	if estados[0] > 0 {
		fmt.Fprintf(os.Stderr, "%d solicitudes fallaron sin respuesta\n", estados[0])
	}
	return 0
}
