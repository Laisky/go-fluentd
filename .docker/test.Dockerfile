# docker build -f .docker/test.Dockerfile -t go-fluentd-test .
FROM golang:1.27.1-bookworm

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
CMD ["go", "test", "-mod=readonly", "-race", "-count=1", "-timeout=180s", "-coverprofile=coverage.txt", "-covermode=atomic", "./..."]
