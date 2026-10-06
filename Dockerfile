# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mailcat ./cmd/mailcat && mkdir /out/data

# Imagen final mínima: sin shell, sin gestor de paquetes, usuario no root.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mailcat /mailcat
COPY --from=build --chown=65532:65532 /out/data /data
WORKDIR /data
ENV MAILCAT_CONFIG=/data/mailcat.yaml
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD ["/mailcat", "health"]
ENTRYPOINT ["/mailcat", "-db", "/data/mailcat.db"]
CMD ["serve", "-addr", "0.0.0.0:8080"]
