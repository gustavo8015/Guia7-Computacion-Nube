# Tienda en la nube · Guía 7 (Computación en la nube, Google Cloud)

Demostración de la alternativa B: aplicación contenerizada con gateway, réplicas, escalamiento y recuperación ante fallas.
Requisitos: Docker Desktop (Windows) con Docker Compose.

## 1. Preparar
Cree el archivo `.env` con una clave de 32 caracteres o más (no se sube al repositorio):

    Set-Content -Path .env -Value "SECRETO_TOKEN=ClaveDeLaboratorioTiendaNube2026xyz" -Encoding ascii

## 2. Construir y desplegar
    docker compose up -d --build
    docker compose ps

Solo el gateway publica el puerto 8080.

## 3. Interfaz web
Abra http://localhost:8080 en el navegador. Inicie sesión con ana / Ana#2026, compre un producto y use el panel "Prueba de carga". Para ver el reparto entre réplicas, escale antes el catálogo:

    docker compose up -d --scale catalogo=3 --no-recreate

## 3b. Autenticación y pedidos desde PowerShell
    Invoke-RestMethod -Method Post http://localhost:8080/api/login -Body '{"usuario":"ana","clave":"Ana#2026"}'
    $t = (Invoke-RestMethod -Method Post http://localhost:8080/api/login -Body '{"usuario":"ana","clave":"Ana#2026"}').token
    Invoke-RestMethod -Method Post http://localhost:8080/api/pedidos -Headers @{Authorization="Bearer $t"} -Body '{"sku":"P-001","cantidad":2}'

Usuarios: ana / Ana#2026 y luis / Luis#2026 (clientes), admin / Adm#2026 (administrador).

## 4. Prueba de carga, escalamiento y falla
    docker run --rm --network tienda-nube_interna -e ROL=carga tienda-nube:1.0 -url http://gateway:8080/api/productos -n 600 -c 30
    docker compose up -d --scale catalogo=3 --no-recreate
    docker run --rm --network tienda-nube_interna -e ROL=carga tienda-nube:1.0 -url http://gateway:8080/api/productos -n 600 -c 30
    docker run --rm --network tienda-nube_interna -e ROL=carga tienda-nube:1.0 -url http://gateway:8080/api/productos -n 600 -c 30 -fallar http://tienda-nube-catalogo-2:8080/simular-falla
    docker inspect -f "{{.Name}} reinicios={{.RestartCount}}" tienda-nube-catalogo-2

## 5. Persistencia, copia y monitoreo
    docker compose restart datos
    docker cp tienda-nube-datos-1:/datos/estado.json respaldo.json
    curl.exe -s localhost:8080/metricas
    docker compose logs gateway usuarios

## 6. Pruebas unitarias (requiere Go 1.24)
    go test -v ./...

## Apagar
    docker compose down        (agregue -v para borrar también el volumen de datos)

`Dockerfile` es la versión multietapa normal. `Dockerfile.local` sirve cuando no hay acceso a Docker Hub: compile antes con `CGO_ENABLED=0 go build -o tienda .`

## Despliegue en Google Cloud (referencia, no ejecutado)
    gcloud artifacts repositories create tienda --repository-format=docker --location=us-central1
    docker tag tienda-nube:1.0 us-central1-docker.pkg.dev/<proyecto>/tienda/tienda-nube:1.0
    docker push us-central1-docker.pkg.dev/<proyecto>/tienda/tienda-nube:1.0
    gcloud run deploy catalogo --region us-central1 --image us-central1-docker.pkg.dev/<proyecto>/tienda/tienda-nube:1.0 --set-env-vars ROL=catalogo --port 8080 --ingress internal --no-allow-unauthenticated --cpu 1 --memory 512Mi --concurrency 10 --min-instances 1 --max-instances 10
