package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReservarNoVendeMasDeLoQueHay(t *testing.T) {
	alm, err := NuevoAlmacen(filepath.Join(t.TempDir(), "estado.json"))
	if err != nil {
		t.Fatal(err)
	}
	// P-004 inicia con 10 unidades; 50 compras simultáneas de 1 unidad
	var wg sync.WaitGroup
	var mu sync.Mutex
	exitos, sinStock := 0, 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := alm.Reservar("ana", "P-004", 1)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				exitos++
			case errors.Is(err, errSinStock):
				sinStock++
			default:
				t.Errorf("error inesperado: %v", err)
			}
		}()
	}
	wg.Wait()
	if exitos != 10 || sinStock != 40 {
		t.Fatalf("esperaba 10 éxitos y 40 rechazos, obtuve %d y %d", exitos, sinStock)
	}
	for _, p := range alm.Productos() {
		if p.SKU == "P-004" && p.Stock != 0 {
			t.Fatalf("stock final esperado 0, obtuve %d", p.Stock)
		}
	}
}

func TestReservarRechazaCantidadInvalidaYProductoInexistente(t *testing.T) {
	alm, _ := NuevoAlmacen("")
	alm.e = estadoDatos{Productos: catalogoInicial()}
	if _, err := alm.Reservar("ana", "P-001", 0); !errors.Is(err, errCantidad) {
		t.Fatalf("esperaba errCantidad, obtuve %v", err)
	}
	if _, err := alm.Reservar("ana", "NO-EXISTE", 1); !errors.Is(err, errProductoNoExiste) {
		t.Fatalf("esperaba errProductoNoExiste, obtuve %v", err)
	}
}

func TestLosDatosSobrevivenAlReinicio(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "estado.json")
	a1, _ := NuevoAlmacen(ruta)
	ped, err := a1.Reservar("luis", "P-002", 3)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := NuevoAlmacen(ruta) // simula reinicio del contenedor con el mismo volumen
	if err != nil {
		t.Fatal(err)
	}
	pedidos := a2.PedidosDe("luis")
	if len(pedidos) != 1 || pedidos[0].ID != ped.ID {
		t.Fatalf("el pedido no persistió: %+v", pedidos)
	}
	for _, p := range a2.Productos() {
		if p.SKU == "P-002" && p.Stock != 22 {
			t.Fatalf("stock esperado 22, obtuve %d", p.Stock)
		}
	}
}

func TestToken(t *testing.T) {
	sec := []byte("clave-de-prueba-de-32-caracteres!!")
	ahora := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	tok := emitirToken(sec, "ana", "cliente", 15*time.Minute, ahora)

	u, rol, err := validarToken(sec, tok, ahora.Add(time.Minute))
	if err != nil || u != "ana" || rol != "cliente" {
		t.Fatalf("token válido rechazado: %v %s %s", err, u, rol)
	}
	if _, _, err := validarToken(sec, tok, ahora.Add(16*time.Minute)); !errors.Is(err, errTokenVencido) {
		t.Fatalf("esperaba token vencido, obtuve %v", err)
	}
	alterado := tok[:len(tok)-2] + "xx"
	if _, _, err := validarToken(sec, alterado, ahora); !errors.Is(err, errTokenInvalido) {
		t.Fatalf("esperaba token inválido, obtuve %v", err)
	}
	if _, _, err := validarToken([]byte("otra-clave-distinta-de-32-caracteres"), tok, ahora); !errors.Is(err, errTokenInvalido) {
		t.Fatalf("una firma con otra clave no debe validar: %v", err)
	}
}

func TestGatewayOmiteReplicaCaidaYRepartePorTurnos(t *testing.T) {
	g := NuevoGrupo("catalogo", "8080")
	g.resolver = func(string) ([]string, error) { return []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}, nil }
	g.probar = func(d string) bool { return d != "10.0.0.2:8080" } // la réplica 2 está caída
	g.Actualizar()

	conteo := map[string]int{}
	for i := 0; i < 10; i++ {
		d, err := g.Siguiente(nil)
		if err != nil {
			t.Fatal(err)
		}
		conteo[d]++
	}
	if conteo["10.0.0.2:8080"] != 0 {
		t.Fatal("se envió tráfico a una réplica caída")
	}
	if conteo["10.0.0.1:8080"] != 5 || conteo["10.0.0.3:8080"] != 5 {
		t.Fatalf("reparto desigual: %v", conteo)
	}

	g.Retirar("10.0.0.1:8080")
	g.Retirar("10.0.0.3:8080")
	if _, err := g.Siguiente(nil); err == nil {
		t.Fatal("sin réplicas sanas debe devolver error")
	}
}

func TestGatewaySirveLaInterfazWeb(t *testing.T) {
	h := servirGateway([]byte("clave-de-prueba-de-32-caracteres!!"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("esperaba HTML 200, obtuve %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "TiendaNube") {
		t.Fatal("la página no contiene el nombre de la tienda")
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/no-existe", nil))
	if rec.Code != 404 {
		t.Fatalf("una ruta desconocida debe dar 404, obtuve %d", rec.Code)
	}
}

func TestReservarCobraElPrecioConDescuento(t *testing.T) {
	alm, _ := NuevoAlmacen(filepath.Join(t.TempDir(), "estado.json"))
	ped, err := alm.Reservar("ana", "P-001", 2) // P-001 tiene 20 % de descuento
	if err != nil {
		t.Fatal(err)
	}
	if ped.Total != 303840 {
		t.Fatalf("total esperado 303840 (2 × 151920), obtuve %.0f", ped.Total)
	}
	ped2, _ := alm.Reservar("ana", "P-002", 1) // sin descuento
	if ped2.Total != 259900 {
		t.Fatalf("total esperado 259900, obtuve %.0f", ped2.Total)
	}
	for _, p := range alm.Productos() {
		if p.SKU == "P-001" && p.PrecioFinal != 151920 {
			t.Fatalf("precio final de P-001 esperado 151920, obtuve %.0f", p.PrecioFinal)
		}
	}
}
