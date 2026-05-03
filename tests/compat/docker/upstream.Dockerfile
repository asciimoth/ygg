FROM golang:1.25.5-bookworm AS build

ARG YGG_UPSTREAM_REF=b88fec63ff4eb94add47191fca292fc3306ee71c

WORKDIR /src/upstream

RUN apt-get update \
	&& apt-get install -y --no-install-recommends \
		ca-certificates \
		git \
	&& rm -rf /var/lib/apt/lists/*

RUN git init . \
	&& git remote add origin https://github.com/yggdrasil-network/yggdrasil-go.git \
	&& git fetch --depth 1 origin "${YGG_UPSTREAM_REF}" \
	&& git checkout --detach FETCH_HEAD

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
