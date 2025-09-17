package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	balancer "github.com/matveyfed03-stack/captcha-service/pb/balancer/v1"
	captcha "github.com/matveyfed03-stack/captcha-service/pb/captcha/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	defaultMinPort            = 38000
	defaultMaxPort            = 40000
	defaultMaxShutdownSeconds = 600
)

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
	captcha.UnimplementedCaptchaServiceServer
}

func (s *captchaServer) NewChallenge(ctx context.Context, req *captcha.ChallengeRequest) (*captcha.ChallengeResponse, error) {
	// Minimal HTML with postMessage integration
	challengeID := generateInstanceID()
	html := "<!doctype html><html><head><meta charset=\"utf-8\"><title>Captcha</title><style>body{font-family:sans-serif}button{font-size:20px;padding:8px 16px}</style></head><body><h3>Click the button</h3><button id=\"btn\">I am human</button><script>\nconst btn=document.getElementById('btn');\nbtn.addEventListener('click',()=>{\n  window.top.postMessage({type:'captcha:sendData',data:new TextEncoder().encode('clicked').buffer},'*');\n});\nwindow.addEventListener('message',e=>{if(e.data?.type==='captcha:serverData'){/* handle server data */}});\n</script></body></html>"
	return &captcha.ChallengeResponse{ChallengeId: challengeID, Html: html}, nil
}

func (s *captchaServer) MakeEventStream(stream captcha.CaptchaService_MakeEventStreamServer) error {
	// Echo logic: upon FRONTEND_EVENT, immediately return success result
	for {
		ev, err := stream.Recv()
		if err != nil {
			return err
		}
		if ev.EventType == captcha.ClientEvent_FRONTEND_EVENT {
			_ = stream.Send(&captcha.ServerEvent{Event: &captcha.ServerEvent_Result{Result: &captcha.ServerEvent_ChallengeResult{ChallengeId: ev.ChallengeId, ConfidencePercent: 100}}})
		}
	}
}

func main() {
	minPort := mustEnvInt("MIN_PORT", defaultMinPort)
	maxPort := mustEnvInt("MAX_PORT", defaultMaxPort)
	shutdownMax := mustEnvInt("MAX_SHUTDOWN_INTERVAL", defaultMaxShutdownSeconds)

	port, lis, err := selectFreePort(minPort, maxPort)
	if err != nil {
		log.Fatalf("select port: %v", err)
	}
	grpcServer := grpc.NewServer()
	captcha.RegisterCaptchaServiceServer(grpcServer, &captchaServer{})

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
		go runBalancerRegistration(ctx, balancerAddr, instanceID, challengeType, port)
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

func runBalancerRegistration(ctx context.Context, addr, instanceID, challengeType string, port int) {
	conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("balancer dial error: %v", err)
		return
	}
	client := balancer.NewBalancerServiceClient(conn)
	stream, err := client.RegisterInstance(ctx)
	if err != nil {
		log.Printf("register stream error: %v", err)
		return
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
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
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
					return
				}
			}
		}
	}()

	// receiver
	go func() {
		for {
			resp, err := stream.Recv()
			if err != nil {
				log.Printf("balancer stream recv: %v", err)
				return
			}
			if resp.Status == balancer.RegisterInstanceResponse_ERROR {
				log.Printf("balancer error: %s", resp.Message)
			}
		}
	}()
}
