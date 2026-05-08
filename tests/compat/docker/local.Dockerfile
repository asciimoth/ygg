FROM golang:1.26.2-bookworm AS build

WORKDIR /src

COPY go.work ./
COPY ygglib/go.mod ygglib/go.sum ./ygglib/
COPY yggd/go.mod yggd/go.sum ./yggd/
COPY examples/go.mod examples/go.sum ./examples/
COPY web/go.mod web/go.sum ./web/
RUN cd ygglib && go mod download
RUN cd yggd && go mod download
RUN cd examples && go mod download
RUN cd web && go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o /out/yggdrasil ./yggd/yggd
RUN CGO_ENABLED=1 GOOS=linux go build -o /out/yggdrasilctl ./yggd/yggctl

FROM debian:bookworm-slim

RUN apt-get update \
	&& apt-get install -y --no-install-recommends \
		ca-certificates \
		curl \
		iproute2 \
		iputils-ping \
		jq \
		openssl \
		python3 \
	&& rm -rf /var/lib/apt/lists/*

COPY --from=build /out/yggdrasil /usr/local/bin/yggdrasil
COPY --from=build /out/yggdrasilctl /usr/local/bin/yggdrasilctl
