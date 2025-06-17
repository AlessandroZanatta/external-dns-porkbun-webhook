package main

import (
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	porkbun "github.com/AlessandroZanatta/external-dns-porkbun-webhook/provider"

	"github.com/alecthomas/kingpin/v2"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	webhook "sigs.k8s.io/external-dns/provider/webhook/api"
)

var (
	logMethodStr = kingpin.Flag("log-method", "Log method. Must be one of 'json', 'console'").Default("console").Envar("LOG_METHOD").String()
	logLevelStr  = kingpin.Flag("log-level", "Log level. Must be one of 'panic', 'fatal', 'error', 'warn', 'info', 'debug', 'trace'").Default("info").Envar("LOG_LEVEL").String()

	listenAddr = kingpin.Flag("listen-address", "The address this plugin listens on").Default(":8888").Envar("PORKBUN_LISTEN_ADDRESS").String()

	domainFilter = kingpin.Flag("domain-filter", "Limit possible target zones by a domain suffix; specify multiple times for multiple domains").Required().Envar("PORKBUN_DOMAIN_FILTER").Strings()
	apiKey       = kingpin.Flag("porkbun-api-key", "The api key used to access porkbun API").Required().Envar("PORKBUN_API_KEY").String()
	secretKey    = kingpin.Flag("porkbun-api-secret", "The secret key used to access Porkbun API").Required().Envar("PORKBUN_SECRET_KEY").String()
)

func main() {
	kingpin.Parse()

	var logMethod io.Writer
	switch *logMethodStr {
	case "json":
		logMethod = os.Stdout
	case "console":
		logMethod = zerolog.ConsoleWriter{Out: os.Stdout}
	default:
		log.Fatal().Msg("Log method must be one of 'json', 'console'")
	}

	log.Logger = log.Output(logMethod)
	level, err := zerolog.ParseLevel(*logLevelStr)
	if err != nil {
		log.Fatal().Err(err).Msg("Invalid log level")
	}
	zerolog.SetGlobalLevel(level)

	log.Info().Msg("starting external-dns Porkbun webhook plugin")
	log.Debug().Str("api-key", strings.Repeat("*", len(*apiKey))).Str("api-password", strings.Repeat("*", len(*secretKey))).Msg("Configuration")

	webhookMux, err := buildWebhookServer(log.Logger)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create provider")
	}
	webhookServer := http.Server{
		Addr:              *listenAddr,
		Handler:           webhookMux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Info().Str("address", *listenAddr).Msg("Started external-dns-porkbun-webhook webhook server")
	err = webhookServer.ListenAndServe()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to start the webhook server")
	}
}

func buildWebhookServer(logger zerolog.Logger) (*http.ServeMux, error) {
	mux := http.NewServeMux()

	var rootPath = "/"
	var healthzPath = "/healthz"
	var recordsPath = "/records"
	var adjustEndpointsPath = "/adjustendpoints"

	ncProvider, err := porkbun.NewPorkbunProvider(domainFilter, *apiKey, *secretKey, logger)
	if err != nil {
		return nil, err
	}

	p := webhook.WebhookServer{
		Provider: ncProvider,
	}

	// Add healthzPath
	mux.HandleFunc(healthzPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(http.StatusText(http.StatusOK)))
	})

	// Add negotiatePath
	mux.HandleFunc(rootPath, p.NegotiateHandler)
	// Add adjustEndpointsPath
	mux.HandleFunc(adjustEndpointsPath, p.AdjustEndpointsHandler)
	// Add recordsPath
	mux.HandleFunc(recordsPath, p.RecordsHandler)

	return mux, nil
}
