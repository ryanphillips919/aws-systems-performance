FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -o /out/api ./cmd/api && CGO_ENABLED=0 go build -o /out/seed ./cmd/seed && CGO_ENABLED=0 go build -o /out/dynamolab ./cmd/dynamolab && CGO_ENABLED=0 go build -o /out/cachelab ./cmd/cachelab

FROM alpine:3.21
RUN addgroup -S app && adduser -S -G app app
USER app
COPY --from=build /out/api /usr/local/bin/api
COPY --from=build /out/seed /usr/local/bin/seed
COPY --from=build /out/dynamolab /usr/local/bin/dynamolab
COPY --from=build /out/cachelab /usr/local/bin/cachelab
EXPOSE 8000
CMD ["api"]
