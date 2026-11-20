FROM golang:1.26-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bot ./cmd/bot

FROM debian:bookworm-slim

# Google Chrome нужен для avito.ru: обычный HTTP отдаёт 439 (Qrator), а также
# для страниц farpost.ru и baza.drom.ru, отдающих проверку браузера.
# Debian-Chromium Qrator отличает от фирменного Chrome и показывает «Доступ
# ограничен: проблема с IP» вместо объявления, поэтому ставим официальный .deb.
# xvfb нужен для CHROME_HEADLESS=false: проверка браузера на avito.ru охотнее
# пропускает окно, чем headless.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates wget xvfb xauth fonts-liberation fonts-dejavu-core \
    && wget -q https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb -O /tmp/chrome.deb \
    && apt-get install -y --no-install-recommends /tmp/chrome.deb \
    && rm -rf /var/lib/apt/lists/* /tmp/chrome.deb \
    && mkdir -p /tmp/.X11-unix && chmod 1777 /tmp/.X11-unix

COPY --from=build /out/bot /usr/local/bin/bot
COPY deploy/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENV CHROME_PATH=/opt/google/chrome/chrome \
    HOME=/tmp \
    TMPDIR=/tmp

USER nobody
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
