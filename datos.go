package main

// Servicio "datos": simula la base de datos administrada (Cloud SQL para PostgreSQL).
// Guarda productos, inventario y pedidos en un archivo JSON dentro de un volumen persistente.

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Producto struct {
	SKU         string  `json:"sku"`
	Nombre      string  `json:"nombre"`
	Precio      float64 `json:"precio"`
	Stock       int     `json:"stock"`
	Descuento   int     `json:"descuento"`              // porcentaje de temporada
	PrecioFinal float64 `json:"precio_final,omitempty"` // calculado, no se guarda
}

// precioFinal aplica el descuento redondeando al peso.
func (p Producto) precioFinal() float64 {
	return math.Round(p.Precio * float64(100-p.Descuento) / 100)
}

type Pedido struct {
	ID       int       `json:"id"`
	Usuario  string    `json:"usuario"`
	SKU      string    `json:"sku"`
	Cantidad int       `json:"cantidad"`
	Total    float64   `json:"total"`
	Estado   string    `json:"estado"`
	Fecha    time.Time `json:"fecha"`
}

type estadoDatos struct {
	Productos []Producto `json:"productos"`
	Pedidos   []Pedido   `json:"pedidos"`
}

type Almacen struct {
	mu   sync.Mutex
	ruta string
	e    estadoDatos
}

var errSinStock = errors.New("stock insuficiente")
var errProductoNoExiste = errors.New("producto no existe")
var errCantidad = errors.New("cantidad inválida")

func catalogoInicial() []Producto {
	return []Producto{
		{SKU: "P-001", Nombre: "Audífonos inalámbricos", Precio: 189900, Stock: 40, Descuento: 20},
		{SKU: "P-002", Nombre: "Teclado mecánico", Precio: 259900, Stock: 25},
		{SKU: "P-003", Nombre: "Mouse ergonómico", Precio: 99900, Stock: 60, Descuento: 15},
		{SKU: "P-004", Nombre: "Monitor 24 pulgadas", Precio: 649900, Stock: 10, Descuento: 10},
	}
}

func NuevoAlmacen(ruta string) (*Almacen, error) {
	a := &Almacen{ruta: ruta}
	b, err := os.ReadFile(ruta)
	if errors.Is(err, os.ErrNotExist) {
		a.e = estadoDatos{Productos: catalogoInicial()}
		return a, a.guardar()
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &a.e); err != nil {
		return nil, err
	}
	// archivos de una versión anterior no traen descuentos: se completan con los de la temporada
	inicial := map[string]int{}
	for _, p := range catalogoInicial() {
		inicial[p.SKU] = p.Descuento
	}
	for i := range a.e.Productos {
		if a.e.Productos[i].Descuento == 0 {
			a.e.Productos[i].Descuento = inicial[a.e.Productos[i].SKU]
		}
		a.e.Productos[i].PrecioFinal = 0
	}
	return a, nil
}

// escritura atómica: archivo temporal + rename
func (a *Almacen) guardar() error {
	if a.ruta == "" {
		return nil
	}
	b, err := json.MarshalIndent(a.e, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.ruta + ".tmp"
	if err := os.MkdirAll(filepath.Dir(a.ruta), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.ruta)
}

func (a *Almacen) Productos() []Producto {
	a.mu.Lock()
	defer a.mu.Unlock()
	r := append([]Producto(nil), a.e.Productos...)
	for i := range r {
		r[i].PrecioFinal = r[i].precioFinal()
	}
	return r
}

// Reservar descuenta stock y registra el pedido en una sola sección crítica,
// de modo que dos compras simultáneas no pueden vender la misma unidad.
func (a *Almacen) Reservar(usuario, sku string, cantidad int) (Pedido, error) {
	if cantidad <= 0 {
		return Pedido{}, errCantidad
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.e.Productos {
		p := &a.e.Productos[i]
		if p.SKU != sku {
			continue
		}
		if p.Stock < cantidad {
			return Pedido{}, errSinStock
		}
		p.Stock -= cantidad
		ped := Pedido{
			ID: len(a.e.Pedidos) + 1001, Usuario: usuario, SKU: sku, Cantidad: cantidad,
			Total: p.precioFinal() * float64(cantidad), Estado: "CONFIRMADO", Fecha: time.Now().UTC(),
		}
		a.e.Pedidos = append(a.e.Pedidos, ped)
		if err := a.guardar(); err != nil {
			p.Stock += cantidad
			a.e.Pedidos = a.e.Pedidos[:len(a.e.Pedidos)-1]
			return Pedido{}, err
		}
		return ped, nil
	}
	return Pedido{}, errProductoNoExiste
}

func (a *Almacen) PedidosDe(usuario string) []Pedido {
	a.mu.Lock()
	defer a.mu.Unlock()
	var r []Pedido
	for _, p := range a.e.Pedidos {
		if usuario == "*" || p.Usuario == usuario {
			r = append(r, p)
		}
	}
	return r
}

func servirDatos() http.Handler {
	alm, err := NuevoAlmacen(entorno("RUTA_DATOS", "/datos/estado.json"))
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /salud", func(w http.ResponseWriter, r *http.Request) {
		responderJSON(w, 200, map[string]string{"estado": "ok", "replica": nombreReplica()})
	})
	mux.HandleFunc("GET /productos", func(w http.ResponseWriter, r *http.Request) {
		responderJSON(w, 200, alm.Productos())
	})
	mux.HandleFunc("POST /reservar", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Usuario  string `json:"usuario"`
			SKU      string `json:"sku"`
			Cantidad int    `json:"cantidad"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
			responderError(w, 400, "JSON inválido")
			return
		}
		ped, err := alm.Reservar(in.Usuario, in.SKU, in.Cantidad)
		switch {
		case errors.Is(err, errSinStock):
			responderError(w, 409, err.Error())
		case errors.Is(err, errProductoNoExiste):
			responderError(w, 404, err.Error())
		case errors.Is(err, errCantidad):
			responderError(w, 400, err.Error())
		case err != nil:
			responderError(w, 500, "error de persistencia")
		default:
			responderJSON(w, 201, ped)
		}
	})
	mux.HandleFunc("GET /pedidos", func(w http.ResponseWriter, r *http.Request) {
		responderJSON(w, 200, alm.PedidosDe(r.URL.Query().Get("usuario")))
	})
	return mux
}
