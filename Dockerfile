# ========================
# Build Stage
# ========================
FROM --platform=linux/amd64 golang:1.25-alpine AS build

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags "-s -w" -o pody .

# ========================
# Final Stage
# ========================
FROM --platform=linux/amd64 python:3.11-alpine

RUN apk add --no-cache \
        ffmpeg \
        ca-certificates \
        tzdata \
    && pip install --no-cache-dir yt-dlp \
    && adduser -D -u 1000 appuser

WORKDIR /app

COPY --from=build /app/pody /usr/local/bin/pody
COPY --from=build /app/index.html /app/index.html

RUN mkdir -p /app/data /app/downloads \
    && chown -R appuser:appuser /app

VOLUME ["/app/data", "/app/downloads", "/app/config.json"]

ENV OUT_DIR=/app/downloads

EXPOSE 8080

USER appuser

ENTRYPOINT ["/usr/local/bin/pody"]
