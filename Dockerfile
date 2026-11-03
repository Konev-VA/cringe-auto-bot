FROM golang:1.26-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bot ./cmd/bot

FROM debian:bookworm-slim

# Chromium нужен для avito.ru: обычный HTTP отдаёт 439 (Qrator).
# xvfb нужен для CHROME_HEADLESS=false: проверка браузера на avito.ru охотнее
# пропускает окно, чем headless.
RUN apt-get update \
    && apt-get install -y --no-install-recommends chromium ca-certificates fonts-liberation fonts-dejavu-core xvfb xauth \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /tmp/.X11-unix && chmod 1777 /tmp/.X11-unix

COPY --from=build /out/bot /usr/local/bin/bot
COPY deploy/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENV CHROME_PATH=/usr/bin/chromium \
    HOME=/tmp \
    TMPDIR=/tmp

USER nobody
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
