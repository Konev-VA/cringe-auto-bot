FROM golang:1.26-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bot ./cmd/bot

FROM debian:bookworm-slim

# Chromium нужен для avito.ru: обычный HTTP отдаёт 439 (Qrator).
RUN apt-get update \
    && apt-get install -y --no-install-recommends chromium ca-certificates fonts-liberation \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/bot /usr/local/bin/bot

ENV CHROME_PATH=/usr/bin/chromium \
    HOME=/tmp \
    TMPDIR=/tmp

USER nobody
ENTRYPOINT ["/usr/local/bin/bot"]
