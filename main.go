package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/nsqio/go-nsq"
	"html/template"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type StringArray []string

func (a *StringArray) Get() interface{} { return []string(*a) }

func (a *StringArray) Set(s string) error {
	*a = append(*a, s)
	return nil
}

func (a *StringArray) String() string {
	return strings.Join(*a, ",")
}

var (
	addr = flag.String("addr", "localhost:8080", "http service address")
	showVersion = flag.Bool("version", false, "print version string")

	channel       = flag.String("channel", "", "NSQ channel")
	maxInFlight   = flag.Int("max-in-flight", 200, "max number of messages to allow in flight")
	totalMessages = flag.Int("n", 0, "total messages to show (will wait if starved)")

	nsqdTCPAddrs     = StringArray{}
	lookupdHTTPAddrs = StringArray{}
	topics           = StringArray{}

	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	} // use default options

	msg	= make(chan []byte, 4096)
	interrupt = make(chan os.Signal, 1)
)

func init() {
	flag.Var(&nsqdTCPAddrs, "nsqd-tcp-address", "nsqd TCP address (may be given multiple times)")
	flag.Var(&lookupdHTTPAddrs, "lookupd-http-address", "lookupd HTTP address (may be given multiple times)")
	flag.Var(&topics, "topic", "NSQ topic (may be given multiple times)")
}

type WebsocketHandler struct {
	topicName     string
	totalMessages int
	messagesSend int
}

func (wh *WebsocketHandler) HandleMessage(m *nsq.Message) error {
	wh.messagesSend++
	msg <- m.Body

	if wh.totalMessages > 0 && wh.messagesSend >= wh.totalMessages {
		os.Exit(0)
	}
	return nil
}

func wsNsq(w http.ResponseWriter, r *http.Request) {
	ticker := time.NewTicker(time.Second * 10)
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Print("upgrade:", err)
		return
	}
	defer c.Close()
	defer ticker.Stop()

	go func() {
		for {
			_, message, err := c.ReadMessage()
			if err != nil {
				log.Println("read:", err)
				break
			}
			log.Printf("recv: %s", message)
			msg <-message
		}
	}()

	for {
		// TODO nsq message receive and write to socket request
		select {
		case t := <-ticker.C:
			err := c.WriteMessage(websocket.PingMessage, []byte(t.String()))
			log.Printf("write: %s", t.String())
			if err != nil {
				log.Println("write:", err)
				return
			}
		case m := <-msg:
			err = c.WriteMessage(websocket.TextMessage, m)
			if err != nil {
				log.Println("write:", err)
				return
			}
		case <-interrupt:
			ticker.Stop()
			log.Println("Close...")
			return
		}
	}
}

func startNsqConn() []*nsq.Consumer {
	cfg := nsq.NewConfig()

	//flag.Var(&nsq.ConfigFlag{cfg}, "consumer-opt", "option to passthrough to nsq.Consumer (may be given multiple times, http://godoc.org/github.com/nsqio/go-nsq#Config)")

	if *channel == "" {
		rand.Seed(time.Now().UnixNano())
		*channel = fmt.Sprintf("websocket%06d#ephemeral", rand.Int()%999999)
	}

	if len(nsqdTCPAddrs) == 0 && len(lookupdHTTPAddrs) == 0 {
		log.Fatal("--nsqd-tcp-address or --lookupd-http-address required")
	}
	if len(nsqdTCPAddrs) > 0 && len(lookupdHTTPAddrs) > 0 {
		log.Fatal("use --nsqd-tcp-address or --lookupd-http-address not both")
	}
	if len(topics) == 0 {
		log.Fatal("--topic required")
	}

	// Don't ask for more messages than we want
	if *totalMessages > 0 && *totalMessages < *maxInFlight {
		*maxInFlight = *totalMessages
	}

	cfg.UserAgent = fmt.Sprintf("nsq_websocket/%s go-nsq/%s", "1.1.1-alpha", nsq.VERSION)
	cfg.MaxInFlight = *maxInFlight

	var consumers []*nsq.Consumer
	for i := 0; i < len(topics); i += 1 {
		log.Printf("Adding consumer for topic: %s\n", topics[i])

		consumer, err := nsq.NewConsumer(topics[i], *channel, cfg)
		if err != nil {
			log.Fatal(err)
		}

		consumer.AddHandler(&WebsocketHandler{topicName: topics[i], totalMessages: *totalMessages})

		err = consumer.ConnectToNSQDs(nsqdTCPAddrs)
		if err != nil {
			log.Fatal(err)
		}

		err = consumer.ConnectToNSQLookupds(lookupdHTTPAddrs)
		if err != nil {
			log.Fatal(err)
		}

		consumers = append(consumers, consumer)
	}

	return consumers
}

func home(w http.ResponseWriter, r *http.Request) {
	homeTemplate.Execute(w, "ws://"+r.Host+"/nsq")
}

func startHttpServer() *http.Server {
	srv := &http.Server{Addr: *addr}

	http.HandleFunc("/nsq", wsNsq)
	http.HandleFunc("/", home)

	go func() {
		// returns ErrServerClosed on graceful close
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			// NOTE: there is a chance that next line won't have time to run,
			// as main() doesn't wait for this goroutine to stop. don't use
			// code with race conditions like these for production. see post
			// comments below on more discussion on how to handle this.
			log.Fatalf("ListenAndServe(): %s", err)
		}
	}()

	// returning reference so caller can call Shutdown()
	return srv
}

func main() {
	log.SetFlags(log.Ldate|log.Lshortfile)
	flag.Parse()

	if *showVersion {
		fmt.Printf("nsq_websocket v%s\n", "1.1.1-alpha")
		return
	}

	signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)

	consumers := startNsqConn()
	srv := startHttpServer()

	<-interrupt

	log.Println("Clear consumers")
	for _, consumer := range consumers {
		consumer.Stop()
	}
	for _, consumer := range consumers {
		<-consumer.StopChan
	}

	log.Printf("main: stopping HTTP server")
	// now close the server gracefully ("shutdown")
	// timeout could be given with a proper context (in real world you shouldn't use TODO() ).
	if err := srv.Shutdown(context.TODO()); err != nil {
		panic(err) // failure/timeout shutting down the server gracefully
	}
	log.Printf("main: done. exiting")
}

var homeTemplate = template.Must(template.New("").Parse(`
<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
</head>
<body>
	<h1>BeeHome miniprogram websocket server</h1>
	<h2>{{.}}</h2>
</body>
</html>
`))