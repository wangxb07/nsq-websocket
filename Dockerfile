FROM golang:1.11

WORKDIR /go/src/app

COPY . .

RUN go get -d -v github.com/gorilla/websocket
RUN go install -v github.com/gorilla/websocket

RUN go get -d -v github.com/nsqio/go-nsq
RUN go install -v github.com/nsqio/go-nsq

RUN go build -o main .

ENTRYPOINT ["/go/src/app/main"]