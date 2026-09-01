# ---- build ----
FROM golang:1.27-alpine AS build
WORKDIR /src

# Dependencies first so edits to the source do not invalidate this layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# ---- runtime ----
FROM alpine:3.22
RUN apk add --no-cache ca-certificates \
    && adduser -D -u 10001 app
WORKDIR /app
COPY --from=build /out/api /app/api
USER app
EXPOSE 5000
CMD ["/app/api"]
