ARG GO_VERSION=1
FROM golang:${GO_VERSION} AS build
WORKDIR /src
# go.sum only exists once the module has dependencies, so it is optional here
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
EXPOSE 9090 8080
USER nonroot:nonroot
ENTRYPOINT ["/server"]
CMD ["serve"]
