package main

// API Gateway con balanceo round-robin, verificación de salud y reintento.
// Descubre las réplicas consultando el DNS interno de Docker (en Google Cloud
// esta función la cumplen el balanceador externo y el propio Cloud Run).

import (
	"bytes"
	_ "embed"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed web/index.html
var paginaWeb []byte

type Grupo struct {
	nombre   string // nombre DNS del servicio, p. ej. "catalogo"
	puerto   string
	mu       sync.RWMutex
	sanas    []string
	contador atomic.Uint64
	resolver func(string) ([]string, error)
	probar   func(string) bool
}

func NuevoGrupo(nombre, puerto string) *Grupo {
	return &Grupo{
		nombre: nombre, puerto: puerto, resolver: net.LookupHost,
		probar: func(dir string) bool {
			c := http.Client{Timeout: 800 * time.Millisecond}
			r, err := c.Get("http://" + dir + "/salud")
			if err != nil {
				return false
			}
			r.Body.Close()
			return r.StatusCode == 200
		},
	}
}

// Actualizar resuelve las réplicas y deja solo las que responden /salud.
func (g *Grupo) Actualizar() {
	ips, err := g.resolver(g.nombre)
	var sanas []string
	if err == nil {
		for _, ip := range ips {
			dir := net.JoinHostPort(ip, g.puerto)
			if g.probar(dir) {
				sanas = append(sanas, dir)
			}
		}
	}
	g.mu.Lock()
	cambio := strings.Join(g.sanas, ",") != strings.Join(sanas, ",")
	g.sanas = sanas
	g.mu.Unlock()
	if cambio {
		registrar(map[string]any{"evento": "replicas_actualizadas", "servicio": g.nombre, "sanas": sanas})
	}
}

func (g *Grupo) Siguiente(excluir map[string]bool) (string, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	n := len(g.sanas)
	for i := 0; i < n; i++ {
		d := g.sanas[int(g.contador.Add(1)-1)%n]
		if !excluir[d] {
			return d, nil
		}
	}
	return "", errors.New("sin réplicas sanas")
}

func (g *Grupo) Retirar(dir string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, d := range g.sanas {
		if d == dir {
			g.sanas = append(g.sanas[:i], g.sanas[i+1:]...)
			registrar(map[string]any{"evento": "replica_retirada", "servicio": g.nombre, "replica": dir})
			return
		}
	}
}

func (g *Grupo) Sanas() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return append([]string(nil), g.sanas...)
}

type metricas struct {
	mu         sync.Mutex
	PorReplica map[string]int `json:"solicitudes_por_replica"`
	PorEstado  map[int]int    `json:"solicitudes_por_estado"`
}

func (m *metricas) sumar(replica string, estado int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if replica != "" {
		m.PorReplica[replica]++
	}
	m.PorEstado[estado]++
}

type grabador struct {
	http.ResponseWriter
	estado int
}

func (g *grabador) WriteHeader(c int) { g.estado = c; g.ResponseWriter.WriteHeader(c) }

func servirGateway(secreto []byte) http.Handler {
	grupos := map[string]*Grupo{
		"usuarios": NuevoGrupo(entorno("SVC_USUARIOS", "usuarios"), "8080"),
		"catalogo": NuevoGrupo(entorno("SVC_CATALOGO", "catalogo"), "8080"),
		"pedidos":  NuevoGrupo(entorno("SVC_PEDIDOS", "pedidos"), "8080"),
	}
	for _, g := range grupos {
		g.Actualizar()
		go func(g *Grupo) {
			for range time.Tick(2 * time.Second) {
				g.Actualizar()
			}
		}(g)
	}
	met := &metricas{PorReplica: map[string]int{}, PorEstado: map[int]int{}}

	reenviar := func(g *Grupo, ruta string, w http.ResponseWriter, r *http.Request) {
		inicio := time.Now()
		gw := &grabador{ResponseWriter: w, estado: 200}
		excluir := map[string]bool{}
		var destino string
		// se guarda el cuerpo para poder reenviarlo si hay que reintentar en otra réplica
		cuerpo, _ := io.ReadAll(io.LimitReader(r.Body, 16<<10))
		// hasta 3 intentos: si una réplica falla antes de responder, se prueba otra
		for intento := 0; intento < 3; intento++ {
			d, err := g.Siguiente(excluir)
			if err != nil && intento == 0 {
				// sin réplicas conocidas (p. ej. recién arrancado): se vuelve a consultar el DNS antes de fallar
				g.Actualizar()
				d, err = g.Siguiente(excluir)
			}
			if err != nil {
				responderError(gw, 503, "servicio temporalmente no disponible")
				break
			}
			destino = d
			fallo := false
			proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: d})
			proxy.Director = func(req *http.Request) {
				req.URL.Scheme, req.URL.Host, req.URL.Path = "http", d, ruta
			}
			proxy.ModifyResponse = func(resp *http.Response) error {
				resp.Header.Set("X-Atendido-Por", resp.Header.Get("X-Replica"))
				return nil
			}
			proxy.ErrorHandler = func(_ http.ResponseWriter, _ *http.Request, _ error) { fallo = true }
			r.Body = io.NopCloser(bytes.NewReader(cuerpo))
			r.ContentLength = int64(len(cuerpo))
			proxy.ServeHTTP(gw, r)
			if !fallo {
				break
			}
			// solo se reintenta cuando no se alcanzó a responder (conexión rechazada o caída)
			g.Retirar(d)
			excluir[d] = true
		}
		met.sumar(destino, gw.estado)
		registrar(map[string]any{"metodo": r.Method, "ruta": r.URL.Path, "estado": gw.estado,
			"replica": destino, "ms": time.Since(inicio).Milliseconds()})
	}

	autenticar := func(next func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			u, rol, err := validarToken(secreto, tok, time.Now())
			if err != nil {
				registrar(map[string]any{"evento": "acceso_denegado", "ruta": r.URL.Path, "motivo": err.Error()})
				met.sumar("", 401)
				responderError(w, 401, "autenticación requerida")
				return
			}
			r.Header.Set("X-Usuario", u)
			r.Header.Set("X-Rol", rol)
			next(w, r)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del("X-Usuario")
		reenviar(grupos["usuarios"], "/login", w, r)
	})
	mux.HandleFunc("GET /api/productos", func(w http.ResponseWriter, r *http.Request) {
		reenviar(grupos["catalogo"], "/productos", w, r)
	})
	mux.HandleFunc("POST /api/pedidos", autenticar(func(w http.ResponseWriter, r *http.Request) {
		reenviar(grupos["pedidos"], "/pedidos", w, r)
	}))
	mux.HandleFunc("GET /api/pedidos", autenticar(func(w http.ResponseWriter, r *http.Request) {
		reenviar(grupos["pedidos"], "/pedidos", w, r)
	}))
	mux.HandleFunc("GET /salud", saludHandler)
	// interfaz web (en Google Cloud se serviría desde un bucket de Cloud Storage)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'")
		_, _ = w.Write(paginaWeb)
	})
	mux.HandleFunc("GET /estado", func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{}
		for k, g := range grupos {
			out[k] = g.Sanas()
		}
		responderJSON(w, 200, out)
	})
	mux.HandleFunc("GET /metricas", func(w http.ResponseWriter, r *http.Request) {
		met.mu.Lock()
		defer met.mu.Unlock()
		responderJSON(w, 200, met)
	})
	// las cabeceras internas nunca se aceptan desde fuera
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del("X-Usuario")
		r.Header.Del("X-Rol")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		mux.ServeHTTP(w, r)
	})
}
