FROM docker.io/golang:bookworm AS builder

ADD . /src
WORKDIR /src
RUN go build ./main.go

FROM docker.io/debian:bookworm-slim

RUN apt-get update && \
    apt-get -y upgrade && \
    apt-get -y install --no-install-recommends netcat-openbsd && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /src/main /listen

CMD ["/listen"]