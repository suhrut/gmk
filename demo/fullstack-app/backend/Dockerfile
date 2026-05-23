# The Go binary is built on the host by gmk (target: images-backend-binary)
# with CGO_ENABLED=0, so we can use a scratch base image. No glibc, no
# package manager, ~10MB image total.
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY build/server /app/server

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/server"]
