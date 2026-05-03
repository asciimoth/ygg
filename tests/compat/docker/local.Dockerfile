FROM golang:1.25.5-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o /out/yggdrasil ./cmd/yggdrasil
RUN CGO_ENABLED=1 GOOS=linux go build -o /out/yggdrasilctl ./cmd/yggdrasilctl

FROM debian:bookworm-slim

RUN apt-get update \
	&& apt-get install -y --no-install-recommends \
		ca-certificates \
		iproute2 \
		iputils-ping \
		jq \
	&& rm -rf /var/lib/apt/lists/*

COPY --from=build /out/yggdrasil /usr/local/bin/yggdrasil
COPY --from=build /out/yggdrasilctl /usr/local/bin/yggdrasilctl
