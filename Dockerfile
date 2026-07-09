FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/vff-fiscal ./cmd/vff-fiscal

FROM alpine:3.24
RUN apk add --no-cache ca-certificates tzdata wget \
    && addgroup -S -g 65532 vff \
    && adduser -S -D -H -u 65532 -G vff vff \
    && mkdir -p /var/lib/vff-fiscal \
    && chown -R 65532:65532 /var/lib/vff-fiscal
COPY --from=build /out/vff-fiscal /usr/local/bin/vff-fiscal
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/vff-fiscal"]
