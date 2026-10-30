# Imagen de la tienda (multietapa). Un solo binario; la variable ROL elige el microservicio.
# Etapa 1: compilación
FROM golang:1.24-alpine AS compilacion
WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY web ./web
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /tienda .

# Etapa 2: imagen mínima, sin shell ni gestor de paquetes, usuario sin privilegios
FROM scratch
COPY --from=compilacion /tienda /tienda
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/tienda"]
