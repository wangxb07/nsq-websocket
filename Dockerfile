FROM golang:1.11

RUN go get -d -v github.com/gorilla/websocket; \
    go install -v github.com/gorilla/websocket; \
    go get -d -v github.com/nsqio/go-nsq; \
    go install -v github.com/nsqio/go-nsq;

WORKDIR /go/src/app
COPY . .

RUN go build -o main .

ENTRYPOINT ["/go/src/app/main"]