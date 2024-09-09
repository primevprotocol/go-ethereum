FROM golang:1.21-alpine AS builder
RUN touch file_in_app.txt
RUN apk add --no-cache gcc musl-dev linux-headers git make

COPY . /mev-commit-geth
WORKDIR /mev-commit-geth
RUN make

WORKDIR /

FROM alpine:latest

RUN apk add --no-cache jq

COPY --from=builder /mev-commit-geth/build/bin/geth /usr/local/bin/

COPY --from=builder  /mev-commit-geth/geth-poa/genesis.json /genesis.json

COPY --from=builder  /mev-commit-geth/geth-poa/entrypoint.sh /entrypoint.sh

RUN chmod +x /entrypoint.sh

EXPOSE 8545 6060 60601

ENTRYPOINT ["/entrypoint.sh"]