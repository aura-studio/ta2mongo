FROM golang:1.23

RUN apt-get update \
    && apt-get install -y --no-install-recommends procps lsof \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace
