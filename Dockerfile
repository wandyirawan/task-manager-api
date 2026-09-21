# syntax=docker/dockerfile:1
# Multi-stage (LOCKED — SPEC §14): build CGO_ENABLED=0 → distroless static runtime.

# ---- Stage 1: build ----
FROM golang:1.27-alpine AS build
WORKDIR /src
# Deps first: layer cache — rebuild cepat kalau go.mod/go.sum gak berubah.
COPY go.mod go.sum ./
RUN go mod download
# Source after deps cached.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/api ./cmd/api

# ---- Stage 2: runtime ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /bin/api /api
EXPOSE 8080
ENTRYPOINT ["/api"]