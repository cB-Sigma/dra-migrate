# Used by GoReleaser: the binary is built outside and copied in.
FROM gcr.io/distroless/static-debian12:nonroot
COPY dra-migrate /usr/local/bin/dra-migrate
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/dra-migrate"]
