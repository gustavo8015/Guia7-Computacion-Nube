package main

// Tienda en la nube - demostración de la Guía 7 (Computación en la nube).
// Un solo binario; la variable ROL decide qué microservicio ejecuta el contenedor:
// gateway | usuarios | catalogo | pedidos | datos | carga

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	log.SetFlags(0)
	rol := entorno("ROL", "gateway")
	secreto := []byte(os.Getenv("SECRETO_TOKEN"))
	if (rol == "gateway" || rol == "usuarios") && len(secreto) < 32 {
		log.Fatal("SECRETO_TOKEN debe tener al menos 32 caracteres (en Google Cloud se lee desde Secret Manager)")
	}

	var h http.Handler
	switch rol {
	case "gateway":
		h = servirGateway(secreto)
	case "usuarios":
		h = servirUsuarios(secreto)
	case "catalogo":
		h = servirCatalogo()
	case "pedidos":
		h = servirPedidos()
	case "datos":
		h = servirDatos()
	case "carga":
		os.Exit(ejecutarCarga(os.Args[1:]))
	default:
		log.Fatalf("ROL desconocido: %s", rol)
	}

	srv := &http.Server{
		Addr: ":" + entorno("PUERTO", "8080"), Handler: h,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second,
	}
	registrar(map[string]any{"evento": "inicio", "rol": rol, "replica": nombreReplica(), "puerto": srv.Addr})
	fmt.Fprintln(os.Stderr)
	log.Fatal(srv.ListenAndServe())
}
