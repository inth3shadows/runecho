# RunEcho — container image for the stdio MCP oracle server.
#
# Built for the Glama MCP directory (https://glama.ai/mcp/servers/inth3shadows/runecho),
# which builds every listed server from a Dockerfile and withholds distribution
# until a reproducible build succeeds. Also usable directly by anyone who wants
# the oracle without a local Go toolchain.
#
#   docker build -t runecho-mcp .
#   docker build --build-arg RUNECHO_VERSION=v0.21.1 -t runecho-mcp .   # stamped
#
# Tool discovery (what Glama's inspection does) needs nothing but the binary:
#
#   docker run --rm -i runecho-mcp
#
# Actually answering queries needs an enrolled repo and a persistent store. Run
# as your own uid so the mounted repo and the store are owned by you — see the
# safe.directory note in the runtime stage for why this image does NOT relax
# git's ownership check for you:
#
#   docker run --rm -i \
#     --user "$(id -u):$(id -g)" \
#     -v "$HOME/.runecho:/data" \
#     -v "$PWD:/repo:ro" \
#     --entrypoint runecho-ir runecho-mcp repo add /repo
#
# THIS IS A SHIPPING CHANNEL, and it is the third one. install.sh and
# .goreleaser.yaml are the other two. internal/parser/grammar_subset_test.go
# exists because a grammar build tag present in one channel and missing from
# another ships a parser that indexes that language to NOTHING while every test
# in the suite passes — that is what happened to Rust and Ruby. So the build
# stage below calls install.sh rather than restating `go build -tags ...`: the
# tag list has exactly one home. The test asserts this file contains no
# `grammar_subset` literal. Do not "simplify" it into a direct go build.

FROM golang:1.25-alpine AS build

# bash: install.sh is a bash script, not POSIX sh — alpine's default shell will
#       not run it.
# git:  install.sh stamps internal/version.Version from `git describe`, so a
#       build from a full checkout reports a real version instead of "dev".
RUN apk add --no-cache bash git

WORKDIR /src

# Module graph first, so editing source does not re-download dependencies.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Automated builders (Glama included) typically copy the repo without usable git
# history, so `git describe` finds no tags and install.sh honestly falls back to
# "dev" rather than asserting a release number it cannot verify. Pass the tag
# you are building to stamp it properly.
ARG RUNECHO_VERSION=""
ENV RUNECHO_VERSION=${RUNECHO_VERSION}

# RUNECHO_BIN_DIR redirects install.sh's output away from $HOME/.local/bin so the
# runtime stage can copy from one known directory. install.sh builds all three
# binaries; the image ships all three because a container holding only the MCP
# server cannot enroll or reindex a repo, and would answer every query empty.
RUN RUNECHO_BIN_DIR=/out bash install.sh


FROM alpine:3.22

# git is a RUNTIME dependency, not merely a build one: internal/gitutil shells
# out to `git` for every index and snapshot operation, so `repo add` and
# `repo reindex` fail without it. ca-certificates covers git over HTTPS.
RUN apk add --no-cache git ca-certificates \
 && adduser -D -h /home/runecho runecho

COPY --from=build /out/runecho-ir    /usr/local/bin/runecho-ir
COPY --from=build /out/runecho-mcp   /usr/local/bin/runecho-mcp
COPY --from=build /out/runecho-guard /usr/local/bin/runecho-guard

# Name the store explicitly instead of letting store.RunechoDir() fall back to
# $HOME/.runecho. Two reasons: the mount point stays stable regardless of which
# user the container runs as, and RunechoDir() returns an error when $HOME is
# unset — which would exit the server before it could speak a single JSON-RPC
# frame, presenting as an inspection failure with no useful message.
ENV RUNECHO_HOME=/data
RUN mkdir -p /data && chown runecho:runecho /data
VOLUME ["/data"]

# Deliberately NOT set here: `git config --system safe.directory '*'`. It would
# make mounting a host-owned repo work without --user, but it also re-enables
# honouring a mounted repo's own .git/config, which is a known command-execution
# path from a repo you do not control. gitutil already passes
# `-c core.fsmonitor=false` for that reason; blanket-disabling the ownership
# check here would give back more than that guard takes away. Run with
# `--user "$(id -u):$(id -g)"` instead, or scope the exemption to the one repo
# you are mounting.

USER runecho
WORKDIR /data

# Transport is newline-delimited JSON-RPC over stdio: no port, so no EXPOSE.
# stdout carries protocol frames and stderr carries diagnostics — never wrap
# this ENTRYPOINT in a script that writes to stdout, it corrupts the stream.
ENTRYPOINT ["/usr/local/bin/runecho-mcp"]
