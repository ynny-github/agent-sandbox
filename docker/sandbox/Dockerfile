FROM debian:12-slim

ARG TARGETARCH
ARG NONO_VERSION=0.57.0

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    git \
    ca-certificates \
    build-essential \
    libdbus-1-3

RUN curl -fsSL "https://github.com/always-further/nono/releases/download/v${NONO_VERSION}/nono-cli_${NONO_VERSION}_${TARGETARCH}.deb" -o /tmp/nono.deb \
    && dpkg -i /tmp/nono.deb \
    && rm /tmp/nono.deb \
    && rm -rf /var/lib/apt/lists/*

COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
