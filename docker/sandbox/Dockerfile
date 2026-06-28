# debian:12-slim ships /bin/bash, which the operator exec path relies on
# (the host wraps container commands containing shell operators as
# ["bash","-c", <command>]). Keep bash available if swapping the base image.
FROM debian:12-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    git \
    ca-certificates \
    build-essential


COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
