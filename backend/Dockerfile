# ---- build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X qomranote/backend/internal/cli.version=${VERSION}" \
    -o /out/qomranote ./cmd/qomranote

# ---- runtime stage ----
FROM alpine:3.21
RUN adduser -D -u 10001 qomra && apk add --no-cache ca-certificates tzdata
USER qomra
WORKDIR /app
COPY --from=build /out/qomranote /usr/local/bin/qomranote
EXPOSE 8080
ENTRYPOINT ["qomranote"]
CMD ["serve"]
