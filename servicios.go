package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

var clienteInterno = &http.Client{Timeout: 3 * time.Second}

func saludHandler(w http.ResponseWriter, r *http.Request) {
	responderJSON(w, 200, map[string]string{"estado": "ok", "replica": nombreReplica()})
}

// ---------- servicio de productos (catálogo) ----------
// Cada réplica atiende como máximo CAPACIDAD solicitudes a la vez y cada una
// tarda TRABAJO_MS; así se simula el límite de CPU de una réplica de 0,5 vCPU.

func servirCatalogo() http.Handler {
	urlDatos := entorno("URL_DATOS", "http://datos:8080")
	capacidad, _ := strconv.Atoi(entorno("CAPACIDAD", "4"))
	trabajo, _ := strconv.Atoi(entorno("TRABAJO_MS", "50"))
	cupos := make(chan struct{}, capacidad)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /salud", saludHandler)
	if entorno("PERMITIR_FALLA", "") == "1" {
		// solo para el laboratorio: termina el proceso para simular la caída de la réplica
		mux.HandleFunc("POST /simular-falla", func(w http.ResponseWriter, r *http.Request) {
			registrar(map[string]any{"evento": "falla_simulada", "replica": nombreReplica()})
			go func() { time.Sleep(50 * time.Millisecond); os.Exit(1) }()
			responderJSON(w, 202, map[string]string{"estado": "terminando"})
		})
	}
	mux.HandleFunc("GET /productos", func(w http.ResponseWriter, r *http.Request) {
		cupos <- struct{}{}
		defer func() { <-cupos }()
		time.Sleep(time.Duration(trabajo) * time.Millisecond)
		resp, err := clienteInterno.Get(urlDatos + "/productos")
		if err != nil {
			responderError(w, 503, "servicio de datos no disponible")
			return
		}
		defer resp.Body.Close()
		var prods []Producto
		if err := json.NewDecoder(resp.Body).Decode(&prods); err != nil {
			responderError(w, 502, "respuesta inválida de datos")
			return
		}
		responderJSON(w, 200, map[string]any{"replica": nombreReplica(), "productos": prods})
	})
	return mux
}

// ---------- servicio de pedidos ----------
// Recibe el usuario autenticado en la cabecera X-Usuario, que solo pone el gateway.

func servirPedidos() http.Handler {
	urlDatos := entorno("URL_DATOS", "http://datos:8080")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /salud", saludHandler)
	mux.HandleFunc("POST /pedidos", func(w http.ResponseWriter, r *http.Request) {
		usuario := r.Header.Get("X-Usuario")
		if usuario == "" {
			responderError(w, 401, "sin usuario autenticado")
			return
		}
		var in struct {
			SKU      string `json:"sku"`
			Cantidad int    `json:"cantidad"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
			responderError(w, 400, "JSON inválido")
			return
		}
		cuerpo, _ := json.Marshal(map[string]any{"usuario": usuario, "sku": in.SKU, "cantidad": in.Cantidad})
		resp, err := clienteInterno.Post(urlDatos+"/reservar", "application/json", bytes.NewReader(cuerpo))
		if err != nil {
			responderError(w, 503, "servicio de datos no disponible")
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Replica", nombreReplica())
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})
	mux.HandleFunc("GET /pedidos", func(w http.ResponseWriter, r *http.Request) {
		usuario := r.Header.Get("X-Usuario")
		if usuario == "" {
			responderError(w, 401, "sin usuario autenticado")
			return
		}
		if r.Header.Get("X-Rol") == "administrador" {
			usuario = "*"
		}
		resp, err := clienteInterno.Get(urlDatos + "/pedidos?usuario=" + url.QueryEscape(usuario))
		if err != nil {
			responderError(w, 503, "servicio de datos no disponible")
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})
	return mux
}

// ---------- servicio de usuarios ----------

type cuenta struct {
	sal, hash, rol string
}

func cuentasDemo() map[string]cuenta {
	m := map[string]cuenta{}
	for _, c := range []struct{ u, clave, rol string }{
		{"ana", "Ana#2026", "cliente"},
		{"luis", "Luis#2026", "cliente"},
		{"admin", "Adm#2026", "administrador"},
	} {
		sal := "sal-" + c.u
		m[c.u] = cuenta{sal: sal, hash: hashClave(c.clave, sal), rol: c.rol}
	}
	return m
}

func servirUsuarios(secreto []byte) http.Handler {
	cuentas := cuentasDemo()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /salud", saludHandler)
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Usuario string `json:"usuario"`
			Clave   string `json:"clave"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&in); err != nil {
			responderError(w, 400, "JSON inválido")
			return
		}
		c, ok := cuentas[in.Usuario]
		if !ok || hashClave(in.Clave, c.sal) != c.hash {
			registrar(map[string]any{"evento": "login_fallido", "usuario": in.Usuario})
			responderError(w, 401, "credenciales inválidas")
			return
		}
		tok := emitirToken(secreto, in.Usuario, c.rol, 15*time.Minute, time.Now())
		responderJSON(w, 200, map[string]any{"token": tok, "rol": c.rol, "vence_en_s": 900})
	})
	return mux
}
