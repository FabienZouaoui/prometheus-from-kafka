package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"strings"
	"net/http"
	"net/url"
	"os"

	cluster "github.com/bsm/sarama-cluster"

	_ "github.com/go-sql-driver/mysql"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/robfig/cron"
	"github.com/mssola/user_agent"
)

const (

	DEFAULT_KAFKA_BROKERS   = "kafka:9092"
	DEFAULT_KAFKA_TOPIC     = "requests"
	DEFAULT_KAFKA_CLIENT_ID = "sirdata-monitoring"

	MYSQL_DSN = "user:password@tcp(127.0.0.1:3306)/database"

	HTTP_SERVER_LISTEN_PORT = ":9090"

	OffsetNewest int64 = -1
	OffsetOldest int64 = -2
)

var (
	debugState    = flag.Bool("debug", false, "enable debugState logs")
	bind          = flag.String("bind-addr", HTTP_SERVER_LISTEN_PORT, "http bind address")
	kafkaBrokers  = flag.String("kafka-brokers", DEFAULT_KAFKA_BROKERS, "Kafka brokers in host:port format, as a comma separated list")
	kafkaTopic    = flag.String("kafka-topic", DEFAULT_KAFKA_TOPIC, "Kafka topic on which to publish messages")
	kafkaClientID = flag.String("kafka-client-id", DEFAULT_KAFKA_CLIENT_ID, "Kafka client ID")
	mysqlDsn      = flag.String("mysql-dsn", MYSQL_DSN, "Mysql DSN")

	partners map[int]string

	/* requestsByPartnerCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:	"sirdata_frontend_http_requests_by_partner",
			Help:	"HTTP Requests counter from REQUESTS_EXCHANGE by partner",
		},
		[]string{"partner_id", "partner_name"},
	)*/
	requestsByPartnerHostCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sirdata_frontend_http_requests_by_partner_host",
			Help: "HTTP Requests counter from REQUESTS_EXCHANGE by partner, host and path",
		},
		[]string{"partner_id", "partner_name", "referer_domain"},
	)
	requestsByCountryCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sirdata_frontend_http_requests_by_country",
			Help: "HTTP Requests counter from REQUESTS_EXCHANGE by country",
		},
		[]string{"country"},
	)
	requestsByPathCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sirdata_frontend_http_requests_by_path",
			Help: "HTTP Requests counter from REQUESTS_EXCHANGE by path",
		},
		[]string{"path"},
	)
	requestsByUserAgentCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sirdata_frontend_http_requests_by_user_agent",
			Help: "HTTP Requests counter from REQUESTS_EXCHANGE by user agent",
		},
		[]string{"user_agent", "os", "browser"},
	)
	newVisitorsCounter = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "sirdata_frontend_new_users_requests",
			Help: "New users HTTP Requests counter from REQUESTS_EXCHANGE",
		},
	)
	knownVisitorsCounter = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "sirdata_frontend_known_users_requests",
			Help: "Known users HTTP Requests counter from REQUESTS_EXCHANGE",
		},
	)
	botsVisitorsCounter = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "sirdata_frontend_bots_requests",
			Help: "Bots HTTP Requests counter from REQUESTS_EXCHANGE",
		},
	)
	mobileVisitorsCounter = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "sirdata_frontend_mobile_requests",
			Help: "Mobile HTTP Requests counter from REQUESTS_EXCHANGE",
		},
	)
)

type Message struct {
	PartnerID int    `json:"partner_id"`
	SiteID    int    `json:"site_id"`
	Referer   string `json:"referer"`
	Path      string `json:"path"`
	UserID    string `json:"user_id"`
	Country   string `json:"country"`
	City      string `json:"city"`
	UserAgent string `json:"user_agent"`
	IsNew     bool   `json:"is_new"`
}

func init() {
	var (
		partnerId      int
		partnerCompany string
	)

	flag.Parse()

	db, err := sql.Open("mysql", *mysqlDsn)
	if err != nil {
		log.Fatalf("%s: %s", "Failed to connect to mysql", err)
	}
	defer db.Close()

	rows, err := db.Query("select id, COALESCE(company, id) as company from partners")
	if err != nil {
		log.Fatalf("%s: %s", "Failed to query mysql db", err)
	}
	defer rows.Close()

	partners = make(map[int]string)
	for rows.Next() {
		err := rows.Scan(&partnerId, &partnerCompany)
		if err != nil {
			log.Fatal(err)
		}
		if partnerCompany == "" {
			partnerCompany = fmt.Sprintf("%d", partnerId)
		}
		partners[partnerId] = partnerCompany
		if *debugState { //TODO: "github.com/inconshreveable/log15"
			log.Printf("Registering partner ID %d with name %s", partnerId, partnerCompany)
		}
	}

	registrars := []func() error{
		func() error {
			return prometheus.Register(requestsByPartnerHostCounter)
		},
		func() error {
			return prometheus.Register(requestsByCountryCounter)
		},
		func() error {
			return prometheus.Register(requestsByPathCounter)
		},
		func() error {
			return prometheus.Register(requestsByUserAgentCounter)
		},
		func() error {
			return prometheus.Register(newVisitorsCounter)
		},
		func() error {
			return prometheus.Register(knownVisitorsCounter)
		},
		func() error {
			return prometheus.Register(botsVisitorsCounter)
		},
		func() error {
			return prometheus.Register(mobileVisitorsCounter)
		},
	}

	for _, registrar := range registrars {
		if err := registrar(); err != nil {
			fmt.Println("Counter could not be registered", err)
			return
		}
	}
}

func main() {
	c := cron.New()
	c.AddFunc("@midnight", func() {
		fmt.Println("Restarting instance...")
		os.Exit(0)
	})
	c.Start()

	// init (custom) config, enable errors and notifications
	config := cluster.NewConfig()
	config.Consumer.Return.Errors = true
	config.Consumer.Offsets.Initial = OffsetNewest
	config.Group.Return.Notifications = true

	brokers := strings.Split(*kafkaBrokers, ",")
	topics := []string{*kafkaTopic}
	consumer, err := cluster.NewConsumer(brokers, *kafkaClientID, topics, config)
	if err != nil {
		panic(err)
	}
	defer consumer.Close()

	// consume errors
	go func() {
		for err := range consumer.Errors() {
			log.Printf("Error: %s\n", err.Error())
		}
	}()

	// consume notifications
	go func() {
		for ntf := range consumer.Notifications() {
			log.Printf("Rebalanced: %+v\n", ntf)
		}
	}()

	// consume messages
	go func() {

		for {
			msg, ok := <-consumer.Messages()
			if ! ok {
				log.Fatalf("%s: %v (%v)", "Consumer error", err, msg)
			}
			if *debugState {
				log.Printf("Received a message: %s/%d/%d\t%s\t%s\n", msg.Topic, msg.Partition, msg.Offset, msg.Key, msg.Value)
			}
			var pName string
			var m Message
			err := json.Unmarshal(msg.Value, &m)
			if err != nil {
				log.Fatalf("%s: %s", "Error while unmarshall json message", err)
			}

			// Referers Host and Partners
			r, err := url.Parse(m.Referer)
			if err != nil {
				r, _ = url.Parse("-")
			}
			pName = fmt.Sprintf("%s", partners[m.PartnerID])
			if pName == "" {
				pName = fmt.Sprintf("%d", m.PartnerID)
			}
			requestsByPartnerHostCounter.WithLabelValues(
				fmt.Sprintf("%d", m.PartnerID),
				pName,
				r.Host,
			).Inc()

			// Pathes
			u, err := url.Parse(m.Path)
			if err != nil {
				u, _ = url.Parse("-")
			}
			requestsByPathCounter.WithLabelValues(u.Path).Inc()

			// User Agents
			ua := user_agent.New(m.UserAgent)
			uaName, uaVersion := ua.Browser()
			requestsByUserAgentCounter.WithLabelValues(
				fmt.Sprintf("%s %s %s", ua.OS(), uaName, uaVersion),
				fmt.Sprintf("%s", ua.OS()),
				fmt.Sprintf("%s", uaName),
			).Inc()

			// Bots
			if ua.Bot() {
				botsVisitorsCounter.Inc()
			}

			// Mobile
			if ua.Mobile() {
				mobileVisitorsCounter.Inc()
			}

			// Countries
			requestsByCountryCounter.WithLabelValues(m.Country).Inc()

			// Knew / Known visitors
			if m.IsNew {
				newVisitorsCounter.Inc()
			} else {
				knownVisitorsCounter.Inc()
			}

			consumer.MarkOffset(msg, "")	// mark message as processed
		}
	}()

	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "OK")
	})
	log.Fatal(http.ListenAndServe(*bind, nil))
}
