package main

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// variable de entorno con valor por defecto
func entorno(clave, defecto string) string {
	if v := os.Getenv(clave); v != "" {
		return v
	}
	return defecto
}

func responderJSON(w http.ResponseWriter, estado int, cuerpo any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Replica", nombreReplica())
	w.WriteHeader(estado)
	_ = json.NewEncoder(w).Encode(cuerpo)
}

func responderError(w http.ResponseWriter, estado int, mensaje string) {
	responderJSON(w, estado, map[string]string{"error": mensaje})
}

func nombreReplica() string {
	h, _ := os.Hostname()
	return h
}

// registro estructurado en una línea JSON (lo recogería Cloud Logging)
func registrar(campos map[string]any) {
	campos["hora"] = time.Now().Format(time.RFC3339Nano)
	b, _ := json.Marshal(campos)
	log.Println(string(b))
}

// ---------- contraseñas (PBKDF2-SHA256) ----------

func hashClave(clave, sal string) string {
	k, _ := pbkdf2.Key(sha256.New, clave, []byte(sal), 100_000, 32)
	return base64.StdEncoding.EncodeToString(k)
}

// ---------- token firmado con HMAC-SHA256 ----------
// formato: base64url(usuario|rol|expiracionUnix).base64url(firma)

var errTokenInvalido = errors.New("token inválido")
var errTokenVencido = errors.New("token vencido")

func emitirToken(secreto []byte, usuario, rol string, vigencia time.Duration, ahora time.Time) string {
	carga := fmt.Sprintf("%s|%s|%d", usuario, rol, ahora.Add(vigencia).Unix())
	p := base64.RawURLEncoding.EncodeToString([]byte(carga))
	return p + "." + firmar(secreto, p)
}

func firmar(secreto []byte, p string) string {
	m := hmac.New(sha256.New, secreto)
	m.Write([]byte(p))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func validarToken(secreto []byte, token string, ahora time.Time) (usuario, rol string, err error) {
	partes := strings.Split(token, ".")
	if len(partes) != 2 {
		return "", "", errTokenInvalido
	}
	if !hmac.Equal([]byte(firmar(secreto, partes[0])), []byte(partes[1])) {
		return "", "", errTokenInvalido
	}
	raw, err := base64.RawURLEncoding.DecodeString(partes[0])
	if err != nil {
		return "", "", errTokenInvalido
	}
	campos := strings.Split(string(raw), "|")
	if len(campos) != 3 {
		return "", "", errTokenInvalido
	}
	exp, err := strconv.ParseInt(campos[2], 10, 64)
	if err != nil {
		return "", "", errTokenInvalido
	}
	if ahora.Unix() > exp {
		return "", "", errTokenVencido
	}
	return campos[0], campos[1], nil
}
