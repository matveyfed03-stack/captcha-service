package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/matveyfed03-stack/captcha-service/internal/store"
	"github.com/matveyfed03-stack/captcha-service/pkg/proto/balancer"
	"github.com/matveyfed03-stack/captcha-service/pkg/proto/captcha/v1"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	defaultMinPort            = 38000
	defaultMaxPort            = 40000
	defaultMaxShutdownSeconds = 600
)

var (
	metricNewChallenges = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "captcha_new_challenges_total",
		Help: "Total number of generated challenges",
	})
	metricActiveChallenges = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "captcha_active_challenges",
		Help: "Number of active (not yet validated/expired) challenges",
	})
	metricValidateTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "captcha_validate_total",
		Help: "Total number of validation attempts",
	})
	metricValidateSuccess = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "captcha_validate_success_total",
		Help: "Total number of successful validations",
	})
	metricNewChallengeLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "captcha_new_challenge_latency_ms",
		Help:    "Latency of NewChallenge in milliseconds",
		Buckets: []float64{0.5, 1, 2, 5, 10, 20, 50, 100},
	})
)

func init() {
	prometheus.MustRegister(
		metricNewChallenges,
		metricActiveChallenges,
		metricValidateTotal,
		metricValidateSuccess,
		metricNewChallengeLatency,
	)
}

func mustEnvInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func selectFreePort(minPort, maxPort int) (int, net.Listener, error) {
	for p := minPort; p <= maxPort; p++ {
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
		if err == nil {
			return p, l, nil
		}
	}
	return 0, nil, fmt.Errorf("no free port in range %d-%d", minPort, maxPort)
}

func generateInstanceID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type captchaServer struct {
	v1.UnimplementedCaptchaServiceServer
	store *store.InMemoryStore
}

func (s *captchaServer) NewChallenge(ctx context.Context, req *v1.ChallengeRequest) (*v1.ChallengeResponse, error) {
	start := time.Now()
	challengeID := generateInstanceID()
	// store expected answer
	s.store.Put(challengeID, []byte("clicked"))
	metricNewChallenges.Inc()
	metricActiveChallenges.Inc()

	// Minimal HTML with postMessage integration
	html := "<!doctype html><html><head><meta charset=\"utf-8\"><title>Captcha</title><style>body{font-family:sans-serif}button{font-size:20px;padding:8px 16px}</style></head><body><h3>Click the button</h3><button id=\"btn\">I am human</button><script>\nconst btn=document.getElementById('btn');\nbtn.addEventListener('click',()=>{\n  window.top.postMessage({type:'captcha:sendData',data:new TextEncoder().encode('clicked').buffer},'*');\n});\nwindow.addEventListener('message',e=>{if(e.data?.type==='captcha:serverData'){/* handle server data */}});\n</script></body></html>"
	resp := &v1.ChallengeResponse{ChallengeId: challengeID, Html: html}
	metricNewChallengeLatency.Observe(float64(time.Since(start).Milliseconds()))
	return resp, nil
}

func (s *captchaServer) MakeEventStream(stream v1.CaptchaService_MakeEventStreamServer) error {
	for {
		ev, err := stream.Recv()
		if err != nil {
			return err
		}
		switch ev.EventType {
		case v1.ClientEvent_FRONTEND_EVENT:
			metricValidateTotal.Inc()
			valid := s.store.ValidateAndDelete(ev.ChallengeId, ev.Data)
			if valid {
				metricValidateSuccess.Inc()
				metricActiveChallenges.Dec()
			}
			confidence := int32(0)
			if valid {
				confidence = 100
			}
			_ = stream.Send(&v1.ServerEvent{Event: &v1.ServerEvent_Result{Result: &v1.ServerEvent_ChallengeResult{ChallengeId: ev.ChallengeId, ConfidencePercent: confidence}}})
		case v1.ClientEvent_CONNECTION_CLOSED:
			// ignore
		default:
			// ignore unknown
		}
	}
}

func startMetricsServer() {
	addr := os.Getenv("METRICS_ADDR")
	if addr == "" {
		addr = ":9090"
	}
	http.Handle("/metrics", promhttp.Handler())
	go func() {
		log.Printf("metrics/pprof listening on %s", addr)
		_ = http.ListenAndServe(addr, nil)
	}()
}

func main() {
	minPort := mustEnvInt("MIN_PORT", defaultMinPort)
	maxPort := mustEnvInt("MAX_PORT", defaultMaxPort)
	shutdownMax := mustEnvInt("MAX_SHUTDOWN_INTERVAL", defaultMaxShutdownSeconds)

	startMetricsServer()

	port, lis, err := selectFreePort(minPort, maxPort)
	if err != nil {
		log.Fatalf("select port: %v", err)
	}
	grpcServer := grpc.NewServer()
	capStore := store.NewInMemoryStore(5 * time.Minute)
	defer capStore.Close()
	v1.RegisterCaptchaServiceServer(grpcServer, &captchaServer{store: capStore})

	go func() {
		log.Printf("CaptchaService listening on :%d", port)
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("serve: %v", err)
		}
	}()

	// Optional: register with balancer if env provided
	balancerAddr := os.Getenv("BALANCER_ADDR") // host:port
	challengeType := os.Getenv("CHALLENGE_TYPE")
	if challengeType == "" {
		challengeType = "basic-click"
	}
	instanceID := generateInstanceID()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if balancerAddr != "" {
		go balancerRegistrationLoop(ctx, balancerAddr, instanceID, challengeType, port)
	}

	// Wait for shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Printf("shutting down... sending STOPPED, waiting up to %ds", shutdownMax)
	shutdownTimer := time.NewTimer(time.Duration(shutdownMax) * time.Second)
	go func() {
		grpcServer.GracefulStop()
		shutdownTimer.Stop()
	}()
	<-shutdownTimer.C
}

func dialBalancer(ctx context.Context, addr string) (*grpc.ClientConn, error) {
	useTLS := os.Getenv("BALANCER_TLS") == "true"
	var creds credentials.TransportCredentials
	if useTLS {
		cfg := &tls.Config{}
		if sni := os.Getenv("BALANCER_SERVER_NAME"); sni != "" {
			cfg.ServerName = sni
		}
		creds = credentials.NewTLS(cfg)
	} else {
		creds = insecure.NewCredentials()
	}
	params := grpc.ConnectParams{
		Backoff: backoff.Config{
			BaseDelay:  200 * time.Millisecond,
			Multiplier: 1.6,
			Jitter:     0.2,
			MaxDelay:   5 * time.Second,
		},
		MinConnectTimeout: 3 * time.Second,
	}
	return grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(creds), grpc.WithConnectParams(params))
}

func balancerRegistrationLoop(ctx context.Context, addr, instanceID, challengeType string, port int) {
	for {
		conn, err := dialBalancer(ctx, addr)
		if err != nil {
			log.Printf("balancer dial error: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		client := balancer.NewBalancerServiceClient(conn)
		stream, err := client.RegisterInstance(ctx)
		if err != nil {
			log.Printf("register stream error: %v", err)
			_ = conn.Close()
			time.Sleep(2 * time.Second)
			continue
		}

		localAddrs, _ := net.InterfaceAddrs()
		host := "127.0.0.1"
		for _, a := range localAddrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.IsGlobalUnicast() {
				if ip, ok := netip.AddrFromSlice(ipnet.IP); ok {
					if ip.Is4() {
						host = ip.String()
						break
					}
				}
			}
		}

		// sender
		sendCtx, cancel := context.WithCancel(ctx)
		go func() {
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-sendCtx.Done():
					_ = stream.CloseSend()
					return
				case <-ticker.C:
					now := time.Now().UnixNano()
					req := &balancer.RegisterInstanceRequest{
						EventType:     balancer.RegisterInstanceRequest_READY,
						InstanceId:    instanceID,
						ChallengeType: challengeType,
						Host:          host,
						PortNumber:    int32(port),
						Timestamp:     now,
					}
					if err := stream.Send(req); err != nil {
						log.Printf("heartbeat send error: %v", err)
						cancel()
						return
					}
				}
			}
		}()

		// receiver (blocks until error)
		for {
			_, err := stream.Recv()
			if err != nil {
				log.Printf("balancer stream recv: %v", err)
				break
			}
		}
		cancel()
		_ = conn.Close()
		// backoff before reconnect
		time.Sleep(2 * time.Second)
	}
}
