FROM golang:1.24 AS builder

WORKDIR /app

COPY . .
RUN go env -w GOPROXY=https://goproxy.cn,direct
RUN rm -f go.mod go.sum
RUN go mod init k8s-consul-registrar
RUN go mod tidy
#RUN go build -ldflags "-s -w -extldflags --static" -o k8s-consul-registrar main.go
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -ldflags '-extldflags "-static"' -o k8s-consul-registrar main.go
RUN ls -lh /app/k8s-consul-registrar # Add this line to verify

FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/k8s-consul-registrar .

ENV CLUSTER_NAME="TEST-CLUSTER"
ENV CONSUL_ADDRESS="localhost:8500"
ENV SYNC_PERIOD="600"

CMD ["/app/k8s-consul-registrar"]
