FROM golang:1.25-alpine AS build

RUN apk add --no-cache gcc musl-dev
WORKDIR /src
COPY scum_run /src/scum_run
WORKDIR /src/scum_run
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -o /out/scum_run ./main.go

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata sqlite curl
WORKDIR /app
COPY --from=build /out/scum_run /usr/local/bin/scum_run
ENTRYPOINT ["/usr/local/bin/scum_run"]
